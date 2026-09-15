package grader

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// Observed is everything an assertion may look at.
//
// It is deliberately a value read back from published surfaces: the two services'
// admin APIs, the three report files of the connector contract, the run's own
// directory tree, and the seed generator's golden set. No assertion opens a file
// the candidate wrote as code, greps for a library, or learns which language or
// channel was used. That is the whole reason the state digest is the primary
// assertion: it is a hash over logical content, so a CSV-over-file submission and
// an NDJSON-over-REST submission are the same answer.
type Observed struct {
	// Scenario, Seed and RunID identify the run.
	Scenario string
	Seed     int64
	RunID    string
	// SeedScenario is the dataset the ERP was seeded with.
	SeedScenario string
	// Chaos reports whether fault injection was on.
	Chaos bool
	// Golden is the scenario's pinned golden values.
	Golden Golden
	// Quotas are the published hard quotas of the scenario.
	ERPQuota, TwinQuota int

	// Dataset is the generated dataset, which carries the golden expected
	// exception set and the record counts.
	Dataset *seed.Dataset

	// ERP is the ERP's state.
	ERPMetrics   ERPMetrics
	ERPDocuments []ERPDocument
	ERPRequests  []ERPRequest
	ERPSOAPCalls []ERPSOAPCall
	ERPIdemKeys  []string

	// Twin is the twin's state.
	TwinMetrics    TwinMetrics
	TwinDigest     TwinDigest
	TwinExceptions []TwinException
	TwinProposals  []TwinProposal
	TwinByStatus   map[string]int
	TwinBatches    []TwinBatch
	TwinRun        *TwinRun
	// PlantedBatches are the batch ids the SCENARIO dropped into the twin's
	// inbox itself, which is how [Observed.NoActivity] tells a delivery the
	// harness planted from one the connector made. H2 plants three.
	PlantedBatches map[string]bool

	// Reports are the connector's three report files.
	Reports Reports

	// Connector records every invocation of the run.
	Connector []ConnectorRun

	// Dirs are the run's directories.
	Dirs Dirs
	// Evidence maps a short name to the evidence file the runner wrote, e.g.
	// "twin-exceptions.json". A check names one when it reports a finding.
	Evidence map[string]string

	// Secrets are the strings that must never appear in a candidate artifact:
	// the issued client secrets and the SOAP password. IBANs are read from the
	// dataset. BUILD-SPEC 17.7: matching is on full values only.
	Secrets []string

	// AuditChains holds the sampled audit chains of the traceability assertion,
	// keyed by the key they were fetched with. The runner fills it lazily.
	AuditChains map[string]*TwinAuditChain
	// fetchAudit fetches one chain. It is a function so an assertion can sample
	// without the Observed holding the whole twin.
	fetchAudit func(key string) (*TwinAuditChain, error)

	// records caches the records the record_field assertion asked for; a nil
	// entry is a record the twin does not hold, cached so a scenario asserting
	// twice over a missing record does not ask twice.
	records map[string]*TwinRecord
	// fetchRecord fetches one record with its revisions.
	fetchRecord func(dataset, key string) (*TwinRecord, error)
}

// Audit returns the audit chain of a key, fetching and caching it.
func (o *Observed) Audit(key string) (*TwinAuditChain, error) {
	if o.AuditChains == nil {
		o.AuditChains = map[string]*TwinAuditChain{}
	}
	if c, ok := o.AuditChains[key]; ok {
		return c, nil
	}
	if o.fetchAudit == nil {
		return nil, fmt.Errorf("no audit fetcher available")
	}
	c, err := o.fetchAudit(key)
	if err != nil {
		return nil, err
	}
	o.AuditChains[key] = c
	return c, nil
}

// Ev returns the evidence path registered under name, or the evidence directory
// itself when the name is unknown. It never returns an empty string, because a
// finding whose evidence path is empty is a finding a reviewer cannot check.
func (o *Observed) Ev(name string) string {
	if p, ok := o.Evidence[name]; ok {
		return p
	}
	return o.Dirs.Evidence
}

// NoActivity reports that the connector left no trace of ever having run: it
// made no request to either server, dropped no batch into the twin's inbox and
// created no document in the ERP.
//
// It exists because almost every invariant in the vocabulary is trivially true
// of such a run. No document was posted twice because none was posted. No
// credential leaked because nothing was written. The request budget held because
// no request was made. An untouched checkout, whose connector/run.sh is still
// the stub that prints its flags and exits 3, used to collect 29 of 100 points
// on exactly those grounds, which is indefensible in a debrief and unfair to a
// candidate who built half a connector.
//
// It is deliberately NOT "some counter is zero". Zero is frequently the correct
// answer and must keep passing: the replay invocation of H1 posts nothing on
// purpose, a well-behaved connector touches no admin surface and triggers no
// lockout, and a disciplined one wastes no retry. What this reports is the
// narrower and unambiguous fact that the run never happened at all.
//
// Two things are deliberately NOT activity. The connector's own report files: a
// run.json written by a connector that never contacted a server describes work
// that was not done. And the batches sitting in the twin: H2 opens by dropping
// two of them itself, so an inert run there shows two batches that nobody's
// connector produced, and counting them would let that scenario keep handing out
// points for free.
func (o *Observed) NoActivity() bool {
	switch {
	case o.ERPMetrics.RequestsTotal > 0, o.TwinMetrics.RequestsTotal > 0:
		return false
	case o.ERPMetrics.DocumentsCreated > 0, len(o.ERPDocuments) > 0:
		return false
	case o.deliveredUnplantedBatch():
		return false
	}
	return true
}

// deliveredUnplantedBatch reports whether the twin holds a batch that the
// scenario did not plant for itself.
//
// It is the one activity signal that cannot be read off a counter. The twin's
// batch count is contaminated: H2 drops three batches of its own before and
// between its connector invocations, so counting batches wholesale kept that
// scenario handing out points to an untouched checkout. Refusing to count any
// batch is the opposite error, and it is the one this repairs: phase 2 is the
// one phase doable with no HTTP at all - read the KRED files out of
// --erp-export-dir, write a batch into --twin-drop-dir - so a candidate who
// built the file channel and nothing else made real deliveries, has zero on
// every counter, and was being told they produced nothing.
//
// A batch whose id is empty is not attributed to anybody: the id is the twin's
// own key for the batch, and a delivery too malformed to have one is not
// evidence of who sent it.
func (o *Observed) deliveredUnplantedBatch() bool {
	for _, b := range o.TwinBatches {
		if b.ID != "" && !o.PlantedBatches[b.ID] {
			return true
		}
	}
	return false
}

// Reports holds the connector's three report files, parsed.
type Reports struct {
	// RunJSONPath, PostingsPath and ExceptionsPath are where they were expected.
	RunJSONPath    string
	PostingsPath   string
	ExceptionsPath string
	// RunJSONPresent and the two siblings report whether the file existed. A
	// missing file is a failed report_schema assertion, not a harness error.
	RunJSONPresent    bool
	PostingsPresent   bool
	ExceptionsPresent bool
	// RunJSONError, PostingsError and ExceptionsError carry a parse failure.
	RunJSONError    string
	PostingsError   string
	ExceptionsError string
	// Run is the parsed run.json.
	Run RunJSON
	// RunRaw is run.json's decoded object, for the report_value assertion, which
	// addresses fields by name rather than through a struct.
	RunRaw map[string]json.RawMessage
	// Postings and Exceptions are the parsed CSV rows.
	Postings   []PostingRow
	Exceptions []ExceptionRow
	// PostingHeader and ExceptionHeader are the header rows as delivered, so a
	// schema failure can show what was there instead.
	PostingHeader   []string
	ExceptionHeader []string
}

