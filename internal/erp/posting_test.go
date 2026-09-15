package erp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// TestPostingHappyPath asserts a bookable item is booked once, with a document
// number, a fiscal year from the posting date and no replay flag.
func TestPostingHappyPath(t *testing.T) {
	h := newHarness(t, nil)
	rec, res := h.postSingle(h.goodItem("prp_0000001"), "key-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if res.Status != StatusPosted {
		t.Fatalf("status %q, want %q (code %q)", res.Status, StatusPosted, res.Code)
	}
	if res.DocumentNumber == "" || res.FiscalYear != 2026 || res.ERPRevision != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.IdempotencyReplay || res.Duplicate {
		t.Fatalf("a first posting is neither a replay nor a duplicate: %+v", res)
	}
	if got := h.metrics().DocumentsCreated; got != 1 {
		t.Fatalf("documents_created %d, want 1", got)
	}
	if got := h.metrics().DuplicateDocumentAttempts; got != 0 {
		t.Fatalf("duplicate_document_attempts %d, want 0", got)
	}
}

// TestPostingBusinessRejections walks the published rejection table. Each case
// mutates exactly one field of a bookable item, so a rejection can only be caused
// by the rule it names.
//
// Every rejection is HTTP 200 with retriable false: a business rejection is not a
// transport error, and a client that retries one is making a mistake the grader
// scores.
func TestPostingBusinessRejections(t *testing.T) {
	h := newHarness(t, nil)
	openPO, openTotal := h.poWithStatus(model.POStatusOpen)
	closedPO, _ := h.poWithStatus(model.POStatusClosed)

	// An amount well above the order's own line sum, in the order's currency.
	overBilled, err := openTotal.MulRate(model.MustDecimal("3.0"), 1, 2)
	if err != nil {
		t.Fatalf("build an over-billed amount: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PostingItem)
		want   string
	}{
		{"supplier unknown", func(i *PostingItem) {
			i.SupplierNumber = "9999999999"
		}, RejectSupplierUnknown},
		{"supplier blocked", func(i *PostingItem) {
			i.SupplierNumber = h.blockedSupplier()
		}, RejectSupplierBlocked},
		{"purchase order not found", func(i *PostingItem) {
			i.PONumber = seed.UnknownPONumber
		}, RejectPONotFound},
		{"purchase order closed", func(i *PostingItem) {
			i.SupplierNumber = closedPO.SupplierNumber
			i.PONumber = closedPO.PONumber
		}, RejectPOClosed},
		{"period closed", func(i *PostingItem) {
			i.PostingDate = "2026-01-15"
		}, RejectPeriodClosed},
		{"cost center unknown", func(i *PostingItem) {
			i.CostCenter = seed.AbsentCostCenter
		}, RejectCostCenterUnknown},
		{"cost center of another company code", func(i *PostingItem) {
			i.CostCenter = h.costCenterOfOtherCompany(model.CompanyCodeCH10)
		}, RejectCostCenterUnknown},
		{"amount above the purchase order", func(i *PostingItem) {
			i.SupplierNumber = openPO.SupplierNumber
			i.PONumber = openPO.PONumber
			i.Currency = openPO.Currency
			i.GrossAmount = overBilled
			i.VATAmount = model.MustDecimal("0.00")
		}, RejectAmountMismatch},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := h.goodItem(fmt.Sprintf("prp_reject_%02d", i))
			tc.mutate(&item)
			rec, res := h.postSingle(item, fmt.Sprintf("key-reject-%02d", i))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200: a business rejection is not a transport error", rec.Code)
			}
			if res.Status != StatusRejected {
				t.Fatalf("status %q with code %q, want a rejection", res.Status, res.Code)
			}
			if res.Code != tc.want {
				t.Fatalf("code %q, want %q (message %q)", res.Code, tc.want, res.Message)
			}
			if res.Retriable == nil || *res.Retriable {
				t.Fatalf("rejection %q must carry retriable false, got %v", res.Code, res.Retriable)
			}
			if res.DocumentNumber != "" {
				t.Fatalf("a rejected item must not carry a document number, got %q", res.DocumentNumber)
			}
		})
	}
	if got := h.metrics().DocumentsCreated; got != 0 {
		t.Fatalf("documents_created %d after only rejections, want 0", got)
	}
	rejections := h.metrics().BusinessRejections
	for _, tc := range tests {
		if rejections[tc.want] == 0 {
			t.Fatalf("business_rejections has no entry for %s", tc.want)
		}
	}
}

