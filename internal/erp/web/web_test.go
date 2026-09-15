package web

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// testSeed is the seed every test uses. It is a constant, like everything else in
// this project: a test that drew its own seed would be a test that fails once in
// a hundred runs.
const testSeed int64 = 20260416

// harness is a real ERP with the UI mounted on it and the observer in front of
// it, plus the request helpers a test needs.
//
// It drives the real server rather than a stub source, because the whole point of
// the UI is what it shows about a run, and a stub would let the UI agree with a
// fiction. Requests go through the handler chain with an httptest recorder rather
// than over a socket: the service is a pure function of its requests, so a
// loopback listener would add nothing but flakiness.
type harness struct {
	t       *testing.T
	ui      *UI
	srv     *erp.Server
	handler http.Handler
	creds   erp.Credentials
	set     *seed.Dataset
	token   string
}

// newHarness builds the S0 landscape with the UI attached and observing.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ui := New(Options{})
	srv, err := erp.New(erp.Config{
		Scenario: seed.ScenarioS0,
		Seed:     testSeed,
		Chaos:    false,
		Log:      io.Discard,
		UI:       ui,
	})
	if err != nil {
		t.Fatalf("erp.New: %v", err)
	}
	ui.Attach(NewAdminSource(srv))
	set, err := seed.Generate(seed.ScenarioS0, testSeed)
	if err != nil {
		t.Fatalf("seed.Generate: %v", err)
	}
	h := &harness{
		t: t, ui: ui, srv: srv, handler: ui.Observe(srv),
		creds: srv.Credentials(), set: set,
	}
	h.token = h.authToken()
	return h
}