// RunJSON is the connector's self-reported run summary of BUILD-SPEC 9.
//
// The grader reads it to check the connector's own arithmetic against itself and
// against the servers, per BUILD-SPEC 17.6, and never as a source of truth about
// what happened: the servers' counters are the truth, and a disagreement is a
// reported finding worth no points.
type RunJSON struct {
	RunID                   string            `json:"run_id"`
	ConnectorVersion        string            `json:"connector_version"`
	ExitCode                *int              `json:"exit_code"`
	ChannelUsed             string            `json:"channel_used"`
	FormatsUsed             []string          `json:"formats_used"`
	ERPRequests             *int64            `json:"erp_requests"`
	ERP429s                 *int64            `json:"erp_429s"`
	ERP5xxRetried           *int64            `json:"erp_5xx_retried"`
	TwinRequests            *int64            `json:"twin_requests"`
	Twin429s                *int64            `json:"twin_429s"`
	SOAPCalls               *int64            `json:"soap_calls"`
	BatchesWritten          *int64            `json:"batches_written"`
	RecordsRead             *int64            `json:"records_read"`
	RecordsIngested         *int64            `json:"records_ingested"`
	RecordsRejected         *int64            `json:"records_rejected"`
	RecordsSkippedUnchanged *int64            `json:"records_skipped_unchanged"`
	ProposalsRead           *int64            `json:"proposals_read"`
	ProposalsPosted         *int64            `json:"proposals_posted"`
	ProposalsRejected       *int64            `json:"proposals_rejected"`
	ProposalsPending        *int64            `json:"proposals_pending"`
	DuplicateDocAttempts    *int64            `json:"duplicate_document_attempts"`
	WatermarkBefore         map[string]string `json:"watermark_before"`
	WatermarkAfter          map[string]string `json:"watermark_after"`
	FullLoad                *bool             `json:"full_load"`
	FxSnapshotToken         string            `json:"fx_snapshot_token"`
	FxCorrelationID         string            `json:"fx_correlation_id"`
	FxTruncated             *bool             `json:"fx_truncated"`
}

// PostingsHeader is the fixed header of postings.csv, per BUILD-SPEC 9. The
// column order is part of the contract because the file is an artifact a finance
// team reads, and a column that moved is a column somebody misread.
var PostingsHeader = []string{
	"proposal_id", "invoice_twin_id", "supplier_number", "supplier_invoice_number",
	"source_batch_id", "source_file_or_chunk", "source_line_or_ordinal",
	"idempotency_key", "erp_document_number", "erp_status", "http_status",
	"attempts", "idempotency_replay",
}

// ExceptionsHeader is the fixed header of exceptions.csv.
var ExceptionsHeader = []string{
	"subject_key", "subject_type", "stage", "code", "field", "message",
	"source_batch_id", "source_file_or_chunk", "source_line_or_ordinal",
}

// A PostingRow is one line of postings.csv: the whole audit chain of one
// document, which is what makes the file the artifact an integration engineer
// hands a customer's finance team.
type PostingRow struct {
	Line                  int
	ProposalID            string
	InvoiceTwinID         string
	SupplierNumber        string
	SupplierInvoiceNumber string
	SourceBatchID         string
	SourceFileOrChunk     string
	SourceLineOrOrdinal   string
	IdempotencyKey        string
	ERPDocumentNumber     string
	ERPStatus             string
	HTTPStatus            string
	Attempts              string
	IdempotencyReplay     string
}

// An ExceptionRow is one line of exceptions.csv.
type ExceptionRow struct {
	Line                int
	SubjectKey          string
	SubjectType         string
	Stage               string
	Code                string
	Field               string
	Message             string
	SourceBatchID       string
	SourceFileOrChunk   string
	SourceLineOrOrdinal string
}

// observe reads the whole observable state of the run and writes the evidence
// files every finding points at.
// observe reads the whole observable state of both services.
//
// The result is CACHED until the next step that can change it. Every assertion
// used to re-read everything, which on the baseline dataset means twenty full
// reads of a seventy-thousand record twin per scenario and turned a fifty-second
// connector run into a five-minute grading run. The cache is invalidated by
// [runner.invalidateObservation], which every mutating step calls, so a stale
// observation cannot be asserted against.
func (r *runner) observe() (*Observed, error) {
	if r.obs != nil {
		return r.obs, nil
	}
	obs, err := r.observeFresh()
	if err != nil {
		return nil, err
	}
	r.obs = obs
	return obs, nil
}

// invalidateObservation drops the cached observation. Called by every step that
// can move either service's state.
func (r *runner) invalidateObservation() { r.obs = nil }

func (r *runner) observeFresh() (*Observed, error) {
	// A final scan before reading, so a batch the connector published just
	// before exiting is picked up. The driver would do it too; doing it here
	// removes the race between "the connector exited" and "the driver ticked".
	_ = r.twc.post(twinPathScan, nil, nil)

	obs := &Observed{
		Scenario: r.sc.ID, Seed: r.seed, RunID: r.runID,
		SeedScenario: r.sc.SeedScenario, Chaos: r.outcome.Chaos,
		Golden: r.sc.Golden, ERPQuota: r.sc.ERPQuota, TwinQuota: r.sc.TwinQuota,
		Dataset: r.dataset, Dirs: r.dirs, Connector: r.outcome.Connector,
		PlantedBatches: r.plantedBatches,
		Evidence:       map[string]string{},
	}

	if err := r.erpc.get(erpPathMetrics, &obs.ERPMetrics); err != nil {
		return nil, err
	}
	var docs erpDocumentsBody
	if err := r.erpc.get(erpPathDocuments, &docs); err != nil {
		return nil, err
	}
	obs.ERPDocuments = docs.Documents
	var reqs erpRequestsBody
	if err := r.erpc.get(erpPathRequests, &reqs); err != nil {
		return nil, err
	}
	obs.ERPRequests = reqs.Requests
	var soap erpSOAPCallsBody
	if err := r.erpc.get(erpPathSOAPCalls, &soap); err != nil {
		return nil, err
	}
	obs.ERPSOAPCalls = soap.Calls
	var idem struct {
		Keys []string `json:"keys"`
	}
	if err := r.erpc.get(erpPathIdemKeys, &idem); err != nil {
		return nil, err
	}
	obs.ERPIdemKeys = idem.Keys

	if err := r.twc.get(twinPathMetrics, &obs.TwinMetrics); err != nil {
		return nil, err
	}
	if err := r.twc.get(twinPathDigest, &obs.TwinDigest); err != nil {
		return nil, err
	}
	var exc twinExceptionsBody
	if err := r.twc.get(twinPathExceptions+"?state=open", &exc); err != nil {
		return nil, err
	}
	obs.TwinExceptions = exc.Exceptions
	var props twinProposalsBody
	if err := r.twc.get(twinPathProposals, &props); err != nil {
		return nil, err
	}
	obs.TwinProposals = props.Proposals
	obs.TwinByStatus = props.ByStatus
	var batches twinBatchesBody
	if err := r.twc.get(twinPathBatches, &batches); err != nil {
		return nil, err
	}
	obs.TwinBatches = batches.Batches
	var run TwinRun
	if err := r.twc.get(twinRunPath(r.runID), &run); err == nil {
		obs.TwinRun = &run
	}

	obs.Reports = readReports(r.dirs.Reports)
	obs.Secrets = r.secrets()
	obs.fetchAudit = func(key string) (*TwinAuditChain, error) {
		var c TwinAuditChain
		if err := r.twc.get(twinAuditPath(key), &c); err != nil {
			return nil, err
		}
		return &c, nil
	}
	obs.fetchRecord = func(dataset, key string) (*TwinRecord, error) {
		body, err := r.twc.raw(twinRecordPath(dataset, key))
		if err != nil {
			return nil, err
		}
		rec, err := decodeRecord(body)
		if err != nil {
			return nil, err
		}
		obs.Evidence["twin-record-"+safeName(dataset+"-"+key)+".json"] =
			r.writeEvidence("twin-record-"+safeName(dataset+"-"+key)+".json", json.RawMessage(body))
		return rec, nil
	}

	// Evidence, written before any assertion runs, so a finding's path always
	// exists by the time somebody follows it.
	obs.Evidence["erp-metrics.json"] = r.writeEvidence("erp-metrics.json", obs.ERPMetrics)
	obs.Evidence["erp-documents.json"] = r.writeEvidence("erp-documents.json", obs.ERPDocuments)
	obs.Evidence["erp-requests.json"] = r.writeEvidence("erp-requests.json", obs.ERPRequests)
	obs.Evidence["erp-soap-calls.json"] = r.writeEvidence("erp-soap-calls.json", obs.ERPSOAPCalls)
	obs.Evidence["twin-metrics.json"] = r.writeEvidence("twin-metrics.json", obs.TwinMetrics)
	obs.Evidence["twin-digest.json"] = r.writeEvidence("twin-digest.json", obs.TwinDigest)
	obs.Evidence["twin-exceptions.json"] = r.writeEvidence("twin-exceptions.json", obs.TwinExceptions)
	obs.Evidence["twin-proposals.json"] = r.writeEvidence("twin-proposals.json", obs.TwinProposals)
	obs.Evidence["twin-batches.json"] = r.writeEvidence("twin-batches.json", obs.TwinBatches)
	obs.Evidence["twin-run.json"] = r.writeEvidence("twin-run.json", obs.TwinRun)
	obs.Evidence["connector-runs.json"] = r.writeEvidence("connector-runs.json", obs.Connector)
	obs.Evidence["expected-exceptions.json"] = r.writeEvidence("expected-exceptions.json",
		obs.Dataset.ExpectedExceptions)
	obs.Evidence["run.json"] = obs.Reports.RunJSONPath
	obs.Evidence["postings.csv"] = obs.Reports.PostingsPath
	obs.Evidence["exceptions.csv"] = obs.Reports.ExceptionsPath
	return obs, nil
}

