package erp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// The business rejection codes of the posting endpoints. A business rejection is
// NOT a transport error: it arrives as a per-item result inside a 200 or a 207,
// it always carries retriable false, and a client that retries one is making a
// mistake the grader scores.
//
// Every message names the field and the rule it violated and never the value the
// client should have sent. That is section 17.2 of the build specification and it
// is enforced by TestNoErrorBodyCarriesASeededValue.
const (
	// RejectSupplierUnknown reports a supplier number no creditor carries.
	//
	// It is not in the published rejection table of section 7.2, because the
	// twin refuses an unknown creditor before it ever proposes a posting. It
	// exists because a system of record that invented a creditor on write would
	// be a worse mock than one that says no.
	RejectSupplierUnknown = "ERP_SUPPLIER_UNKNOWN"
	// RejectSupplierBlocked reports a creditor with a payment block.
	RejectSupplierBlocked = "ERP_SUPPLIER_BLOCKED"
	// RejectPONotFound reports a purchase order number no order carries.
	RejectPONotFound = "ERP_PO_NOT_FOUND"
	// RejectPOClosed reports a purchase order that is not open.
	RejectPOClosed = "ERP_PO_CLOSED"
	// RejectPeriodClosed reports a posting date in a closed fiscal period.
	RejectPeriodClosed = "ERP_PERIOD_CLOSED"
	// RejectCostCenterUnknown reports a cost center that is not configured for
	// the posting's company code.
	RejectCostCenterUnknown = "ERP_COST_CENTER_UNKNOWN"
	// RejectAmountMismatch reports a posted amount above the referenced
	// purchase order plus the permitted tolerance.
	RejectAmountMismatch = "ERP_AMOUNT_MISMATCH"
)

// Posting result statuses.
const (
	// StatusPosted means a document exists for this item: either the document
	// this request created, or, with Duplicate set, the document an earlier
	// request created for the same external reference.
	StatusPosted = "posted"
	// StatusRejected means the ERP refused the item on business grounds.
	StatusRejected = "rejected"
)

// AmountTolerancePercent is the published tolerance between a posted amount and
// the purchase order it references.
const AmountTolerancePercent = 5

// toleranceScale is the scale the tolerance comparison is carried out at. Both
// sides rescale to it exactly (line amounts are at two decimals, unit prices at
// four), so the comparison is integer arithmetic over minor units with no
// rounding and no float64 anywhere near it.
const toleranceScale uint8 = 4

// A PostingItem is one item of an AP document posting request.
//
// Money is a plain decimal string on this surface and dates are YYYY-MM-DD,
// deliberately different from the SOAP channel's host-locale numbers and from
// the legacy file's Swiss grouping. A JSON number in an amount or a key field is
// rejected: a number has already lost the leading zeros and the exact scale by
// the time it is parsed.
type PostingItem struct {
	// ItemKey is the client's handle for this item, echoed on the result. It
	// defaults to ExternalReference.
	ItemKey string `json:"item_key"`
	// ExternalReference is the client's stable identity of the business
	// document. The ERP indexes duplicates by (tenant, external_reference), so
	// this field, not the idempotency key, is what protects a customer ledger.
	ExternalReference string `json:"external_reference"`

	CompanyCode           string `json:"company_code"`
	SupplierNumber        string `json:"supplier_number"`
	SupplierInvoiceNumber string `json:"supplier_invoice_number"`
	DocumentType          string `json:"document_type"`
	DocumentDate          string `json:"document_date"`
	PostingDate           string `json:"posting_date"`
	PONumber              string `json:"po_number"`
	CostCenter            string `json:"cost_center"`
	Currency              string `json:"currency"`

	GrossAmount model.Decimal `json:"gross_amount"`
	VATAmount   model.Decimal `json:"vat_amount"`

	// SourceCurrency, SourceGrossAmount, FxRate and FxRateFactor document the
	// conversion the client performed. The ERP stores them and never recomputes
	// them: an ERP that recomputed the conversion and answered with its own
	// result would be handing out the answer.
	SourceCurrency    string        `json:"source_currency"`
	SourceGrossAmount model.Decimal `json:"source_gross_amount"`
	FxRate            model.Decimal `json:"fx_rate"`
	FxRateFactor      int64         `json:"fx_rate_factor"`
}

