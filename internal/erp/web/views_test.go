package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// soapEnvelope renders a GetExchangeRateTable request for one company code. It is
// the documented envelope of section 7.3: SOAP 1.1, a wsse:UsernameToken and a
// fin:RequestContext carrying a non-empty correlation id.
func (h *harness) soapEnvelope(companyCode, correlationID string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<soap:Envelope xmlns:soap="` + erp.NSSoapEnv + `"` +
		` xmlns:fin="` + erp.NSFinRef + `" xmlns:cmn="` + erp.NSCommon + `"` +
		` xmlns:wsse="` + erp.NSWSSE + `">` + "\n")
	b.WriteString("  <soap:Header>\n")
	b.WriteString(`    <wsse:Security soap:mustUnderstand="1"><wsse:UsernameToken>` +
		"<wsse:Username>" + h.creds.SOAPUsername + "</wsse:Username>" +
		`<wsse:Password Type="PasswordText">` + h.creds.SOAPPassword + "</wsse:Password>" +
		"</wsse:UsernameToken></wsse:Security>\n")
	b.WriteString(`    <fin:RequestContext soap:mustUnderstand="1">` +
		"<cmn:CorrelationId>" + correlationID + "</cmn:CorrelationId></fin:RequestContext>\n")
	b.WriteString("  </soap:Header>\n  <soap:Body>\n    <fin:GetExchangeRateTable>\n")
	b.WriteString("      <fin:CompanyCode>" + companyCode + "</fin:CompanyCode>\n")
	b.WriteString("      <fin:RateType>DAILY</fin:RateType>\n")
	b.WriteString("    </fin:GetExchangeRateTable>\n  </soap:Body>\n</soap:Envelope>\n")
	return b.String()
}

// soapCall performs one SOAP call. action is the SOAPAction header verbatim; the
// empty string omits the header entirely, which the ERP answers with HTTP 500.
func (h *harness) soapCall(companyCode, correlationID, action string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, erp.RouteSOAP,
		strings.NewReader(h.soapEnvelope(companyCode, correlationID)))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if action != "" {
		req.Header.Set(erp.SOAPActionHeader, action)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// TestSOAPCallLogView asserts the view shows what a debrief argues from: the
// company code, whether the SOAPAction was usable, whether a fault came back and
// which code, how many rows were served and the echoed correlation id.
func TestSOAPCallLogView(t *testing.T) {
	h := newHarness(t)

	// CH10 twice: the first delivery of that signature is the run's one
	// RETRYABLE fault, the second succeeds with the whole table.
	first := h.soapCall(model.CompanyCodeCH10, "run_ui_0001", erp.SOAPActionQuoted)
	if first.Code != http.StatusOK {
		t.Fatalf("first CH10 call: status %d", first.Code)
	}
	if second := h.soapCall(model.CompanyCodeCH10, "run_ui_0001",
		erp.SOAPActionQuoted); second.Code != http.StatusOK {
		t.Fatalf("second CH10 call: status %d", second.Code)
	}
	// CH20 has no rate table configured and faults permanently, every time.
	if rec := h.soapCall(model.CompanyCodeCH20, "run_ui_0002",
		erp.SOAPActionQuoted); rec.Code != http.StatusOK {
		t.Fatalf("CH20 call: status %d", rec.Code)
	}
	// A present but unquoted action, and then no action header at all.
	if rec := h.soapCall(model.CompanyCodeCH10, "run_ui_0003",
		"urn:blp-erp:finref:1.0/GetExchangeRateTable"); rec.Code != http.StatusOK {
		t.Fatalf("unquoted action: status %d", rec.Code)
	}
	if rec := h.soapCall(model.CompanyCodeCH10, "run_ui_0004",
		""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("absent action: status %d, want 500", rec.Code)
	}

	body := h.page("/ui/soap-calls")
	mustContain(t, "soap call log", body,
		model.CompanyCodeCH10, model.CompanyCodeCH20,
		"run_ui_0001", "run_ui_0002",
		erp.FaultTemporary, erp.FaultNoRateTable, erp.FaultBadAction, erp.FaultActionMissing,
		erp.SeverityPermanent, erp.SeverityRetryable,
		"wrong or unquoted", "missing", "valid",
		"returned a fault",
	)
	// A permanent fault is flagged: retrying one is a graded mistake, so it must
	// be legible as permanent and not merely as a fault.
	if !strings.Contains(body, `<td class="warn">`+erp.SeverityPermanent+`</td>`) {
		t.Fatalf("the permanent fault is not marked as a warning: %s", body)
	}
	// And the successful call shows the rows it served.
	if strings.Contains(body, "no SOAP call in this run") {
		t.Fatal("the call log rendered as empty")
	}
}

// TestSOAPViewSearch asserts the search box narrows the call log by the fields a
// reviewer has in hand: a correlation id from a connector log, or a fault code.
func TestSOAPViewSearch(t *testing.T) {
	h := newHarness(t)
	h.soapCall(model.CompanyCodeCH20, "run_ui_needle", erp.SOAPActionQuoted)
	h.soapCall(model.CompanyCodeCH10, "run_ui_other", erp.SOAPActionQuoted)

	found := h.page("/ui/soap-calls?q=run_ui_needle")
	mustContain(t, "search by correlation id", found, "run_ui_needle", "rows 1 to 1 of 1")
	if strings.Contains(found, "run_ui_other") {
		t.Fatal("the search returned a call it should have filtered out")
	}
}

// TestEveryPageIsAPureFunctionOfTheState is the determinism assertion this whole
// project is built on: two servers on the same seed, driven by the same requests,
// render byte-identical pages. A map ranged over into a template, a wall clock in
// a footer or an unsorted collection anywhere would fail here.
func TestEveryPageIsAPureFunctionOfTheState(t *testing.T) {
	drive := func() map[string]string {
		ui := New(Options{})
		srv, err := erp.New(erp.Config{
			Scenario: seed.ScenarioS1, Seed: testSeed, Chaos: true, Quota: 400,
			Log: io.Discard, UI: ui,
		})
		if err != nil {
			t.Fatalf("erp.New: %v", err)
		}
		ui.Attach(NewAdminSource(srv))
		set, err := seed.Generate(seed.ScenarioS1, testSeed)
		if err != nil {
			t.Fatalf("seed.Generate: %v", err)
		}
		h := &harness{t: t, ui: ui, srv: srv, handler: ui.Observe(srv),
			creds: srv.Credentials(), set: set}
		h.token = h.authToken()

		// A run with something of everything in it: list pages, a single GET, a
		// posting, a duplicate of it, a replay, a conflict and two SOAP calls.
		h.apiGet("/erp/v1/suppliers?limit=250")
		h.apiGet("/erp/v1/purchase-orders?limit=100")
		h.apiGet("/erp/v1/suppliers/" + set.Suppliers[0].SupplierNumber)
		item := h.goodItem("prp_determinism")
		h.post(item, "key-det-1")
		h.post(item, "key-det-1")
		h.post(item, "key-det-2")
		changed := item
		changed.GrossAmount = model.MustDecimal("1250.02")
		h.post(changed, "key-det-1")
		h.soapCall(model.CompanyCodeCH10, "run_det", erp.SOAPActionQuoted)
		h.soapCall(model.CompanyCodeCH20, "run_det", erp.SOAPActionQuoted)

		out := map[string]string{}
		for _, path := range []string{
			"/ui/", "/ui/suppliers?limit=25&offset=50", "/ui/purchase-orders",
			"/ui/purchase-orders?key=" + strings.TrimSpace(set.PurchaseOrders[0].PONumber),
			"/ui/cost-centers?q=08", "/ui/uom-conversions", "/ui/documents",
			"/ui/duplicates", "/ui/idempotency-keys", "/ui/requests",
			"/ui/requests?class=4xx", "/ui/soap-calls",
		} {
			out[path] = h.page(path)
		}
		return out
	}

	a, b := drive(), drive()
	if len(a) != len(b) {
		t.Fatalf("%d pages then %d pages", len(a), len(b))
	}
	for _, path := range sortedPaths(a) {
		if a[path] != b[path] {
			t.Errorf("%s differs between two runs of the same seed", path)
		}
	}
	// And the duplicate is visible in the second run's pages too, which is what
	// makes the comparison worth making.
	mustContain(t, "duplicates", a["/ui/duplicates"], "DUPLICATE: 3 attempts", "prp_determinism")
}

// sortedPaths returns the keys of a page map in ascending byte order, because a
// test that ranged over the map would report its failures in a different order
// every run.
func sortedPaths(pages map[string]string) []string {
	out := make([]string, 0, len(pages))
	for path := range pages {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
