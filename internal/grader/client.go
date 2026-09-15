package grader

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// AdminClient reads one service's admin surface.
//
// The admin surface is the only thing the grader talks to on a running service.
// It costs no quota, advances no virtual clock, meets no injected fault and
// produces no log line, so reading a counter cannot perturb the transcript that
// counter describes. The connector never receives an admin token.
type AdminClient struct {
	// BaseURL is the service root, e.g. "http://127.0.0.1:41235".
	BaseURL string
	// Token goes into the X-Admin-Token header. Both services gate their admin
	// surface on that header and never on a bearer token.
	Token string
	// Client is the HTTP client. Its timeout protects the harness from a wedged
	// service and grades nothing.
	Client *http.Client
}

// AdminHeader is the header both services gate their admin surface on.
const AdminHeader = "X-Admin-Token"

// adminTimeout bounds one admin request. It is a harness safety net: the admin
// surface does no work proportional to anything, so a slow answer means a wedged
// service.
const adminTimeout = 60 * time.Second

// newAdminClient builds a client for a service.
func newAdminClient(svc *Service) *AdminClient {
	return &AdminClient{
		BaseURL: svc.BaseURL,
		Token:   svc.AdminToken,
		Client:  &http.Client{Timeout: adminTimeout},
	}
}

// get reads path and decodes the JSON body into v. A non-2xx answer is an error
// carrying the body, because an admin surface's error body is the only clue
// available when a harness step fails.
func (c *AdminClient) get(path string, v any) error {
	return c.do(http.MethodGet, path, nil, v)
}

// post sends body to path and decodes the answer into v. A nil v discards it.
func (c *AdminClient) post(path string, body any, v any) error {
	var buf []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("grader: %s: %w", path, err)
		}
		buf = b
	}
	return c.do(http.MethodPost, path, buf, v)
}

// do performs one admin request.
func (c *AdminClient) do(method, path string, body []byte, v any) error {
	u := c.BaseURL + path
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return fmt.Errorf("grader: %s: %w", u, err)
	}
	req.Header.Set(AdminHeader, c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("grader: %s %s: %w", method, u, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("grader: %s %s: reading body: %w", method, u, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("grader: %s %s: status %d: %s", method, u, resp.StatusCode, snippet(raw))
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("grader: %s %s: decoding body: %w: %s", method, u, err, snippet(raw))
	}
	return nil
}

// raw reads path and returns the body bytes undecoded, for the state dumps the
// evidence tree holds verbatim.
func (c *AdminClient) raw(path string) ([]byte, error) {
	u := c.BaseURL + path
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("grader: %s: %w", u, err)
	}
	req.Header.Set(AdminHeader, c.Token)
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grader: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("grader: GET %s: %w", u, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("grader: GET %s: status %d: %s", u, resp.StatusCode, snippet(body))
	}
	return body, nil
}

// snippet trims a body for an error message.
func snippet(b []byte) string {
	const max = 400
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// The ERP admin paths the grader uses. They are string constants here rather
// than references into internal/erp so this package stays a reader of a published
// HTTP surface, which is the same surface a candidate could read.
const (
	erpPathMetrics     = "/erp-admin/v1/metrics"
	erpPathDocuments   = "/erp-admin/v1/documents"
	erpPathIdemKeys    = "/erp-admin/v1/idempotency-keys"
	erpPathRequests    = "/erp-admin/v1/requests"
	erpPathSOAPCalls   = "/erp-admin/v1/soap-calls"
	erpPathReset       = "/erp-admin/v1/reset"
	erpPathSeed        = "/erp-admin/v1/seed"
	erpPathAdvance     = "/erp-admin/v1/advance"
	erpPathDigest      = "/erp-admin/v1/state/digest"
	erpPathCredentials = "/erp-admin/v1/credentials"
)

// The twin admin paths the grader uses.
const (
	twinPathMetrics    = "/admin/v1/metrics"
	twinPathDigest     = "/admin/v1/state/digest"
	twinPathExceptions = "/admin/v1/exceptions"
	twinPathProposals  = "/admin/v1/proposals"
	twinPathBatches    = "/admin/v1/batches"
	twinPathRunLast    = "/admin/v1/runs/last"
	twinPathScan       = "/admin/v1/inbox/scan"
	twinPathReset      = "/admin/v1/reset"
	twinPathSeed       = "/admin/v1/seed"
	twinPathAudit      = "/admin/v1/audit/"
)

// twinRunPath is the per-run report path.
func twinRunPath(runID string) string { return "/admin/v1/runs/" + url.PathEscape(runID) }

// twinAuditPath is the audit chain path for a key.
func twinAuditPath(key string) string { return twinPathAudit + url.PathEscape(key) }

// ERPCredentials is the body of GET /erp-admin/v1/credentials.
type ERPCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	SOAPUsername string `json:"soap_username"`
	SOAPPassword string `json:"soap_password"`
	AdminToken   string `json:"admin_token"`
}

