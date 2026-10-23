package model

import (
	"strconv"
	"strings"
)

// Record-level error codes. They are the vocabulary of the per-record `errors[]`
// on ingest responses, of the `code` column in records.csv and rejects.csv, and
// of every FieldError this package produces.
const (
	// CodeKeyMissing reports an empty natural-key component.
	CodeKeyMissing = "E_KEY_MISSING"
	// CodeKeyNotString reports a natural key delivered as a JSON number or any
	// other non-string. Keys are strings whose leading zeros are significant, so
	// a number has already lost information by the time it is parsed.
	CodeKeyNotString = "E_KEY_NOT_STRING"
	// CodeCurrencyUnknown reports a currency outside the fixed currency table.
	CodeCurrencyUnknown = "E_CURRENCY_UNKNOWN"
	// CodeEnumUnknown reports a value outside a closed enumeration.
	CodeEnumUnknown = "E_ENUM_UNKNOWN"
	// CodeFieldRequired reports an empty mandatory non-key field.
	CodeFieldRequired = "E_FIELD_REQUIRED"
	// CodeFieldInvalid reports a field that is present and well-formed on its own
	// but wrong in context, such as a validity range that ends before it starts.
	CodeFieldInvalid = "E_FIELD_INVALID"
	// CodeDateFormat reports a date that is not YYYY-MM-DD or does not exist.
	CodeDateFormat = "E_DATE_FORMAT"
	// CodeUoMUnknown reports a unit of measure outside the known set.
	CodeUoMUnknown = "E_UOM_UNKNOWN"
	// CodeNumberFormat reports a numeric field that is not a plain decimal.
	CodeNumberFormat = "E_NUMBER_FORMAT"
	// CodeMoneyNotIntegerMinor reports an amount that cannot be expressed exactly
	// in the minor units of its currency, e.g. 45.505 CHF or 1.5 JPY.
	CodeMoneyNotIntegerMinor = "E_MONEY_NOT_INTEGER_MINOR"
	// CodeMoneyScale reports a rate or price carrying more fraction digits than
	// its field allows. It is deliberately a different code from
	// CodeMoneyNotIntegerMinor: an amount that is not expressible in a
	// currency's minor unit is a different defect from a price with too much
	// precision, and a candidate reading the receipt needs to know which.
	CodeMoneyScale = "E_MONEY_SCALE"
	// CodeCountryFormat reports a country that is not two uppercase letters.
	CodeCountryFormat = "E_COUNTRY_FORMAT"
)

// FieldError is one structured, machine-readable validation finding. Code is one
// of the E_* constants above, Field is the canonical JSON field name (dotted and
// indexed for nested fields, e.g. "lines[2].uom"), and Message is a short human
// explanation that never contains the record's own free-text payload.
type FieldError struct {
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements error so a single finding can travel as one.
func (e FieldError) Error() string {
	return e.Code + " " + e.Field + ": " + e.Message
}

// FieldErrors is an ordered list of findings. The order is the field order of
// the entity's declaration, never map order, so reports are reproducible.
type FieldErrors []FieldError

// Error implements error, joining the findings with "; ".
func (e FieldErrors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Error()
	}
	return strings.Join(parts, "; ")
}

// OrNil returns e, or nil when there are no findings, for callers that want an
// error value rather than a slice.
func (e FieldErrors) OrNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// The validators below are pure: they read the value they are given, allocate
// findings, and touch nothing else. They validate shape and closed sets only.
// Cross-record questions (does the supplier exist, is the cost center valid for
// this company code, is there an FX rate for this date) belong to the matching
// engine, which has the other records; the deliberate ambiguities of section 5
// of the build spec are never validated away here: an empty invoice line cost
// center and a non-empty raw discount are valid states that consumers surface.

// ValidateSupplier reports the findings for a supplier record. SupplierNumber is
// the natural key; Name and Currency are mandatory because a creditor without a
// name or an invoicing currency cannot be posted. Country is optional but must
// be two uppercase letters when present. IBAN and VATNumber are not validated:
// their checksum rules are jurisdiction-specific and the ERP owns them.
func ValidateSupplier(s Supplier) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "supplier_number", s.SupplierNumber)
	errs = requireField(errs, "name", s.Name)
	errs = requireCurrency(errs, "currency", s.Currency)
	if s.Country != "" && !isAlpha2Upper(s.Country) {
		errs = append(errs, FieldError{Code: CodeCountryFormat, Field: "country",
			Message: "want ISO 3166-1 alpha-2, two uppercase letters"})
	}
	if s.PaymentTermsDays < 0 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "payment_terms_days",
			Message: "must not be negative"})
	}
	return errs
}

