package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The admin surface is the UI's second door, for the reports the Server holds in
// memory and the store does not: the batches, their receipts, the per-record
// outcomes and the runs. The UI calls it in process - the twin's own handler,
// invoked directly, no socket, no client, no port - so what a page shows is
// byte-for-byte what a grader reading GET /admin/v1/batches would see, and the
// UI cannot drift from it.
//
// The calls are free: httpx.Guard exempts the /admin/v1 prefix, so they cost no
// quota, advance no virtual clock, meet no injected fault and write no log line.

// errNoAdminToken reports a UI that was configured without the twin's admin
// token. It is not a failure of the twin: the UI simply cannot reach the two
// views that need it, and says so rather than inventing their content.
var errNoAdminToken = errors.New(
	"this interface has no admin token, so the batch, receipt and audit views cannot be read")

// An adminError is a non-200 answer from the admin surface, carrying the
// canonical error body's code so a notice can name it.
type adminError struct {
	Status  int
	Code    string
	Message string
}

// Error implements error.
func (e *adminError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("the twin's admin surface answered %d", e.Status)
	}
	return fmt.Sprintf("the twin's admin surface answered %d %s: %s", e.Status, e.Code, e.Message)
}

// notFound reports whether the admin surface resolved the lookup to nothing.
func (e *adminError) notFound() bool { return e.Status == http.StatusNotFound }

// recorder is the http.ResponseWriter of an in-process call. It is a few lines
// of its own rather than net/http/httptest, which is a testing package and has
// no business in a served path.
type recorder struct {
	status int
	header http.Header
	body   bytes.Buffer
}

// Header implements http.ResponseWriter.
func (rec *recorder) Header() http.Header {
	if rec.header == nil {
		rec.header = http.Header{}
	}
	return rec.header
}

// Write implements http.ResponseWriter.
func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.body.Write(b)
}

// WriteHeader implements http.ResponseWriter.
func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
}

// adminGet performs one in-process GET against the twin's admin surface and
// decodes the answer into out.
//
// The path is passed both decoded and escaped, because a natural key may contain
// the store's U+001F separator or a slash - a purchase order number with a line
// suffix does - and only the escaped form routes correctly.
func (u *UI) adminGet(t Twin, r *http.Request, apiPath string, query url.Values, out any) error {
	if strings.TrimSpace(u.cfg.AdminToken) == "" {
		return errNoAdminToken
	}
	decoded, escaped := splitAdminPath(apiPath)
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: decoded, RawPath: escaped, RawQuery: query.Encode()},
		Header: http.Header{httpx.HeaderAdminToken: []string{u.cfg.AdminToken}},
		Host:   "ui.local",
	}
	req = req.WithContext(r.Context())
	rec := &recorder{}
	t.Handler().ServeHTTP(rec, req)
	if rec.status != http.StatusOK {
		e := &adminError{Status: rec.status}
		var body httpx.Error
		if json.Unmarshal(rec.body.Bytes(), &body) == nil {
			e.Code, e.Message = body.Code, body.Message
		}
		return e
	}
	if err := json.Unmarshal(rec.body.Bytes(), out); err != nil {
		return fmt.Errorf("web: decoding %s: %w", apiPath, err)
	}
	return nil
}

// splitAdminPath returns an admin path in both spellings: the decoded one, whose
// segments carry the raw bytes of a natural key, and the escaped one, whose
// segments are percent-encoded so a key containing a slash still names one
// segment.
func splitAdminPath(apiPath string) (decoded, escaped string) {
	parts := strings.Split(apiPath, "/")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = url.PathEscape(p)
	}
	return apiPath, strings.Join(out, "/")
}

