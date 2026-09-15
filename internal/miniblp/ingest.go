package miniblp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// Headers of the REST ingest surface.
const (
	// HeaderChunkOrdinal carries the chunk's ordinal, strictly increasing per
	// (batch ref, dataset), counted from 1. It is mandatory: without it a gap
	// is undetectable, and an undetected gap is a silently incomplete batch.
	HeaderChunkOrdinal = "X-Chunk-Ordinal"
	// HeaderIfMatch and HeaderIfNoneMatch carry the optimistic concurrency
	// precondition of the single-record PUT.
	HeaderIfMatch     = "If-Match"
	HeaderIfNoneMatch = "If-None-Match"
)

// CodeChunkOrdinalRequired reports a records chunk with no X-Chunk-Ordinal.
const CodeChunkOrdinalRequired = "CHUNK_ORDINAL_REQUIRED"

// CodeKeyMismatch reports a single-record PUT whose payload has a different
// natural key than the path it was addressed to.
const CodeKeyMismatch = "KEY_MISMATCH"

// handleOpenBatch serves POST /v1/ingest/batches: it opens a REST batch from the
// same manifest object the file channel reads, minus the per-file paths and
// hashes.
//
// The batch ref a client addresses chunks by is the batch id itself. A generated
// ref would be state a resuming client has to persist, and a client that has to
// persist a mapping in order to retry is a client that will get the mapping
// wrong.
//
// Replay and conflict are decided here, on the manifest hash, exactly as on the
// file channel: an identical manifest re-opens the batch and reports replay, and
// a different manifest under a known batch id is CodeBatchIDConflict and never a
// silent overwrite.
func (s *Server) handleOpenBatch(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, tooLargeAsBatch(err))
		return
	}
	format, err := httpx.RequestFormat(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if format != httpx.FormatJSON && format != httpx.FormatNDJSON {
		// A manifest has a nested file list, which a CSV cell and the generic
		// XML record mapping cannot carry. Records travel in all four formats;
		// the manifest is JSON.
		httpx.Fail(w, httpx.UnsupportedContentType(r.Header.Get("Content-Type")).
			WithDetail("hint", "a batch manifest is application/json"))
		return
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		httpx.Fail(w, httpx.MalformedBody("json_invalid", "the manifest is a JSON object"))
		return
	}
	m.normalize()
	if err := m.validate(ChannelREST); err != nil {
		e := &httpx.Error{Code: CodeManifestInvalid, Message: err.Error(),
			Retriable: false, Status: http.StatusBadRequest}
		httpx.Fail(w, e)
		return
	}
	for _, f := range m.Files {
		if _, err := ResolveFormat(f.Profile, f.Format); err != nil {
			httpx.Fail(w, httpx.AsError(err))
			return
		}
	}
	sha := model.ContentHash(m)

	s.mu.Lock()
	defer s.mu.Unlock()

	replay := false
	if prev, ok := s.batches[m.BatchID]; ok {
		switch {
		case prev.ManifestSHA256 != sha:
			e := &httpx.Error{Code: CodeBatchIDConflict,
				Message:   "this batch id was already delivered with a different manifest",
				Retriable: false, Status: http.StatusConflict}
			httpx.Fail(w, e.WithDetail("batch_id", m.BatchID))
			return
		case prev.Status == BatchOpen:
			// Re-opening an open batch is idempotent: a client that lost its
			// answer may repeat the call.
			writeJSON(w, http.StatusOK, s.openBatchResponse(prev, false))
			return
		default:
			// A byte-identical re-delivery. The batch is re-opened so the
			// client may push its chunks; every record will be
			// skipped_unchanged, nothing changes and no proposal is emitted,
			// which is what makes a replay a no-op without refusing the
			// client's traffic.
			replay = true
		}
	}
	b := newBatchState(m, sha, ChannelREST, 0)
	s.remember(b)
	s.run(m.RunID)
	writeJSON(w, http.StatusCreated, s.openBatchResponse(b, replay))
}