// Validate reports the findings for the supplier.
func (s Supplier) Validate() FieldErrors { return ValidateSupplier(s) }

// ValidateCostCenter reports the findings for a cost center record. The validity
// range is half-open [valid_from, valid_to), so valid_to equal to valid_from is
// an empty range and therefore invalid.
func ValidateCostCenter(c CostCenter) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "code", c.Code)
	errs = requireField(errs, "name", c.Name)
	errs = requireEnum(errs, "company_code", c.CompanyCode, IsKnownCompanyCode)
	errs = requireDate(errs, "valid_from", c.ValidFrom)
	errs = optionalDate(errs, "valid_to", c.ValidTo)
	errs = checkRange(errs, "valid_to", c.ValidFrom, c.ValidTo)
	return errs
}

// Validate reports the findings for the cost center.
func (c CostCenter) Validate() FieldErrors { return ValidateCostCenter(c) }

// ValidatePurchaseOrder reports the findings for a purchase order header.
func ValidatePurchaseOrder(p PurchaseOrder) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "po_number", p.PONumber)
	errs = requireField(errs, "supplier_number", p.SupplierNumber)
	errs = requireEnum(errs, "company_code", p.CompanyCode, IsKnownCompanyCode)
	errs = requireCurrency(errs, "currency", p.Currency)
	errs = requireEnum(errs, "status", p.Status, IsKnownPOStatus)
	errs = requireDate(errs, "order_date", p.OrderDate)
	return errs
}

// Validate reports the findings for the purchase order.
func (p PurchaseOrder) Validate() FieldErrors { return ValidatePurchaseOrder(p) }

// ValidatePurchaseOrderLine reports the findings for a purchase order line. The
// natural key is (po_number, line_no) and line_no keeps its leading zeros, so an
// empty line number is E_KEY_MISSING rather than a defaulted "0".
func ValidatePurchaseOrderLine(l PurchaseOrderLine) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "po_number", l.PONumber)
	errs = requireKey(errs, "line_no", l.LineNo)
	errs = requireUoM(errs, "uom", l.UoM)
	errs = requireCurrency(errs, "currency", l.Currency)
	// A unit price is a RATE, not a booked amount, so it is not held to the
	// currency's minor unit. Purchase order lines from real ERPs carry four to
	// six fraction digits routinely, and the customer's own KRED-EXP layout
	// specifies two to four for Einzelpreis. What is held to the minor unit is
	// every amount that gets booked: gross, VAT and the line amount.
	errs = requireMaxScale(errs, "unit_price", l.UnitPrice, MaxPriceScale)
	return errs
}

// Validate reports the findings for the purchase order line.
func (l PurchaseOrderLine) Validate() FieldErrors { return ValidatePurchaseOrderLine(l) }

// ValidateFxRate reports the findings for an FX rate row. Rate and RateFactor are
// checked independently: a non-positive factor is invalid because dividing by it
// is either a division by zero or a sign flip, and both would be silent money
// bugs. A DELETED row is still validated: it must be well-formed to be dropped
// for the right reason.
func ValidateFxRate(f FxRate) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "base", f.Base)
	errs = requireKey(errs, "quote", f.Quote)
	errs = requireKey(errs, "valid_from", f.ValidFrom)
	errs = requireKey(errs, "rate_type", f.RateType)
	if f.Base != "" && !IsKnownCurrency(f.Base) {
		errs = append(errs, FieldError{Code: CodeCurrencyUnknown, Field: "base",
			Message: "unknown currency " + strconv.Quote(f.Base)})
	}
	if f.Quote != "" && !IsKnownCurrency(f.Quote) {
		errs = append(errs, FieldError{Code: CodeCurrencyUnknown, Field: "quote",
			Message: "unknown currency " + strconv.Quote(f.Quote)})
	}
	if f.ValidFrom != "" && !IsValidDate(f.ValidFrom) {
		errs = append(errs, FieldError{Code: CodeDateFormat, Field: "valid_from",
			Message: "want YYYY-MM-DD"})
	}
	if f.RateType != "" && !IsKnownRateType(f.RateType) {
		errs = append(errs, FieldError{Code: CodeEnumUnknown, Field: "rate_type",
			Message: "want DAILY or MONTHLY_AVG"})
	}
	errs = optionalDate(errs, "valid_to", f.ValidTo)
	errs = checkRange(errs, "valid_to", f.ValidFrom, f.ValidTo)
	errs = requireEnum(errs, "status", f.Status, IsKnownFxStatus)
	if f.RateFactor <= 0 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "rate_factor",
			Message: "must be positive, e.g. 1 or 100 for a rate quoted per 100 units"})
	}
	if f.Rate.Sign() <= 0 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "rate",
			Message: "must be positive"})
	}
	if f.Sequence < 0 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "sequence",
			Message: "must not be negative"})
	}
	return errs
}