// ERPMetrics is the subset of GET /erp-admin/v1/metrics the grader asserts over.
// Unknown fields are ignored on purpose: the ERP may grow a counter without
// breaking a grading run.
type ERPMetrics struct {
	RequestsTotal             int64            `json:"requests_total"`
	ByEndpoint                map[string]int64 `json:"by_endpoint"`
	RateLimited               int64            `json:"429s"`
	FiveXXInjected            int64            `json:"5xx_injected"`
	FiveXXRetried             int64            `json:"5xx_retried"`
	QuotaUsed                 int64            `json:"quota_used"`
	QuotaLimit                int64            `json:"quota_limit"`
	RecordsReturned           int64            `json:"records_returned"`
	VirtualClockMs            int64            `json:"virtual_clock_ms"`
	DuplicateDocumentAttempts int64            `json:"duplicate_document_attempts"`
	Scenario                  string           `json:"scenario"`
	Seed                      int64            `json:"seed"`
	Chaos                     bool             `json:"chaos"`
	DocumentsCreated          int              `json:"documents_created"`
	BusinessRejections        map[string]int64 `json:"business_rejections"`
	IdempotencyKeys           int              `json:"idempotency_keys"`
	SOAPCalls                 int              `json:"soap_calls"`
	SOAPFaults                map[string]int64 `json:"soap_faults"`
	ExportFiles               []string         `json:"export_files"`
}

// TwinMetrics is the subset of GET /admin/v1/metrics the grader asserts over.
type TwinMetrics struct {
	RequestsTotal          int64            `json:"requests_total"`
	ByEndpoint             map[string]int64 `json:"by_endpoint"`
	RateLimited            int64            `json:"429s"`
	FiveXXInjected         int64            `json:"5xx_injected"`
	FiveXXRetried          int64            `json:"5xx_retried"`
	QuotaUsed              int64            `json:"quota_used"`
	QuotaLimit             int64            `json:"quota_limit"`
	VirtualClockMs         int64            `json:"virtual_clock_ms"`
	DuplicateApplyAttempts int64            `json:"duplicate_apply_attempts"`
	Scenario               string           `json:"scenario"`
	Seed                   int64            `json:"seed"`
	Chaos                  bool             `json:"chaos"`
	Scans                  int64            `json:"inbox_scans"`
	Batches                int              `json:"batches"`
	Records                map[string]int   `json:"records_by_dataset"`
	Proposals              map[string]int   `json:"proposals_by_status"`
	ExceptionsOpen         int              `json:"exceptions_open"`
	ExceptionsTotal        int              `json:"exceptions_total"`
	ClosureViolations      int              `json:"closure_violations"`
	StateDigest            string           `json:"state_digest"`
	HighSeq                int64            `json:"high_seq"`
	Runs                   []string         `json:"runs"`
}

// TwinDigest is the body of GET /admin/v1/state/digest.
type TwinDigest struct {
	Digest   string         `json:"digest"`
	Datasets map[string]int `json:"datasets"`
	HighSeq  int64          `json:"high_seq"`
}

// TwinException is one row of the twin's exception queue. It is the graded
// output of the twin per BUILD-SPEC 17.5, keyed by (subject_key, code).
type TwinException struct {
	SubjectKey          string            `json:"subject_key"`
	SubjectType         string            `json:"subject_type"`
	Stage               string            `json:"stage"`
	Code                string            `json:"code"`
	Field               string            `json:"field"`
	Message             string            `json:"message"`
	State               string            `json:"state"`
	SourceBatchID       string            `json:"source_batch_id"`
	SourceFileOrChunk   string            `json:"source_file_or_chunk"`
	SourceLineOrOrdinal string            `json:"source_line_or_ordinal"`
	Details             map[string]string `json:"details"`
}

// twinExceptionsBody is the envelope of GET /admin/v1/exceptions.
type twinExceptionsBody struct {
	State      string          `json:"state"`
	Returned   int             `json:"returned"`
	Exceptions []TwinException `json:"exceptions"`
	Codes      []string        `json:"codes"`
}

// TwinMoney mirrors the wire shape of model.Money: minor units, currency and the
// currency's minor-unit scale. Amounts never cross this boundary as a float.
type TwinMoney struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Scale       uint8  `json:"scale"`
}

