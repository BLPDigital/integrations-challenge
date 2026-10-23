package erp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// moneyShaped matches anything that looks like an amount, a rate or a quantity:
// digits, a decimal separator, and at least two fraction digits. No error body
// this service writes may contain one.
var moneyShaped = regexp.MustCompile(`[0-9]+[.,][0-9]{2,}`)

// TestNoErrorBodyCarriesASeededValue is the mechanical enforcement of section 17.2
// of the build specification: nothing in an error body is a correctness oracle.
//
// It drives every error path in the package, collects every response body from
// them, and asserts that none of them contains
//
//   - anything shaped like an amount, a rate or a quantity,
//   - any exact value from the seeded dataset: an exchange rate in either
//     spelling, an invoice or line amount, a unit price, a quantity, a purchase
//     order line total, or one of the four pinned graded results,
//   - any credential: a client secret, a SOAP password, an admin token, an issued
//     bearer token, or a seeded IBAN.
//
// A rejection may name the field and the rule it violated. It may never say what
// the right answer would have been: that is the difference between an error
// message and an answer key, and a candidate who can read the expected amount out
// of a rejection is being graded on nothing.
func TestNoErrorBodyCarriesASeededValue(t *testing.T) {
	h := newHarness(t, nil)
	forbidden := forbiddenValues(t, h)

	for _, sample := range collectErrorBodies(t, h) {
		if m := moneyShaped.FindString(sample.body); m != "" {
			t.Errorf("%s carries the amount-shaped token %q:\n%s", sample.name, m, sample.body)
		}
		for _, value := range forbidden {
			if strings.Contains(sample.body, value) {
				t.Errorf("%s carries the seeded value %q:\n%s", sample.name, value, sample.body)
			}
		}
	}
}

// errorSample is one collected response from an error path.
type errorSample struct {
	name string
	body string
}