// TestPostingAcceptsAnEmptyCostCenter asserts the ambiguity rule is not punished
// here: the customer's format leaves the position cost center undefined, a correct
// client therefore leaves it empty, and the ERP books the document anyway.
func TestPostingAcceptsAnEmptyCostCenter(t *testing.T) {
	h := newHarness(t, nil)
	item := h.goodItem("prp_no_cost_center")
	item.CostCenter = ""
	_, res := h.postSingle(item, "key-no-cc")
	if res.Status != StatusPosted {
		t.Fatalf("an empty cost center was rejected with %q: leaving the undefined field empty is "+
			"the documented correct behavior", res.Code)
	}
}

// TestPostingUnderBillingIsAccepted asserts the tolerance check is one sided. An
// invoice may cover a subset of an order's lines, which is normal in accounts
// payable, and refusing it would reject correct work.
func TestPostingUnderBillingIsAccepted(t *testing.T) {
	h := newHarness(t, nil)
	po, total := h.poWithStatus(model.POStatusOpen)
	half, err := total.MulRate(model.MustDecimal("0.5"), 1, 2)
	if err != nil {
		t.Fatalf("halve the order total: %v", err)
	}
	item := h.goodItem("prp_partial")
	item.SupplierNumber = po.SupplierNumber
	item.PONumber = po.PONumber
	item.Currency = po.Currency
	item.GrossAmount = half
	item.VATAmount = model.MustDecimal("0.00")
	_, res := h.postSingle(item, "key-partial")
	if res.Status != StatusPosted {
		t.Fatalf("a partial invoice was rejected with %q", res.Code)
	}
}

// TestPostingSkipsToleranceAcrossCurrencies asserts the mock forms no FX opinion:
// a posting in a currency the order is not denominated in is not compared, because
// a rejection that depended on our own conversion would be an oracle.
func TestPostingSkipsToleranceAcrossCurrencies(t *testing.T) {
	h := newHarness(t, nil)
	var po model.PurchaseOrder
	for _, candidate := range h.dataset().purchaseOrders.items {
		if candidate.Status == model.POStatusOpen && candidate.Currency == "CHF" {
			po = candidate
			break
		}
	}
	if po.PONumber == "" {
		t.Skip("this seed has no open CHF purchase order")
	}
	item := h.goodItem("prp_fx")
	item.SupplierNumber = po.SupplierNumber
	item.PONumber = po.PONumber
	item.Currency = "EUR"
	item.SourceCurrency = "EUR"
	item.GrossAmount = model.MustDecimal("999999.00")
	item.VATAmount = model.MustDecimal("0.00")
	_, res := h.postSingle(item, "key-fx")
	if res.Status != StatusPosted {
		t.Fatalf("a cross-currency posting was rejected with %q", res.Code)
	}
}

// TestIdempotencyMatrix asserts the whole idempotency contract at once: a missing
// key is 400, the same key with the same body replays the original response with
// idempotency_replay true and creates nothing, and the same key with a different
// body is 409.
func TestIdempotencyMatrix(t *testing.T) {
	h := newHarness(t, nil)

	rec := h.post(RouteDocuments, h.goodItem("prp_nokey"), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no idempotency key: status %d, want 400", rec.Code)
	}
	if got := h.errorBody(rec).Code; got != httpx.CodeIdempotencyKeyRequired {
		t.Fatalf("no idempotency key: code %q, want %q", got, httpx.CodeIdempotencyKeyRequired)
	}
	if h.metrics().DocumentsCreated != 0 {
		t.Fatal("a posting without an idempotency key created a document")
	}

	item := h.goodItem("prp_idem")
	first, firstRes := h.postSingle(item, "key-idem")
	if firstRes.Status != StatusPosted || firstRes.IdempotencyReplay {
		t.Fatalf("first posting: %+v", firstRes)
	}

	second, secondRes := h.postSingle(item, "key-idem")
	if second.Code != first.Code {
		t.Fatalf("replay status %d, want the original %d", second.Code, first.Code)
	}
	if !secondRes.IdempotencyReplay {
		t.Fatal("a replay must carry idempotency_replay true")
	}
	if secondRes.DocumentNumber != firstRes.DocumentNumber {
		t.Fatalf("replay document number %q, want the original %q",
			secondRes.DocumentNumber, firstRes.DocumentNumber)
	}
	if got := second.Header().Get(httpx.HeaderIdempotentReplay); got != "true" {
		t.Fatalf("%s = %q on a replay, want true", httpx.HeaderIdempotentReplay, got)
	}
	// Verbatim: the replayed body differs from the original in exactly one
	// member, the replay flag itself.
	wantReplayBody := strings.Replace(first.Body.String(),
		`"idempotency_replay":false`, `"idempotency_replay":true`, 1)
	if second.Body.String() != wantReplayBody {
		t.Fatalf("the replayed body is not the original with the replay flag set:\n%s\n%s",
			first.Body.String(), second.Body.String())
	}
	if h.metrics().DocumentsCreated != 1 {
		t.Fatalf("documents_created %d after a replay, want 1", h.metrics().DocumentsCreated)
	}
	if h.metrics().DuplicateDocumentAttempts != 0 {
		t.Fatal("a replay is not a duplicate attempt: the client did exactly the right thing")
	}

	changed := item
	changed.GrossAmount = model.MustDecimal("1251.00")
	conflict := h.post(RouteDocuments, changed, "key-idem")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same key, different body: status %d, want 409", conflict.Code)
	}
	if got := h.errorBody(conflict).Code; got != httpx.CodeIdempotencyKeyReused {
		t.Fatalf("same key, different body: code %q, want %q", got, httpx.CodeIdempotencyKeyReused)
	}
	if h.metrics().DocumentsCreated != 1 {
		t.Fatal("a conflicting key created a second document")
	}
}

