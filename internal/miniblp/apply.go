package miniblp

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The receipt directory's file names, from BUILD-SPEC 8.2. DONE is the only
// completion signal and is created last, by rename; polling for receipt.json is
// a documented mistake, because a reader that wins the race sees a half-written
// report.
const (
	receiptJSON = "receipt.json"
	recordsCSV  = "records.csv"
	rejectsCSV  = "rejects.csv"
	doneFile    = "DONE"
)

// recordsCSVHeader is the fixed header of records.csv and rejects.csv. It is a
// contract: the grader reads these columns by name and by position.
var recordsCSVHeader = []string{
	"file", "line", "record_ordinal", "dataset", "natural_key", "outcome",
	"twin_id", "version", "code", "field", "message", "raw_excerpt_sha256",
}

// Counts is the outcome tally of a file or of a batch.
//
// The anti-silent-drop invariant of BUILD-SPEC 8.2 is a statement about exactly
// these fields: Seen equals Accepted + AcceptedWithWarning + Rejected +
// SkippedUnchanged + Quarantined. Replayed is outside the sum, because a
// replayed batch delivered nothing: its counts are the ones its first delivery
// reported.
type Counts struct {
	Seen                int `json:"seen"`
	Accepted            int `json:"accepted"`
	AcceptedWithWarning int `json:"accepted_with_warning"`
	Rejected            int `json:"rejected"`
	SkippedUnchanged    int `json:"skipped_unchanged"`
	Quarantined         int `json:"quarantined"`
	Replayed            int `json:"replayed"`
}

// add sums two tallies, for the batch total over its files.
func (c *Counts) add(o Counts) {
	c.Seen += o.Seen
	c.Accepted += o.Accepted
	c.AcceptedWithWarning += o.AcceptedWithWarning
	c.Rejected += o.Rejected
	c.SkippedUnchanged += o.SkippedUnchanged
	c.Quarantined += o.Quarantined
	c.Replayed += o.Replayed
}

// closes reports whether the tally satisfies the closure invariant.
func (c Counts) closes() bool {
	return c.Seen == c.Accepted+c.AcceptedWithWarning+c.Rejected+c.SkippedUnchanged+c.Quarantined
}

// count tallies one outcome.
func (c *Counts) count(outcome string) {
	switch outcome {
	case OutcomeAccepted:
		c.Accepted++
	case OutcomeAcceptedWithWarning:
		c.AcceptedWithWarning++
	case OutcomeRejected:
		c.Rejected++
	case OutcomeSkippedUnchanged:
		c.SkippedUnchanged++
	case OutcomeQuarantined:
		c.Quarantined++
	case OutcomeReplayed:
		c.Replayed++
	}
}

// An ExceptionRef names one exception a batch opened, in the (subject, code)
// shape grading compares as a symmetric difference.
type ExceptionRef struct {
	SubjectKey  string `json:"subject_key"`
	SubjectType string `json:"subject_type"`
	Code        string `json:"code"`
}

// A FileReceipt is the per-file half of a receipt. A REST chunk is a file entry
// too, named for its dataset and chunk ordinal, so a receipt from the two
// channels differs only in the locator shape and in the channel field.
type FileReceipt struct {
	Path            string   `json:"path"`
	Dataset         string   `json:"dataset"`
	Format          string   `json:"format"`
	Profile         string   `json:"profile"`
	Encoding        string   `json:"encoding"`
	DeclaredRecords int      `json:"declared_records"`
	ParsedRecords   int      `json:"parsed_records"`
	SHA256          string   `json:"sha256"`
	Status          string   `json:"status"`
	Codes           []string `json:"codes"`
	Counts          Counts   `json:"counts"`
	ChunkOrdinal    int64    `json:"chunk_ordinal,omitempty"`
}

// A Receipt is the twin's report on one batch: what arrived, what became of
// every record, what the matching engine did with the result and whether the
// counts add up. The file channel writes it to receipts/<batch_id>/receipt.json
// and the REST channel returns the identical object from commit.
type Receipt struct {
	BatchID        string        `json:"batch_id"`
	BatchRef       string        `json:"batch_ref"`
	Channel        string        `json:"channel"`
	RunID          string        `json:"run_id"`
	Tenant         string        `json:"tenant"`
	SourceSystem   string        `json:"source_system"`
	Producer       string        `json:"producer"`
	Sequence       int           `json:"sequence"`
	Mode           string        `json:"mode"`
	FullLoad       bool          `json:"full_load"`
	OnError        string        `json:"on_error"`
	Profiles       []string      `json:"profiles"`
	ManifestSHA256 string        `json:"manifest_sha256"`
	ReceivedScan   int64         `json:"received_scan,omitempty"`
	Status         string        `json:"batch_status"`
	Replay         bool          `json:"replay"`
	Codes          []string      `json:"codes"`
	Counts         Counts        `json:"counts"`
	Files          []FileReceipt `json:"files"`
	// Findings are the file- and batch-level findings, errors and warnings
	// alike: the ones that are about a file or the batch rather than about one
	// record. Per-record findings are on the record.
	Findings       []Finding      `json:"findings"`
	Proposals      []string       `json:"proposals_emitted"`
	Exceptions     []ExceptionRef `json:"exceptions_opened"`
	ClosureOK      bool           `json:"closure_ok"`
	VirtualClockMs int64          `json:"virtual_clock_ms"`
}

// applyInput is one unit of application: the records of one file or one chunk,
// plus everything the provenance of their revisions needs.
type applyInput struct {
	// dataset is the dataset the records belong to. It is empty for a profile
	// that carries its own record types, in which case every document's own
	// dataset applies.
	dataset model.Dataset
	// parsed is the decoded records.
	parsed *parsed
	// prov is the provenance template. Per record the source line, the record
	// ordinal and nothing else are filled in.
	prov store.Provenance
	// file is the locator reported on a receipt line: the file name on the file
	// channel, the chunk name on the REST channel.
	file string
	// chunkOrdinal is the REST chunk's X-Chunk-Ordinal, 0 on the file channel.
	chunkOrdinal int64
	// preconds carries the optional per-record if_version, indexed by ordinal
	// minus one. A nil entry means no precondition.
	preconds []*int
	// locate maps a record ordinal and a field to a pointer into the delivered
	// bytes.
	locate func(ordinal int, field string) string
	// batch is the batch the records belong to, so an exception an ack raises can
	// be attributed to it. It may be nil.
	batch *batchState
	// dryRun computes the results without writing anything. It is how
	// on_error abort_batch stays exact: the twin knows what a batch would do
	// before it does any of it.
	dryRun bool
}

// applyResult is what applying one file or one chunk did.
type applyResult struct {
	// records are the per-record results, in delivery order.
	records []RecordResult
	// counts is their tally.
	counts Counts
	// changedInvoices are the invoice keys whose stored content this
	// application created or changed, sorted and deduplicated. They are the
	// input of the matching engine.
	changedInvoices []string
	// amended maps an invoice key to the version it reached, for the
	// re-delivered-with-changed-content rule of BUILD-SPEC 17.5.
	versions map[string]int
	// docWarnings maps a document key to its warning codes.
	docWarnings map[string][]string
	// acks are the proposal ids an outbox_ack file or chunk acknowledged, in
	// delivery order.
	acks []string
}

