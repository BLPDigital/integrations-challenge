package miniblp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// adminTokenForTests is the admin credential every harness uses.
const adminTokenForTests = "test-admin-token"

// A harness is one twin under test: a server on temporary directories, an
// httptest listener in front of it and an access token for the versioned
// surface.
type harness struct {
	t            *testing.T
	srv          *Server
	ts           *httptest.Server
	token        string
	dataDir      string
	inboxDir     string
	outboxDir    string
	clientID     string
	clientSecret string
}

// newHarness starts a twin. Chaos is off unless the caller turns it on: a test
// that wants a fault asks for it by seed, and every other test would otherwise
// meet one at a signature it did not choose.
func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		DataDir:    filepath.Join(root, "data"),
		InboxDir:   filepath.Join(root, "inbox"),
		OutboxDir:  filepath.Join(root, "outbox"),
		Scenario:   "S0",
		Seed:       20260416,
		AdminToken: adminTokenForTests,
		RunID:      "run_test",
		Log:        io.Discard,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	h := &harness{t: t, srv: srv, ts: ts,
		dataDir: cfg.DataDir, inboxDir: cfg.InboxDir, outboxDir: cfg.OutboxDir,
		clientID: cfg.ClientID, clientSecret: cfg.ClientSecret}
	if h.clientID == "" && h.clientSecret == "" {
		// An unconfigured twin accepts any non-empty pair.
		h.clientID, h.clientSecret = "c", "s"
	}
	h.token = h.authToken()
	return h
}

// authToken fetches an access token.
func (h *harness) authToken() string {
	h.t.Helper()
	status, body := h.post("/v1/auth/token", mustJSON(h.t, map[string]string{
		"client_id": h.clientID, "client_secret": h.clientSecret,
	}), nil)
	if status != http.StatusOK {
		h.t.Fatalf("auth token: status %d body %s", status, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatalf("auth token: %v", err)
	}
	return out.AccessToken
}

// request performs one request and returns the status, the body and the
// response headers.
func (h *harness) request(method, path string, body []byte, hdr map[string]string) (int, []byte, http.Header) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, reader)
	if err != nil {
		h.t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", httpx.MediaJSON)
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out, resp.Header
}

// post is request with the POST method.
func (h *harness) post(path string, body []byte, hdr map[string]string) (int, []byte) {
	h.t.Helper()
	status, out, _ := h.request(http.MethodPost, path, body, hdr)
	return status, out
}

// get is request with the GET method.
func (h *harness) get(path string) (int, []byte) {
	h.t.Helper()
	status, out, _ := h.request(http.MethodGet, path, nil, nil)
	return status, out
}

// adminGet performs an admin GET with the admin token.
func (h *harness) adminGet(path string) (int, []byte) {
	h.t.Helper()
	status, out, _ := h.request(http.MethodGet, path, nil,
		map[string]string{httpx.HeaderAdminToken: adminTokenForTests})
	return status, out
}

// adminPost performs an admin POST with the admin token.
func (h *harness) adminPost(path string, body []byte) (int, []byte) {
	h.t.Helper()
	status, out, _ := h.request(http.MethodPost, path, body,
		map[string]string{httpx.HeaderAdminToken: adminTokenForTests})
	return status, out
}

// scan drives one inbox scan and returns its report.
func (h *harness) scan() ScanReport {
	h.t.Helper()
	status, body := h.adminPost("/admin/v1/inbox/scan", nil)
	if status != http.StatusOK {
		h.t.Fatalf("scan: status %d body %s", status, body)
	}
	var rep ScanReport
	if err := json.Unmarshal(body, &rep); err != nil {
		h.t.Fatalf("scan report: %v", err)
	}
	return rep
}

// digest returns the state digest.
func (h *harness) digest() string {
	h.t.Helper()
	status, body := h.adminGet("/admin/v1/state/digest")
	if status != http.StatusOK {
		h.t.Fatalf("digest: status %d body %s", status, body)
	}
	var out struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		h.t.Fatalf("digest: %v", err)
	}
	return out.Digest
}

// metrics returns the twin's metrics.
func (h *harness) metrics() TwinMetrics {
	h.t.Helper()
	status, body := h.adminGet("/admin/v1/metrics")
	if status != http.StatusOK {
		h.t.Fatalf("metrics: status %d body %s", status, body)
	}
	var m TwinMetrics
	if err := json.Unmarshal(body, &m); err != nil {
		h.t.Fatalf("metrics: %v", err)
	}
	return m
}