// secrets returns the strings a candidate artifact must never contain verbatim.
func (r *runner) secrets() []string {
	out := []string{r.creds.ClientSecret, r.creds.SOAPPassword, r.twinSec}
	// The admin tokens are never handed to the connector, so finding one in a
	// candidate artifact would mean something stranger than a leak; including
	// them costs nothing and closes the case.
	out = append(out, r.adminERP, r.adminTwin)
	var kept []string
	for _, s := range out {
		if len(strings.TrimSpace(s)) >= 8 {
			kept = append(kept, s)
		}
	}
	sort.Strings(kept)
	return kept
}

// readReports reads the connector's three report files from dir.
func readReports(dir string) Reports {
	rp := Reports{
		RunJSONPath:    filepath.Join(dir, "run.json"),
		PostingsPath:   filepath.Join(dir, "postings.csv"),
		ExceptionsPath: filepath.Join(dir, "exceptions.csv"),
	}
	if raw, err := os.ReadFile(rp.RunJSONPath); err == nil {
		rp.RunJSONPresent = true
		if err := json.Unmarshal(raw, &rp.Run); err != nil {
			rp.RunJSONError = err.Error()
		}
		if err := json.Unmarshal(raw, &rp.RunRaw); err != nil && rp.RunJSONError == "" {
			rp.RunJSONError = err.Error()
		}
	}
	if rows, header, err := readCSV(rp.PostingsPath); err == nil {
		rp.PostingsPresent = true
		rp.PostingHeader = header
		for i, row := range rows {
			p := PostingRow{Line: i + 2}
			assignPosting(&p, header, row)
			rp.Postings = append(rp.Postings, p)
		}
	} else if !os.IsNotExist(err) {
		rp.PostingsPresent = true
		rp.PostingsError = err.Error()
	}
	if rows, header, err := readCSV(rp.ExceptionsPath); err == nil {
		rp.ExceptionsPresent = true
		rp.ExceptionHeader = header
		for i, row := range rows {
			e := ExceptionRow{Line: i + 2}
			assignException(&e, header, row)
			rp.Exceptions = append(rp.Exceptions, e)
		}
	} else if !os.IsNotExist(err) {
		rp.ExceptionsPresent = true
		rp.ExceptionsError = err.Error()
	}
	return rp
}