// postingItemWire is the decoding shape of a PostingItem. The amount fields are
// pointers so an absent amount can be told apart from an explicit zero, which is
// the difference between "no VAT on this invoice" and "the client forgot the
// gross amount".
type postingItemWire struct {
	ItemKey               string         `json:"item_key"`
	ExternalReference     string         `json:"external_reference"`
	CompanyCode           string         `json:"company_code"`
	SupplierNumber        string         `json:"supplier_number"`
	SupplierInvoiceNumber string         `json:"supplier_invoice_number"`
	DocumentType          string         `json:"document_type"`
	DocumentDate          string         `json:"document_date"`
	PostingDate           string         `json:"posting_date"`
	PONumber              string         `json:"po_number"`
	CostCenter            string         `json:"cost_center"`
	Currency              string         `json:"currency"`
	GrossAmount           *model.Decimal `json:"gross_amount"`
	VATAmount             *model.Decimal `json:"vat_amount"`
	SourceCurrency        string         `json:"source_currency"`
	SourceGrossAmount     *model.Decimal `json:"source_gross_amount"`
	FxRate                *model.Decimal `json:"fx_rate"`
	FxRateFactor          int64          `json:"fx_rate_factor"`
}

// A PostingResult is the per-item outcome of a posting request.
//
// Retriable is a pointer so it is present, and false, on every rejection and
// absent on a success: a business rejection must say out loud that retrying it
// is wrong, and a success has nothing to say about retries.
type PostingResult struct {
	ItemKey string `json:"item_key"`
	Status  string `json:"status"`

	DocumentNumber string `json:"document_number,omitempty"`
	FiscalYear     int    `json:"fiscal_year,omitempty"`
	PostingDate    string `json:"posting_date,omitempty"`
	ERPRevision    int    `json:"erp_revision,omitempty"`

	// IdempotencyReplay is true when this whole response is the stored answer
	// to a repeated idempotency key.
	IdempotencyReplay bool `json:"idempotency_replay"`
	// Duplicate is true when the external reference already had a document and
	// this item was answered with that document's number. No second document
	// was created, and duplicate_document_attempts was incremented.
	Duplicate bool `json:"duplicate,omitempty"`

	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retriable *bool  `json:"retriable,omitempty"`
}

// A BatchResponse is the body of a batched posting. The counts are a convenience
// over Results, which stays the authority and keeps the request's item order.
type BatchResponse struct {
	Results    []PostingResult `json:"results"`
	Posted     int             `json:"posted"`
	Rejected   int             `json:"rejected"`
	Duplicates int             `json:"duplicates"`
}

// A Document is a created AP document. It is the ERP's own record of what it
// booked and is served by GET /erp-admin/v1/documents.
type Document struct {
	DocumentNumber        string        `json:"document_number"`
	FiscalYear            int           `json:"fiscal_year"`
	PostingDate           string        `json:"posting_date"`
	Revision              int           `json:"erp_revision"`
	Tenant                string        `json:"tenant"`
	ExternalReference     string        `json:"external_reference"`
	CompanyCode           string        `json:"company_code"`
	SupplierNumber        string        `json:"supplier_number"`
	SupplierInvoiceNumber string        `json:"supplier_invoice_number"`
	DocumentType          string        `json:"document_type"`
	DocumentDate          string        `json:"document_date"`
	Currency              string        `json:"currency"`
	GrossAmount           model.Decimal `json:"gross_amount"`
	VATAmount             model.Decimal `json:"vat_amount"`
	PONumber              string        `json:"po_number"`
	CostCenter            string        `json:"cost_center"`
	// IdempotencyKey is the key of the request that created the document. A
	// later posting of the same external reference under a different key is
	// answered with this document and creates nothing.
	IdempotencyKey string `json:"idempotency_key"`
}

