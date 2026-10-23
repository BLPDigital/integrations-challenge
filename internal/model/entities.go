package model

// Enumerations of the canonical AP model. The sets are closed: a value outside
// them is E_ENUM_UNKNOWN, never a pass-through.
const (
	// CompanyCodeCH10 and CompanyCodeCH20 are the two Swiss company codes.
	CompanyCodeCH10 = "CH10"
	CompanyCodeCH20 = "CH20"

	// POStatusOpen, POStatusClosed and POStatusCancelled are purchase order states.
	POStatusOpen      = "OPEN"
	POStatusClosed    = "CLOSED"
	POStatusCancelled = "CANCELLED"

	// RateTypeDaily and RateTypeMonthlyAvg are FX rate types. A daily conversion
	// must never fall back to a monthly average.
	RateTypeDaily      = "DAILY"
	RateTypeMonthlyAvg = "MONTHLY_AVG"

	// FxStatusActive and FxStatusDeleted are FX row states; DELETED is a soft
	// delete and must be dropped, not used.
	FxStatusActive  = "ACTIVE"
	FxStatusDeleted = "DELETED"

	// DocumentTypeInvoice and DocumentTypeCreditNote are AP document types. The
	// document type never implies a sign: the explicit sign of the amount wins.
	DocumentTypeInvoice    = "RE"
	DocumentTypeCreditNote = "GU"
)

// companyCodes, poStatuses, rateTypes, fxStatuses, documentTypes and unitsOfMeasure
// are the closed enumeration sets. They are only ever probed by key, never ranged
// over for output.
var (
	companyCodes  = map[string]bool{CompanyCodeCH10: true, CompanyCodeCH20: true}
	poStatuses    = map[string]bool{POStatusOpen: true, POStatusClosed: true, POStatusCancelled: true}
	rateTypes     = map[string]bool{RateTypeDaily: true, RateTypeMonthlyAvg: true}
	fxStatuses    = map[string]bool{FxStatusActive: true, FxStatusDeleted: true}
	documentTypes = map[string]bool{DocumentTypeInvoice: true, DocumentTypeCreditNote: true}

	// unitsOfMeasure is the twin's closed set. It carries the international
	// units AND the German-language units the Swiss landscape actually
	// delivers, because the subsidiary's legacy export writes STK, STD, PAU and
	// M2 and the group ERP's purchase orders carry STK as well. Accepting them
	// as first-class values keeps the challenge about parsing and matching
	// rather than about a unit-mapping table nobody published.
	//
	// TON and PAL are in the set even though neither is a base unit, because a
	// record carrying them is well formed and must reach the matching engine:
	// TON converts to KG through the published conversion table, and PAL has no
	// conversion at all and is therefore the documented EXC_UOM_UNMAPPABLE
	// case. Rejecting them here instead would replace a business exception a
	// clerk can act on with a record-level parse reject nobody asked for.
	unitsOfMeasure = map[string]bool{
		"EA": true, "PCE": true, "CTN": true, "KG": true, "G": true, "L": true, "M": true,
		"STK": true, "STD": true, "PAU": true, "M2": true, "H": true,
		"TON": true, "PAL": true,
	}
)

// IsKnownCompanyCode reports whether code is a known company code.
func IsKnownCompanyCode(code string) bool { return companyCodes[code] }

// IsKnownPOStatus reports whether status is a known purchase order status.
func IsKnownPOStatus(status string) bool { return poStatuses[status] }

// IsKnownRateType reports whether t is a known FX rate type.
func IsKnownRateType(t string) bool { return rateTypes[t] }

// IsKnownFxStatus reports whether status is a known FX row status.
func IsKnownFxStatus(status string) bool { return fxStatuses[status] }

// IsKnownDocumentType reports whether t is a known AP document type.
func IsKnownDocumentType(t string) bool { return documentTypes[t] }

// IsKnownUoM reports whether u is a known unit of measure
// (EA, PCE, CTN, KG, G, L, M).
func IsKnownUoM(u string) bool { return unitsOfMeasure[u] }

// Supplier is a creditor master record. SupplierNumber is the natural key; it is
// a string and its leading zeros are significant ("0000417" is not "417").
type Supplier struct {
	SupplierNumber   string `json:"supplier_number"`
	Name             string `json:"name"`
	Country          string `json:"country"` // ISO 3166-1 alpha-2
	Currency         string `json:"currency"`
	IBAN             string `json:"iban"`
	VATNumber        string `json:"vat_number"`
	PaymentTermsDays int    `json:"payment_terms_days"`
	Blocked          bool   `json:"blocked"`
	ChangeSeq        int64  `json:"change_seq"`
}

// Key returns the natural key of the supplier.
func (s Supplier) Key() string { return SupplierKey(s.SupplierNumber) }

// CostCenter is a cost center master record valid over a half-open date range
// [ValidFrom, ValidTo); an empty ValidTo is open-ended.
type CostCenter struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	CompanyCode string `json:"company_code"`
	ValidFrom   string `json:"valid_from"` // YYYY-MM-DD
	ValidTo     string `json:"valid_to"`   // YYYY-MM-DD or ""
	Blocked     bool   `json:"blocked"`
}

// Key returns the natural key of the cost center.
func (c CostCenter) Key() string { return CostCenterKey(c.Code) }