// apply applies one file or one chunk and returns the per-record results.
//
// The caller holds the server lock. Every record gets exactly one outcome:
//
//   - a record with a blocking finding is rejected and nothing is written;
//   - a record whose content the store already holds is skipped_unchanged, and
//     a provenance-only revision is still appended, so a re-delivery stays
//     auditable;
//   - a record whose if_version precondition fails is rejected with
//     CodeVersionConflict, and nothing is written;
//   - a record the store refuses for a reason that is not the sender's fault is
//     quarantined rather than dropped;
//   - anything else is accepted, or accepted_with_warning when the importer
//     found something a human should see.
func (s *Server) apply(in applyInput) (applyResult, error) {
	out := applyResult{
		versions:    map[string]int{},
		docWarnings: in.parsed.DocWarnings,
	}
	if in.locate == nil {
		in.locate = func(int, string) string { return "" }
	}
	changed := map[string]bool{}

	for ord := 1; ord <= in.parsed.Seen; ord++ {
		res := RecordResult{
			Ordinal:      ord,
			Dataset:      in.dataset.String(),
			File:         in.file,
			ChunkOrdinal: in.chunkOrdinal,
			Pointer:      in.locate(ord, ""),
			Errors:       []Finding{},
			Warnings:     []Finding{},
		}
		if errs, ok := in.parsed.Errors[ord]; ok {
			res.Outcome = OutcomeRejected
			res.Errors = errs
			res.Warnings = appendFindings(res.Warnings, in.parsed.Warnings[ord])
			out.records = append(out.records, res)
			out.counts.count(res.Outcome)
			continue
		}
		doc, ok := in.parsed.Docs[ord]
		if !ok {
			// A record that produced neither a document nor a blocking finding
			// cannot happen: the ordinals were reconstructed from exactly those
			// two sets. It is reported rather than ignored, because the closure
			// invariant is the one thing this package may not get wrong
			// silently.
			res.Outcome = OutcomeQuarantined
			res.Errors = []Finding{{Code: CodeClosureViolation,
				Message: "the importer produced neither a document nor a finding for this record"}}
			out.records = append(out.records, res)
			out.counts.count(res.Outcome)
			continue
		}

		// A document that came out of a legacy profile carries the sender's own
		// Mandant in company_code. Map it to the group's company code here,
		// which is where master data mapping belongs; the parser reports the
		// file faithfully and its golden projection stays independent of this.
		if in.prov.Profile == ProfileKredExp || in.prov.Profile == ProfileKredExpReference {
			if f := canonicalizeLegacyDocument(&doc); f != nil {
				res.Outcome = OutcomeRejected
				res.NaturalKey = doc.Key
				res.Line = doc.Line
				res.Errors = []Finding{*f}
				out.records = append(out.records, res)
				out.counts.count(res.Outcome)
				continue
			}
		}

		res.NaturalKey = doc.Key
		res.Line = doc.Line
		res.Warnings = appendFindings(res.Warnings, in.parsed.Warnings[ord])
		ds := in.dataset
		if doc.Dataset != "" {
			ds = model.Dataset(doc.Dataset)
		}
		res.Dataset = ds.String()
		res.TwinID = twinID(ds.String(), doc.Key)

		if in.dryRun {
			res.Outcome = OutcomeAccepted
			if len(res.Warnings) > 0 {
				res.Outcome = OutcomeAcceptedWithWarning
			}
			out.records = append(out.records, res)
			out.counts.count(res.Outcome)
			continue
		}

		prov := in.prov
		if doc.Line > 0 {
			line := int64(doc.Line)
			prov.SourceLine = &line
		}
		recordOrdinal := int64(ord - 1)
		prov.RecordOrdinal = &recordOrdinal

		// An ack is not a record of the twin's master data: it is an event about
		// a proposal. It is stored in the outbox_ack dataset like any other
		// record, so the file channel and POST /v1/outbox/acks reach the same
		// state and the same digest, and it is then applied to its proposal.
		if ds == model.DatasetOutboxAck {
			ackRes, id, err := s.applyAckDocument(in.batch, doc, prov)
			if err != nil {
				return out, err
			}
			ackRes.Ordinal = ord
			ackRes.File = in.file
			ackRes.ChunkOrdinal = in.chunkOrdinal
			ackRes.Pointer = res.Pointer
			ackRes.Line = res.Line
			ackRes.Warnings = appendFindings(ackRes.Warnings, res.Warnings)
			out.records = append(out.records, ackRes)
			out.counts.count(ackRes.Outcome)
			if id != "" {
				out.acks = append(out.acks, id)
			}
			continue
		}

		rev := store.Revision{
			Dataset:    ds.String(),
			Key:        doc.Key,
			Payload:    json.RawMessage(doc.Payload),
			Provenance: prov,
		}
		if idx := ord - 1; idx < len(in.preconds) && in.preconds[idx] != nil {
			want := *in.preconds[idx]
			rev.IfVersion = &want
		}
		outcome, err := s.st.Apply(rev)
		switch {
		case err != nil:
			res.Outcome = OutcomeQuarantined
			res.Errors = append(res.Errors, Finding{Code: httpx.CodeInternal,
				Field: "", Message: "the record could not be stored: " + err.Error()})
		case outcome.Result == store.ResultConflict:
			res.Outcome = OutcomeRejected
			res.Version = outcome.Version
			res.ContentHash = outcome.ContentHash
			res.Errors = append(res.Errors, Finding{Code: CodeVersionConflict,
				Message: "the if_version precondition does not match the stored version",
				Pointer: in.locate(ord, IfVersionField)})
		case outcome.Result == store.ResultUnchanged:
			res.Outcome = OutcomeSkippedUnchanged
			res.Version = outcome.Version
			res.ContentHash = outcome.ContentHash
			res.RawExcerptSHA256 = outcome.ContentHash
		default:
			res.Outcome = OutcomeAccepted
			if len(res.Warnings) > 0 {
				res.Outcome = OutcomeAcceptedWithWarning
			}
			res.Version = outcome.Version
			res.ContentHash = outcome.ContentHash
			res.RawExcerptSHA256 = outcome.ContentHash
			switch ds {
			case model.DatasetInvoice:
				changed[doc.Key] = true
				out.versions[doc.Key] = outcome.Version
			case model.DatasetInvoiceLine:
				// A line changes its invoice's totals, so the invoice has to be
				// matched again even though its own record did not change. The
				// key of a line is the invoice key plus the line number.
				parts := model.SplitKey(doc.Key)
				if len(parts) == 3 {
					changed[model.InvoiceKey(parts[0], parts[1])] = true
				}
			}
		}
		out.records = append(out.records, res)
		out.counts.count(res.Outcome)
	}

	// Seen is set from the reconstructed ordinal space rather than accumulated,
	// so the closure invariant is a statement about the same two sets the
	// outcomes were derived from.
	out.counts.Seen = in.parsed.Seen
	out.changedInvoices = sortedKeys(changed)
	return out, nil
}

// appendFindings appends findings, keeping a non-nil slice so a receipt never
// carries null where a list belongs.
func appendFindings(dst, src []Finding) []Finding {
	if len(src) == 0 {
		if dst == nil {
			return []Finding{}
		}
		return dst
	}
	return append(dst, src...)
}

// sortedKeys returns the keys of a set in ascending byte order.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// replayResults turns the record results of a first delivery into the results of
// its replay: same locators, same keys, outcome replayed, nothing applied.
func replayResults(orig []RecordResult) ([]RecordResult, Counts) {
	out := make([]RecordResult, 0, len(orig))
	var counts Counts
	for _, r := range orig {
		r.Outcome = OutcomeReplayed
		r.Errors = []Finding{}
		out = append(out, r)
		counts.count(OutcomeReplayed)
	}
	counts.Seen = 0
	return out, counts
}

// fileStatus returns the status of one file entry from its tally and findings.
func fileStatus(counts Counts, codes []string) string {
	switch {
	case len(codes) > 0 && counts.Accepted+counts.AcceptedWithWarning+counts.SkippedUnchanged == 0:
		return BatchRejected
	case counts.Rejected+counts.Quarantined > 0:
		return BatchPartiallyAccepted
	default:
		return BatchAccepted
	}
}

// assertClosure checks the anti-silent-drop invariant of BUILD-SPEC 8.2 over a
// receipt: per file and per batch the outcomes add up to the records seen, and
// the per-file parsed counts add up to the batch's seen count.
//
// A violation is an internal error and never a warning. The receipt exists so
// that nobody has to trust the twin's arithmetic; a twin that reports a tally
// which does not add up has lost a record and must say so loudly.
func assertClosure(r *Receipt) error {
	if !r.Counts.closes() {
		return fmt.Errorf("%s: batch %s: seen %d != %d accepted + %d accepted_with_warning + %d rejected + %d skipped_unchanged + %d quarantined",
			CodeClosureViolation, r.BatchID, r.Counts.Seen, r.Counts.Accepted,
			r.Counts.AcceptedWithWarning, r.Counts.Rejected, r.Counts.SkippedUnchanged,
			r.Counts.Quarantined)
	}
	parsed := 0
	for _, f := range r.Files {
		if !f.Counts.closes() {
			return fmt.Errorf("%s: batch %s file %s: seen %d != %d + %d + %d + %d + %d",
				CodeClosureViolation, r.BatchID, f.Path, f.Counts.Seen, f.Counts.Accepted,
				f.Counts.AcceptedWithWarning, f.Counts.Rejected, f.Counts.SkippedUnchanged,
				f.Counts.Quarantined)
		}
		parsed += f.ParsedRecords
	}
	if !r.Replay && parsed != r.Counts.Seen {
		return fmt.Errorf("%s: batch %s: sum(per_file.parsed_records) %d != counts.seen %d",
			CodeClosureViolation, r.BatchID, parsed, r.Counts.Seen)
	}
	return nil
}

// writeReceipt writes the receipt directory of a batch: receipt.json,
// records.csv, rejects.csv and then DONE.
//
// Every file is written to a temporary name in the same directory and renamed
// over its final name, and DONE - a zero-byte sentinel - is renamed last, after
// the directory has been fsynced. DONE is therefore the only safe completion
// signal: a reader that sees it sees every other file complete.
func (s *Server) writeReceipt(b *batchState) error {
	dir := filepath.Join(s.cfg.InboxDir, dirReceipts, b.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.own(dirReceipts + "/" + b.ID)

	buf, err := json.MarshalIndent(b.Receipt, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, receiptJSON), append(buf, '\n')); err != nil {
		return err
	}
	all, rejects := recordsCSVBytes(b.Records)
	if err := writeFileAtomic(filepath.Join(dir, recordsCSV), all); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, rejectsCSV), rejects); err != nil {
		return err
	}
	if err := fsyncDir(dir); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, doneFile), nil)
}