// Validate reports the findings for the FX rate row.
func (f FxRate) Validate() FieldErrors { return ValidateFxRate(f) }

// ValidateAPInvoice reports the findings for an invoice and its lines. Line
// findings carry an indexed field path, e.g. "lines[2].uom".
//
// The amounts are checked for representability in the minor units of the
// invoice currency (E_MONEY_NOT_INTEGER_MINOR), not for arithmetic consistency:
// gross == sum(lines) + vat is a matching-engine concern (EXC_TOTALS_MISMATCH).
// DiscountRaw is never parsed, and the document type never implies a sign.
func ValidateAPInvoice(i APInvoice) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "supplier_number", i.SupplierNumber)
	errs = requireKey(errs, "supplier_invoice_number", i.SupplierInvoiceNumber)
	errs = requireEnum(errs, "company_code", i.CompanyCode, IsKnownCompanyCode)
	errs = requireEnum(errs, "document_type", i.DocumentType, IsKnownDocumentType)
	errs = requireDate(errs, "document_date", i.DocumentDate)
	errs = optionalDate(errs, "receipt_date", i.ReceiptDate)
	errs = requireCurrency(errs, "currency", i.Currency)
	errs = requireMinorUnits(errs, "gross_amount", i.GrossAmount, i.Currency)
	errs = requireMinorUnits(errs, "vat_amount", i.VATAmount, i.Currency)
	if i.PaymentTermsDays < 0 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "payment_terms_days",
			Message: "must not be negative"})
	}
	if i.DiscountDays < 0 {
		errs = append(errs, FieldError{Code: CodeFieldInvalid, Field: "discount_days",
			Message: "must not be negative"})
	}
	for n, l := range i.Lines {
		prefix := "lines[" + strconv.Itoa(n) + "]."
		for _, fe := range ValidateAPInvoiceLine(l, i.Currency) {
			fe.Field = prefix + fe.Field
			errs = append(errs, fe)
		}
	}
	return errs
}

// Validate reports the findings for the invoice and its lines.
func (i APInvoice) Validate() FieldErrors { return ValidateAPInvoice(i) }

// ValidateAPInvoiceLine reports the findings for one invoice line. currency is
// the invoice's currency, used to check that the amounts are expressible in
// minor units; pass "" to skip that check.
//
// An empty CostCenter is valid on purpose: the source spec does not define
// inheritance from the header, so the gap is surfaced as an
// ambiguous_field_semantics warning by the importer rather than rejected or
// silently filled here.
func ValidateAPInvoiceLine(l APInvoiceLine, currency string) FieldErrors {
	var errs FieldErrors
	errs = requireKey(errs, "line_no", l.LineNo)
	errs = requireUoM(errs, "uom", l.UoM)
	if currency != "" {
		errs = requireMinorUnits(errs, "line_amount", l.LineAmount, currency)
	}
	return errs
}

// Validate reports the findings for the invoice line, without the amount check
// that needs the invoice currency.
func (l APInvoiceLine) Validate() FieldErrors { return ValidateAPInvoiceLine(l, "") }

// requireKey appends E_KEY_MISSING when a natural-key component is empty or
// whitespace only.
func requireKey(errs FieldErrors, field, value string) FieldErrors {
	if strings.TrimSpace(value) == "" {
		return append(errs, FieldError{Code: CodeKeyMissing, Field: field,
			Message: "natural key component must not be empty"})
	}
	return errs
}

// requireField appends E_FIELD_REQUIRED when a mandatory field is empty.
func requireField(errs FieldErrors, field, value string) FieldErrors {
	if strings.TrimSpace(value) == "" {
		return append(errs, FieldError{Code: CodeFieldRequired, Field: field,
			Message: "must not be empty"})
	}
	return errs
}

// requireEnum appends E_FIELD_REQUIRED for an empty value and E_ENUM_UNKNOWN for
// one outside the closed set.
func requireEnum(errs FieldErrors, field, value string, known func(string) bool) FieldErrors {
	if value == "" {
		return append(errs, FieldError{Code: CodeFieldRequired, Field: field,
			Message: "must not be empty"})
	}
	if !known(value) {
		return append(errs, FieldError{Code: CodeEnumUnknown, Field: field,
			Message: "unknown value " + strconv.Quote(value)})
	}
	return errs
}

// requireCurrency appends E_FIELD_REQUIRED for an empty currency and
// E_CURRENCY_UNKNOWN for one outside the fixed table.
func requireCurrency(errs FieldErrors, field, value string) FieldErrors {
	if value == "" {
		return append(errs, FieldError{Code: CodeFieldRequired, Field: field,
			Message: "must not be empty"})
	}
	if !IsKnownCurrency(value) {
		return append(errs, FieldError{Code: CodeCurrencyUnknown, Field: field,
			Message: "unknown currency " + strconv.Quote(value)})
	}
	return errs
}