// openBatchResponse is the answer to a batch open.
func (s *Server) openBatchResponse(b *batchState, replay bool) map[string]any {
	return map[string]any{
		"batch_ref":        b.Ref,
		"batch_id":         b.ID,
		"manifest_sha256":  b.ManifestSHA256,
		"status":           b.Status,
		"replay":           replay,
		"max_records":      MaxChunkRecords,
		"max_body_bytes":   httpx.MaxRequestBodyBytes,
		"records_endpoint": "/v1/ingest/batches/" + b.Ref + "/records",
	}
}

// handleChunk serves POST /v1/ingest/batches/{ref}/records?dataset=<ds>.
//
// The order of the checks is the contract: idempotency first, so a retry can
// never do the work twice; then the chunk ordinal, so a gap is refused before
// anything is applied; then the published caps; then the records themselves. The
// per-record results come back in request order, whatever happened to them, and
// the status is 200 when all were accepted, 422 when none were and 207 in
// between.
//
// The per-record response is JSON on every request, whatever the request's own
// format was. Records travel in all four wire formats and so do outbox pages,
// but a per-record result carries nested errors[] and warnings[] lists, and a
// CSV cell cannot hold a list: emitting one would mean inventing a flattening
// the contract does not define. The four formats are an axis of the DATA, not of
// the diagnostics.
//
// The applied-then-500 of BUILD-SPEC 3.4 and 17.1 is implemented at the end of
// this handler and is deliberately honest: the records ARE applied, the real
// 207 IS stored under the client's idempotency key, and only then does the
// client get a 500. A client that retries with the same key gets the stored 207
// and its counts stay right; a client that regenerates the key on retry applies
// the same chunk a second time, which the store absorbs as skipped_unchanged and
// duplicate_apply_attempts counts. That counter is the graded evidence that the
// client's idempotency key is a function of its payload and not of its attempt.
func (s *Server) handleChunk(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	dataset := r.URL.Query().Get("dataset")

	key, err := httpx.RequireIdempotencyKey(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	rawOrdinal := strings.TrimSpace(r.Header.Get(HeaderChunkOrdinal))
	if rawOrdinal == "" {
		httpx.Fail(w, &httpx.Error{Code: CodeChunkOrdinalRequired,
			Message:   "X-Chunk-Ordinal is mandatory and strictly increasing per batch and dataset",
			Retriable: false, Status: http.StatusBadRequest})
		return
	}
	ordinal, convErr := strconv.ParseInt(rawOrdinal, 10, 64)
	if convErr != nil || ordinal < 1 {
		httpx.Fail(w, &httpx.Error{Code: CodeChunkOrdinalRequired,
			Message:   "X-Chunk-Ordinal is a whole number of at least 1",
			Retriable: false, Status: http.StatusBadRequest})
		return
	}
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, tooLargeAsBatch(err))
		return
	}
	format, err := httpx.RequestFormat(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := s.openBatch(ref)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	ds, err := datasetFor(dataset)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}

	idemKey := httpx.IdempotencyKey{Tenant: b.manifest.Tenant, Method: r.Method,
		Path: r.URL.Path + "?dataset=" + dataset, Key: key}
	bodyHash := httpx.BodyHash(body)
	if rec, outcome := s.idem.Check(idemKey, bodyHash); outcome != httpx.IdempotencyNew {
		if outcome == httpx.IdempotencyConflict {
			httpx.Fail(w, httpx.IdempotencyKeyReused())
			return
		}
		if b.applyFailKey == key {
			// The client is retrying the chunk that was applied and then
			// answered with 500. This is the correct behavior and it is what
			// 5xx_retried counts.
			s.gov.Inc5xxRetried()
		}
		httpx.WriteReplay(w, rec)
		return
	}

	// Chunk ordinals are strictly increasing per (batch, dataset). A skipped
	// ordinal is a gap and is refused with the expected and the seen value, so
	// a client can resend the chunk it lost instead of guessing.
	expected := b.chunkNext[ds.String()]
	if expected == 0 {
		expected = 1
	}
	if ordinal > expected {
		httpx.Fail(w, httpx.ChunkGap(expected, ordinal))
		return
	}

	// The dialect of the chunk's dataset, from the manifest's file entry when it
	// declared one.
	dial := s.chunkDialect(b, ds)
	ctx := httpx.WithCSVDialect(r.Context(), dial)
	records, err := httpx.DecodeRecords(r.WithContext(ctx), ds.String())
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if len(records) > MaxChunkRecords {
		e := &httpx.Error{Code: CodeBatchTooLarge,
			Message:   "one chunk carries at most " + strconv.Itoa(MaxChunkRecords) + " records",
			Retriable: false, Status: http.StatusRequestEntityTooLarge}
		httpx.Fail(w, e.WithDetail("limit", MaxChunkRecords).WithDetail("seen", len(records)))
		return
	}

	locate := pointerLocator(format, body, dial)
	parsedChunk, preconds, err := parseRESTRecords(ctx, ds, format, records, dial, locate)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}

	// A repeated or lower ordinal under a NEW idempotency key is a second
	// delivery of a chunk the twin already applied. It is applied again -
	// the store is content-addressed, so nothing changes - and counted, because
	// the counter is the evidence that a client's key is not a pure function of
	// its payload.
	chunkKey := ds.String() + "|" + strconv.FormatInt(ordinal, 10)
	if prev, ok := b.chunkApplied[chunkKey]; ok && prev != key {
		s.gov.IncDuplicateApplyAttempts()
	}

	sha, err := s.st.PutRaw(body)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	chunkName := fmt.Sprintf("chunk-%s-%04d", ds, ordinal)
	entry := manifestFile{Path: chunkName, Dataset: ds.String(), Format: format.String(),
		Profile: ProfileCanonical, Encoding: importer.EncodingUTF8}
	b.sourceSHA[chunkName] = sha
	prov := b.provenanceTemplate(entry, 0, httpx.RequestSeq(r.Context()))
	chunk := ordinal
	prov.ChunkOrdinal = &chunk

	res, err := s.apply(applyInput{
		dataset:      ds,
		parsed:       parsedChunk,
		prov:         prov,
		file:         chunkName,
		chunkOrdinal: ordinal,
		preconds:     preconds,
		locate:       locate,
		batch:        b,
	})
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	b.absorb(res)
	b.chunkApplied[chunkKey] = key
	if ordinal >= expected {
		b.chunkNext[ds.String()] = ordinal + 1
	}
	b.Files = append(b.Files, FileReceipt{
		Path: chunkName, Dataset: ds.String(), Format: format.String(),
		Profile: ProfileCanonical, Encoding: importer.EncodingUTF8,
		DeclaredRecords: -1, ParsedRecords: parsedChunk.Seen, SHA256: sha,
		Status: fileStatus(res.counts, nil), Codes: []string{},
		Counts: res.counts, ChunkOrdinal: ordinal,
	})

	status := statusForResults(res.records)
	payload := map[string]any{
		"batch_ref":     b.Ref,
		"dataset":       ds.String(),
		"chunk_ordinal": ordinal,
		"counts":        res.counts,
		"records":       res.records,
	}
	stored, err := renderRecords(httpx.FormatJSON, payload, httpx.DefaultCSVDialect())
	if err != nil {
		httpx.Fail(w, httpx.Internal("the chunk response could not be encoded"))
		return
	}
	s.idem.Put(idemKey, httpx.IdempotencyRecord{
		Status: status, Header: http.Header{"Content-Type": []string{httpx.MediaJSON}},
		Body: stored, BodyHash: bodyHash,
	})

	// The applied-then-500 chunk. The response is already stored, so the retry
	// path is exact.
	sig := "APPLYFAIL|POST /v1/ingest/records|" + ref + "|" + ds.String() + "|" +
		strconv.FormatInt(ordinal, 10)
	if s.gov.ApplyThenFail(sig) {
		b.applyFailKey = key
		httpx.Fail(w, httpx.Internal("the chunk was applied and the response was lost"))
		return
	}

	w.Header().Set("Content-Type", httpx.MediaJSON)
	w.WriteHeader(status)
	_, _ = w.Write(stored)
}