// TestDuplicateDetector asserts the detector of section 7.2: a second posting of
// an external reference that already produced a document, under a DIFFERENT
// idempotency key, is answered with the ORIGINAL document number, marked
// duplicate, creates no second document and increments
// duplicate_document_attempts.
func TestDuplicateDetector(t *testing.T) {
	h := newHarness(t, nil)
	item := h.goodItem("prp_dup")
	_, first := h.postSingle(item, "key-dup-a")
	if first.Status != StatusPosted {
		t.Fatalf("first posting: %+v", first)
	}

	// A different key, and a different body too, so nothing about this is a
	// replay: it is the same business document arriving twice.
	changed := item
	changed.VATAmount = model.MustDecimal("89.36")
	rec, second := h.postSingle(changed, "key-dup-b")
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate: status %d, want 200", rec.Code)
	}
	if !second.Duplicate {
		t.Fatal("the second posting of an external reference must be marked duplicate")
	}
	if second.IdempotencyReplay {
		t.Fatal("a duplicate is not a replay: the key was different")
	}
	if second.DocumentNumber != first.DocumentNumber {
		t.Fatalf("duplicate document number %q, want the original %q",
			second.DocumentNumber, first.DocumentNumber)
	}
	m := h.metrics()
	if m.DocumentsCreated != 1 {
		t.Fatalf("documents_created %d, want 1: no second document may exist", m.DocumentsCreated)
	}
	if m.DuplicateDocumentAttempts != 1 {
		t.Fatalf("duplicate_document_attempts %d, want 1", m.DuplicateDocumentAttempts)
	}
}

// TestDuplicateWithinOneBatch asserts a batch that carries the same external
// reference twice books it once. The second occurrence is a duplicate, not a
// second document: a corrupting mock would be worse than a strict one.
func TestDuplicateWithinOneBatch(t *testing.T) {
	h := newHarness(t, nil)
	item := h.goodItem("prp_batch_dup")
	rec, resp := h.postBatch([]PostingItem{item, item}, "key-batch-dup")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: both items succeeded", rec.Code)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("%d results, want 2", len(resp.Results))
	}
	if resp.Results[0].Duplicate || !resp.Results[1].Duplicate {
		t.Fatalf("want the second occurrence marked duplicate, got %+v", resp.Results)
	}
	if resp.Results[0].DocumentNumber != resp.Results[1].DocumentNumber {
		t.Fatal("both occurrences must resolve to one document number")
	}
	if h.metrics().DocumentsCreated != 1 {
		t.Fatalf("documents_created %d, want 1", h.metrics().DocumentsCreated)
	}
}