// handlePostDocument serves the single posting.
func (s *Server) handlePostDocument(w http.ResponseWriter, r *http.Request) {
	s.servePosting(w, r, false)
}

// handlePostDocumentBatch serves the batched posting.
func (s *Server) handlePostDocumentBatch(w http.ResponseWriter, r *http.Request) {
	s.servePosting(w, r, true)
}

// servePosting is the whole posting contract, single and batch.
//
// The order is fixed, and each step is a documented rule of section 7.2:
//
//  1. read the body once, so it can be hashed and then decoded,
//  2. require an Idempotency-Key; a posting without one is 400,
//  3. decode and validate every item before any of them is booked, so a
//     malformed batch cannot half-post,
//  4. resolve the idempotency key: a known key with the same body replays the
//     stored response, a known key with a different body is 409,
//  5. book the items in request order under one lock, so document numbers are
//     assigned deterministically,
//  6. render the response twice, once as sent and once as it will be replayed,
//     and store the replay copy.
func (s *Server) servePosting(w http.ResponseWriter, r *http.Request, batch bool) {
	if ct := r.Header.Get("Content-Type"); !isJSONMediaType(ct) {
		// The ERP posts JSON and only JSON. The twin is the service with four
		// wire formats; offering them here would be inventing a contract the
		// specification does not describe.
		httpx.Fail(w, httpx.UnsupportedContentType(ct))
		return
	}
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	key, err := httpx.RequireIdempotencyKey(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	if strings.TrimSpace(key) == "" {
		httpx.Fail(w, httpx.IdempotencyKeyRequired())
		return
	}
	items, err := decodePostingItems(body, batch)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	for i := range items {
		if err := validatePostingItem(items[i]); err != nil {
			httpx.Fail(w, err)
			return
		}
	}

	_, st := s.state()
	idemKey := httpx.IdempotencyKey{Tenant: Tenant, Method: r.Method, Path: r.URL.Path, Key: key}
	rec, outcome := st.idem.Check(idemKey, httpx.BodyHash(body))
	switch outcome {
	case httpx.IdempotencyReplay:
		httpx.WriteReplay(w, rec)
		return
	case httpx.IdempotencyConflict:
		httpx.Fail(w, httpx.IdempotencyKeyReused())
		return
	}

	results := s.book(items, key)
	status := postingStatus(results, batch)

	sent, err := marshalJSON(postingPayload(results, batch, false))
	if err != nil {
		httpx.Fail(w, httpx.Internal("response body not serializable"))
		return
	}
	replay, err := marshalJSON(postingPayload(results, batch, true))
	if err != nil {
		httpx.Fail(w, httpx.Internal("response body not serializable"))
		return
	}
	header := http.Header{"Content-Type": []string{httpx.MediaJSON}}
	st.idem.Put(idemKey, httpx.IdempotencyRecord{
		Status:   status,
		Header:   header,
		Body:     replay,
		BodyHash: httpx.BodyHash(body),
	})

	// The applied-then-500 posting, and the reason an idempotency key exists at
	// all on this endpoint. The documents are booked and the replay copy is
	// stored, so the retry path is exact:
	//
	//   - a client that retries with the SAME key replays the stored 207 and
	//     ends with one document, which is the whole contract,
	//   - a client that draws a fresh key posts the same external references
	//     again, which the duplicate detector answers with the original document
	//     while counting duplicate_document_attempts.
	//
	// The signature is the external references and never the idempotency key: it
	// has to identify the same business request across attempts, so that attempt
	// 0 faults and every retry passes. Keying it on the client's key would fault
	// a fresh-key retry as well, and a client would then be punished for the
	// injection rather than for what it did.
	//
	// Without this, nothing in the landscape ever double-posted: the middleware
	// injects faults BEFORE a handler runs, so an injected fault never applied
	// anything, and three different idempotency-key defects cost nothing. The
	// mutation gate is what surfaced that.
	if s.governor().ApplyThenFail(applyFailSignature(r.URL.Path, items)) {
		httpx.Fail(w, httpx.Internal("the posting was booked and the response was lost"))
		return
	}

	w.Header().Set("Content-Type", httpx.MediaJSON)
	w.WriteHeader(status)
	_, _ = w.Write(sent)
}

// applyFailSignature identifies one posting request by what it is about, so the
// same business request has one signature across every attempt and whatever
// idempotency key the client chose.
func applyFailSignature(path string, items []PostingItem) string {
	refs := make([]string, 0, len(items))
	for i := range items {
		refs = append(refs, items[i].ExternalReference)
	}
	// Request order is the client's; the signature must not be.
	sort.Strings(refs)
	return "APPLYFAIL|" + path + "|" + strings.Join(refs, ",")
}

// isJSONMediaType reports whether a Content-Type is acceptable on the posting
// endpoints: absent, or application/json with either no charset or UTF-8.
func isJSONMediaType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	mt, params := contentType, ""
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		mt, params = contentType[:i], contentType[i+1:]
	}
	if !strings.EqualFold(strings.TrimSpace(mt), httpx.MediaJSON) {
		return false
	}
	for _, part := range strings.Split(params, ";") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 || !strings.EqualFold(strings.TrimSpace(part[:eq]), "charset") {
			continue
		}
		cs := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
		if !strings.EqualFold(cs, "utf-8") && !strings.EqualFold(cs, "utf8") {
			return false
		}
	}
	return true
}