// openBatch opens a REST batch and returns its ref.
func (h *harness) openBatch(m map[string]any) (int, string) {
	h.t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		h.t.Fatalf("manifest: %v", err)
	}
	status, out := h.post("/v1/ingest/batches", body, nil)
	if status != http.StatusCreated && status != http.StatusOK {
		return status, string(out)
	}
	var res struct {
		Ref string `json:"batch_ref"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		h.t.Fatalf("open batch: %v (%s)", err, out)
	}
	return status, res.Ref
}

// chunk posts one records chunk.
func (h *harness) chunk(ref, dataset string, ordinal int, key string, records []any) (int, []byte) {
	h.t.Helper()
	body, err := json.Marshal(records)
	if err != nil {
		h.t.Fatalf("records: %v", err)
	}
	return h.post("/v1/ingest/batches/"+ref+"/records?dataset="+dataset, body, map[string]string{
		HeaderChunkOrdinal:         strconv.Itoa(ordinal),
		httpx.HeaderIdempotencyKey: key,
	})
}

// rawChunk posts one records chunk with an explicit content type and raw body,
// so a test can deliver the same bytes over a different wire format.
func (h *harness) rawChunk(ref, dataset string, ordinal int, key, contentType string, body []byte) (int, []byte) {
	h.t.Helper()
	status, out, _ := h.request(http.MethodPost,
		"/v1/ingest/batches/"+ref+"/records?dataset="+dataset, body, map[string]string{
			"Content-Type":             contentType,
			HeaderChunkOrdinal:         strconv.Itoa(ordinal),
			httpx.HeaderIdempotencyKey: key,
		})
	return status, out
}

// commit commits a REST batch and returns its receipt.
func (h *harness) commit(ref string) (int, Receipt) {
	h.t.Helper()
	status, body := h.post("/v1/ingest/batches/"+ref+"/commit", nil, nil)
	var r Receipt
	if status == http.StatusOK {
		if err := json.Unmarshal(body, &r); err != nil {
			h.t.Fatalf("receipt: %v (%s)", err, body)
		}
	}
	return status, r
}

// restManifest is a minimal REST manifest.
func restManifest(batchID string, files ...map[string]any) map[string]any {
	m := map[string]any{
		"manifest_version": "1",
		"batch_id":         batchID,
		"run_id":           "run_test",
		"tenant":           "acme-ch",
		"source_system":    "erp-prod",
		"producer":         "test/1.0",
		"mode":             ModeUpsert,
		"on_error":         OnErrorContinue,
	}
	if len(files) > 0 {
		m["files"] = files
	}
	return m
}

// writeBatchDir publishes a file batch the way the contract requires: the files
// and the manifest are written into a staging directory and the whole directory
// is renamed into incoming in one step.
func (h *harness) writeBatchDir(batchID string, m map[string]any, files map[string]string) {
	h.t.Helper()
	staging := filepath.Join(h.inboxDir, dirIncoming, stagingPrefix+batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		h.t.Fatalf("staging: %v", err)
	}
	entries := make([]map[string]any, 0, len(files))
	if declared, ok := m["files"].([]map[string]any); ok {
		entries = declared
	}
	for _, entry := range entries {
		name, _ := entry["path"].(string)
		content, ok := files[name]
		if !ok {
			continue
		}
		if err := os.WriteFile(filepath.Join(staging, name), []byte(content), 0o644); err != nil {
			h.t.Fatalf("write %s: %v", name, err)
		}
		if _, ok := entry["sha256"]; !ok {
			entry["sha256"] = model.Sha256Hex([]byte(content))
		}
	}
	// Files the manifest does not declare are written too, so a test can plant
	// an undeclared file.
	for name, content := range files {
		p := filepath.Join(staging, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			h.t.Fatalf("write %s: %v", name, err)
		}
	}
	if m != nil {
		buf, err := json.Marshal(m)
		if err != nil {
			h.t.Fatalf("manifest: %v", err)
		}
		if err := os.WriteFile(filepath.Join(staging, manifestName), buf, 0o644); err != nil {
			h.t.Fatalf("write manifest: %v", err)
		}
	}
	if err := os.Rename(staging, filepath.Join(h.inboxDir, dirIncoming, batchID)); err != nil {
		h.t.Fatalf("publish: %v", err)
	}
}

// fileManifest is a minimal file-channel manifest with one file entry per
// declared file.
func fileManifest(batchID string, files ...map[string]any) map[string]any {
	m := restManifest(batchID)
	entries := make([]map[string]any, 0, len(files))
	entries = append(entries, files...)
	m["files"] = entries
	return m
}

// fileEntry is one manifest file entry.
func fileEntry(path, dataset, format string, recordCount int) map[string]any {
	return map[string]any{
		"path":         path,
		"dataset":      dataset,
		"format":       format,
		"profile":      ProfileCanonical,
		"encoding":     "UTF-8",
		"record_count": recordCount,
	}
}

// mustJSON marshals a value for a test body.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return buf
}

// supplierRecord builds a canonical supplier record.
func supplierRecord(number, name string, blocked bool) map[string]any {
	return map[string]any{
		"supplier_number":    number,
		"name":               name,
		"country":            "CH",
		"currency":           "CHF",
		"iban":               "CH9300762011623852957",
		"vat_number":         "CHE-123.456.789",
		"payment_terms_days": 30,
		"blocked":            blocked,
		"change_seq":         1,
	}
}

// costCenterRecord builds a canonical cost center record.
func costCenterRecord(code, companyCode string) map[string]any {
	return map[string]any{
		"code":         code,
		"name":         "Cost center " + code,
		"company_code": companyCode,
		"valid_from":   "2026-01-01",
		"valid_to":     "",
		"blocked":      false,
	}
}

// invoiceRecord builds a canonical invoice record with one line.
func invoiceRecord(supplier, number, currency, gross, vat, lineAmount string) map[string]any {
	return map[string]any{
		"supplier_number":         supplier,
		"supplier_invoice_number": number,
		"company_code":            "CH10",
		"document_type":           "RE",
		"document_date":           "2026-03-16",
		"receipt_date":            "2026-03-17",
		"po_number":               "",
		"currency":                currency,
		"gross_amount":            gross,
		"vat_amount":              vat,
		"vat_code":                "V81",
		"payment_terms_days":      30,
		"discount_raw":            "",
		"discount_days":           0,
		"cost_center":             "CC-2200",
		"text":                    "",
		"lines": []map[string]any{{
			"line_no":     "00010",
			"gl_account":  "400000",
			"cost_center": "CC-2200",
			"quantity":    "1.000",
			"uom":         "EA",
			"unit_price":  lineAmount,
			"line_amount": lineAmount,
			"tax_code":    "V81",
		}},
	}
}