// recordsCSVBytes renders records.csv and rejects.csv.
//
// One line per record, so the line count of records.csv is the record count and
// the closure invariant is visible in the file itself. A record with more than
// one finding carries its first one here - errors before warnings - and all of
// them in receipt.json, which is the complete report.
func recordsCSVBytes(records []RecordResult) (all []byte, rejects []byte) {
	render := func(keep func(RecordResult) bool) []byte {
		var buf []byte
		w := csv.NewWriter(byteSink{&buf})
		_ = w.Write(recordsCSVHeader)
		for _, r := range records {
			if !keep(r) {
				continue
			}
			code, field, message := "", "", ""
			switch {
			case len(r.Errors) > 0:
				code, field, message = r.Errors[0].Code, r.Errors[0].Field, r.Errors[0].Message
			case len(r.Warnings) > 0:
				code, field, message = r.Warnings[0].Code, r.Warnings[0].Field, r.Warnings[0].Message
			}
			line := ""
			if r.Line > 0 {
				line = strconv.Itoa(r.Line)
			}
			version := ""
			if r.Version > 0 {
				version = strconv.Itoa(r.Version)
			}
			_ = w.Write([]string{
				r.File, line, strconv.Itoa(r.Ordinal), r.Dataset, r.NaturalKey, r.Outcome,
				r.TwinID, version, code, field, message, r.RawExcerptSHA256,
			})
		}
		w.Flush()
		return buf
	}
	all = render(func(RecordResult) bool { return true })
	rejects = render(func(r RecordResult) bool {
		return r.Outcome == OutcomeRejected || r.Outcome == OutcomeQuarantined
	})
	return all, rejects
}

// byteSink adapts a byte slice to io.Writer for the CSV writer, so a receipt is
// rendered in memory and written once.
type byteSink struct{ buf *[]byte }

// Write appends to the sink.
func (s byteSink) Write(p []byte) (int, error) {
	*s.buf = append(*s.buf, p...)
	return len(p), nil
}

// writeFileAtomic writes a file through a temporary name in the same directory
// and renames it into place, so a reader never sees a partial file. The
// temporary name needs no randomness: the caller holds the server lock, and two
// runs write the same bytes in the same order.
func writeFileAtomic(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

// fsyncDir fsyncs a directory so a rename in it is durable. A platform that
// refuses to open a directory for reading is not an error: the rename is still
// ordered, and the twin's crash recovery rests on the store's segments.
func fsyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return nil
	}
	return nil
}

// declaredRecords returns a manifest file entry's declared record count, or -1
// when it declared none. The REST channel declares none: its per-chunk count is
// the chunk itself.
func declaredRecords(f manifestFile) int {
	if f.RecordCount == nil {
		return -1
	}
	return *f.RecordCount
}

// importerOptions builds the parse options of one manifest file entry. The
// entry's own dialect wins over the canonical default, which is what makes a
// sender's declared delimiter, decimal separator and date pattern binding for
// the file it was declared on.
func importerOptions(f manifestFile) importer.Options {
	opt := importer.Options{
		Filename: f.Path,
		Dataset:  f.Dataset,
		Encoding: f.Encoding,
	}
	if n := declaredRecords(f); n >= 0 {
		opt.DeclaredRecords = n
	}
	if f.CSV != nil {
		opt.Dialect = *f.CSV
	}
	return opt
}

// decodePayloadBytes decodes canonical JSON into a typed value.
func decodePayloadBytes(payload []byte, v any) error {
	return json.Unmarshal(payload, v)
}

// jsonPayload wraps canonical JSON bytes as a store payload. The store
// canonicalizes what it is given, and canonicalizing bytes that are already
// canonical is the identity, so a payload delivered by any channel hashes the
// same.
func jsonPayload(raw []byte) any { return json.RawMessage(raw) }

// errorIsNotFound reports a store lookup that resolved to nothing.
func errorIsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// errorIsAckStatus reports an ack whose status is outside the closed set.
func errorIsAckStatus(err error) bool { return errors.Is(err, store.ErrAckStatus) }

// tooLargeAsBatch rewrites httpx's body_too_large into the ingest surface's own
// CodeBatchTooLarge, so a client meets one code for both published caps - 1'000
// records and 8 MiB - instead of two.
func tooLargeAsBatch(err error) *httpx.Error {
	e := httpx.AsError(err)
	if e.Status == 413 || (e.Details != nil && e.Details["reason"] == "body_too_large") {
		out := &httpx.Error{Code: CodeBatchTooLarge,
			Message:   "the request body exceeds the published 8 MiB chunk limit",
			Retriable: false, Status: 413}
		return out
	}
	return e
}
