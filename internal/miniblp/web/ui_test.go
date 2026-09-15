package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// The UI renders exactly the twin it is given, and *miniblp.Server is that twin.
var _ Twin = (*miniblp.Server)(nil)

// TestViewsRenderExpectedContent drives every route against a twin that has
// ingested three real batches and asserts the status and the key content of each
// view. It is the one test that would fail if a view stopped answering the
// question it exists for.
func TestViewsRenderExpectedContent(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	key := invoiceKey()
	proposal := f.proposal()
	sha := f.sourceSHA(model.DatasetInvoice.String(), key)
	recordHref := "/ui/record?dataset=invoice&key=" + url.QueryEscape(key)

	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "dashboard answers did the data arrive",
			path: "/ui/",
			want: []string{
				"Records per dataset", "Invoice funnel", "Proposal pending", "Posted",
				"Open exceptions by code", "EXC_SUPPLIER_UNKNOWN", "Last batches",
				"batch-three", "Requests and quota", "state digest", "supplier", "invoice",
			},
		},
		{
			name: "the mount point without a slash is the dashboard",
			path: "/ui",
			want: []string{"Invoice funnel"},
		},
		{
			name: "dataset index lists the segments with counts",
			path: "/ui/datasets/",
			want: []string{"Datasets", "supplier", "invoice", "proposal", "exception",
				"the exception queue"},
		},
		{
			name: "dataset browser shows the columns that matter",
			path: "/ui/datasets/supplier",
			want: []string{"Natural key", "Supplier", "IBAN", "Change seq",
				"CH93*************2957", fixtureSupplier, "Search by natural key"},
		},
		{
			name: "dataset browser searches by natural key",
			path: "/ui/datasets/invoice?q=" + url.QueryEscape("0004711"),
			want: []string{"0004711", "Gross", "GBP"},
		},
		{
			name: "dataset browser paginates",
			path: "/ui/datasets/supplier?limit=1",
			want: []string{"1 rows"},
		},
		{
			name: "record detail shows payload, revisions and provenance",
			path: recordHref,
			want: []string{
				"Current payload", "Revision history", "source locator", "invoices.json",
				"content hash", "batch-one", "Proposals of this invoice", "audit chain",
				"provenance only", "gross_amount",
			},
		},
		{
			name: "raw bytes are reachable by content address",
			path: "/ui/raw/" + sha,
			want: []string{"Raw source", "content address", sha, "supplier_invoice_number",
				"Show the bytes as hex"},
		},
		{
			name: "raw bytes render as hex too",
			path: "/ui/raw/" + sha + "?view=hex",
			want: []string{"Byte-exact hex dump", "|", "Show the bytes as text"},
		},
		{
			name: "batch list carries the receipt status",
			path: "/ui/batches/",
			want: []string{"Batches", "batch-one", "batch-two", "batch-three", "accepted",
				"Seen", "Rejected"},
		},
		{
			name: "batch detail carries files, records and the receipt",
			path: "/ui/batches/batch-one",
			want: []string{"Receipt", "Files", "invoices.json", "Records", "Twin id",
				"Receipt as written", "closure_ok", "counts close", "Outcome filter"},
		},
		{
			name: "batch detail filters by outcome",
			path: "/ui/batches/batch-one?outcome=accepted",
			want: []string{"Records: accepted", "accepted"},
		},
		{
			name: "exception queue groups by code",
			path: "/ui/exceptions",
			want: []string{"Exception queue", "EXC_SUPPLIER_UNKNOWN", "Subject", "Stage",
				"Message", "Source batch", fixtureBadKey},
		},
		{
			name: "exception queue filters by code and pages",
			path: "/ui/exceptions?code=EXC_SUPPLIER_UNKNOWN&state=open",
			want: []string{"EXC_SUPPLIER_UNKNOWN", "Line or ordinal"},
		},
		{
			name: "proposal list carries the ack state",
			path: "/ui/proposals",
			want: []string{"Proposals", proposal.ProposalID, "Idempotency key", "ERP document",
				fixtureERPDocument, "acknowledged", "108.23 CHF", "100.00 GBP"},
		},
		{
			name: "proposal list filters by status",
			path: "/ui/proposals?status=acknowledged",
			want: []string{"Proposals: acknowledged", proposal.ProposalID},
		},
		{
			name: "audit chain resolves an invoice key",
			path: "/ui/audit?q=" + url.QueryEscape(displayKey(key)),
			want: []string{"The chain", "invoice_key", "invoices.json", "raw bytes",
				"twin revision", "proposal", "idempotency key", "ERP document number",
				fixtureERPDocument, "ack"},
		},
		{
			name: "audit chain resolves a proposal id",
			path: "/ui/audit?q=" + url.QueryEscape(proposal.ProposalID),
			want: []string{"proposal_id", proposal.ProposalID},
		},
		{
			name: "audit chain resolves an ERP document number",
			path: "/ui/audit?q=" + fixtureERPDocument,
			want: []string{"erp_document_number", fixtureERPDocument},
		},
		{
			name: "audit chain with no query explains itself",
			path: "/ui/audit",
			want: []string{"Paste a key", "natural key", "proposal id"},
		},
		{
			name: "the lockup is served from the binary",
			path: "/ui/static/blp-logo-full.svg",
			want: []string{"<svg", "#2E75FF"},
		},
		{
			name: "the stylesheet is served from the binary",
			path: "/ui/static/ui.css",
			want: []string{"--atlas: #162b54", "Roboto, Lato"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := f.body(tc.path, http.StatusOK)
			contains(t, tc.path, body, tc.want...)
		})
	}
}

// TestOlderVersionOverwritingANewerOneIsObvious is the defect the record view
// exists for: the second batch delivered an older change_seq, and a reviewer must
// see it without comparing two payloads by hand.
func TestOlderVersionOverwritingANewerOneIsObvious(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	path := "/ui/record?dataset=supplier&key=" + url.QueryEscape(fixtureSupplier)
	body := f.body(path, http.StatusOK)
	contains(t, path, body,
		"An older version of this record overwrote a newer one",
		"batch-two",
		"older version overwrote a newer one",
		"118422",
		"118400",
	)
}

// TestEverythingIsEscaped asserts that no delivered byte can reach a browser as
// markup. The fixture delivers a script tag as a supplier name on purpose.
func TestEverythingIsEscaped(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	for _, path := range []string{
		"/ui/datasets/supplier",
		"/ui/record?dataset=supplier&key=" + url.QueryEscape(fixtureSupplier),
	} {
		body := f.body(path, http.StatusOK)
		if strings.Contains(body, "<script>alert(1)</script>") {
			t.Fatalf("%s rendered a delivered script tag as markup", path)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatalf("%s does not carry the escaped supplier name", path)
		}
	}
}

// scriptTag finds every script element in a rendered page.
var scriptTag = regexp.MustCompile(`(?i)<script`)

// TestTheOnlyScriptIsTheRefreshToggle asserts the zero-JavaScript rule: a view
// carries no script at all until the auto-refresh toggle is on, and then exactly
// one, which does nothing but reload.
func TestTheOnlyScriptIsTheRefreshToggle(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	off := f.body("/ui/", http.StatusOK)
	if n := len(scriptTag.FindAllString(off, -1)); n != 0 {
		t.Fatalf("the dashboard carries %d script elements with auto-refresh off", n)
	}
	contains(t, "/ui/", off, "Auto-refresh: off", "refresh=5")

	on := f.body("/ui/?refresh=5", http.StatusOK)
	if n := len(scriptTag.FindAllString(on, -1)); n != 1 {
		t.Fatalf("the dashboard carries %d script elements with auto-refresh on, want 1", n)
	}
	contains(t, "/ui/?refresh=5", on, "setTimeout", "location.reload", "5000",
		"Auto-refresh: on (5s)")
	if strings.Contains(on, "fetch(") || strings.Contains(on, "XMLHttpRequest") {
		t.Fatal("the inline script does more than reload")
	}
}

// externalURL matches anything a page could try to fetch from the network.
var externalURL = regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}|@import|url\(\s*['"]?https?:`)

// TestNoTemplateReferencesAnExternalURL is the offline guarantee: no template
// and no stylesheet may name a host, a CDN or a webfont, because the interface
// has to render with the network unplugged.
func TestNoTemplateReferencesAnExternalURL(t *testing.T) {
	names, err := fs.Glob(assets, "templates/*.gohtml")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	names = append(names, "static/ui.css")
	if len(names) < 4 {
		t.Fatalf("only %d assets were scanned, which cannot be right", len(names))
	}
	for _, name := range names {
		buf, err := assets.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if m := externalURL.FindString(string(buf)); m != "" {
			t.Errorf("%s references an external resource: %q", name, m)
		}
		for _, forbidden := range []string{"fonts.googleapis", "cdn", "webfont", "@font-face"} {
			if strings.Contains(strings.ToLower(string(buf)), forbidden) {
				t.Errorf("%s mentions %q", name, forbidden)
			}
		}
	}
}

// TestRenderedPagesNameNoExternalHost asserts the same property of the output,
// not just of the sources: every URL a page emits is relative to the mount
// point.
func TestRenderedPagesNameNoExternalHost(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	for _, path := range []string{"/ui/", "/ui/datasets/supplier", "/ui/batches/batch-one",
		"/ui/exceptions", "/ui/proposals", "/ui/audit?q=" + url.QueryEscape(displayKey(invoiceKey()))} {
		body := f.body(path, http.StatusOK)
		if m := externalURL.FindString(body); m != "" {
			t.Errorf("%s emitted an external reference: %q", path, m)
		}
	}
}

// TestDeterministicRendering asserts that two renderings of one view are
// byte-identical. It is the map-iteration guard: a histogram or a metric map
// rendered in hash order would fail here.
func TestDeterministicRendering(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	for _, path := range []string{"/ui/", "/ui/datasets/supplier", "/ui/exceptions",
		"/ui/batches/batch-one", "/ui/proposals",
		"/ui/audit?q=" + url.QueryEscape(displayKey(invoiceKey()))} {
		first := f.body(path, http.StatusOK)
		second := f.body(path, http.StatusOK)
		if first != second {
			t.Errorf("%s rendered differently twice", path)
		}
	}
}

// TestReadingTheUICostsNothing asserts the observer property: a reviewer
// refreshing every view spends no quota, no virtual time and no request count,
// because /ui is outside the Guard and the admin surface is exempt from it.
func TestReadingTheUICostsNothing(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	before := f.srv.Governor().Metrics()
	for _, path := range []string{"/ui/", "/ui/datasets/supplier", "/ui/datasets/",
		"/ui/batches/", "/ui/batches/batch-one", "/ui/exceptions", "/ui/proposals",
		"/ui/audit?q=" + url.QueryEscape(displayKey(invoiceKey())),
		"/ui/record?dataset=invoice&key=" + url.QueryEscape(invoiceKey()),
		"/ui/static/ui.css"} {
		f.body(path, http.StatusOK)
	}
	after := f.srv.Governor().Metrics()
	if before.RequestsTotal != after.RequestsTotal || before.QuotaUsed != after.QuotaUsed ||
		before.VirtualClockMs != after.VirtualClockMs {
		t.Fatalf("the UI perturbed the transcript: %+v then %+v", before, after)
	}
}

// TestUIIsServedOnTheTwinsOwnPort asserts the mount: the twin's handler serves
// /ui itself, so a reviewer needs no second process and no second port.
func TestUIIsServedOnTheTwinsOwnPort(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/ui/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
}

// TestNotFoundAndBadRequests asserts that every wrong address answers a rendered
// page with the right status rather than a panic or a bare string.
func TestNotFoundAndBadRequests(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	tests := []struct {
		name string
		path string
		want int
	}{
		{"unknown view", "/ui/nowhere", http.StatusNotFound},
		{"unknown dataset", "/ui/datasets/not_a_dataset$", http.StatusNotFound},
		{"unknown record", "/ui/record?dataset=supplier&key=0000000000", http.StatusNotFound},
		{"record without a key", "/ui/record?dataset=supplier", http.StatusBadRequest},
		{"unknown raw address", "/ui/raw/" + strings.Repeat("a", 64), http.StatusNotFound},
		{"unknown batch", "/ui/batches/no-such-batch", http.StatusNotFound},
		{"unknown asset", "/ui/static/nothing.png", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.get(tc.path)
			if rec.Code != tc.want {
				t.Fatalf("GET %s status = %d, want %d", tc.path, rec.Code, tc.want)
			}
			if tc.want == http.StatusNotFound || tc.want == http.StatusBadRequest {
				contains(t, tc.path, rec.Body.String(), "Nothing to show here", "blp-logo-full.svg")
			}
		})
	}
}

// TestTheInterfaceIsReadOnly asserts that no method but GET and HEAD is served,
// so nothing a reviewer does in a browser can change the twin's state.
func TestTheInterfaceIsReadOnly(t *testing.T) {
	f := newFixture(t, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch} {
		req := httptest.NewRequest(method, "/ui/", nil)
		rec := httptest.NewRecorder()
		f.ui.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /ui/ status = %d, want 405", method, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodHead, "/ui/", nil)
	rec := httptest.NewRecorder()
	f.ui.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD /ui/ status = %d, body %d bytes", rec.Code, rec.Body.Len())
	}
}

// TestUnboundUISaysSo asserts that a UI constructed before its twin answers a
// page rather than panicking on a nil store.
func TestUnboundUISaysSo(t *testing.T) {
	ui, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
	rec := httptest.NewRecorder()
	ui.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	contains(t, "unbound /ui/", rec.Body.String(), "no twin is bound")
}

// TestMissingAdminTokenDegradesWithAnExplanation asserts that the two views
// which need the admin surface say why they are empty instead of inventing
// content, and that every other view still works.
func TestMissingAdminTokenDegradesWithAnExplanation(t *testing.T) {
	f := newFixture(t, func(cfg *Config) { cfg.AdminToken = "" })
	f.seed()
	dashboard := f.body("/ui/", http.StatusOK)
	contains(t, "/ui/", dashboard, "has no admin token", "Records per dataset", "Invoice funnel")

	batches := f.body("/ui/batches/", http.StatusOK)
	contains(t, "/ui/batches/", batches, "has no admin token")

	audit := f.body("/ui/audit?q="+url.QueryEscape(displayKey(invoiceKey())), http.StatusOK)
	contains(t, "/ui/audit", audit, "has no admin token")

	// The store-backed views are unaffected: they never needed the token.
	f.body("/ui/datasets/supplier", http.StatusOK)
	f.body("/ui/proposals", http.StatusOK)
	f.body("/ui/exceptions", http.StatusOK)
}

// TestWrongAdminTokenIsReportedNotHidden asserts that a misconfigured token
// produces the twin's own code, so the misconfiguration is diagnosable.
func TestWrongAdminTokenIsReportedNotHidden(t *testing.T) {
	f := newFixture(t, func(cfg *Config) { cfg.AdminToken = "not-the-token" })
	f.seed()
	body := f.body("/ui/batches/", http.StatusOK)
	contains(t, "/ui/batches/", body, "ADMIN_TOKEN_REQUIRED")
}

// TestBrandTokensArePresent asserts the palette, the type stack and the lockup
// the brand book makes binding, and the absence of the things it forbids.
func TestBrandTokensArePresent(t *testing.T) {
	css, err := assets.ReadFile("static/ui.css")
	if err != nil {
		t.Fatalf("stylesheet: %v", err)
	}
	contains(t, "the stylesheet", string(css),
		"#ffffff", "#162b54", "#2e75ff", "#e0f0ff",
		`Roboto, Lato, "Helvetica Neue", Arial, sans-serif`,
	)
	// Exactly two accent families, red and green.
	for _, forbidden := range []string{"#f21df2", "#ff6e0d", "#f7d200", "#834fe8"} {
		if strings.Contains(strings.ToLower(string(css)), forbidden) {
			t.Errorf("the stylesheet uses a third accent family: %s", forbidden)
		}
	}
	f := newFixture(t, nil)
	f.seed()
	body := f.body("/ui/", http.StatusOK)
	contains(t, "/ui/", body, `src="/ui/static/blp-logo-full.svg"`, `width="160"`)

	logo, err := assets.ReadFile("static/blp-logo-full.svg")
	if err != nil {
		t.Fatalf("lockup: %v", err)
	}
	if !strings.Contains(string(logo), "viewBox=\"0 0 500 166.28\"") {
		t.Fatal("the official lockup was replaced")
	}
}