// postingStatus is the HTTP status of a posting response: 207 Multi-Status when a
// batch produced more than one distinct outcome, 200 otherwise. A batch in which
// everything succeeded, or everything was rejected, is not mixed and is 200.
func postingStatus(results []PostingResult, batch bool) int {
	if !batch || len(results) < 2 {
		return http.StatusOK
	}
	first := results[0].Status
	for _, res := range results[1:] {
		if res.Status != first {
			return http.StatusMultiStatus
		}
	}
	return http.StatusOK
}

// postingPayload builds the response body. replay marks every item as a replayed
// answer, which is the one field that differs between the response as first sent
// and the response as later replayed: the stored bytes are otherwise identical,
// down to the document numbers and the item order.
func postingPayload(results []PostingResult, batch bool, replay bool) any {
	out := make([]PostingResult, len(results))
	copy(out, results)
	body := BatchResponse{Results: out}
	for i := range out {
		out[i].IdempotencyReplay = replay
		switch {
		case out[i].Status == StatusRejected:
			body.Rejected++
		case out[i].Duplicate:
			body.Posted++
			body.Duplicates++
		default:
			body.Posted++
		}
	}
	if !batch {
		if len(out) == 0 {
			return PostingResult{}
		}
		return out[0]
	}
	return body
}

// book applies the items in request order. It holds the server lock for the whole
// request, so two concurrent postings cannot interleave document numbers and the
// transcript of a run is a function of the request order alone.
func (s *Server) book(items []PostingItem, idempotencyKey string) []PostingResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, st := s.data, s.st
	out := make([]PostingResult, 0, len(items))
	for _, item := range items {
		out = append(out, s.bookOne(d, st, item, idempotencyKey))
	}
	return out
}