// TwinProposal is one posting proposal as the admin surface serves it.
type TwinProposal struct {
	ProposalID          string    `json:"proposal_id"`
	InvoiceKey          string    `json:"invoice_key"`
	InvoiceVersion      int       `json:"invoice_version"`
	ProposalContentHash string    `json:"proposal_content_hash"`
	CreatedInRun        string    `json:"created_in_run"`
	CreatedSeq          int64     `json:"created_seq"`
	Status              string    `json:"status"`
	MatchedPO           string    `json:"matched_po"`
	MatchedCostCenter   string    `json:"matched_cost_center"`
	FxRateUsed          string    `json:"fx_rate_used"`
	FxRateFactor        int64     `json:"fx_rate_factor"`
	Amount              TwinMoney `json:"amount"`
	SourceAmount        TwinMoney `json:"source_amount"`
	SupersededBy        string    `json:"superseded_by"`
	Attempts            int       `json:"attempts"`
	Warnings            []string  `json:"warnings"`
	Ack                 *struct {
		Status                 string `json:"status"`
		ExternalDocumentNumber string `json:"external_document_number"`
		IdempotencyKey         string `json:"idempotency_key"`
		RunID                  string `json:"run_id"`
		Attempts               int    `json:"attempts"`
		HTTPStatus             int    `json:"http_status"`
		IdempotencyReplay      bool   `json:"idempotency_replay"`
		Error                  string `json:"error"`
		Reason                 string `json:"reason"`
	} `json:"ack"`
}

// twinProposalsBody is the envelope of GET /admin/v1/proposals.
type twinProposalsBody struct {
	Returned  int            `json:"returned"`
	Proposals []TwinProposal `json:"proposals"`
	ByStatus  map[string]int `json:"by_status"`
}

// ERPDocument is one AP document the ERP holds.
type ERPDocument struct {
	DocumentNumber        string `json:"document_number"`
	FiscalYear            int    `json:"fiscal_year"`
	PostingDate           string `json:"posting_date"`
	Revision              int    `json:"erp_revision"`
	Tenant                string `json:"tenant"`
	ExternalReference     string `json:"external_reference"`
	CompanyCode           string `json:"company_code"`
	SupplierNumber        string `json:"supplier_number"`
	SupplierInvoiceNumber string `json:"supplier_invoice_number"`
	DocumentType          string `json:"document_type"`
	DocumentDate          string `json:"document_date"`
	Currency              string `json:"currency"`
	GrossAmount           string `json:"gross_amount"`
	VATAmount             string `json:"vat_amount"`
	PONumber              string `json:"po_number"`
	CostCenter            string `json:"cost_center"`
	IdempotencyKey        string `json:"idempotency_key"`
}

// erpDocumentsBody is the envelope of GET /erp-admin/v1/documents.
type erpDocumentsBody struct {
	Documents []ERPDocument `json:"documents"`
	Count     int           `json:"count"`
}