// readCSV reads a CSV file and returns its data rows and its header.
//
// FieldsPerRecord is relaxed so a row with the wrong arity is reported by the
// schema assertion rather than aborting the read: a report file with one bad line
// still carries the other four thousand, and a grader that refuses to read it
// tells the candidate nothing.
func readCSV(path string) ([][]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	records, err := cr.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	header := records[0]
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], bomUTF8))
	}
	return records[1:], header, nil
}

// columnIndex maps a header to column positions, so a report whose columns are
// present but reordered is still readable and the ordering itself is what the
// schema assertion complains about.
func columnIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return idx
}

// at returns row's value for a named column, "" when absent.
func at(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// assignPosting fills a posting row from a CSV record.
func assignPosting(p *PostingRow, header []string, row []string) {
	idx := columnIndex(header)
	p.ProposalID = at(row, idx, "proposal_id")
	p.InvoiceTwinID = at(row, idx, "invoice_twin_id")
	p.SupplierNumber = at(row, idx, "supplier_number")
	p.SupplierInvoiceNumber = at(row, idx, "supplier_invoice_number")
	p.SourceBatchID = at(row, idx, "source_batch_id")
	p.SourceFileOrChunk = at(row, idx, "source_file_or_chunk")
	p.SourceLineOrOrdinal = at(row, idx, "source_line_or_ordinal")
	p.IdempotencyKey = at(row, idx, "idempotency_key")
	p.ERPDocumentNumber = at(row, idx, "erp_document_number")
	p.ERPStatus = at(row, idx, "erp_status")
	p.HTTPStatus = at(row, idx, "http_status")
	p.Attempts = at(row, idx, "attempts")
	p.IdempotencyReplay = at(row, idx, "idempotency_replay")
}

// assignException fills an exception row from a CSV record.
func assignException(e *ExceptionRow, header []string, row []string) {
	idx := columnIndex(header)
	e.SubjectKey = at(row, idx, "subject_key")
	e.SubjectType = at(row, idx, "subject_type")
	e.Stage = at(row, idx, "stage")
	e.Code = at(row, idx, "code")
	e.Field = at(row, idx, "field")
	e.Message = at(row, idx, "message")
	e.SourceBatchID = at(row, idx, "source_batch_id")
	e.SourceFileOrChunk = at(row, idx, "source_file_or_chunk")
	e.SourceLineOrOrdinal = at(row, idx, "source_line_or_ordinal")
}

// i64 dereferences an optional counter, reporting whether it was present. An
// absent counter and a zero counter are different answers, and reporting them as
// the same is how a missing field becomes a silent pass.
func i64(p *int64) (int64, bool) {
	if p == nil {
		return 0, false
	}
	return *p, true
}

// formatInt renders an integer for a human-readable diff.
func formatInt(n int64) string { return strconv.FormatInt(n, 10) }

// bomUTF8 is the UTF-8 encoding of U+FEFF. A CSV writer in some languages emits
// it on the first cell, so it is trimmed from a header rather than being reported
// as a wrong column name: a byte order mark is a writer's habit, not a schema
// mistake, and failing a submission for it would be a language penalty.
const bomUTF8 = "\xef\xbb\xbf"