// get performs one request through the whole chain.
func (h *harness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// apiGet performs an authenticated read on the ERP's own REST surface, which is
// how a test puts entries into the request log the UI renders.
func (h *harness) apiGet(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// page fetches a UI page and fails unless it answered 200 with an HTML body.
func (h *harness) page(path string) string {
	h.t.Helper()
	rec := h.get(path)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("GET %s: status %d, want 200; body %s", path, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		h.t.Fatalf("GET %s: content type %q, want text/html", path, ct)
	}
	return rec.Body.String()
}

// authToken obtains an access token with the server's own credentials.
func (h *harness) authToken() string {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{
		"client_id": h.creds.ClientID, "client_secret": h.creds.ClientSecret,
	})
	req := httptest.NewRequest(http.MethodPost, erp.RouteAuthToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", httpx.MediaJSON)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("auth token: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp erp.TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		h.t.Fatalf("auth token: %v", err)
	}
	return resp.AccessToken
}

// goodItem builds a posting item the ERP accepts: an unblocked creditor, no
// purchase order reference, a posting date inside the open period and no cost
// center, which is the documented correct shape when the source leaves the
// position cost center undefined.
func (h *harness) goodItem(ref string) erp.PostingItem {
	h.t.Helper()
	supplier := ""
	for _, s := range h.set.Suppliers {
		if !s.Blocked {
			supplier = s.SupplierNumber
			break
		}
	}
	if supplier == "" {
		h.t.Fatal("no unblocked creditor in the seeded master")
	}
	return erp.PostingItem{
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

// post posts one item under an idempotency key and returns the per-item result.
func (h *harness) post(item erp.PostingItem, key string) (int, erp.PostingResult) {
	h.t.Helper()
	body, err := json.Marshal(item)
	if err != nil {
		h.t.Fatalf("marshal posting item: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, erp.RouteDocuments, bytes.NewReader(body))
	req.Header.Set("Content-Type", httpx.MediaJSON)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set(httpx.HeaderIdempotencyKey, key)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	var res erp.PostingResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			h.t.Fatalf("decode posting result: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec.Code, res
}

// metrics reads the ERP's own counters, which is what the UI must agree with.
func (h *harness) metrics() erp.AdminMetrics {
	h.t.Helper()
	m, err := NewAdminSource(h.srv).Metrics()
	if err != nil {
		h.t.Fatalf("metrics: %v", err)
	}
	return m
}

// mustContain fails unless body contains every want.
func mustContain(t *testing.T, what, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s: expected content %q not found", what, w)
		}
	}
}

// TestEveryRouteRendersItsView drives every route of the UI and asserts a 200
// with the content that view exists for. It is the smoke test that a template
// rename or a field removal cannot survive.
func TestEveryRouteRendersItsView(t *testing.T) {
	h := newHarness(t)
	// One posted document and one SOAP-free run is enough content for every
	// view to have something to render; the empty-state text is asserted
	// separately.
	if code, res := h.post(h.goodItem("prp_0000001"), "key-route-1"); code != http.StatusOK ||
		res.Status != erp.StatusPosted {
		t.Fatalf("seed posting: status %d, result %+v", code, res)
	}
	if rec := h.apiGet("/erp/v1/suppliers?limit=5"); rec.Code != http.StatusOK {
		t.Fatalf("seed list request: status %d", rec.Code)
	}

	tests := []struct {
		name string
		path string
		want []string
	}{
		{"overview", "/ui/", []string{
			"Overview", "Seeded scenario", "S0", "token bucket",
			"duplicate_document_attempts", "Quota and virtual clock", "suppliers",
		}},
		{"overview without trailing slash", "/ui", []string{"Overview", "Counters"}},
		{"suppliers", "/ui/suppliers", []string{
			"Suppliers", "supplier_number", "iban (masked)", "change_seq", "legacy_id",
		}},
		{"purchase orders", "/ui/purchase-orders", []string{
			"Purchase orders", "po_number", "cost_center", "lines",
		}},
		{"cost centers", "/ui/cost-centers", []string{
			"Cost centers", "valid_from", "company_code", "0815",
		}},
		{"uom conversions", "/ui/uom-conversions", []string{
			"UoM conversions", "alt_uom", "numerator", "denominator", "base_uom",
		}},
		{"documents", "/ui/documents", []string{
			"Received AP documents", "external_reference", "idempotency_key",
			"prp_0000001", "key-route-1",
		}},
		{"duplicates", "/ui/duplicates", []string{
			"Duplicate view", "external_reference", "attempts", "verdict", "prp_0000001",
		}},
		{"idempotency keys", "/ui/idempotency-keys", []string{
			"Idempotency keys", "first_status", "replays", "conflicts", "key-route-1",
		}},
		{"request log", "/ui/requests", []string{
			"Request log", "single GETs per list page", "injected_fault", "signature",
			"erp_list_page", "quota_used",
		}},
		{"soap calls", "/ui/soap-calls", []string{
			"SOAP call log", "company_code", "soap_action", "correlation_id",
			"no SOAP call in this run",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := h.page(tc.path)
			mustContain(t, tc.path, body, tc.want...)
			// Every page carries the identity strip and the whole navigation,
			// so no screenshot of this UI can be mistaken for the twin's.
			mustContain(t, tc.path, body, "system of record - read only", "Request log")
			if strings.Contains(body, "could not be rendered") {
				t.Fatalf("%s rendered the error page: %s", tc.path, body)
			}
		})
	}
}

// TestStylesheetIsServedFromTheBinary asserts the one asset the UI needs comes
// out of the embedded FS, so the UI works with no network and no asset directory.
func TestStylesheetIsServedFromTheBinary(t *testing.T) {
	h := newHarness(t)
	rec := h.get("/ui/static/erp.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("content type %q, want text/css", ct)
	}
	mustContain(t, "stylesheet", rec.Body.String(), "tablebox", "ui-monospace", "flagged")
}

// TestDuplicateViewFlagsASeededDuplicateGroup is the assertion the duplicate view
// exists for: a second posting of one external reference under a different
// idempotency key must be visible as a flagged row, not as a number a reviewer
// has to go looking for.
func TestDuplicateViewFlagsASeededDuplicateGroup(t *testing.T) {
	h := newHarness(t)
	item := h.goodItem("prp_0004311")

	code, first := h.post(item, "blp:acme-ch:prp_0004311:first")
	if code != http.StatusOK || first.Status != erp.StatusPosted || first.Duplicate {
		t.Fatalf("first posting: status %d, result %+v", code, first)
	}
	code, second := h.post(item, "blp:acme-ch:prp_0004311:second")
	if code != http.StatusOK || !second.Duplicate {
		t.Fatalf("second posting under a different key must be a duplicate: status %d, result %+v",
			code, second)
	}
	if second.DocumentNumber != first.DocumentNumber {
		t.Fatalf("duplicate answered with document %q, want the original %q",
			second.DocumentNumber, first.DocumentNumber)
	}
	if got := h.metrics().DuplicateDocumentAttempts; got != 1 {
		t.Fatalf("duplicate_document_attempts %d, want 1", got)
	}

	body := h.page("/ui/duplicates")
	mustContain(t, "duplicate view", body,
		`<tr class="flagged">`,
		"prp_0004311",
		"DUPLICATE: 2 attempts",
		`<div class="banner warn">`,
		"duplicate_document_attempts = 1",
		first.DocumentNumber,
	)
	// The flagged group sorts first, so a reviewer sees it without paging.
	flaggedAt := strings.Index(body, `<tr class="flagged">`)
	firstRowAt := strings.Index(body, "<tbody>")
	if flaggedAt < 0 || firstRowAt < 0 || flaggedAt-firstRowAt > 400 {
		t.Fatalf("the flagged group must be the first row of the table (tbody at %d, flagged at %d)",
			firstRowAt, flaggedAt)
	}

	// The same warning reaches the overview and the document list, because a
	// reviewer who opens neither the duplicates view nor the counter must still
	// meet it.
	mustContain(t, "overview", h.page("/ui/"),
		`<div class="banner warn">`, "duplicate_document_attempts = 1")
	mustContain(t, "documents", h.page("/ui/documents"),
		`<tr class="flagged">`, "posted, 2 attempts")
}

// TestDuplicateViewIsCalmWhenNothingIsWrong asserts the warning state is reserved
// for a real warning: a run with no duplicate attempt renders no red anywhere.
func TestDuplicateViewIsCalmWhenNothingIsWrong(t *testing.T) {
	h := newHarness(t)
	if code, res := h.post(h.goodItem("prp_0000002"), "key-calm"); code != http.StatusOK ||
		res.Status != erp.StatusPosted {
		t.Fatalf("posting: status %d, result %+v", code, res)
	}
	for _, path := range []string{"/ui/", "/ui/duplicates", "/ui/documents"} {
		body := h.page(path)
		if strings.Contains(body, `banner warn`) || strings.Contains(body, `class="flagged"`) {
			t.Errorf("%s shows a warning state on a clean run", path)
		}
	}
	mustContain(t, "duplicates", h.page("/ui/duplicates"),
		"no duplicate posting attempt in this run", "single posting")
}

// TestASafeRetryIsNotADuplicate is the other half of the duplicate view, and the
// reason it is worth trusting: a client that retries one posting under the same
// idempotency key after a timeout is doing exactly the right thing, and a view
// that painted that red would train reviewers to ignore it.
func TestASafeRetryIsNotADuplicate(t *testing.T) {
	h := newHarness(t)
	item := h.goodItem("prp_0000006")
	const key = "key-safe-retry"

	if code, res := h.post(item, key); code != http.StatusOK || res.Status != erp.StatusPosted {
		t.Fatalf("first posting: status %d, result %+v", code, res)
	}
	if code, res := h.post(item, key); code != http.StatusOK || !res.IdempotencyReplay {
		t.Fatalf("the retry must be a replay: status %d, result %+v", code, res)
	}
	if got := h.metrics().DuplicateDocumentAttempts; got != 0 {
		t.Fatalf("duplicate_document_attempts %d, want 0: a same-key retry is not a duplicate", got)
	}

	body := h.page("/ui/duplicates")
	mustContain(t, "duplicates", body,
		"prp_0000006", "2 attempts, no duplicate recorded",
		"no duplicate posting attempt in this run")
	if strings.Contains(body, `class="flagged"`) || strings.Contains(body, "banner warn") {
		t.Fatal("a same-key retry was flagged as a duplicate")
	}
	if strings.Contains(h.page("/ui/documents"), `class="flagged"`) {
		t.Fatal("the document list flagged a same-key retry")
	}
}

// TestIdempotencyViewCountsReplaysAndConflicts asserts the three columns the
// observer exists for.
func TestIdempotencyViewCountsReplaysAndConflicts(t *testing.T) {
	h := newHarness(t)
	item := h.goodItem("prp_0000003")
	const key = "key-idem"

	if code, res := h.post(item, key); code != http.StatusOK || res.Status != erp.StatusPosted {
		t.Fatalf("first posting: status %d, result %+v", code, res)
	}
	// Same key, same body: the original response replayed verbatim.
	if code, res := h.post(item, key); code != http.StatusOK || !res.IdempotencyReplay {
		t.Fatalf("second posting under the same key must replay: status %d, result %+v", code, res)
	}
	// Same key, different body: 409 IDEMPOTENCY_KEY_REUSED.
	changed := item
	changed.GrossAmount = model.MustDecimal("1250.01")
	if code, _ := h.post(changed, key); code != http.StatusConflict {
		t.Fatalf("same key with a changed body: status %d, want 409", code)
	}

	got, ok := h.ui.observer.stat(http.MethodPost, erp.RouteDocuments, key)
	if !ok {
		t.Fatal("the observer recorded nothing for a key it saw three times")
	}
	if got.FirstStatus != http.StatusOK || got.Replays != 1 || got.Conflicts != 1 || got.Attempts != 3 {
		t.Fatalf("observed %+v, want first_status 200, 1 replay, 1 conflict, 3 attempts", got)
	}

	body := h.page("/ui/idempotency-keys")
	mustContain(t, "idempotency view", body, key, erp.RouteDocuments,
		"idempotency key conflict", `<tr class="flagged">`)
}

// TestIdempotencyViewSaysSoWhenItIsNotObserving asserts the honest degradation:
// an unwrapped server leaves three columns unknown, and the view says which and
// why instead of printing a zero that looks like a fact.
func TestIdempotencyViewSaysSoWhenItIsNotObserving(t *testing.T) {
	ui := New(Options{})
	srv, err := erp.New(erp.Config{
		Scenario: seed.ScenarioS0, Seed: testSeed, Chaos: false, Log: io.Discard, UI: ui,
	})
	if err != nil {
		t.Fatalf("erp.New: %v", err)
	}
	ui.Attach(NewAdminSource(srv))
	h := &harness{t: t, ui: ui, srv: srv, handler: srv, creds: srv.Credentials()}
	set, err := seed.Generate(seed.ScenarioS0, testSeed)
	if err != nil {
		t.Fatalf("seed.Generate: %v", err)
	}
	h.set = set
	h.token = h.authToken()

	if code, res := h.post(h.goodItem("prp_0000004"), "key-unobserved"); code != http.StatusOK ||
		res.Status != erp.StatusPosted {
		t.Fatalf("posting: status %d, result %+v", code, res)
	}
	body := h.page("/ui/idempotency-keys")
	mustContain(t, "idempotency view", body,
		"key-unobserved", "are not being observed", "UI.Observe")
}

// TestRequestLogSummaryCountsTheReadPattern asserts the strip above the request
// log, which is what makes an N+1 read pattern visible without reading a log.
func TestRequestLogSummaryCountsTheReadPattern(t *testing.T) {
	h := newHarness(t)
	if rec := h.apiGet("/erp/v1/suppliers?limit=5"); rec.Code != http.StatusOK {
		t.Fatalf("list page: status %d", rec.Code)
	}
	supplier := h.set.Suppliers[0].SupplierNumber
	for i := 0; i < 3; i++ {
		if rec := h.apiGet("/erp/v1/suppliers/" + supplier); rec.Code != http.StatusOK {
			t.Fatalf("single GET: status %d, body %s", rec.Code, rec.Body.String())
		}
	}
	body := h.page("/ui/requests")
	mustContain(t, "request log", body,
		"erp_single_get", "erp_list_page", "3.000 single GETs per list page")
}

// TestRequestLogFilters asserts both filters narrow the table and neither one
// disturbs the summary strip, which is a run total and not a view of the filter.
func TestRequestLogFilters(t *testing.T) {
	h := newHarness(t)
	if rec := h.apiGet("/erp/v1/suppliers?limit=5"); rec.Code != http.StatusOK {
		t.Fatalf("list page: status %d", rec.Code)
	}
	// An unauthenticated read: a 401 inside the Guard, so it is logged and paid
	// for like everything else.
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/erp/v1/purchase-orders", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read: status %d, want 401", rec.Code)
	}

	tests := []struct {
		name       string
		path       string
		wantRows   []string
		unwantRows []string
	}{
		// The three requests of this run are distinguishable by path, which
		// makes an absence assertion exact: the filter either dropped that row
		// or it did not.
		{"unfiltered", "/ui/requests",
			[]string{erp.RouteAuthToken, erp.RouteSuppliers, erp.RoutePurchaseOrders}, nil},
		{"by endpoint", "/ui/requests?endpoint=erp_auth_token",
			[]string{erp.RouteAuthToken}, []string{erp.RouteSuppliers}},
		{"by status class 4xx", "/ui/requests?class=4xx",
			[]string{erp.RoutePurchaseOrders}, []string{erp.RouteAuthToken}},
		{"by status class 2xx", "/ui/requests?class=2xx",
			[]string{erp.RouteAuthToken}, []string{erp.RoutePurchaseOrders}},
		{"an endpoint with no matching status", "/ui/requests?endpoint=erp_auth_token&class=5xx",
			[]string{"no request matches this filter"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := h.page(tc.path)
			// The summary strip is a run total on every one of these.
			mustContain(t, tc.path, body, "single GETs per list page")
			mustContain(t, tc.path, body, tc.wantRows...)
			for _, unwanted := range tc.unwantRows {
				if strings.Contains(body, unwanted) {
					t.Errorf("%s: filtered table still contains %q", tc.path, unwanted)
				}
			}
		})
	}
}

// TestBrowserSearchAndPagingAreStable asserts search by key, that a page window
// is a window over a total order, and that the same query renders the same bytes
// twice: a UI whose paging wandered would be useless as evidence.
func TestBrowserSearchAndPagingAreStable(t *testing.T) {
	h := newHarness(t)

	first := h.page("/ui/suppliers?limit=10")
	again := h.page("/ui/suppliers?limit=10")
	if first != again {
		t.Fatal("two renders of the same query produced different bytes")
	}
	second := h.page("/ui/suppliers?limit=10&offset=10")
	if first == second {
		t.Fatal("page 2 rendered the same rows as page 1")
	}
	mustContain(t, "page 1", first, "rows 1 to 10 of", "page 1 of")
	mustContain(t, "page 2", second, "rows 11 to 20 of", "page 2 of", "previous", "first")

	// A search by key narrows to the record it names.
	supplier := h.set.Suppliers[3].SupplierNumber
	found := h.page("/ui/suppliers?q=" + supplier)
	mustContain(t, "search", found, supplier, "rows 1 to 1 of 1")

	// A hand-edited offset past the end shows an empty window rather than an
	// error, and a hand-edited limit is clamped rather than honored.
	mustContain(t, "offset past the end", h.page("/ui/suppliers?offset=99999"),
		"no supplier matches this search", "no rows on this page")
	mustContain(t, "search that matches nothing", h.page("/ui/suppliers?q=zzz-no-such-supplier"),
		"no supplier matches this search")
}

// TestPurchaseOrderDetailShowsItsLines asserts the purchase order browser can be
// drilled into, which is the one place lines are readable next to their header.
func TestPurchaseOrderDetailShowsItsLines(t *testing.T) {
	h := newHarness(t)
	po := h.set.PurchaseOrders[0]
	body := h.page("/ui/purchase-orders?key=" + strings.TrimSpace(po.PONumber))
	mustContain(t, "purchase order detail", body,
		"Purchase order", "Header", "po_number (as delivered)", "canonical",
		"line_no", "unit_price", po.SupplierNumber)

	mustContain(t, "unknown purchase order", h.page("/ui/purchase-orders?key=PO-does-not-exist"),
		"no such purchase order", "this order has no lines")
}

// TestEverythingIsEscaped asserts the UI renders user input through
// html/template and nothing else: a search term that looks like markup comes back
// as text.
func TestEverythingIsEscaped(t *testing.T) {
	h := newHarness(t)
	body := h.page("/ui/suppliers?q=%3Cscript%3Ealert(1)%3C%2Fscript%3E")
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("a search term was rendered as markup")
	}
	mustContain(t, "escaped search term", body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

// TestAutoRefreshIsOffByDefault asserts the one piece of script in this UI is
// absent until a reviewer asks for it, and is one inline setTimeout when they do.
func TestAutoRefreshIsOffByDefault(t *testing.T) {
	h := newHarness(t)
	off := h.page("/ui/")
	if strings.Contains(off, "<script") {
		t.Fatal("the default page carries a script")
	}
	mustContain(t, "toggle off", off, "auto-refresh: off", "refresh=5")

	on := h.page("/ui/?refresh=5")
	mustContain(t, "toggle on", on,
		"auto-refresh: every 5s", "setTimeout", "location.reload()", "5000")
	if strings.Count(on, "<script") != 1 {
		t.Fatalf("expected exactly one script element, got %d", strings.Count(on, "<script"))
	}
}

// TestNoTemplateReferencesAnExternalURL is the offline guarantee. Every asset the
// UI needs is in the binary, so no template and no stylesheet may name a host, a
// CDN or a webfont: a page that silently loses its stylesheet on a machine with
// no network would be worse than a plain one.
func TestNoTemplateReferencesAnExternalURL(t *testing.T) {
	forbidden := []string{
		"http://", "https://", "//fonts.", "cdn.", "cdnjs", "jsdelivr", "unpkg",
		"googleapis", "gstatic", "@import", "src=\"//", "href=\"//", "url(http",
		"integrity=", "crossorigin",
	}
	count := 0
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		count++
		lower := strings.ToLower(string(b))
		for _, bad := range forbidden {
			if strings.Contains(lower, bad) {
				t.Errorf("%s references something external: %q", path, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded files: %v", err)
	}
	if count < 6 {
		t.Fatalf("walked %d embedded files, expected the layout, the partials, "+
			"four pages and the stylesheet", count)
	}
}

// TestNoBLPBrandingOnTheERPScreens asserts the visual separation the two UIs owe
// each other. The ERP is the customer's system: none of the BLP palette, no
// wordmark, no logo, and no rounded corner to soften it.
func TestNoBLPBrandingOnTheERPScreens(t *testing.T) {
	h := newHarness(t)
	forbidden := []string{
		"#2e75ff", "#001c54", "#162b54", "#e0f0ff", // the BLP palette
		"roboto", "lato", // the BLP type stack
		"border-radius", "gradient", "box-shadow", // and the shapes it uses
		"blp digital", "miniblp", "<svg", "logo",
	}
	pages := []string{
		"/ui/", "/ui/suppliers", "/ui/purchase-orders", "/ui/cost-centers",
		"/ui/uom-conversions", "/ui/documents", "/ui/duplicates",
		"/ui/idempotency-keys", "/ui/requests", "/ui/soap-calls",
	}
	for _, path := range pages {
		lower := strings.ToLower(h.page(path))
		for _, bad := range forbidden {
			if strings.Contains(lower, bad) {
				t.Errorf("%s carries %q: the ERP UI must be unmistakable from the twin's", path, bad)
			}
		}
	}
	css := strings.ToLower(h.get("/ui/static/erp.css").Body.String())
	for _, bad := range forbidden {
		if strings.Contains(css, bad) {
			t.Errorf("the stylesheet carries %q", bad)
		}
	}
	// And it does declare the plain stacks it is supposed to use.
	mustContain(t, "stylesheet", css, "ui-monospace", "sfmono-regular", "menlo", "consolas")
}

// TestTheUIIsReadOnlyAndFree asserts the two properties section 14 fixes: the UI
// serves no write, and looking at it spends no quota and moves no clock.
func TestTheUIIsReadOnlyAndFree(t *testing.T) {
	h := newHarness(t)
	before := h.metrics()

	for _, path := range []string{"/ui/", "/ui/requests", "/ui/documents", "/ui/duplicates"} {
		h.page(path)
	}
	after := h.metrics()
	if after.RequestsTotal != before.RequestsTotal || after.QuotaUsed != before.QuotaUsed ||
		after.VirtualClockMs != before.VirtualClockMs {
		t.Fatalf("reading the UI changed the accounting: %+v then %+v",
			before.Metrics, after.Metrics)
	}

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/documents", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to the UI: status %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow header %q, want %q", allow, "GET, HEAD")
	}
}

// TestUnknownViewAndUnattachedUI asserts both degenerate cases render a readable
// notice rather than a stack trace or a blank page.
func TestUnknownViewAndUnattachedUI(t *testing.T) {
	h := newHarness(t)
	rec := h.get("/ui/no-such-view")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown view: status %d, want 404", rec.Code)
	}
	mustContain(t, "unknown view", rec.Body.String(), "no such view", "/ui/no-such-view")

	unattached := New(Options{})
	rec = httptest.NewRecorder()
	unattached.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unattached UI: status %d, want 200", rec.Code)
	}
	mustContain(t, "unattached UI", rec.Body.String(), "not attached", "UI.Attach")
}

// TestOverviewAgreesWithTheServersOwnCounters asserts the UI is a view of the
// admin surface and not a second opinion: every number it prints is the number
// the grader would read.
func TestOverviewAgreesWithTheServersOwnCounters(t *testing.T) {
	h := newHarness(t)
	if code, _ := h.post(h.goodItem("prp_0000005"), "key-overview"); code != http.StatusOK {
		t.Fatalf("posting: status %d", code)
	}
	if rec := h.apiGet("/erp/v1/suppliers?limit=250"); rec.Code != http.StatusOK {
		t.Fatalf("list page: status %d", rec.Code)
	}
	m := h.metrics()
	body := h.page("/ui/")
	mustContain(t, "overview", body,
		">"+strconv.FormatInt(m.RequestsTotal, 10)+"<",
		">"+strconv.FormatInt(m.QuotaUsed, 10)+"<",
		">"+strconv.FormatInt(m.VirtualClockMs, 10)+" vms<",
		">"+strconv.Itoa(m.DocumentsCreated)+"<",
	)
	// And the seeded dataset counts are the generator's own.
	set, err := seed.Generate(seed.ScenarioS0, testSeed)
	if err != nil {
		t.Fatalf("seed.Generate: %v", err)
	}
	for _, name := range sortedKeys(set.Counts()) {
		mustContain(t, "counts", body, ">"+name+"</td>")
	}
}