// collectErrorBodies drives every error path of the service and returns what came
// back. A path added to the package without being added here is a path this test
// does not protect, so the list is meant to be exhaustive and is reviewed with the
// handlers.
func collectErrorBodies(t *testing.T, h *harness) []errorSample {
	t.Helper()
	var out []errorSample
	add := func(name string, rec *httptest.ResponseRecorder) {
		out = append(out, errorSample{name: name, body: rec.Body.String()})
	}

	// Transport errors on the REST surface.
	add("401 no bearer token", h.do(httptest.NewRequest(http.MethodGet, RouteSuppliers, nil)))
	bad := httptest.NewRequest(http.MethodGet, RouteSuppliers, nil)
	bad.Header.Set("Authorization", "Bearer not-a-token-this-run-issued")
	add("401 unknown bearer token", h.do(bad))
	add("400 limit not an integer", h.get(RouteSuppliers+"?limit=many"))
	add("400 limit out of range", h.get(RouteSuppliers+"?limit=0"))
	add("400 changed_since not an integer", h.get(RouteSuppliers+"?changed_since=yesterday"))
	add("400 forged cursor", h.get(RouteSuppliers+"?cursor=eyJhIjoxfQ"))
	add("404 unknown supplier", h.get(RouteSuppliers+"/9999999999"))
	add("404 unknown endpoint", h.get("/erp/v1/nope"))
	add("401 wrong client credentials", h.post(RouteAuthToken,
		map[string]string{"client_id": ClientID, "client_secret": "wrong"}, ""))
	add("400 token body not json", h.post(RouteAuthToken, "{not json", ""))
	add("403 admin token missing", h.do(httptest.NewRequest(http.MethodGet, AdminPathMetrics, nil)))
	add("404 unknown admin endpoint", h.adminGet(AdminPathMetrics+"/nope"))

	// Posting transport errors.
	add("400 no idempotency key", h.post(RouteDocuments, h.goodItem("prp_err_1"), ""))
	add("400 empty body", h.post(RouteDocuments, "", "key-err-empty"))
	add("400 body not json", h.post(RouteDocuments, "not json", "key-err-json"))
	add("400 amount as a JSON number", h.post(RouteDocuments, `{"external_reference":"a",
		"company_code":"CH10","supplier_number":"0000000401","supplier_invoice_number":"1",
		"document_type":"RE","document_date":"2026-03-01","currency":"CHF",
		"gross_amount":1250.00}`, "key-err-number"))
	add("400 amount below the minor unit", h.post(RouteDocuments, `{"external_reference":"a",
		"company_code":"CH10","supplier_number":"0000000401","supplier_invoice_number":"1",
		"document_type":"RE","document_date":"2026-03-01","currency":"CHF",
		"gross_amount":"1.005"}`, "key-err-scale"))
	add("400 empty batch", h.post(RouteDocumentsBatch, map[string]any{"items": []PostingItem{}}, "key-err-batch"))

	tooMany := make([]PostingItem, 0, MaxBatchItems+1)
	for i := 0; i <= MaxBatchItems; i++ {
		tooMany = append(tooMany, h.goodItem(fmt.Sprintf("prp_err_big_%03d", i)))
	}
	add("400 batch too large", h.post(RouteDocumentsBatch, map[string]any{"items": tooMany}, "key-err-big"))

	// Idempotency conflict, which needs a stored response first.
	item := h.goodItem("prp_err_conflict")
	h.post(RouteDocuments, item, "key-err-conflict")
	changed := item
	changed.GrossAmount = model.MustDecimal("2.00")
	add("409 idempotency key reused", h.post(RouteDocuments, changed, "key-err-conflict"))

	// Every business rejection, each as its own body.
	openPO, openTotal := h.poWithStatus(model.POStatusOpen)
	closedPO, _ := h.poWithStatus(model.POStatusClosed)
	overBilled, err := openTotal.MulRate(model.MustDecimal("4.0"), 1, 2)
	if err != nil {
		t.Fatalf("build an over-billed amount: %v", err)
	}
	rejections := []struct {
		name   string
		mutate func(*PostingItem)
	}{
		{"ERP_SUPPLIER_UNKNOWN", func(i *PostingItem) { i.SupplierNumber = "9999999999" }},
		{"ERP_SUPPLIER_BLOCKED", func(i *PostingItem) { i.SupplierNumber = h.blockedSupplier() }},
		{"ERP_PO_NOT_FOUND", func(i *PostingItem) { i.PONumber = seed.UnknownPONumber }},
		{"ERP_PO_CLOSED", func(i *PostingItem) {
			i.SupplierNumber = closedPO.SupplierNumber
			i.PONumber = closedPO.PONumber
		}},
		{"ERP_PERIOD_CLOSED", func(i *PostingItem) { i.PostingDate = "2026-01-02" }},
		{"ERP_COST_CENTER_UNKNOWN", func(i *PostingItem) { i.CostCenter = seed.AbsentCostCenter }},
		{"ERP_AMOUNT_MISMATCH", func(i *PostingItem) {
			i.SupplierNumber = openPO.SupplierNumber
			i.PONumber = openPO.PONumber
			i.Currency = openPO.Currency
			i.GrossAmount = overBilled
			i.VATAmount = model.MustDecimal("0.00")
		}},
	}
	for i, tc := range rejections {
		it := h.goodItem(fmt.Sprintf("prp_err_reject_%02d", i))
		tc.mutate(&it)
		rec, _ := h.postSingle(it, fmt.Sprintf("key-err-reject-%02d", i))
		add("rejection "+tc.name, rec)
	}

	// The SOAP fault matrix.
	soapCases := []struct {
		name string
		opts soapRequestOptions
	}{
		{"SOAP 415", soapRequestOptions{contentType: "application/soap+xml"}},
		{"SOAP action missing", soapRequestOptions{omitAction: true}},
		{"SOAP action unquoted", soapRequestOptions{action: "urn:blp-erp:finref:1.0/GetExchangeRateTable"}},
		{"SOAP mustUnderstand", soapRequestOptions{
			extraHeader: `<x:Trace xmlns:x="urn:example:t" soap:mustUnderstand="1">on</x:Trace>`}},
		{"SOAP unauthorized", soapRequestOptions{password: "wrong"}},
		{"SOAP no correlation id", soapRequestOptions{omitContext: true}},
		{"SOAP no company code", soapRequestOptions{omitCompany: true}},
		{"SOAP CH20 permanent", soapRequestOptions{companyCode: model.CompanyCodeCH20}},
		{"SOAP retryable", soapRequestOptions{}},
	}
	for _, tc := range soapCases {
		add(tc.name, h.do(h.soapRequest(tc.opts)))
	}
	malformed := httptest.NewRequest(http.MethodPost, RouteSOAP, strings.NewReader("<not-an-envelope/>"))
	malformed.Header.Set("Content-Type", "text/xml; charset=utf-8")
	malformed.Header.Set(SOAPActionHeader, SOAPActionQuoted)
	add("SOAP malformed envelope", h.do(malformed))
	add("SOAP GET without wsdl", h.get(RouteSOAP))

	// The quota 503, on its own server so it cannot starve the others.
	quota := newHarness(t, func(cfg *Config) { cfg.Quota = 1 })
	add("503 quota exhausted", quota.get(RouteSuppliers))

	// The injected faults, which are content addressed and therefore reachable
	// by walking enough logical requests on a chaos server.
	chaos := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	injected := 0
	for _, p := range chaosProbes() {
		rec := chaos.get(p)
		if rec.Code != http.StatusOK {
			injected++
			add(fmt.Sprintf("injected %d", rec.Code), rec)
		}
	}
	if injected == 0 {
		t.Fatal("no injected fault was collected: this test would not be checking those bodies")
	}
	return out
}

// forbiddenValues is every exact value from the seeded landscape that an error
// body must never contain, plus the credentials of the run.
func forbiddenValues(t *testing.T, h *harness) []string {
	t.Helper()
	d := h.dataset()
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		// Short tokens would match inside unrelated numbers and turn this
		// assertion into a source of false positives, which is worse than no
		// assertion at all. The amount-shaped regex covers them anyway.
		if len(v) < 4 || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}

	for _, row := range d.set.FxRows {
		add(row.Rate.String())
		add(row.RateLiteral)
	}
	for _, inv := range d.set.Invoices {
		add(inv.GrossAmount.String())
		add(inv.VATAmount.String())
		for _, line := range inv.Lines {
			add(line.LineAmount.String())
			add(line.UnitPrice.String())
			add(line.Quantity.String())
		}
	}
	for _, line := range d.set.PurchaseOrderLines {
		add(line.UnitPrice.String())
		add(line.Quantity.String())
	}
	for _, po := range d.set.PurchaseOrders {
		if total, ok := d.poNetTotal[seed.CanonicalPONumber(po.PONumber)]; ok {
			add(total.String())
		}
	}
	// The four pinned graded amounts of section 16.1 and the rates behind them.
	for _, v := range []string{
		"108.23", "108.22", "1390.75", "1252.78", "-108.23",
		"1.082250", "0.931000", "0.556300", "1,082250", "0,931000", "0,556300",
	} {
		add(v)
	}
	for _, v := range []string{
		h.creds.ClientSecret, h.creds.SOAPPassword, h.creds.AdminToken, h.token,
	} {
		add(v)
	}
	for _, sup := range d.set.Suppliers {
		add(sup.IBAN)
	}
	return out
}