// requireUoM appends E_FIELD_REQUIRED for an empty unit and E_UOM_UNKNOWN for an
// unknown one.
func requireUoM(errs FieldErrors, field, value string) FieldErrors {
	if value == "" {
		return append(errs, FieldError{Code: CodeFieldRequired, Field: field,
			Message: "must not be empty"})
	}
	if !IsKnownUoM(value) {
		return append(errs, FieldError{Code: CodeUoMUnknown, Field: field,
			Message: "unknown unit of measure " + strconv.Quote(value)})
	}
	return errs
}

// requireDate appends E_FIELD_REQUIRED for an empty date and E_DATE_FORMAT for a
// malformed or non-existent one.
func requireDate(errs FieldErrors, field, value string) FieldErrors {
	if value == "" {
		return append(errs, FieldError{Code: CodeFieldRequired, Field: field,
			Message: "must not be empty"})
	}
	if !IsValidDate(value) {
		return append(errs, FieldError{Code: CodeDateFormat, Field: field,
			Message: "want YYYY-MM-DD"})
	}
	return errs
}

// optionalDate appends E_DATE_FORMAT for a non-empty malformed date. An empty
// value is a valid open-ended or absent date.
func optionalDate(errs FieldErrors, field, value string) FieldErrors {
	if value == "" {
		return errs
	}
	if !IsValidDate(value) {
		return append(errs, FieldError{Code: CodeDateFormat, Field: field,
			Message: "want YYYY-MM-DD"})
	}
	return errs
}

// checkRange appends E_FIELD_INVALID when a half-open validity range is empty,
// i.e. to is not strictly after from. Malformed dates are reported by the date
// checks and skipped here.
func checkRange(errs FieldErrors, field, from, to string) FieldErrors {
	if from == "" || to == "" {
		return errs
	}
	c, err := CompareDates(from, to)
	if err != nil {
		return errs
	}
	if c >= 0 {
		return append(errs, FieldError{Code: CodeFieldInvalid, Field: field,
			Message: "must be after valid_from: the range is half-open"})
	}
	return errs
}

// MaxPriceScale is the most fraction digits a unit price may carry. Six is what
// SAP-style price conditions use and it is well inside Decimal's range.
const MaxPriceScale uint8 = 6

// requireMaxScale appends E_MONEY_SCALE when a value carries more fraction digits
// than the field allows. It is the check for rates and prices, which are exact
// values with their own precision rather than amounts in a currency's minor unit.
func requireMaxScale(errs FieldErrors, field string, v Decimal, max uint8) FieldErrors {
	if v.Scale() <= max {
		return errs
	}
	return append(errs, FieldError{Code: CodeMoneyScale, Field: field,
		Message: v.String() + " carries more than " + itoa(int(max)) + " fraction digits"})
}

// itoa is strconv.Itoa without the import, for the two error messages that need
// it in this file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// requireMinorUnits appends E_MONEY_NOT_INTEGER_MINOR when an amount cannot be
// expressed exactly in the minor units of the currency. An unknown currency is
// reported by the currency check, not here.
func requireMinorUnits(errs FieldErrors, field string, amount Decimal, currency string) FieldErrors {
	scale, err := CurrencyScale(currency)
	if err != nil {
		return errs
	}
	if _, err := amount.MinorUnits(scale); err != nil {
		return append(errs, FieldError{Code: CodeMoneyNotIntegerMinor, Field: field,
			Message: amount.String() + " is not an exact amount in minor units of " + currency})
	}
	return errs
}

// isAlpha2Upper reports whether s is exactly two uppercase ASCII letters.
func isAlpha2Upper(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// KeyString extracts a natural-key component from a value decoded out of JSON,
// XML or CSV. It is the boundary check that keeps leading zeros alive: a key
// delivered as a JSON number has already lost them, so it is
// E_KEY_NOT_STRING rather than a formatted integer. A missing or empty value is
// E_KEY_MISSING. The returned string is whitespace-trimmed.
func KeyString(field string, v any) (string, FieldErrors) {
	switch t := v.(type) {
	case nil:
		return "", FieldErrors{{Code: CodeKeyMissing, Field: field,
			Message: "natural key component must not be empty"}}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", FieldErrors{{Code: CodeKeyMissing, Field: field,
				Message: "natural key component must not be empty"}}
		}
		return s, nil
	}
	return "", FieldErrors{{Code: CodeKeyNotString, Field: field,
		Message: "natural key must be a string: leading zeros are significant"}}
}