// chunkDialect returns the CSV dialect declared for a dataset in the batch's
// manifest, or the canonical dialect when the manifest declared none. It is the
// same lookup the file channel does per file entry, so a sender's declared
// delimiter and decimal separator bind on both channels.
func (s *Server) chunkDialect(b *batchState, ds model.Dataset) httpx.CSVDialect {
	for _, f := range b.manifest.Files {
		if f.Dataset == ds.String() && f.CSV != nil {
			return *f.CSV
		}
	}
	return httpx.DefaultCSVDialect()
}

// openBatch returns an open REST batch by ref.
func (s *Server) openBatch(ref string) (*batchState, error) {
	b, ok := s.batches[ref]
	if !ok {
		e := &httpx.Error{Code: CodeBatchNotFound, Message: "no such batch",
			Retriable: false, Status: http.StatusNotFound}
		return nil, e.WithDetail("batch_ref", ref)
	}
	if b.Status != BatchOpen {
		e := &httpx.Error{Code: CodeBatchClosed,
			Message:   "this batch is already committed, aborted or rejected",
			Retriable: false, Status: http.StatusConflict}
		return nil, e.WithDetail("batch_ref", ref).WithDetail("status", b.Status)
	}
	return b, nil
}

// handleCommit serves POST /v1/ingest/batches/{ref}/commit.
//
// Commit is where the matching engine runs and where the receipt is assembled,
// so the receipt can name the proposals and exceptions the batch's own records
// produced. The returned object is the same Receipt the file channel writes to
// receipts/<batch_id>/receipt.json.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := s.openBatch(ref)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	switch {
	case b.Counts.Seen == 0:
		b.Status = BatchAccepted
	case b.Counts.Rejected+b.Counts.Quarantined == 0:
		b.Status = BatchAccepted
	case b.Counts.Accepted+b.Counts.AcceptedWithWarning+b.Counts.SkippedUnchanged == 0:
		b.Status = BatchRejected
	default:
		b.Status = BatchPartiallyAccepted
	}
	if err := s.matchBatch(b); err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	b.Receipt = b.receipt(s.gov.NowVms())
	b.Receipt.ClosureOK = true
	run := s.run(b.manifest.RunID)
	run.note(b)
	// The receipt is written to receipts/<batch_id>/ on this channel too, not
	// only returned. Section 8.2 of the build specification describes one
	// receipt shape for both channels, and a reviewer who wants to see what a
	// run did should not have to know which channel each batch arrived on. The
	// response is the same object, so a client needs nothing from the disk.
	if err := s.writeReceipt(b); err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if err := assertClosure(b.Receipt); err != nil {
		b.Receipt.ClosureOK = false
		b.addCode(CodeClosureViolation)
		b.Receipt.Codes = b.Codes
		run.Status = "failed"
		run.addCode(CodeClosureViolation)
		e := &httpx.Error{Code: CodeClosureViolation, Message: err.Error(),
			Retriable: false, Status: http.StatusInternalServerError}
		httpx.Fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, b.Receipt)
}