// A batchSummary is one row of GET /admin/v1/batches.
//
// The Server keeps batch state in an unexported type, so the shape is restated
// here with the same JSON names it serves. Only the shape is restated: the
// numbers, the codes and the statuses are the twin's own, and the counts,
// per-file receipts and per-record outcomes reuse the twin's exported types
// verbatim rather than a copy of them.
type batchSummary struct {
	ID             string                `json:"batch_id"`
	Ref            string                `json:"batch_ref"`
	Channel        string                `json:"channel"`
	Status         string                `json:"status"`
	ManifestSHA256 string                `json:"manifest_sha256"`
	Scan           int64                 `json:"received_scan"`
	Codes          []string              `json:"codes"`
	Counts         miniblp.Counts        `json:"counts"`
	Files          []miniblp.FileReceipt `json:"files"`
}

// batchList is the answer of GET /admin/v1/batches.
type batchList struct {
	Batches  []batchSummary `json:"batches"`
	Returned int            `json:"returned"`
}

// batchRecords is the answer of GET /admin/v1/batches/{id}/records.
type batchRecords struct {
	BatchID  string                 `json:"batch_id"`
	Returned int                    `json:"returned"`
	Records  []miniblp.RecordResult `json:"records"`
	Receipt  *miniblp.Receipt       `json:"receipt"`
}

// runReport is the answer of GET /admin/v1/runs/{run_id} and /runs/last.
type runReport struct {
	RunID      string            `json:"run_id"`
	Status     string            `json:"status"`
	Scans      int64             `json:"scans"`
	Batches    []string          `json:"batches"`
	Counts     miniblp.Counts    `json:"counts"`
	FullLoad   bool              `json:"full_load"`
	Watermark  map[string]string `json:"watermark"`
	Proposals  int               `json:"proposals_emitted"`
	Exceptions int               `json:"exceptions_opened"`
	Codes      []string          `json:"codes"`
}

// batches reads every batch the twin has seen, in first-seen order.
func (u *UI) batches(t Twin, r *http.Request) ([]batchSummary, error) {
	var out batchList
	if err := u.adminGet(t, r, "/admin/v1/batches", nil, &out); err != nil {
		return nil, err
	}
	return out.Batches, nil
}

// batchDetail reads one batch's receipt and per-record outcomes, optionally
// filtered to one outcome, which is the filter BUILD-SPEC 14 asks for.
func (u *UI) batchDetail(t Twin, r *http.Request, id, outcome string) (*batchRecords, error) {
	q := url.Values{}
	if outcome != "" {
		q.Set("outcome", outcome)
	}
	var out batchRecords
	if err := u.adminGet(t, r, "/admin/v1/batches/"+id+"/records", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// lastRun reads the most recent run report, or nil when the twin has seen none.
func (u *UI) lastRun(t Twin, r *http.Request) (*runReport, error) {
	var out runReport
	err := u.adminGet(t, r, "/admin/v1/runs/last", nil, &out)
	var ae *adminError
	if errors.As(err, &ae) && ae.notFound() {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// auditChain resolves one audit chain through the twin's own resolver, which
// accepts an invoice key, a proposal id or an ERP document number and answers in
// both directions.
func (u *UI) auditChain(t Twin, r *http.Request, key string) (*miniblp.AuditChain, error) {
	var out miniblp.AuditChain
	if err := u.adminGet(t, r, "/admin/v1/audit/"+key, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// exceptionRefsOf is a small convenience for the batch view: the exceptions a
// receipt names, sorted, so the rendering never depends on delivery order.
func exceptionRefsOf(rec *miniblp.Receipt) []miniblp.ExceptionRef {
	if rec == nil {
		return nil
	}
	out := append([]miniblp.ExceptionRef(nil), rec.Exceptions...)
	sortExceptionRefs(out)
	return out
}

// datasetsWithSegments returns every dataset the store has a segment for, in
// ascending name order, minus the two the store keeps for itself and shows in
// their own views.
func datasetsWithSegments(st *store.Store) []string {
	out := make([]string, 0, 8)
	for _, ds := range st.Datasets() {
		if ds == store.DatasetProposal || ds == store.DatasetException {
			continue
		}
		out = append(out, ds)
	}
	return out
}