// PurchaseOrder is a purchase order header.
type PurchaseOrder struct {
	PONumber       string `json:"po_number"`
	SupplierNumber string `json:"supplier_number"`
	CompanyCode    string `json:"company_code"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	OrderDate      string `json:"order_date"` // YYYY-MM-DD
	CostCenter     string `json:"cost_center"`
	ChangeSeq      int64  `json:"change_seq"`
}

// Key returns the natural key of the purchase order.
func (p PurchaseOrder) Key() string { return POKey(p.PONumber) }

// PurchaseOrderLine is a purchase order item. Its natural key is
// (PONumber, LineNo) and LineNo keeps its leading zeros ("00010").
type PurchaseOrderLine struct {
	PONumber    string  `json:"po_number"`
	LineNo      string  `json:"line_no"`
	Material    string  `json:"material"`
	Description string  `json:"description"`
	Quantity    Decimal `json:"quantity"`
	UoM         string  `json:"uom"`
	UnitPrice   Decimal `json:"unit_price"`
	Currency    string  `json:"currency"`
	GLAccount   string  `json:"gl_account"`
	CostCenter  string  `json:"cost_center"`
	ChangeSeq   int64   `json:"change_seq"`
}

// Key returns the natural key of the purchase order line.
func (l PurchaseOrderLine) Key() string { return POLineKey(l.PONumber, l.LineNo) }

// FxRate is one exchange rate row. Rate and RateFactor are kept separate on
// purpose: dividing early loses precision and a factor of 100 misread as 1 is a
// 100x posting error. Sequence resolves supersession, highest wins.
type FxRate struct {
	Base       string  `json:"base"`
	Quote      string  `json:"quote"`
	ValidFrom  string  `json:"valid_from"` // YYYY-MM-DD, Europe/Zurich calendar date
	RateType   string  `json:"rate_type"`
	ValidTo    string  `json:"valid_to"` // YYYY-MM-DD or "" for open-ended
	Rate       Decimal `json:"rate"`
	RateFactor int64   `json:"rate_factor"`
	Sequence   int     `json:"sequence"`
	Status     string  `json:"status"`
}

// Key returns the natural key of the FX rate row: base, quote, valid-from date
// and rate type. Sequence is not part of the key; it orders revisions of it.
func (f FxRate) Key() string { return FxRateKey(f.Base, f.Quote, f.ValidFrom, f.RateType) }

// Covers reports whether the row's half-open validity interval
// [ValidFrom, ValidTo) contains the given calendar date. An empty ValidTo is
// open-ended. A malformed date is an error, never a silent false.
func (f FxRate) Covers(date string) (bool, error) {
	return DateInHalfOpenRange(date, f.ValidFrom, f.ValidTo)
}

// APInvoice is an incoming supplier invoice. Its natural key is
// (SupplierNumber, SupplierInvoiceNumber), both strings with significant leading
// zeros.
//
// DiscountRaw carries the source Skonto field verbatim. The customer's own spec
// contradicts itself about whether it is an amount or a percentage rate, so the
// value is never interpreted here: consumers surface it and report
// ambiguous_field_semantics instead of converting it to money.
type APInvoice struct {
	SupplierNumber        string          `json:"supplier_number"`
	SupplierInvoiceNumber string          `json:"supplier_invoice_number"`
	CompanyCode           string          `json:"company_code"`
	DocumentType          string          `json:"document_type"`
	DocumentDate          string          `json:"document_date"` // YYYY-MM-DD
	ReceiptDate           string          `json:"receipt_date"`  // YYYY-MM-DD or ""
	PONumber              string          `json:"po_number"`
	Currency              string          `json:"currency"`
	GrossAmount           Decimal         `json:"gross_amount"`
	VATAmount             Decimal         `json:"vat_amount"`
	VATCode               string          `json:"vat_code"`
	PaymentTermsDays      int             `json:"payment_terms_days"`
	DiscountRaw           string          `json:"discount_raw"`
	DiscountDays          int             `json:"discount_days"`
	CostCenter            string          `json:"cost_center"`
	Text                  string          `json:"text"`
	Lines                 []APInvoiceLine `json:"lines"`
}

// Key returns the natural key of the invoice.
func (i APInvoice) Key() string { return InvoiceKey(i.SupplierNumber, i.SupplierInvoiceNumber) }

// APInvoiceLine is one invoice item. An empty CostCenter is a valid state and is
// never inherited from the header: the source spec does not define inheritance,
// so consumers surface the gap instead of inventing it.
type APInvoiceLine struct {
	LineNo     string  `json:"line_no"`
	GLAccount  string  `json:"gl_account"`
	CostCenter string  `json:"cost_center"`
	Quantity   Decimal `json:"quantity"`
	UoM        string  `json:"uom"`
	UnitPrice  Decimal `json:"unit_price"`
	LineAmount Decimal `json:"line_amount"`
	TaxCode    string  `json:"tax_code"`
}

// Key returns the natural key of the invoice line within its invoice.
func (l APInvoiceLine) Key(supplierNumber, supplierInvoiceNumber string) string {
	return InvoiceLineKey(supplierNumber, supplierInvoiceNumber, l.LineNo)
}

// LineTotal returns the exact sum of the line amounts. It is the left-hand side
// of the header total check GrossAmount == sum(line amounts) + VATAmount.
func (i APInvoice) LineTotal() (Decimal, error) {
	var sum Decimal
	if len(i.Lines) > 0 {
		sum = Decimal{scale: i.Lines[0].LineAmount.scale}
	}
	for _, l := range i.Lines {
		next, err := sum.Add(l.LineAmount)
		if err != nil {
			return Decimal{}, err
		}
		sum = next
	}
	return sum, nil
}