// bookOne resolves one item. The caller holds the lock.
//
// The duplicate check comes first, before any business rule: an external
// reference that already produced a document is answered with that document
// whatever else is true of the item, because creating a second document in a
// customer's ledger is unrecoverable and there is no reversal endpoint.
func (s *Server) bookOne(d *data, st *runState, item PostingItem, idempotencyKey string) PostingResult {
	if i, ok := st.docByRef[dupIndexKey(item.ExternalReference)]; ok {
		doc := st.documents[i]
		st.gov.IncDuplicateDocumentAttempts()
		return PostingResult{
			ItemKey:        item.ItemKey,
			Status:         StatusPosted,
			DocumentNumber: doc.DocumentNumber,
			FiscalYear:     doc.FiscalYear,
			PostingDate:    doc.PostingDate,
			ERPRevision:    doc.Revision,
			Duplicate:      true,
		}
	}
	if code, message := d.rejectPosting(item); code != "" {
		st.rejections[code]++
		no := false
		return PostingResult{
			ItemKey:   item.ItemKey,
			Status:    StatusRejected,
			Code:      code,
			Message:   message,
			Retriable: &no,
		}
	}

	postingDate := item.effectivePostingDate()
	year := fiscalYearOf(postingDate)
	st.docSeq++
	doc := Document{
		DocumentNumber:        fmt.Sprintf("AP-%04d-%07d", year, st.docSeq),
		FiscalYear:            year,
		PostingDate:           postingDate,
		Revision:              1,
		Tenant:                Tenant,
		ExternalReference:     item.ExternalReference,
		CompanyCode:           item.CompanyCode,
		SupplierNumber:        item.SupplierNumber,
		SupplierInvoiceNumber: item.SupplierInvoiceNumber,
		DocumentType:          item.DocumentType,
		DocumentDate:          item.DocumentDate,
		Currency:              item.Currency,
		GrossAmount:           item.GrossAmount,
		VATAmount:             item.VATAmount,
		PONumber:              item.PONumber,
		CostCenter:            item.CostCenter,
		IdempotencyKey:        idempotencyKey,
	}
	st.documents = append(st.documents, doc)
	st.docByRef[dupIndexKey(item.ExternalReference)] = len(st.documents) - 1
	return PostingResult{
		ItemKey:        item.ItemKey,
		Status:         StatusPosted,
		DocumentNumber: doc.DocumentNumber,
		FiscalYear:     doc.FiscalYear,
		PostingDate:    doc.PostingDate,
		ERPRevision:    doc.Revision,
	}
}

// dupIndexKey is the duplicate index key: (tenant, external_reference). This
// landscape has one tenant, and the tenant is in the key anyway, because a
// duplicate index that spanned tenants would be a data leak the first time a
// second one appeared.
func dupIndexKey(externalReference string) string {
	return Tenant + model.KeySeparator + externalReference
}

// effectivePostingDate is the item's posting date, defaulting to the document
// date. Defaulting an absent posting date to the document date is what an AP
// clerk does; inventing today's date would need a clock.
func (item PostingItem) effectivePostingDate() string {
	if item.PostingDate != "" {
		return item.PostingDate
	}
	return item.DocumentDate
}

// fiscalYearOf returns the fiscal year of a YYYY-MM-DD date. The date has
// already been validated, so a parse failure here is impossible and yields zero
// rather than a panic.
func fiscalYearOf(date string) int {
	d, err := model.ParseDate(date)
	if err != nil {
		return 0
	}
	return d.Year
}

// rejectPosting applies the business rules in a fixed, documented order and
// returns the first failure, or an empty code when the item is bookable.
//
// The order mirrors the twin's published matching order, so an item the twin let
// through cannot be refused here for a reason the twin already checked and
// accepted: creditor, then purchase order, then the fiscal period, then the cost
// center, then the amount tolerance. Exactly one rejection is returned, because a
// list of reasons invites a client to fix them one request at a time.
func (d *data) rejectPosting(item PostingItem) (code, message string) {
	sup, ok := d.supplier(item.SupplierNumber)
	if !ok {
		return RejectSupplierUnknown, "supplier_number: no creditor carries this supplier number"
	}
	if sup.Blocked {
		return RejectSupplierBlocked, "supplier_number: the creditor carries a payment block"
	}

	var po model.PurchaseOrder
	havePO := false
	if strings.TrimSpace(item.PONumber) != "" {
		number, _ := seed.SplitPOReference(item.PONumber)
		po, havePO = d.purchaseOrder(number)
		if !havePO {
			return RejectPONotFound, "po_number: no purchase order carries this number"
		}
		if po.Status != model.POStatusOpen {
			return RejectPOClosed, "po_number: the purchase order is not open"
		}
	}

	if closed, err := model.CompareDates(item.effectivePostingDate(), d.set.ClosedPeriodBefore); err == nil && closed < 0 {
		return RejectPeriodClosed, "posting_date: the fiscal period of this posting date is closed"
	}

	if code := strings.TrimSpace(item.CostCenter); code != "" {
		// An empty cost center is accepted on purpose. The customer's own
		// format leaves the position cost center undefined, a correct client
		// therefore surfaces the gap instead of inventing a value, and refusing
		// the posting for it would punish exactly that behavior.
		cc, ok := d.costCenter(code)
		if !ok || cc.CompanyCode != item.CompanyCode {
			return RejectCostCenterUnknown, "cost_center: not configured for this company code"
		}
	}

	if havePO {
		comparable, within := d.withinAmountTolerance(item, po)
		if comparable && !within {
			return RejectAmountMismatch,
				"gross_amount: exceeds the referenced purchase order by more than the permitted tolerance of " +
					strconv.Itoa(AmountTolerancePercent) + " percent"
		}
	}
	return "", ""
}

// withinAmountTolerance compares a posting against the purchase order it
// references. It returns whether the comparison could be made at all and, if so,
// whether the posting is within tolerance.
//
// Three decisions are load bearing and each is a deviation from the literal
// wording of section 7.2, recorded here because the literal reading would reject
// correct work:
//
//   - The comparison is NET of VAT. The purchase order carries net line amounts
//     and the posting carries a gross amount, so comparing them directly would
//     put every ordinary Swiss invoice 8.1 percent above its order and reject
//     it.
//   - The comparison is one sided. An invoice may cover a subset of an order's
//     lines, which is normal in accounts payable and would otherwise look like a
//     large negative deviation. Only over-billing is refused.
//   - It is skipped when the posting currency is not the order's currency. The
//     mock will not form an FX opinion to reject a posting: its own conversion
//     could contradict the client's, and the rejection would then be an oracle.
func (d *data) withinAmountTolerance(item PostingItem, po model.PurchaseOrder) (comparable, within bool) {
	if item.Currency != po.Currency {
		return false, true
	}
	// Fourth decision, and the same reasoning as the third: the comparison is
	// also skipped when the posting was CONVERTED, that is when its source
	// currency differs from the order's. The number on such a posting is the
	// client's conversion, so comparing it against the order would make our
	// rejection depend on the client's own exchange rate, which is exactly the
	// oracle the third rule refuses to become. A foreign-currency invoice
	// against a franc order is therefore accepted on amount, and whether the
	// conversion was right is decided by the state digest and the proposal
	// amounts, where it belongs.
	if src := strings.TrimSpace(item.SourceCurrency); src != "" && src != po.Currency {
		return false, true
	}
	net, err := item.GrossAmount.Sub(item.VATAmount)
	if err != nil {
		return false, true
	}
	netMinor, err := net.MinorUnits(toleranceScale)
	if err != nil {
		return false, true
	}
	total, ok := d.poNetTotal[poKey(po.PONumber)]
	if !ok {
		return false, true
	}
	totalMinor, err := total.MinorUnits(toleranceScale)
	if err != nil || totalMinor <= 0 {
		return false, true
	}
	// Integer arithmetic over minor units, so no float64 comes near a money
	// comparison: net <= total * (100 + tolerance) / 100.
	return true, netMinor*100 <= totalMinor*(100+AmountTolerancePercent)
}

// decodePostingItems decodes a posting body.
//
// The single endpoint takes one item object; the batch endpoint takes
// {"items":[...]} and, as a convenience, a bare array. Unknown members are
// ignored rather than rejected: a client that sends a field we do not read has
// not made a mistake worth failing a batch over.
func decodePostingItems(body []byte, batch bool) ([]PostingItem, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, malformedItem("body_empty", "request body is empty", "", "")
	}
	if !batch {
		var wire postingItemWire
		if err := json.Unmarshal(trimmed, &wire); err != nil {
			return nil, malformedFromJSON(err, "")
		}
		item, err := wire.item()
		if err != nil {
			return nil, err
		}
		return []PostingItem{item}, nil
	}

	var wires []postingItemWire
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &wires); err != nil {
			return nil, malformedFromJSON(err, "")
		}
	} else {
		var envelope struct {
			Items []postingItemWire `json:"items"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, malformedFromJSON(err, "")
		}
		wires = envelope.Items
	}
	if len(wires) == 0 {
		return nil, malformedItem("items_required", "a batch must carry at least one item", "items", "")
	}
	if len(wires) > MaxBatchItems {
		e := malformedItem("too_many_items",
			"a batch must not carry more than "+strconv.Itoa(MaxBatchItems)+" items", "items", "")
		return nil, e
	}
	out := make([]PostingItem, 0, len(wires))
	for _, wire := range wires {
		item, err := wire.item()
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// item converts a decoded wire item to a PostingItem, defaulting the item key to
// the external reference.
func (w postingItemWire) item() (PostingItem, error) {
	item := PostingItem{
		ItemKey:               strings.TrimSpace(w.ItemKey),
		ExternalReference:     strings.TrimSpace(w.ExternalReference),
		CompanyCode:           strings.TrimSpace(w.CompanyCode),
		SupplierNumber:        strings.TrimSpace(w.SupplierNumber),
		SupplierInvoiceNumber: strings.TrimSpace(w.SupplierInvoiceNumber),
		DocumentType:          strings.TrimSpace(w.DocumentType),
		DocumentDate:          strings.TrimSpace(w.DocumentDate),
		PostingDate:           strings.TrimSpace(w.PostingDate),
		PONumber:              strings.TrimSpace(w.PONumber),
		CostCenter:            strings.TrimSpace(w.CostCenter),
		Currency:              strings.TrimSpace(w.Currency),
		SourceCurrency:        strings.TrimSpace(w.SourceCurrency),
		FxRateFactor:          w.FxRateFactor,
	}
	if item.ItemKey == "" {
		item.ItemKey = item.ExternalReference
	}
	if w.GrossAmount == nil {
		return PostingItem{}, malformedItem("field_required", "gross_amount is mandatory",
			"gross_amount", item.ItemKey)
	}
	item.GrossAmount = *w.GrossAmount
	if w.VATAmount != nil {
		item.VATAmount = *w.VATAmount
	}
	if w.SourceGrossAmount != nil {
		item.SourceGrossAmount = *w.SourceGrossAmount
	}
	if w.FxRate != nil {
		item.FxRate = *w.FxRate
	}
	return item, nil
}

// validatePostingItem checks the shape of one item. A shape failure is a
// transport error (400 MALFORMED_BODY) and not a business rejection: the ERP
// cannot say whether a document with no document date would have been bookable.
//
// The details name the offending field and echo the client's own item key, never
// a value the client was supposed to compute.
func validatePostingItem(item PostingItem) error {
	if item.ExternalReference == "" {
		return malformedItem("field_required", "external_reference is mandatory",
			"external_reference", item.ItemKey)
	}
	for _, f := range []struct{ field, value string }{
		{"company_code", item.CompanyCode},
		{"supplier_number", item.SupplierNumber},
		{"supplier_invoice_number", item.SupplierInvoiceNumber},
		{"document_type", item.DocumentType},
		{"document_date", item.DocumentDate},
		{"currency", item.Currency},
	} {
		if f.value == "" {
			return malformedItem("field_required", f.field+" is mandatory", f.field, item.ItemKey)
		}
	}
	if !model.IsKnownCompanyCode(item.CompanyCode) {
		return malformedItem("enum_unknown", "company_code is not a company code of this landscape",
			"company_code", item.ItemKey)
	}
	if !model.IsKnownDocumentType(item.DocumentType) {
		return malformedItem("enum_unknown", "document_type is not RE or GU", "document_type", item.ItemKey)
	}
	if !isSupplierNumberShape(item.SupplierNumber) {
		return malformedItem("supplier_number_format", "supplier_number must be one to ten digits",
			"supplier_number", item.ItemKey)
	}
	if !model.IsValidDate(item.DocumentDate) {
		return malformedItem("date_format", "document_date must be YYYY-MM-DD", "document_date", item.ItemKey)
	}
	if item.PostingDate != "" && !model.IsValidDate(item.PostingDate) {
		return malformedItem("date_format", "posting_date must be YYYY-MM-DD", "posting_date", item.ItemKey)
	}
	if !model.IsKnownCurrency(item.Currency) {
		return malformedItem("enum_unknown", "currency is not a currency of this landscape",
			"currency", item.ItemKey)
	}
	if item.SourceCurrency != "" && !model.IsKnownCurrency(item.SourceCurrency) {
		return malformedItem("enum_unknown", "source_currency is not a currency of this landscape",
			"source_currency", item.ItemKey)
	}
	scale, err := model.CurrencyScale(item.Currency)
	if err != nil {
		return malformedItem("enum_unknown", "currency is not a currency of this landscape",
			"currency", item.ItemKey)
	}
	// An input value is never rounded: an amount that does not fit the minor
	// units of its currency is refused, because rounding an input is how a
	// supplier gets paid the wrong sum.
	for _, f := range []struct {
		field  string
		amount model.Decimal
	}{{"gross_amount", item.GrossAmount}, {"vat_amount", item.VATAmount}} {
		if _, err := f.amount.MinorUnits(scale); err != nil {
			return malformedItem("money_scale",
				f.field+" carries more fraction digits than the currency has minor units",
				f.field, item.ItemKey)
		}
	}
	if item.FxRateFactor < 0 {
		return malformedItem("number_format", "fx_rate_factor must not be negative",
			"fx_rate_factor", item.ItemKey)
	}
	return nil
}

// isSupplierNumberShape reports whether a supplier number is one to ten digits.
//
// Section 16.1 calls supplier numbers canonically ten characters and says the
// ERP validates ^[0-9]{10}$ on write. That cannot be enforced here: section 1.3
// and the seeded creditor master both contain the seven character migration
// remnant "0000417", whose invoices are legitimately postable, and padding it to
// ten characters collides it with a different creditor. Enforcing the ten
// character rule would make a correct posting impossible, so the shape check is
// one to ten digits and the identity check is existence in the creditor master.
func isSupplierNumberShape(s string) bool {
	if len(s) == 0 || len(s) > 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// malformedItem builds the 400 MALFORMED_BODY of a shape failure. reason is from
// this closed vocabulary: body_empty, json_invalid, json_not_an_object,
// items_required, too_many_items, field_required, enum_unknown, date_format,
// money_scale, number_format, supplier_number_format.
func malformedItem(reason, message, field, itemKey string) *httpx.Error {
	e := httpx.MalformedBody(reason, message)
	if field != "" {
		e.WithDetail("field", field)
	}
	if itemKey != "" {
		e.WithDetail("item_key", itemKey)
	}
	return e
}

// malformedFromJSON classifies a decode failure. A JSON number or null where a
// decimal string belongs is reported as json_not_an_object's sibling
// number_format, because that mistake is a float64 in a money path and worth
// naming precisely.
func malformedFromJSON(err error, itemKey string) *httpx.Error {
	msg := "request body is not the documented JSON shape"
	reason := "json_invalid"
	switch {
	case errors.Is(err, model.ErrNotString):
		reason = "number_format"
		msg = "an amount must be a decimal STRING, never a JSON number"
	case errors.Is(err, model.ErrDecimalSyntax), errors.Is(err, model.ErrScaleRange):
		reason = "number_format"
		msg = "an amount must be a plain decimal string"
	}
	return malformedItem(reason, msg, "", itemKey)
}

// postingRefs returns the signature fragment and the item count of a posting
// request: the sorted set of the items' external references.
//
// It is called by the classifier, before the handler, and it buffers the body so
// the handler can still read it. A body it cannot decode yields an empty
// fragment; the handler will answer 400, and a request that never named an
// external reference has no logical identity to address a fault to.
func postingRefs(r *http.Request, batch bool) (string, int) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		return "", 0
	}
	items, err := decodePostingItems(body, batch)
	if err != nil {
		return "", 0
	}
	refs := make([]string, 0, len(items))
	for _, item := range items {
		refs = append(refs, item.ExternalReference)
	}
	sort.Strings(refs)
	return strings.Join(refs, ","), len(items)
}
