package miniblp

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The admin surface of the twin, BUILD-SPEC 8.5.
//
// Every route here is gated by the admin token, costs no virtual time, spends no
// quota, meets no injected fault and produces no log line, because httpx.Guard
// exempts the /admin/v1 prefix. That is what makes it safe for a grader to read
// metrics between two connector requests and for a reviewer to refresh a UI: an
// observer cannot perturb the transcript it is observing. The connector never
// receives the admin token.

// adminRecord serves GET /admin/v1/records/{dataset}/{key}: every revision of a
// record with the provenance of each, which is how a reviewer sees at a glance
// whether an older version overwrote a newer one.
func (s *Server) adminRecord(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	dataset := r.PathValue("dataset")
	key := decodeKey(r.PathValue("key"))
	if !store.ValidDataset(dataset) {
		httpx.Fail(w, &httpx.Error{Code: CodeDatasetUnknown, Message: "unknown dataset",
			Retriable: false, Status: http.StatusBadRequest})
		return
	}
	s.mu.Lock()
	history, err := s.st.History(dataset, key)
	version, contentHash, seq, deleted, revisions, exists := s.st.Head(dataset, key)
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if !exists && len(history) == 0 {
		httpx.Fail(w, notFound("no such record"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dataset":        dataset,
		"natural_key":    key,
		"twin_id":        twinID(dataset, key),
		"version":        version,
		"content_hash":   contentHash,
		"seq":            seq,
		"deleted":        deleted,
		"revision_count": revisions,
		"revisions":      history,
	})
}

// adminRawRecord serves GET /admin/v1/records/raw/{sha256}: the exact source
// bytes a record was parsed from, by content address. It is the far end of the
// audit chain: a receipt line names a content hash, and this hands back the
// bytes it was computed over.
func (s *Server) adminRawRecord(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	raw, err := s.st.GetRaw(r.PathValue("sha256"))
	s.mu.Unlock()
	if err != nil {
		if errorIsNotFound(err) {
			httpx.Fail(w, notFound("no raw source with this content address"))
			return
		}
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// adminBatches serves GET /admin/v1/batches, in first-seen order.
func (s *Server) adminBatches(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	out := make([]*batchState, 0, len(s.batchOrder))
	for _, id := range s.batchOrder {
		if b, ok := s.batches[id]; ok {
			out = append(out, b)
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"batches": out, "returned": len(out)})
}

// adminBatchRecords serves GET /admin/v1/batches/{id}/records?outcome=, the
// per-record outcomes of one delivery in delivery order.
func (s *Server) adminBatchRecords(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	id := r.PathValue("id")
	outcome := r.URL.Query().Get("outcome")
	s.mu.Lock()
	b, ok := s.batches[id]
	var records []RecordResult
	var receipt *Receipt
	if ok {
		receipt = b.Receipt
		for _, rec := range b.Records {
			if outcome != "" && rec.Outcome != outcome {
				continue
			}
			records = append(records, rec)
		}
	}
	s.mu.Unlock()
	if !ok {
		httpx.Fail(w, notFound("no such batch"))
		return
	}
	if records == nil {
		records = []RecordResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"batch_id": id, "returned": len(records), "records": records, "receipt": receipt,
	})
}

// adminRun serves GET /admin/v1/runs/{run_id}.
func (s *Server) adminRun(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	run, ok := s.runs[r.PathValue("run_id")]
	s.mu.Unlock()
	if !ok {
		httpx.Fail(w, notFound("no such run"))
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// adminRunLast serves GET /admin/v1/runs/last: the most recent run the twin saw
// a batch for.
func (s *Server) adminRunLast(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	run, ok := s.runs[s.lastRun]
	s.mu.Unlock()
	if !ok {
		httpx.Fail(w, notFound("this twin has not seen a run yet"))
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// adminExceptions serves GET /admin/v1/exceptions?state=open.
//
// This queue is the graded output of the twin: grading reads it, keys it by
// (subject_key, code) and compares it against the expected set as a symmetric
// difference. Warnings are deliberately absent from it - they are on the record,
// on the proposal and in the receipt - so a cautious implementation that
// surfaces an ambiguity is not punished for it and an implementation that
// rejects everything is not rewarded.
func (s *Server) adminExceptions(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	state := r.URL.Query().Get("state")
	if state == "all" {
		state = ""
	}
	code := r.URL.Query().Get("code")
	s.mu.Lock()
	list, err := s.st.Exceptions(state)
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	out := make([]store.Exception, 0, len(list))
	for _, e := range list {
		if code != "" && e.Code != code {
			continue
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": state, "returned": len(out), "exceptions": out,
		"codes": ExceptionCodes(),
	})
}

// adminProposals serves GET /admin/v1/proposals, the full proposal list with its
// ack state, in created_seq order. It is the admin twin of the outbox and, unlike
// the outbox, it costs nothing and shows every status.
func (s *Server) adminProposals(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	status := r.URL.Query().Get("status")
	s.mu.Lock()
	var out []store.Proposal
	err := s.st.ScanProposals(func(p store.Proposal) bool {
		if status == "" || p.Status == status {
			out = append(out, p)
		}
		return true
	})
	counts, cerr := s.st.CountProposalsByStatus()
	s.mu.Unlock()
	if err != nil || cerr != nil {
		httpx.Fail(w, httpx.Internal("the proposals could not be read"))
		return
	}
	if out == nil {
		out = []store.Proposal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"returned": len(out), "proposals": out, "by_status": counts,
	})
}

// An AuditChain is the answer of GET /admin/v1/audit/{key}: the whole chain from
// the delivered bytes to the ERP document number, in both directions.
type AuditChain struct {
	// Query is what was asked for and Resolved names what it turned out to be:
	// an invoice key, a proposal id or an ERP document number.
	Query    string `json:"query"`
	Resolved string `json:"resolved_as"`
	// InvoiceKey and Invoice are the twin's invoice and its revisions.
	InvoiceKey string           `json:"invoice_key"`
	Revisions  []store.Revision `json:"invoice_revisions"`
	// Sources are the content addresses of the delivered bytes every revision
	// came from, with the batch and locator that delivered them.
	Sources []AuditSource `json:"sources"`
	// Proposals are the proposals of the invoice, oldest first, with their acks.
	Proposals []store.Proposal `json:"proposals"`
	// Exceptions are the open and resolved exceptions about the invoice and its
	// proposals.
	Exceptions []store.Exception `json:"exceptions"`
	// ERPDocumentNumbers are the ERP documents the chain closed with.
	ERPDocumentNumbers []string `json:"erp_document_numbers"`
	// IdempotencyKeys are the keys the postings were made under.
	IdempotencyKeys []string `json:"idempotency_keys"`
}

// An AuditSource is one delivery in an audit chain.
type AuditSource struct {
	Version      int    `json:"version"`
	BatchID      string `json:"batch_id"`
	Channel      string `json:"channel"`
	SourceFile   string `json:"source_file,omitempty"`
	SourceLine   int64  `json:"source_line,omitempty"`
	ChunkOrdinal int64  `json:"chunk_ordinal,omitempty"`
	RecordOrd    int64  `json:"record_ordinal,omitempty"`
	SourceSHA256 string `json:"source_sha256,omitempty"`
	Profile      string `json:"profile,omitempty"`
	Format       string `json:"format,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	RawURL       string `json:"raw_url,omitempty"`
}

// adminAudit serves GET /admin/v1/audit/{key}.
//
// The key may be an invoice key, a proposal id or an ERP document number, and
// the chain resolves in both directions from any of them: source bytes and line
// to twin record versions to proposal to idempotency key to ERP document number,
// and back. Both directions matter because the two questions a finance team asks
// are "where did this posting come from" and "what happened to this invoice",
// and an interface that can only answer one of them is half an interface.
func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	query := decodeKey(r.PathValue("key"))
	s.mu.Lock()
	chain, ok, err := s.auditChain(query)
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if !ok {
		httpx.Fail(w, notFound("nothing in this twin resolves that key"))
		return
	}
	writeJSON(w, http.StatusOK, chain)
}

// auditChain resolves one audit chain. The caller holds the server lock.
func (s *Server) auditChain(query string) (AuditChain, bool, error) {
	chain := AuditChain{
		Query:              query,
		Revisions:          []store.Revision{},
		Sources:            []AuditSource{},
		Proposals:          []store.Proposal{},
		Exceptions:         []store.Exception{},
		ERPDocumentNumbers: []string{},
		IdempotencyKeys:    []string{},
	}

	// Direction one: the query is an invoice key.
	invoiceKey := ""
	if _, ok := s.st.Get(model.DatasetInvoice.String(), query); ok {
		invoiceKey, chain.Resolved = query, "invoice_key"
	}

	// Direction two: the query is a proposal id or an ERP document number. Both
	// resolve through the proposal index, which is the only place the twin knows
	// an ERP document number at all.
	var proposals []store.Proposal
	if err := s.st.ScanProposals(func(p store.Proposal) bool {
		proposals = append(proposals, p)
		return true
	}); err != nil {
		return chain, false, err
	}
	if invoiceKey == "" {
		for _, p := range proposals {
			if p.ProposalID == query {
				invoiceKey, chain.Resolved = p.InvoiceKey, "proposal_id"
				break
			}
			if p.Ack != nil && p.Ack.ExternalDocumentNumber == query {
				invoiceKey, chain.Resolved = p.InvoiceKey, "erp_document_number"
				break
			}
			for _, c := range p.AckConflicts {
				if c.ExternalDocumentNumber == query {
					invoiceKey, chain.Resolved = p.InvoiceKey, "erp_document_number"
					break
				}
			}
			if invoiceKey != "" {
				break
			}
		}
	}
	if invoiceKey == "" {
		return chain, false, nil
	}
	chain.InvoiceKey = invoiceKey

	history, err := s.st.History(model.DatasetInvoice.String(), invoiceKey)
	if err != nil {
		return chain, false, err
	}
	chain.Revisions = history
	for _, rev := range history {
		src := AuditSource{
			Version:      rev.Version,
			BatchID:      rev.Provenance.BatchID,
			Channel:      rev.Provenance.Channel,
			SourceFile:   rev.Provenance.SourceFile,
			SourceSHA256: rev.Provenance.SourceSHA256,
			Profile:      rev.Provenance.Profile,
			Format:       rev.Provenance.Format,
			RunID:        rev.Provenance.RunID,
		}
		if rev.Provenance.SourceLine != nil {
			src.SourceLine = *rev.Provenance.SourceLine
		}
		if rev.Provenance.ChunkOrdinal != nil {
			src.ChunkOrdinal = *rev.Provenance.ChunkOrdinal
		}
		if rev.Provenance.RecordOrdinal != nil {
			src.RecordOrd = *rev.Provenance.RecordOrdinal
		}
		if src.SourceSHA256 != "" {
			src.RawURL = "/admin/v1/records/raw/" + src.SourceSHA256
		}
		chain.Sources = append(chain.Sources, src)
	}

	for _, p := range proposals {
		if p.InvoiceKey != invoiceKey {
			continue
		}
		chain.Proposals = append(chain.Proposals, p)
		if p.Ack != nil {
			if p.Ack.ExternalDocumentNumber != "" {
				chain.ERPDocumentNumbers = appendCode(chain.ERPDocumentNumbers, p.Ack.ExternalDocumentNumber)
			}
			if p.Ack.IdempotencyKey != "" {
				chain.IdempotencyKeys = appendCode(chain.IdempotencyKeys, p.Ack.IdempotencyKey)
			}
		}
		for _, c := range p.AckConflicts {
			if c.ExternalDocumentNumber != "" {
				chain.ERPDocumentNumbers = appendCode(chain.ERPDocumentNumbers, c.ExternalDocumentNumber)
			}
		}
	}
	sort.Slice(chain.Proposals, func(i, j int) bool {
		return chain.Proposals[i].CreatedSeq < chain.Proposals[j].CreatedSeq
	})

	subjects := map[string]bool{invoiceKey: true}
	for _, p := range chain.Proposals {
		subjects[p.ProposalID] = true
	}
	if err := s.st.ScanExceptions("", func(e store.Exception) bool {
		if subjects[e.SubjectKey] {
			chain.Exceptions = append(chain.Exceptions, e)
		}
		return true
	}); err != nil {
		return chain, false, err
	}
	return chain, true, nil
}

// adminDigest serves GET /admin/v1/state/digest.
//
// The digest is the primary grading assertion: it covers logical content only -
// natural keys and payloads, in a fixed order - and excludes versions, sequence
// numbers, provenance, channel, format and every counter. The same data
// delivered by file or by REST, as CSV or as XML, in one batch or in twenty,
// therefore hashes identically.
func (s *Server) adminDigest(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	digest, err := s.st.Digest()
	counts := map[string]int{}
	for _, ds := range model.Datasets() {
		if n := s.st.Count(ds.String()); n > 0 {
			counts[ds.String()] = n
		}
	}
	high := s.st.HighSeq()
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"digest": digest, "datasets": counts, "high_seq": high,
	})
}

// TwinMetrics is the answer of GET /admin/v1/metrics: the Governor's request
// accounting, whose field names are the published contract, plus the twin's own
// state counters. The Governor's fields are embedded, so they stay at the top
// level of the JSON where the grader reads them.
type TwinMetrics struct {
	simclock.Metrics
	Scenario          string         `json:"scenario"`
	Seed              int64          `json:"seed"`
	Chaos             bool           `json:"chaos"`
	Scans             int64          `json:"inbox_scans"`
	Batches           int            `json:"batches"`
	Records           map[string]int `json:"records_by_dataset"`
	Proposals         map[string]int `json:"proposals_by_status"`
	ExceptionsOpen    int            `json:"exceptions_open"`
	ExceptionsTotal   int            `json:"exceptions_total"`
	ClosureViolations int            `json:"closure_violations"`
	StateDigest       string         `json:"state_digest"`
	HighSeq           int64          `json:"high_seq"`
	Runs              []string       `json:"runs"`
}

// adminMetrics serves GET /admin/v1/metrics.
func (s *Server) adminMetrics(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	m := TwinMetrics{
		Metrics:  s.gov.Metrics(),
		Scenario: s.cfg.Scenario,
		Seed:     s.cfg.Seed,
		Chaos:    s.cfg.Chaos,
		Scans:    s.scanCount,
		Batches:  len(s.batchOrder),
		Records:  map[string]int{},
		Runs:     append([]string{}, s.runList...),
	}
	for _, ds := range model.Datasets() {
		if n := s.st.Count(ds.String()); n > 0 {
			m.Records[ds.String()] = n
		}
	}
	if n := s.st.Count(DatasetUoMConversion); n > 0 {
		m.Records[DatasetUoMConversion] = n
	}
	m.Proposals, _ = s.st.CountProposalsByStatus()
	m.ExceptionsOpen, _ = s.st.CountExceptions(store.ExceptionOpen)
	m.ExceptionsTotal, _ = s.st.CountExceptions("")
	for _, b := range s.batches {
		for _, c := range b.Codes {
			if c == CodeClosureViolation {
				m.ClosureViolations++
			}
		}
	}
	m.HighSeq = s.st.HighSeq()
	digest, err := s.st.Digest()
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	m.StateDigest = digest
	if m.Proposals == nil {
		m.Proposals = map[string]int{}
	}
	writeJSON(w, http.StatusOK, m)
}

// adminReset serves POST /admin/v1/reset: it throws the run away and starts a
// new one.
//
// Everything goes: the store's segments and raw blobs, the inbox's processing,
// processed, rejected and receipt directories, the outbox, the batch and run
// reports, the idempotency store and the Governor with all its counters. A reset
// is what the grader does between scenarios, and a leftover from the previous
// scenario is exactly the kind of state that makes a flaky assertion, so the
// reset is total rather than selective.
func (s *Server) adminReset(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	s.mu.Lock()
	if err := s.st.Close(); err != nil {
		s.mu.Unlock()
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	victims := []string{
		s.storeDir,
		filepath.Join(s.cfg.InboxDir, dirIncoming),
		filepath.Join(s.cfg.InboxDir, dirProcessing),
		filepath.Join(s.cfg.InboxDir, dirProcessed),
		filepath.Join(s.cfg.InboxDir, dirRejected),
		filepath.Join(s.cfg.InboxDir, dirReceipts),
		s.cfg.OutboxDir,
	}
	for _, dir := range victims {
		if err := os.RemoveAll(dir); err != nil {
			s.mu.Unlock()
			httpx.Fail(w, httpx.AsError(err))
			return
		}
	}
	err := s.open()
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reset", "scenario": s.cfg.Scenario})
}

// SeedResult is the answer of POST /admin/v1/seed.
type SeedResult struct {
	Scenario     string `json:"scenario"`
	Seed         int64  `json:"seed"`
	CostCenters  int    `json:"cost_centers"`
	Conversions  int    `json:"uom_conversions"`
	StateDigest  string `json:"state_digest"`
	SourceSystem string `json:"source_system"`
}

// adminSeed serves POST /admin/v1/seed.
//
// It loads the two tables the twin is expected to already have when the
// connector starts: the cost centers and the unit-of-measure conversions. Both
// arrive as though another integration had loaded them long ago - provenance
// channel internal, source system "other-integration" - which is exactly what
// happens at a real customer, and it removes one pagination loop from the
// connector's request budget while keeping referential matching, and with it
// EXC_COST_CENTER_UNKNOWN and the two EXC_UOM_ codes, alive.
//
// Nothing else is seeded. The suppliers, purchase orders, exchange rates and
// invoices are the connector's job, and a twin that pre-seeded them would grade
// a connector that does nothing at all.
func (s *Server) adminSeed(w http.ResponseWriter, r *http.Request) {
	if !httpx.RequireAdminToken(w, r, s.cfg.AdminToken) {
		return
	}
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	req := struct {
		Scenario string `json:"scenario"`
		Seed     *int64 `json:"seed"`
	}{Scenario: s.cfg.Scenario}
	if len(body) > 0 {
		if err := decodePayloadBytes(body, &req); err != nil {
			httpx.Fail(w, httpx.MalformedBody("json_invalid",
				"the seed request is a JSON object with scenario and seed"))
			return
		}
	}
	if req.Scenario == "" {
		req.Scenario = seed.ScenarioS0
	}
	seedValue := s.cfg.Seed
	if req.Seed != nil {
		seedValue = *req.Seed
	}
	dataset, err := seed.Generate(req.Scenario, seedValue)
	if err != nil {
		httpx.Fail(w, &httpx.Error{Code: "SEED_FAILED", Message: err.Error(),
			Retriable: false, Status: http.StatusBadRequest})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prov := store.Provenance{
		Channel:      store.ChannelInternal,
		Profile:      "pre-seeded",
		SourceSystem: "other-integration",
		RunID:        s.cfg.RunID,
	}
	result := SeedResult{Scenario: req.Scenario, Seed: seedValue, SourceSystem: prov.SourceSystem}
	for _, cc := range dataset.CostCenters {
		if _, err := s.st.Apply(store.Revision{
			Dataset: model.DatasetCostCenter.String(), Key: cc.Key(), Payload: cc, Provenance: prov,
		}); err != nil {
			httpx.Fail(w, httpx.AsError(err))
			return
		}
		result.CostCenters++
	}
	for _, c := range dataset.UoMConversions {
		conv := UoMConversion{
			Material: c.Material, AltUoM: c.AltUoM, Numerator: c.Numerator,
			Denominator: c.Denominator, BaseUoM: c.BaseUoM,
		}
		if _, err := s.st.Apply(store.Revision{
			Dataset: DatasetUoMConversion, Key: conv.Key(), Payload: conv, Provenance: prov,
		}); err != nil {
			httpx.Fail(w, httpx.AsError(err))
			return
		}
		result.Conversions++
	}
	digest, err := s.st.Digest()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	result.StateDigest = digest
	writeJSON(w, http.StatusOK, result)
}

// notFound is the admin surface's 404.
func notFound(message string) *httpx.Error {
	return &httpx.Error{Code: CodeNotFound, Message: message,
		Retriable: false, Status: http.StatusNotFound}
}