// TestBatchMixedOutcomeIs207 asserts the multi-status rule: a batch with more
// than one distinct outcome is 207, a uniform batch is 200, and the per-item
// results keep the request's order.
func TestBatchMixedOutcomeIs207(t *testing.T) {
	h := newHarness(t, nil)
	good := h.goodItem("prp_mix_ok")
	bad := h.goodItem("prp_mix_bad")
	bad.SupplierNumber = h.blockedSupplier()

	rec, resp := h.postBatch([]PostingItem{good, bad}, "key-mix")
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207", rec.Code)
	}
	if resp.Posted != 1 || resp.Rejected != 1 {
		t.Fatalf("counts posted=%d rejected=%d, want 1 and 1", resp.Posted, resp.Rejected)
	}
	if resp.Results[0].ItemKey != good.ItemKey || resp.Results[1].ItemKey != bad.ItemKey {
		t.Fatalf("results are not in request order: %+v", resp.Results)
	}

	uniform, uniformResp := h.postBatch([]PostingItem{h.goodItem("prp_u1"), h.goodItem("prp_u2")}, "key-uniform")
	if uniform.Code != http.StatusOK {
		t.Fatalf("uniform batch: status %d, want 200", uniform.Code)
	}
	if uniformResp.Posted != 2 {
		t.Fatalf("uniform batch posted %d, want 2", uniformResp.Posted)
	}

	allBad := []PostingItem{h.goodItem("prp_b1"), h.goodItem("prp_b2")}
	for i := range allBad {
		allBad[i].SupplierNumber = h.blockedSupplier()
	}
	rejected, rejectedResp := h.postBatch(allBad, "key-allbad")
	if rejected.Code != http.StatusOK {
		t.Fatalf("all-rejected batch: status %d, want 200: nothing about it is mixed", rejected.Code)
	}
	if rejectedResp.Rejected != 2 {
		t.Fatalf("all-rejected batch rejected %d, want 2", rejectedResp.Rejected)
	}
}

// TestBatchSizeLimit asserts the maximum batch size, that the limit is a 400 and
// not a partial application, and that an empty batch is refused.
func TestBatchSizeLimit(t *testing.T) {
	h := newHarness(t, nil)
	items := make([]PostingItem, 0, MaxBatchItems+1)
	for i := 0; i <= MaxBatchItems; i++ {
		items = append(items, h.goodItem(fmt.Sprintf("prp_big_%03d", i)))
	}
	rec := h.post(RouteDocumentsBatch, map[string]any{"items": items}, "key-too-big")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := h.errorBody(rec).Code; got != httpx.CodeMalformedBody {
		t.Fatalf("code %q, want %q", got, httpx.CodeMalformedBody)
	}
	if h.metrics().DocumentsCreated != 0 {
		t.Fatal("an over-sized batch was partially applied")
	}

	ok, resp := h.postBatch(items[:MaxBatchItems], "key-exactly-max")
	if ok.Code != http.StatusOK {
		t.Fatalf("a batch of exactly %d: status %d, want 200", MaxBatchItems, ok.Code)
	}
	if resp.Posted != MaxBatchItems {
		t.Fatalf("posted %d of %d", resp.Posted, MaxBatchItems)
	}

	empty := h.post(RouteDocumentsBatch, map[string]any{"items": []PostingItem{}}, "key-empty")
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty batch: status %d, want 400", empty.Code)
	}
}

// TestPostingRejectsMalformedItems asserts the shape checks. A shape failure is a
// transport error and fails the whole request, so a malformed batch can never be
// half applied.
func TestPostingRejectsMalformedItems(t *testing.T) {
	h := newHarness(t, nil)
	tests := []struct {
		name   string
		body   string
		reason string
	}{
		{"empty body", ``, "body_empty"},
		{"not json", `not json at all`, "json_invalid"},
		{"amount as a JSON number", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"2026-03-01","currency":"CHF","gross_amount":1250.00}`, "number_format"},
		{"gross amount missing", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"2026-03-01","currency":"CHF"}`, "field_required"},
		{"external reference missing", `{"company_code":"CH10","supplier_number":"0000000401",
			"supplier_invoice_number":"1","document_type":"RE","document_date":"2026-03-01",
			"currency":"CHF","gross_amount":"1.00"}`, "field_required"},
		{"unknown company code", `{"external_reference":"a","company_code":"CH99",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"2026-03-01","currency":"CHF","gross_amount":"1.00"}`, "enum_unknown"},
		{"unknown document type", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"XX",
			"document_date":"2026-03-01","currency":"CHF","gross_amount":"1.00"}`, "enum_unknown"},
		{"supplier number not digits", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"SUP-417","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"2026-03-01","currency":"CHF","gross_amount":"1.00"}`, "supplier_number_format"},
		{"document date not a date", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"29.03.2026","currency":"CHF","gross_amount":"1.00"}`, "date_format"},
		{"unknown currency", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"2026-03-01","currency":"XBT","gross_amount":"1.00"}`, "enum_unknown"},
		{"amount below the minor unit", `{"external_reference":"a","company_code":"CH10",
			"supplier_number":"0000000401","supplier_invoice_number":"1","document_type":"RE",
			"document_date":"2026-03-01","currency":"CHF","gross_amount":"1.005"}`, "money_scale"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.post(RouteDocuments, tc.body, fmt.Sprintf("key-malformed-%02d", i))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", rec.Code)
			}
			body := h.errorBody(rec)
			if body.Code != httpx.CodeMalformedBody {
				t.Fatalf("code %q, want %q", body.Code, httpx.CodeMalformedBody)
			}
			if got, _ := body.Details["reason"].(string); got != tc.reason {
				t.Fatalf("reason %q, want %q", got, tc.reason)
			}
		})
	}
	if h.metrics().DocumentsCreated != 0 {
		t.Fatal("a malformed request created a document")
	}
}

