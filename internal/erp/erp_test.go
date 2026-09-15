package erp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// testSeed is the seed every test uses unless it needs another one. It is a
// constant, like everything else in this package: a test that drew its own seed
// would be a test that fails once in a hundred runs.
const testSeed int64 = 20260416

// harness is a server plus the request helpers a test needs. Requests go through
// Server.ServeHTTP with an httptest recorder rather than over a socket: the whole
// service is a pure function of its requests, so a loopback listener would add
// nothing but flakiness.
type harness struct {
	t     *testing.T
	srv   *Server
	creds Credentials
	token string
}

// newHarness builds a server for a test. The scenario defaults to S0, the log is
// discarded and the export drop is disabled unless the test asks for one.
func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	cfg := Config{
		Scenario: seed.ScenarioS0,
		Seed:     testSeed,
		Chaos:    false,
		Log:      io.Discard,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &harness{t: t, srv: srv, creds: srv.Credentials()}
	h.token = h.authToken()
	return h
}

// do performs one request and returns the recorder.
func (h *harness) do(req *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

// authToken obtains an access token with the server's own credentials.
func (h *harness) authToken() string {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{
		"client_id": h.creds.ClientID, "client_secret": h.creds.ClientSecret,
	})
	req := httptest.NewRequest(http.MethodPost, RouteAuthToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", httpx.MediaJSON)
	rec := h.do(req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("auth token: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("auth token: %v", err)
	}
	return resp.AccessToken
}

// get performs an authenticated GET.
func (h *harness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	return h.do(req)
}

// list performs an authenticated GET and decodes the list envelope.
func (h *harness) list(path string) (*httptest.ResponseRecorder, ListEnvelope) {
	h.t.Helper()
	rec := h.get(path)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var env ListEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		h.t.Fatalf("GET %s: decode envelope: %v", path, err)
	}
	return rec, env
}

// post performs an authenticated POST with an idempotency key. An empty key omits
// the header entirely.
func (h *harness) post(path string, body any, idempotencyKey string) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf []byte
	switch v := body.(type) {
	case nil:
	case string:
		buf = []byte(v)
	case []byte:
		buf = v
	default:
		var err error
		buf, err = json.Marshal(v)
		if err != nil {
			h.t.Fatalf("marshal request: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", httpx.MediaJSON)
	req.Header.Set("Authorization", "Bearer "+h.token)
	if idempotencyKey != "" {
		req.Header.Set(httpx.HeaderIdempotencyKey, idempotencyKey)
	}
	return h.do(req)
}

// postSingle posts one item and decodes the result.
func (h *harness) postSingle(item PostingItem, key string) (*httptest.ResponseRecorder, PostingResult) {
	h.t.Helper()
	rec := h.post(RouteDocuments, item, key)
	var res PostingResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			h.t.Fatalf("decode posting result: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, res
}

// postBatch posts a batch and decodes the response.
func (h *harness) postBatch(items []PostingItem, key string) (*httptest.ResponseRecorder, BatchResponse) {
	h.t.Helper()
	rec := h.post(RouteDocumentsBatch, map[string]any{"items": items}, key)
	var resp BatchResponse
	if rec.Code == http.StatusOK || rec.Code == http.StatusMultiStatus {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			h.t.Fatalf("decode batch response: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, resp
}

// adminGet performs an admin GET with the admin token.
func (h *harness) adminGet(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(httpx.HeaderAdminToken, h.creds.AdminToken)
	return h.do(req)
}

// adminPost performs an admin POST with the admin token.
func (h *harness) adminPost(path string, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(httpx.HeaderAdminToken, h.creds.AdminToken)
	return h.do(req)
}

// metrics reads the admin metrics.
func (h *harness) metrics() AdminMetrics {
	h.t.Helper()
	rec := h.adminGet(AdminPathMetrics)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("metrics: status %d", rec.Code)
	}
	var m AdminMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		h.t.Fatalf("metrics: %v", err)
	}
	return m
}

// errorBody decodes the canonical error body.
func (h *harness) errorBody(rec *httptest.ResponseRecorder) httpx.Error {
	h.t.Helper()
	var e httpx.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		h.t.Fatalf("decode error body: %v (body %s)", err, rec.Body.String())
	}
	return e
}

// data returns the loaded dataset, for tests that need a seeded fixture.
func (h *harness) dataset() *data {
	d, _ := h.srv.state()
	return d
}

// goodItem builds a posting item that the ERP accepts: an unblocked creditor, no
// purchase order reference, a posting date inside the open period and no cost
// center. Tests mutate one field at a time from here, so a rejection test can
// only fail for the reason it names.
func (h *harness) goodItem(ref string) PostingItem {
	h.t.Helper()
	d := h.dataset()
	var supplier string
	for _, s := range d.suppliers.items {
		if !s.Blocked {
			supplier = s.SupplierNumber
			break
		}
	}
	if supplier == "" {
		h.t.Fatal("no unblocked creditor in the seeded master")
	}
	return PostingItem{
		ItemKey:               ref,
		ExternalReference:     ref,
		CompanyCode:           model.CompanyCodeCH10,
		SupplierNumber:        supplier,
		SupplierInvoiceNumber: "0004711",
		DocumentType:          model.DocumentTypeInvoice,
		DocumentDate:          "2026-03-29",
		PostingDate:           "2026-03-29",
		Currency:              "CHF",
		GrossAmount:           model.MustDecimal("1250.00"),
		VATAmount:             model.MustDecimal("89.35"),
	}
}

// blockedSupplier returns the first payment-blocked creditor of the seeded
// master.
func (h *harness) blockedSupplier() string {
	h.t.Helper()
	for _, s := range h.dataset().suppliers.items {
		if s.Blocked {
			return s.SupplierNumber
		}
	}
	h.t.Fatal("no blocked creditor in the seeded master")
	return ""
}

// poWithStatus returns the first purchase order in the given status, together
// with the exact sum of its lines.
func (h *harness) poWithStatus(status string) (model.PurchaseOrder, model.Decimal) {
	h.t.Helper()
	d := h.dataset()
	for _, po := range d.purchaseOrders.items {
		if po.Status != status {
			continue
		}
		total, ok := d.poNetTotal[seed.CanonicalPONumber(po.PONumber)]
		if !ok {
			continue
		}
		return po, total
	}
	h.t.Fatalf("no purchase order with status %s in the seeded master", status)
	return model.PurchaseOrder{}, model.Decimal{}
}

// costCenterOfOtherCompany returns a cost center configured for a company code
// other than want.
func (h *harness) costCenterOfOtherCompany(want string) string {
	h.t.Helper()
	for _, cc := range h.dataset().set.CostCenters {
		if cc.CompanyCode != want {
			return cc.Code
		}
	}
	h.t.Fatalf("no cost center outside company code %s", want)
	return ""
}

// decodeJSON is json.Unmarshal, named so a test reads as prose.
func decodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }
