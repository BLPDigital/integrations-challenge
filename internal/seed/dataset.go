package seed

import (
	"fmt"
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// ClosedPeriodBefore is the seeded closed fiscal period boundary: a posting date
// strictly before this calendar date falls in a closed period and the ERP
// rejects it with ERP_PERIOD_CLOSED. No invoice this generator writes is dated
// before it, so the boundary is a fact of the seeded landscape rather than a
// planted rejection; purchase orders do carry earlier order dates, which is what
// the boundary looks like in real master data.
const ClosedPeriodBefore = "2026-02-01"

// QuoteCurrency is the currency every seeded exchange rate converts to.
const QuoteCurrency = "CHF"

// Sender, Receiver and TestIndicator are the fixed Vorlaufsatz identities of the
// seeded landscape, per section 4 of the customer document.
const (
	Sender        = "SUBSG01"
	Receiver      = "BLP"
	TestIndicator = "P"
)

// FormatVersion is the KRED-EXP layout version every generated file declares.
const FormatVersion = "2.1"

// RateProvider is the value the SOAP response carries in its RateProvider field.
// It exists to exercise the ISO-8859-1 prolog of the SOAP channel and is
// confined to one cosmetic field so a mangled decoding can never reach an
// amount.
const RateProvider = "Zürcher Kantonalbank"

// Mandant values of the legacy delivery. The Mandant is field 3 of the
// Vorlaufsatz and the first component of the file name, and it is the documented
// carrier of the company code: one delivery per company code, so an invoice's
// company code never has to be guessed from its cost center.
const (
	MandantCH10 = "0100"
	MandantCH20 = "0200"
)

// CompanyCodeForMandant maps a Vorlaufsatz Mandant to a canonical company code.
func CompanyCodeForMandant(mandant string) (string, error) {
	switch mandant {
	case MandantCH10:
		return model.CompanyCodeCH10, nil
	case MandantCH20:
		return model.CompanyCodeCH20, nil
	}
	return "", fmt.Errorf("seed: unknown Mandant %q", mandant)
}

// MandantForCompanyCode maps a canonical company code to its Mandant.
func MandantForCompanyCode(companyCode string) (string, error) {
	switch companyCode {
	case model.CompanyCodeCH10:
		return MandantCH10, nil
	case model.CompanyCodeCH20:
		return MandantCH20, nil
	}
	return "", fmt.Errorf("seed: unknown company code %q", companyCode)
}

// Exception codes of the twin's open queue. The set is closed: an unpublished
// code cannot be graded, so the generator only ever emits one of these.
const (
	ExcSupplierUnknown       = "EXC_SUPPLIER_UNKNOWN"
	ExcSupplierBlocked       = "EXC_SUPPLIER_BLOCKED"
	ExcPONotFound            = "EXC_PO_NOT_FOUND"
	ExcPOSupplierMismatch    = "EXC_PO_SUPPLIER_MISMATCH"
	ExcPOClosed              = "EXC_PO_CLOSED"
	ExcTotalsMismatch        = "EXC_TOTALS_MISMATCH"
	ExcCostCenterUnknown     = "EXC_COST_CENTER_UNKNOWN"
	ExcFxRateMissing         = "EXC_FX_RATE_MISSING"
	ExcUoMUnmappable         = "EXC_UOM_UNMAPPABLE"
	ExcUoMUnconvertible      = "EXC_UOM_UNCONVERTIBLE"
	ExcDuplicateInvoiceAmend = "EXC_DUPLICATE_INVOICE_AMENDED"
	// ExcAckRejected is the one code in this set that the ERP raises rather than
	// the match: an invoice that bills more than its order allows is refused
	// with ERP_AMOUNT_MISMATCH, and the twin turns that answer into an
	// exception. It is defined here as well as in the twin so the generator can
	// state the expectation without importing the twin.
	ExcAckRejected = CodeAckRejected
)

// ExceptionCodes returns every exception code this generator can produce, in
// ascending byte order. The grader compares its own set against this one, so a
// code that is not here is a code that cannot be scored.
func ExceptionCodes() []string {
	out := []string{
		ExcCostCenterUnknown,
		ExcDuplicateInvoiceAmend,
		ExcFxRateMissing,
		ExcPOClosed,
		ExcPONotFound,
		ExcPOSupplierMismatch,
		ExcSupplierBlocked,
		ExcSupplierUnknown,
		ExcTotalsMismatch,
		ExcUoMUnconvertible,
		ExcUoMUnmappable,
		ExcAckRejected,
	}
	sort.Strings(out)
	return out
}

// Supplier is a seeded creditor: the canonical model record plus the ERP's
// legacy_id convenience field.
//
// LegacyID is a real JSON number on the ERP's REST surface, and it is the
// numeric value of the supplier number, so the two suppliers whose numbers
// collide when leading zeros are stripped also share a legacy_id. That is
// exactly why it must never be used as a key: it has already lost the leading
// zeros that distinguish the records.
type Supplier struct {
	model.Supplier
	LegacyID int64 `json:"legacy_id"`
}

// UoMConversion is one row of the ERP's unit-of-measure conversion table, as
// served by GET /erp/v1/uom-conversions.
//
// The base quantity of an alternative quantity is alt * Numerator / Denominator.
// Material is the material the row applies to; an empty Material is the generic
// rule for the alternative unit, and a row with a Material overrides it for that
// material. A conversion whose base quantity would need more than three decimals
// is EXC_UOM_UNCONVERTIBLE and never a silent round; an alternative unit with no
// row at all is EXC_UOM_UNMAPPABLE and never a guess.
type UoMConversion struct {
	Material    string `json:"material"`
	AltUoM      string `json:"alt_uom"`
	Numerator   int64  `json:"numerator"`
	Denominator int64  `json:"denominator"`
	BaseUoM     string `json:"base_uom"`
}

// FxRow is one row of the SOAP exchange rate table, in the shape the SOAP
// channel serves it.
//
// Rate and RateFactor are kept apart on purpose: dividing early loses precision,
// and a factor of 100 misread as 1 is a hundredfold posting error. ValidFrom and
// ValidTo are RFC3339 instants carrying the Europe/Zurich offset in force on
// that calendar date, so the two rows adjacent across the 2026-03-29 switch
// carry different offsets and a client that truncates to UTC picks the wrong
// one. ValidFromDate and ValidToDate are the same boundaries as Europe/Zurich
// calendar dates, which is the form the twin matches on.
type FxRow struct {
	// Sequence resolves supersession: for an otherwise identical key the highest
	// Sequence wins, whatever the document order.
	Sequence int `json:"sequence"`
	// Status is model.FxStatusActive or model.FxStatusDeleted. DELETED is a soft
	// delete and must be dropped, not used.
	Status string `json:"status"`
	// Base is the currency being converted from, Quote is always CHF.
	Base  string `json:"base"`
	Quote string `json:"quote"`
	// RateType is model.RateTypeDaily or model.RateTypeMonthlyAvg. A daily
	// conversion must never fall back to a monthly average.
	RateType string `json:"rate_type"`
	// Rate carries exactly six fraction digits, as delivered.
	Rate model.Decimal `json:"rate"`
	// RateFactor is the per-unit factor: JPY is quoted per 100 units, so its
	// rows carry 100.
	RateFactor int64 `json:"rate_factor"`
	// ValidFrom and ValidTo are the half-open validity interval as RFC3339
	// instants with the Europe/Zurich offset. ValidTo is empty when the row is
	// open-ended, in which case ValidToNil is true and the SOAP element carries
	// xsi:nil="true".
	ValidFrom  string `json:"valid_from"`
	ValidTo    string `json:"valid_to"`
	ValidToNil bool   `json:"valid_to_nil"`
	// ValidFromDate and ValidToDate are the same boundaries as Europe/Zurich
	// calendar dates; ValidToDate is empty for an open-ended row.
	ValidFromDate string `json:"valid_from_date"`
	ValidToDate   string `json:"valid_to_date"`
	// RateLiteral is the rate exactly as it goes on the wire: ERP host locale,
	// decimal comma, six fraction digits, dot thousands separator.
	RateLiteral string `json:"rate_literal"`
	// Provider is the RateProvider field; Comment is a free-text field that is
	// xsi:nil on one row on purpose, because a nil Comment must produce no
	// complaint at all and a blanket nil-is-an-error rule has to fail on it.
	Provider   string `json:"provider"`
	Comment    string `json:"comment"`
	CommentNil bool   `json:"comment_nil"`
	// PrettyPrinted marks the one row the SOAP serializer surrounds with
	// whitespace, so a client that does not trim before parsing fails on a value
	// that is otherwise correct.
	PrettyPrinted bool `json:"pretty_printed"`
}

// Canonical returns the row as a canonical model record with Europe/Zurich
// calendar-date boundaries. It is the normalization the connector owes the twin,
// and the generator uses it to derive the expected exception set.
func (r FxRow) Canonical() model.FxRate {
	return model.FxRate{
		Base:       r.Base,
		Quote:      r.Quote,
		ValidFrom:  r.ValidFromDate,
		RateType:   r.RateType,
		ValidTo:    r.ValidToDate,
		Rate:       r.Rate,
		RateFactor: r.RateFactor,
		Sequence:   r.Sequence,
		Status:     r.Status,
	}
}

// FxGap is a deliberate hole in the daily rate coverage of one currency: no
// DAILY row is valid over [From, To), so an invoice in that currency dated
// inside it lands in EXC_FX_RATE_MISSING.
type FxGap struct {
	Currency string `json:"currency"`
	From     string `json:"from"` // YYYY-MM-DD, inclusive
	To       string `json:"to"`   // YYYY-MM-DD, exclusive
}

// ChangeSeqEntry is the ERP change sequence assigned to one master record.
//
// Change sequences are strictly increasing and unique across the whole dataset,
// which is what makes ?changed_since a usable watermark. A few records are
// deliberately assigned out of natural-key order, so a connector that sorts by
// key and persists the last key it saw instead of the highest change sequence
// loses records.
type ChangeSeqEntry struct {
	Dataset   string `json:"dataset"`
	Key       string `json:"key"`
	ChangeSeq int64  `json:"change_seq"`
}

// LegacyDelivery is one KRED-EXP file drop: the data file bytes and the name of
// the empty sentinel that must exist before the data file may be read.
type LegacyDelivery struct {
	// Name is the data file name, KRED_<Mandant>_<YYYYMMDD>_<NNN>.txt.
	Name string `json:"name"`
	// OKName is the sentinel file name, the same base with the .ok extension.
	// The sentinel is empty and is written last.
	OKName string `json:"ok_name"`
	// Mandant is field 3 of the Vorlaufsatz, CompanyCode its canonical form.
	Mandant     string `json:"mandant"`
	CompanyCode string `json:"company_code"`
	// RunNumber is the three digit run counter of the file name.
	RunNumber int `json:"run_number"`
	// ExportDate is the YYYY-MM-DD date in the file name.
	ExportDate string `json:"export_date"`
	// Bytes are the genuine CP1252, CRLF terminated file contents, without a
	// byte order mark.
	Bytes []byte `json:"-"`
	// Invoices are the invoices this file carries, in file order. A later
	// delivery may repeat an invoice: byte-identical content must dedupe, and
	// changed content must raise EXC_DUPLICATE_INVOICE_AMENDED.
	Invoices []model.APInvoice `json:"-"`
	// KopfCount, PosCount, SumGross and SumLines are the Nachlaufsatz control
	// totals, and they are correct for the file. The invalid variants live only
	// in the fixtures of the Go task.
	KopfCount int           `json:"kopf_count"`
	PosCount  int           `json:"pos_count"`
	SumGross  model.Decimal `json:"sum_gross"`
	SumLines  model.Decimal `json:"sum_lines"`
}

// ExpectedException is one entry of the golden expected-exception set: the
// natural key of the subject at fault and the published exception code.
//
// The grader compares the twin's open queue against this set as a symmetric
// difference, so a missing pair and a spurious pair cost the same and
// over-rejecting scores as badly as under-rejecting.
type ExpectedException struct {
	SubjectKey  string `json:"subject_key"`
	SubjectType string `json:"subject_type"`
	Code        string `json:"code"`
}

// Dataset is everything a scenario needs: the ERP's master data, the SOAP
// exchange rate table, the legacy deliveries and the golden expected-exception
// set. Every slice is in a deterministic order, documented on the field.
type Dataset struct {
	// Scenario and Seed identify the generation completely: the same pair always
	// yields byte-identical output.
	Scenario string `json:"scenario"`
	Seed     int64  `json:"seed"`

	// Suppliers are sorted by supplier number in byte order.
	Suppliers []Supplier `json:"suppliers"`
	// CostCenters are sorted by code in byte order. They are pre-seeded in the
	// twin, as though another integration had already loaded them.
	CostCenters []model.CostCenter `json:"cost_centers"`
	// PurchaseOrders are sorted by purchase order number as delivered, which is
	// not the same as canonical order: the delivered spelling carries the
	// "PO-" prefix or surrounding spaces on some records.
	PurchaseOrders []model.PurchaseOrder `json:"purchase_orders"`
	// PurchaseOrderLines are sorted by (purchase order number, line number).
	PurchaseOrderLines []model.PurchaseOrderLine `json:"purchase_order_lines"`
	// FxRows are in SOAP document order, which is deliberately not validity
	// order: a superseded row sits after its winner, and the filler tail runs
	// backwards in time.
	FxRows []FxRow `json:"fx_rows"`
	// FxGaps are the deliberate holes in the daily coverage.
	FxGaps []FxGap `json:"fx_gaps"`
	// UoMConversions are sorted by (material, alt unit).
	UoMConversions []UoMConversion `json:"uom_conversions"`

	// ChangeSeqs is the change sequence assigned to every master record that the
	// ERP exposes with ?changed_since, sorted by change sequence ascending.
	ChangeSeqs []ChangeSeqEntry `json:"change_seqs"`
	// MaxChangeSeq is the highest change sequence in the dataset, i.e. the
	// watermark a connector should hold after a complete pull.
	MaxChangeSeq int64 `json:"max_change_seq"`
	// DeltaFromChangeSeq is the watermark a delta scenario starts from: records
	// with a change sequence above it are the delta. It is zero for a cold load.
	DeltaFromChangeSeq int64 `json:"delta_from_change_seq"`

	// Invoices are the distinct invoices of the deliveries as FIRST delivered,
	// sorted by natural key. A re-delivery with changed content does not replace
	// the entry here; the changed version is in AmendedInvoices.
	Invoices []model.APInvoice `json:"-"`
	// AmendedInvoices are the re-deliveries whose content changed, sorted by
	// natural key. Each one raises EXC_DUPLICATE_INVOICE_AMENDED.
	AmendedInvoices []model.APInvoice `json:"-"`

	// Deliveries are the legacy file drops in the order the ERP writes them.
	Deliveries []LegacyDelivery `json:"deliveries"`
	// LegacyFile and LegacyFileName are the primary delivery: Deliveries[0], the
	// Mandant 0100 file. OKFileName is its sentinel.
	LegacyFile     []byte `json:"-"`
	LegacyFileName string `json:"legacy_file_name"`
	OKFileName     string `json:"ok_file_name"`

	// ExpectedExceptions is the golden set, sorted by (subject key, code).
	ExpectedExceptions []ExpectedException `json:"expected_exceptions"`

	// ClosedPeriodBefore is the seeded closed fiscal period boundary.
	ClosedPeriodBefore string `json:"closed_period_before"`
}

// Counts returns the record counts of the dataset, for the summary cmd/seed
// writes and for the size assertions of the scenario table.
func (d *Dataset) Counts() map[string]int {
	// The map is a return value the caller keys into; nothing in this package
	// ranges over it.
	return map[string]int{
		"suppliers":            len(d.Suppliers),
		"cost_centers":         len(d.CostCenters),
		"purchase_orders":      len(d.PurchaseOrders),
		"purchase_order_lines": len(d.PurchaseOrderLines),
		"fx_rows":              len(d.FxRows),
		"uom_conversions":      len(d.UoMConversions),
		"invoices":             len(d.Invoices),
		"invoice_lines":        d.InvoiceLineCount(),
		"deliveries":           len(d.Deliveries),
		"expected_exceptions":  len(d.ExpectedExceptions),
	}
}

// InvoiceLineCount returns the number of invoice lines across the distinct
// invoices.
func (d *Dataset) InvoiceLineCount() int {
	n := 0
	for i := range d.Invoices {
		n += len(d.Invoices[i].Lines)
	}
	return n
}

// ResolvedDailyRates returns the DAILY rows a correct client keeps: DELETED rows
// dropped, supersession resolved by highest Sequence per natural key, sorted by
// (base, valid-from date). It is the connector's job on the real path; the
// generator uses it to derive the expected exception set.
func (d *Dataset) ResolvedDailyRates() []model.FxRate {
	best := map[string]model.FxRate{}
	for _, row := range d.FxRows {
		if row.Status != model.FxStatusActive || row.RateType != model.RateTypeDaily {
			continue
		}
		c := row.Canonical()
		k := c.Key()
		if cur, ok := best[k]; ok && cur.Sequence >= c.Sequence {
			continue
		}
		best[k] = c
	}
	// Collected out of a map, so the result is sorted explicitly before it is
	// returned or used for anything order-sensitive.
	out := make([]model.FxRate, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Base != out[j].Base {
			return out[i].Base < out[j].Base
		}
		if out[i].ValidFrom != out[j].ValidFrom {
			return out[i].ValidFrom < out[j].ValidFrom
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out
}