// handleAbort serves POST /v1/ingest/batches/{ref}/abort.
//
// Abort closes the batch without matching and without an outbox emission. It
// does not unapply what its chunks already applied: the store is append-only and
// a delivered record is a fact, so an abort is a statement about the batch and
// not a rollback. The receipt says so, with status aborted, which is a more
// useful answer than a rollback the twin cannot honestly perform.
func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := s.openBatch(ref)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	b.Status = BatchAborted
	b.Receipt = b.receipt(s.gov.NowVms())
	b.Receipt.ClosureOK = assertClosure(b.Receipt) == nil
	s.run(b.manifest.RunID).note(b)
	if err := s.writeReceipt(b); err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	writeJSON(w, http.StatusOK, b.Receipt)
}

// handlePutRecord serves PUT /v1/ingest/records/{dataset}/{key}: the
// single-record correction path.
//
// It is net token negative on purpose - one record costs the same 20 vms a
// thousand-record chunk's base cost does - so correcting a handful of records is
// cheap and re-delivering a dataset one record at a time is not. If-Match
// carries the version the client believes is current and If-None-Match: * means
// the record must not exist; a failed precondition is 412 and writes nothing.
func (s *Server) handlePutRecord(w http.ResponseWriter, r *http.Request) {
	dataset := r.PathValue("dataset")
	key := decodeKey(r.PathValue("key"))

	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, tooLargeAsBatch(err))
		return
	}
	format, err := httpx.RequestFormat(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	ds, err := datasetFor(dataset)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	precond, err := parsePrecondition(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dial := httpx.CSVDialectFrom(r.Context())
	records, err := httpx.DecodeRecords(r, ds.String())
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if len(records) != 1 {
		httpx.Fail(w, httpx.MalformedBody("json_not_an_object",
			"a single-record PUT carries exactly one record"))
		return
	}
	locate := pointerLocator(format, body, dial)
	parsedOne, preconds, err := parseRESTRecords(r.Context(), ds, format, records, dial, locate)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if precond != nil {
		preconds = []*int{precond}
	}
	if doc, ok := parsedOne.Docs[1]; ok && doc.Key != key {
		e := &httpx.Error{Code: CodeKeyMismatch,
			Message:   "the record's natural key and the path disagree",
			Retriable: false, Status: http.StatusBadRequest}
		httpx.Fail(w, e.WithDetail("path_key", key))
		return
	}

	sha, err := s.st.PutRaw(body)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	// A single-record correction is its own delivery: it belongs to no batch, so
	// it is attributed to a transient batch that is never listed. The record's
	// provenance still names the run, the channel and the request sequence, so
	// the audit chain is complete.
	b := newBatchState(manifest{BatchID: "", RunID: s.cfg.RunID}, "", ChannelREST, 0)
	b.manifest.normalize()
	prov := store.Provenance{
		Channel:      ChannelREST,
		SourceSHA256: sha,
		Profile:      ProfileCanonical,
		Format:       format.String(),
		Encoding:     importer.EncodingUTF8,
		RunID:        s.cfg.RunID,
	}
	if seq := httpx.RequestSeq(r.Context()); seq > 0 {
		q := seq
		prov.RequestSeq = &q
	}
	res, err := s.apply(applyInput{
		dataset:  ds,
		parsed:   parsedOne,
		prov:     prov,
		file:     "put",
		preconds: preconds,
		locate:   locate,
		batch:    b,
	})
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	b.absorb(res)
	if err := s.matchBatch(b); err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if len(res.records) != 1 {
		httpx.Fail(w, httpx.Internal("the single-record path produced more than one result"))
		return
	}
	result := res.records[0]
	status := http.StatusOK
	for _, e := range result.Errors {
		if e.Code == CodeVersionConflict {
			status = http.StatusPreconditionFailed
		}
	}
	if status == http.StatusOK && result.Outcome == OutcomeRejected {
		status = http.StatusUnprocessableEntity
	}
	if result.Version > 0 {
		w.Header().Set("ETag", `"`+strconv.Itoa(result.Version)+`"`)
	}
	writeJSON(w, status, map[string]any{
		"dataset":           ds.String(),
		"natural_key":       key,
		"record":            result,
		"proposals_emitted": b.Proposals,
		"exceptions_opened": b.Exceptions,
	})
}

// parsePrecondition reads If-Match and If-None-Match into an if_version
// precondition. If-None-Match: * is version 0, which the store reads as "must
// not exist"; a quoted or bare version in If-Match is that version.
func parsePrecondition(r *http.Request) (*int, error) {
	if v := strings.TrimSpace(r.Header.Get(HeaderIfNoneMatch)); v != "" {
		if v != "*" {
			return nil, httpx.MalformedBody("if_none_match_unsupported",
				"If-None-Match is * on this surface")
		}
		zero := 0
		return &zero, nil
	}
	v := strings.Trim(strings.TrimSpace(r.Header.Get(HeaderIfMatch)), `"`)
	if v == "" || v == "*" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return nil, httpx.MalformedBody("if_match_invalid",
			"If-Match carries the version the client believes is current")
	}
	return &n, nil
}

// decodeKey accepts the two spellings of a composite natural key in a path: the
// canonical model.KeySeparator, percent-decoded by the router, and a vertical bar
// as a typeable alias. Leading zeros are significant in both.
func decodeKey(raw string) string {
	if strings.Contains(raw, "|") && !strings.Contains(raw, model.KeySeparator) {
		return strings.ReplaceAll(raw, "|", model.KeySeparator)
	}
	return raw
}