// TestPostingItemKeyDefaultsToExternalReference asserts the documented default,
// so a client that only sends one identifier still gets its results correlated.
func TestPostingItemKeyDefaultsToExternalReference(t *testing.T) {
	h := newHarness(t, nil)
	item := h.goodItem("prp_default_key")
	item.ItemKey = ""
	_, res := h.postSingle(item, "key-default")
	if res.ItemKey != item.ExternalReference {
		t.Fatalf("item_key %q, want the external reference %q", res.ItemKey, item.ExternalReference)
	}
}

// TestDocumentNumbersAreSequentialAndStable asserts document numbers are handed
// out in one deterministic sequence, so two runs of the same script produce the
// same numbers.
func TestDocumentNumbersAreSequentialAndStable(t *testing.T) {
	h := newHarness(t, nil)
	var got []string
	for i := 0; i < 3; i++ {
		_, res := h.postSingle(h.goodItem(fmt.Sprintf("prp_seq_%d", i)), fmt.Sprintf("key-seq-%d", i))
		got = append(got, res.DocumentNumber)
	}
	want := []string{"AP-2026-0000001", "AP-2026-0000002", "AP-2026-0000003"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("document numbers %v, want %v", got, want)
		}
	}

	rec := h.adminGet(AdminPathDocuments)
	var docs AdminDocuments
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("decode documents: %v", err)
	}
	if docs.Count != 3 {
		t.Fatalf("admin reports %d documents, want 3", docs.Count)
	}
	for i, doc := range docs.Documents {
		if doc.DocumentNumber != want[i] {
			t.Fatalf("admin document %d is %q, want %q", i, doc.DocumentNumber, want[i])
		}
	}
}

// TestPostingDateDefaultsToDocumentDate asserts the documented default. Inventing
// a date would need a clock, and there is none.
func TestPostingDateDefaultsToDocumentDate(t *testing.T) {
	h := newHarness(t, nil)
	item := h.goodItem("prp_no_posting_date")
	item.PostingDate = ""
	item.DocumentDate = "2026-03-15"
	_, res := h.postSingle(item, "key-no-posting-date")
	if res.PostingDate != "2026-03-15" {
		t.Fatalf("posting_date %q, want the document date", res.PostingDate)
	}
}

// TestPostingRejectsANonJSONMediaType asserts the posting endpoints take JSON and
// only JSON. The twin is the service with four wire formats; inventing them here
// would be inventing a contract.
func TestPostingRejectsANonJSONMediaType(t *testing.T) {
	h := newHarness(t, nil)
	body, err := marshalJSON(h.goodItem("prp_media"))
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, RouteDocuments, bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/csv")
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set(httpx.HeaderIdempotencyKey, "key-media")
	rec := h.do(req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", rec.Code)
	}
	if got := h.errorBody(rec).Code; got != httpx.CodeUnsupportedContentType {
		t.Fatalf("code %q, want %q", got, httpx.CodeUnsupportedContentType)
	}
	if h.metrics().DocumentsCreated != 0 {
		t.Fatal("a request with the wrong media type created a document")
	}

	// A charset parameter on the documented media type is fine.
	ok := httptest.NewRequest(http.MethodPost, RouteDocuments, bytes.NewReader(body))
	ok.Header.Set("Content-Type", "application/json; charset=UTF-8")
	ok.Header.Set("Authorization", "Bearer "+h.token)
	ok.Header.Set(httpx.HeaderIdempotencyKey, "key-media-ok")
	if rec := h.do(ok); rec.Code != http.StatusOK {
		t.Fatalf("charset parameter: status %d, want 200", rec.Code)
	}
}