// ERPRequest is one entry of the ERP's request log. It is how the efficiency and
// retry-discipline assertions are evaluated: the log is the server's own record
// of what the connector actually did, and it is order-independent evidence.
type ERPRequest struct {
	Sequence       int64  `json:"sequence"`
	Endpoint       string `json:"endpoint"`
	Signature      string `json:"signature"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	Status         int    `json:"status"`
	VirtualCostVms int64  `json:"virtual_cost_vms"`
	VirtualClockMs int64  `json:"virtual_clock_ms"`
	QuotaUsed      int64  `json:"quota_used"`
	// InjectedFault names the fault the Governor injected instead of calling the
	// handler: "RATE_LIMITED" or "SERVICE_UNAVAILABLE". It is empty both for a
	// response the handler produced and for an admission rejection, which is how
	// the retry-discipline assertion tells an injected 503 (retriable) from a
	// quota 503 (not retriable) without reading a response body it never saw.
	InjectedFault string `json:"injected_fault"`
	// Retriable is the flag the error body carried, nil when the accounting
	// middleware did not produce the body. It is the authority: a status code is
	// not, which is the whole reason every error in this landscape publishes a
	// flag.
	Retriable *bool `json:"retriable,omitempty"`
}

// erpRequestsBody is the envelope of GET /erp-admin/v1/requests.
type erpRequestsBody struct {
	Requests []ERPRequest `json:"requests"`
	Count    int          `json:"count"`
	Dropped  int64        `json:"dropped"`
}

// ERPSOAPCall is one entry of the ERP's SOAP call log.
type ERPSOAPCall struct {
	Sequence      int    `json:"sequence"`
	Signature     string `json:"signature"`
	CompanyCode   string `json:"company_code"`
	CorrelationID string `json:"correlation_id"`
	HTTPStatus    int    `json:"http_status"`
	FaultCode     string `json:"fault_code"`
	Severity      string `json:"severity"`
	RowCount      int    `json:"row_count"`
	Truncated     bool   `json:"truncated"`
}

// erpSOAPCallsBody is the envelope of GET /erp-admin/v1/soap-calls.
type erpSOAPCallsBody struct {
	Calls []ERPSOAPCall `json:"calls"`
	Count int           `json:"count"`
}

// TwinRun is the twin's per-run report.
type TwinRun struct {
	RunID      string            `json:"run_id"`
	Status     string            `json:"status"`
	Scans      int64             `json:"scans"`
	Batches    []string          `json:"batches"`
	FullLoad   bool              `json:"full_load"`
	Watermark  map[string]string `json:"watermark"`
	Proposals  int               `json:"proposals_emitted"`
	Exceptions int               `json:"exceptions_opened"`
	Codes      []string          `json:"codes"`
	Counts     Counts            `json:"counts"`
}

// Counts is the outcome tally of a batch or of one file inside it. The
// anti-silent-drop invariant of BUILD-SPEC 8.2 is a statement about exactly these
// numbers, and [Counts.Closes] is that statement.
type Counts struct {
	Seen                int `json:"seen"`
	Accepted            int `json:"accepted"`
	AcceptedWithWarning int `json:"accepted_with_warning"`
	Rejected            int `json:"rejected"`
	SkippedUnchanged    int `json:"skipped_unchanged"`
	Quarantined         int `json:"quarantined"`
	Replayed            int `json:"replayed"`
}

// Closes reports the anti-silent-drop invariant:
//
//	seen == accepted + accepted_with_warning + rejected + skipped_unchanged + quarantined
//
// Replayed is deliberately outside the sum: a replayed batch saw its records
// again and applied none of them, so counting a replay as an outcome would make
// the invariant hold for a batch that dropped everything.
func (c Counts) Closes() bool {
	return c.Seen == c.Accepted+c.AcceptedWithWarning+c.Rejected+c.SkippedUnchanged+c.Quarantined
}

// Sum returns the right-hand side of the closure invariant.
func (c Counts) Sum() int {
	return c.Accepted + c.AcceptedWithWarning + c.Rejected + c.SkippedUnchanged + c.Quarantined
}

// TwinFileReceipt is one file's or one chunk's half of a receipt.
type TwinFileReceipt struct {
	Path          string   `json:"path"`
	Dataset       string   `json:"dataset"`
	Format        string   `json:"format"`
	Profile       string   `json:"profile"`
	Encoding      string   `json:"encoding"`
	ParsedRecords int      `json:"parsed_records"`
	Counts        Counts   `json:"counts"`
	Codes         []string `json:"codes"`
}

// TwinBatch is one row of GET /admin/v1/batches, reduced to what the receipt
// closure and replay assertions need.
type TwinBatch struct {
	ID             string            `json:"batch_id"`
	Ref            string            `json:"batch_ref"`
	Channel        string            `json:"channel"`
	Status         string            `json:"status"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Scan           int64             `json:"received_scan"`
	Codes          []string          `json:"codes"`
	Counts         Counts            `json:"counts"`
	Files          []TwinFileReceipt `json:"files"`
}

// twinBatchesBody is the envelope of GET /admin/v1/batches.
type twinBatchesBody struct {
	Batches  []TwinBatch `json:"batches"`
	Returned int         `json:"returned"`
}

// TwinAuditChain is the subset of an audit chain the traceability assertion
// walks.
type TwinAuditChain struct {
	Query      string `json:"query"`
	ResolvedAs string `json:"resolved_as"`
	InvoiceKey string `json:"invoice_key"`
	Sources    []struct {
		Version      int    `json:"version"`
		BatchID      string `json:"batch_id"`
		Channel      string `json:"channel"`
		SourceFile   string `json:"source_file"`
		SourceLine   int64  `json:"source_line"`
		ChunkOrdinal int64  `json:"chunk_ordinal"`
		RecordOrd    int64  `json:"record_ordinal"`
		SourceSHA256 string `json:"source_sha256"`
		Profile      string `json:"profile"`
		Format       string `json:"format"`
		RunID        string `json:"run_id"`
		RawURL       string `json:"raw_url"`
	} `json:"sources"`
	Proposals          []TwinProposal `json:"proposals"`
	ERPDocumentNumbers []string       `json:"erp_document_numbers"`
	IdempotencyKeys    []string       `json:"idempotency_keys"`
}
