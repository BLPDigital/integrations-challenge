package seed

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// exceptionClass names the single defect an invoice is constructed to carry.
// Each class is a mutation of an otherwise clean invoice, so exactly one rule of
// the matching engine fails and the golden exception set has one entry per
// invoice.
type exceptionClass int

const (
	clsClean exceptionClass = iota
	clsMissingPO
	clsClosedPO
	clsPOSupplierMismatch
	clsBlockedSupplier
	clsTotalsMismatch
	clsUnknownCostCenter
	clsDeletedRateDKK
	clsFxGap
	clsPalletLine
	clsUoMUnconvertible
	clsCompanyCH20
	// clsAmountMismatch bills more than the referenced purchase order allows.
	// The twin matches it happily, because over-billing is not a twin concern;
	// the ERP refuses the posting with ERP_AMOUNT_MISMATCH and the twin turns
	// that answer into an E_ACK_REJECTED exception. It is the one class whose
	// exception is opened by the ERP's answer rather than by the match, which is
	// exactly why it earns a class of its own: without it the amount tolerance
	// would fire on whatever invoices happened to be drawn high, and an expected
	// set that depends on a draw is not an expected set.
	clsAmountMismatch
)

// exceptionClasses is the round-robin order defects are handed out in, so every
// class is present even in the smallest scenario and the mix is stable across
// seeds.
var exceptionClasses = []exceptionClass{
	clsMissingPO,
	clsClosedPO,
	clsPOSupplierMismatch,
	clsBlockedSupplier,
	clsTotalsMismatch,
	clsUnknownCostCenter,
	clsDeletedRateDKK,
	clsFxGap,
	clsPalletLine,
	clsUoMUnconvertible,
	clsCompanyCH20,
	clsAmountMismatch,
}

// UnknownPONumber is a purchase order number no seeded order carries, used by
// the missing-purchase-order class.
const UnknownPONumber = "4599999999"

// pinnedInvoiceCount is the number of hand-written invoices at the head of the
// base delivery. They carry the published graded amounts and the customer
// document's own worked example, so they are never mutated into exceptions.
const pinnedInvoiceCount = 5

// cleanForeignCurrencies are the currencies a clean invoice may be issued in.
// JPY is absent on purpose: its minor unit scale is zero, so an ordinary invoice
// with centimes could not be expressed in it, and the one graded JPY invoice is
// hand-written instead. DKK is absent because it has no usable rate at all.
var cleanForeignCurrencies = []string{"EUR", "USD", "GBP", "SEK"}

// vatCodes are the local tax codes and their rates. The rate is exact at three
// decimals and no float64 is involved in applying it.
var vatCodes = []struct {
	code string
	rate model.Decimal
}{
	{"V81", dec(81, 3)},
	{"V26", dec(26, 3)},
	{"V0", dec(0, 3)},
}

// buildInvoices builds one set of invoices in file order.
//
// count and lineTotal are the published sizes, and the generator hits them
// exactly: every invoice carries three lines except the last few, which carry
// two, so the totals match without any invoice at the head of the file losing a
// line. pinned selects whether the hand-written graded invoices open the set.
// numberOffset shifts the document numbering, so a delta delivery issues numbers
// that no earlier delivery used.
func (b *builder) buildInvoices(count, lineTotal, numberOffset int, pinned bool) ([]model.APInvoice, []exceptionClass, error) {
	if count <= 0 {
		return nil, nil, nil
	}
	head := 0
	if pinned {
		head = pinnedInvoiceCount
		if count < head {
			return nil, nil, fmt.Errorf("seed: scenario %s wants %d invoices, fewer than the %d pinned ones",
				b.spec.ID, count, head)
		}
	}
	// Line count plan: three lines each, with the deficit taken off the tail so
	// the hand-written head keeps its three lines.
	deficit := 3*count - lineTotal
	if deficit < 0 || deficit > count {
		return nil, nil, fmt.Errorf("seed: scenario %s wants %d lines for %d invoices, which is not %d..%d",
			b.spec.ID, lineTotal, count, 2*count, 3*count)
	}
	lineCounts := make([]int, count)
	for i := range lineCounts {
		lineCounts[i] = 3
		if i >= count-deficit {
			lineCounts[i] = 2
		}
	}

	// Exception plan: evenly strided indices after the pinned head, one class
	// each in round-robin order.
	classes := make([]exceptionClass, count)
	excCount := count * b.spec.ExceptionPerMille / 1000
	if excCount > 0 && count > head {
		span := count - head
		if excCount > span {
			excCount = span
		}
		for k := 0; k < excCount; k++ {
			idx := head + k*span/excCount
			classes[idx] = exceptionClasses[k%len(exceptionClasses)]
		}
	}

	out := make([]model.APInvoice, 0, count)
	for i := 0; i < count; i++ {
		var inv model.APInvoice
		var err error
		if pinned && i < head {
			inv, err = b.pinnedInvoice(i)
		} else {
			inv, err = b.ordinaryInvoice(i, numberOffset, lineCounts[i], classes[i])
		}
		if err != nil {
			return nil, nil, err
		}
		b.decorateInvoice(&inv, i)
		if err := b.retotalInvoice(&inv, classes[i], pinned && i < head); err != nil {
			return nil, nil, err
		}
		out = append(out, inv)
	}
	return out, classes, nil
}

// invoiceNumber returns the Belegnummer of an ordinary invoice: seven characters,
// zero padded, leading zeros significant.
//
// Index 1 is the exception: it is issued as the bare "4711" while index 0 is
// "0004711". They are two different invoices of the same creditor, and a client
// that strips leading zeros or parses the field as a number merges them and
// loses one.
func invoiceNumber(index, offset int) string {
	if index == 1 && offset == 0 {
		return "4711"
	}
	return PadLeftZero(int64(4711+offset+index), 7)
}

// pinnedInvoice returns one of the hand-written invoices at the head of the base
// delivery. Their amounts are published and graded, so nothing here is drawn.
func (b *builder) pinnedInvoice(index int) (model.APInvoice, error) {
	switch index {
	case 0:
		// Anhang A of the customer document, verbatim except for the document
		// date, which is moved onto the summer time switch so that the EUR row
		// pair decides the amount: 1'345.63 EUR at 0.931000 is 1252.78 CHF, and
		// 1244.03 CHF when the FX date is truncated to UTC.
		return model.APInvoice{
			SupplierNumber:        CollisionSupplierCanonical,
			SupplierInvoiceNumber: "0004711",
			CompanyCode:           model.CompanyCodeCH10,
			DocumentType:          model.DocumentTypeInvoice,
			DocumentDate:          DSTSwitchDate,
			ReceiptDate:           "2026-04-02",
			PONumber:              AnhangAPONumber + "/00010",
			Currency:              "EUR",
			GrossAmount:           mustParseDec("1345.63"),
			VATAmount:             mustParseDec("100.83"),
			VATCode:               "V81",
			PaymentTermsDays:      30,
			DiscountRaw:           "2.00",
			DiscountDays:          10,
			CostCenter:            "0012340",
			Text:                  "Wartung Presse 3; Rahmenvertrag 2026",
			Lines: []model.APInvoiceLine{
				{LineNo: "00010", GLAccount: "0006400", CostCenter: "",
					Quantity: mustParseDec("12.000"), UoM: UoMStueck,
					UnitPrice: mustParseDec("45.5000"), LineAmount: mustParseDec("546.00"),
					TaxCode: ""},
				{LineNo: "00020", GLAccount: "0006810", CostCenter: "0012350",
					Quantity: mustParseDec("6.500"), UoM: "PCE",
					UnitPrice: mustParseDec("105.0000"), LineAmount: mustParseDec("682.50"),
					TaxCode: "V81"},
				{LineNo: "00030", GLAccount: "0006810", CostCenter: "0012350",
					Quantity: mustParseDec("1.000"), UoM: "EA",
					UnitPrice: mustParseDec("16.3000"), LineAmount: mustParseDec("16.30"),
					TaxCode: ""},
			},
		}, nil
	case 1:
		// The leading-zero twin of index 0: same creditor, document number
		// "4711" rather than "0004711", different content.
		return model.APInvoice{
			SupplierNumber:        CollisionSupplierCanonical,
			SupplierInvoiceNumber: "4711",
			CompanyCode:           model.CompanyCodeCH10,
			DocumentType:          model.DocumentTypeInvoice,
			DocumentDate:          "2026-03-05",
			ReceiptDate:           "2026-03-09",
			PONumber:              "",
			Currency:              "CHF",
			GrossAmount:           mustParseDec("324.10"),
			VATAmount:             mustParseDec("24.30"),
			VATCode:               "V81",
			PaymentTermsDays:      30,
			CostCenter:            "0815",
			Text:                  "Nachlieferung Werk Wil",
			Lines: []model.APInvoiceLine{
				{LineNo: "00010", GLAccount: "0006400", Quantity: mustParseDec("2.000"), UoM: "EA",
					UnitPrice: mustParseDec("100.0000"), LineAmount: mustParseDec("200.00")},
				{LineNo: "00020", GLAccount: "0006400", CostCenter: "0815",
					Quantity: mustParseDec("1.000"), UoM: "EA",
					UnitPrice: mustParseDec("79.8000"), LineAmount: mustParseDec("79.80")},
				{LineNo: "00030", GLAccount: "0006400", CostCenter: "815",
					Quantity: mustParseDec("1.000"), UoM: "EA",
					UnitPrice: mustParseDec("20.0000"), LineAmount: mustParseDec("20.00")},
			},
		}, nil
	case 2:
		// GBP 100.00 at 1.082250 is 108.23 with commercial rounding, 108.22 with
		// banker's rounding or a float64 path.
		return b.pinnedFlat("0004713", "GBP", model.DocumentTypeInvoice, JPYPinnedDate,
			[]string{"40.00", "35.00", "25.00"}), nil
	case 3:
		// JPY 250000 at 0.556300 per 100 units is 1390.75 CHF, and 139'075.00
		// when RateFactorFrom is ignored.
		return b.pinnedFlat("0004714", "JPY", model.DocumentTypeInvoice, JPYPinnedDate,
			[]string{"100000.00", "100000.00", "50000.00"}), nil
	case 4:
		// The credit note: -100.00 GBP is -108.23 CHF, which is where rounding
		// half away from zero differs from rounding half to even. The sign is
		// explicit in the amounts and is never derived from the document type.
		return b.pinnedFlat("0004715", "GBP", model.DocumentTypeCreditNote, JPYPinnedDate,
			[]string{"-40.00", "-35.00", "-25.00"}), nil
	}
	return model.APInvoice{}, fmt.Errorf("seed: no pinned invoice at index %d", index)
}

// pinnedFlat builds a hand-written invoice with a zero tax code, so its gross
// amount is exactly the sum of the given line amounts.
func (b *builder) pinnedFlat(number, currency, docType, date string, amounts []string) model.APInvoice {
	gross := dec(0, 2)
	inv := model.APInvoice{
		SupplierNumber:        CollisionSupplierCanonical,
		SupplierInvoiceNumber: number,
		CompanyCode:           model.CompanyCodeCH10,
		DocumentType:          docType,
		DocumentDate:          date,
		ReceiptDate:           "2026-03-20",
		Currency:              currency,
		VATCode:               "V0",
		VATAmount:             dec(0, 2),
		PaymentTermsDays:      30,
		CostCenter:            "0012340",
		Text:                  "Pauschale " + currency,
	}
	for i, a := range amounts {
		amount := mustParseDec(a)
		sum, err := gross.Add(amount)
		if err != nil {
			panic(err)
		}
		if gross, err = sum.Rescale(2); err != nil {
			panic(err)
		}
		inv.Lines = append(inv.Lines, model.APInvoiceLine{
			LineNo:     PadLeftZero(int64((i+1)*10), 5),
			GLAccount:  "0006400",
			CostCenter: "0012340",
			Quantity:   dec(1000, 3),
			UoM:        "EA",
			UnitPrice:  mustParseDecScale(a, 4),
			LineAmount: amount,
		})
	}
	// The gross amount of a zero-rated document is exactly the sum of its lines,
	// which is what makes these four amounts pinnable.
	inv.GrossAmount = gross
	return inv
}

// mustParseDecScale parses a literal and rescales it, panicking on a literal
// this package got wrong.
func mustParseDecScale(s string, scale uint8) model.Decimal {
	d, err := mustParseDec(s).Rescale(scale)
	if err != nil {
		panic(err)
	}
	return d
}

// ordinaryInvoice builds one generated invoice and applies its exception class.
func (b *builder) ordinaryInvoice(index, numberOffset, lineCount int, class exceptionClass) (model.APInvoice, error) {
	inv := model.APInvoice{
		SupplierInvoiceNumber: invoiceNumber(index, numberOffset),
		DocumentType:          model.DocumentTypeInvoice,
		VATCode:               vatCodes[index%len(vatCodes)].code,
	}
	// Roughly one document in twelve is a credit note, and its amounts carry an
	// explicit negative sign. The sign is never derived from the document type.
	credit := b.p.chance(80)
	// The over-billing class is never a credit note: a credit note's amounts are
	// negative, the ERP's amount comparison is one sided, and the planted trap
	// would silently not fire on the seeds that drew one.
	if credit && class != clsAmountMismatch {
		inv.DocumentType = model.DocumentTypeCreditNote
	} else {
		credit = false
	}

	usePO := b.p.chance(700)
	var po model.PurchaseOrder
	var poLines []model.PurchaseOrderLine
	switch class {
	case clsPalletLine, clsUoMUnconvertible:
		usePO = true
		po = b.d.PurchaseOrders[b.poByDelivered[UoMTrapPONumber]]
	case clsClosedPO:
		usePO = true
		po = b.d.PurchaseOrders[b.poByDelivered[b.closedPOsUsableSupplier[b.p.intn(len(b.closedPOsUsableSupplier))]]]
	case clsBlockedSupplier, clsUnknownCostCenter, clsCompanyCH20, clsMissingPO:
		usePO = false
	case clsPOSupplierMismatch:
		usePO = true
		po = b.d.PurchaseOrders[b.poByDelivered[b.safePOs[b.p.intn(len(b.safePOs))]]]
	case clsAmountMismatch:
		// Over-billing needs an order WITH LINES to over-bill against, so this
		// class always references one. Drawing blindly cost a seed its planted
		// trap once: a purchase order with no lines has no net total, the fit
		// below returns early, and the expected exception silently disappears.
		// The order must have lines to over-bill against, and it must be in the
		// company currency: the posting reaches the ERP in CHF, and the ERP
		// skips its amount comparison when the posting currency differs from the
		// order's, so a foreign-currency order would silently swallow the trap.
		usePO = true
		for attempt := 0; attempt < 64; attempt++ {
			cand := b.d.PurchaseOrders[b.poByDelivered[b.safePOs[b.p.intn(len(b.safePOs))]]]
			po = cand
			if cand.Currency == QuoteCurrency && len(b.linesOf(cand.PONumber)) > 0 {
				break
			}
		}
	default:
		if usePO {
			po = b.d.PurchaseOrders[b.poByDelivered[b.safePOs[b.p.intn(len(b.safePOs))]]]
		}
	}
	if usePO {
		poLines = b.linesOf(po.PONumber)
		inv.SupplierNumber = po.SupplierNumber
		inv.CompanyCode = po.CompanyCode
		inv.CostCenter = po.CostCenter
		inv.PONumber = b.poReference(po, index, poLines)
	} else {
		inv.SupplierNumber = b.safeSuppliers[b.p.intn(len(b.safeSuppliers))]
		inv.CompanyCode = model.CompanyCodeCH10
		if b.p.chance(80) {
			inv.CompanyCode = model.CompanyCodeCH20
		}
		safe := b.safeCostCenters[inv.CompanyCode]
		inv.CostCenter = safe[b.p.intn(len(safe))]
	}

	// Currency. A foreign currency is only ever issued for CH10, because the
	// CH20 rate table faults permanently; the CH20 class below overrides that on
	// purpose.
	inv.Currency = "CHF"
	// The over-billing class stays in the order's currency: the ERP skips its
	// amount comparison across currencies, so a foreign-currency draw would make
	// the planted trap silently not fire on some seeds.
	if inv.CompanyCode == model.CompanyCodeCH10 && class != clsAmountMismatch && b.p.chance(300) {
		inv.Currency = b.p.pickString(cleanForeignCurrencies)
	}

	// Document date, then the currency-specific coverage correction.
	span, err := daysBetween(b.spec.InvoiceDateFrom, b.spec.InvoiceDateTo)
	if err != nil {
		return model.APInvoice{}, err
	}
	date, err := addDays(b.spec.InvoiceDateFrom, b.p.intn(span+1))
	if err != nil {
		return model.APInvoice{}, err
	}
	inv.DocumentDate = date
	inv.ReceiptDate, err = addDays(date, b.p.between(1, 10))
	if err != nil {
		return model.APInvoice{}, err
	}
	inv.PaymentTermsDays = supplierTerms[b.p.intn(len(supplierTerms))]
	if b.p.chance(100) {
		// A non-empty Skonto must raise ambiguous_field_semantics: the customer's
		// own document calls the field an amount in one place and a percentage
		// rate in another, so it is carried verbatim and never converted.
		inv.DiscountRaw = "2.00"
		inv.DiscountDays = 10
	}

	// Lines, mirroring the purchase order where there is one.
	if err := b.fillInvoiceLines(&inv, lineCount, poLines, class, credit); err != nil {
		return model.APInvoice{}, err
	}

	// Now the single mutation of the exception class.
	switch class {
	case clsMissingPO:
		inv.PONumber = UnknownPONumber
	case clsPOSupplierMismatch:
		other := b.safeSuppliers[b.p.intn(len(b.safeSuppliers))]
		for other == inv.SupplierNumber {
			other = b.safeSuppliers[b.p.intn(len(b.safeSuppliers))]
		}
		inv.SupplierNumber = other
	case clsBlockedSupplier:
		inv.SupplierNumber = b.blockedSuppliers[b.p.intn(len(b.blockedSuppliers))]
	case clsUnknownCostCenter:
		inv.CostCenter = AbsentCostCenter
	case clsDeletedRateDKK:
		inv.CompanyCode = model.CompanyCodeCH10
		inv.Currency = "DKK"
		safe := b.safeCostCenters[model.CompanyCodeCH10]
		if !b.isSafeCostCenter(inv.CostCenter, model.CompanyCodeCH10) {
			inv.CostCenter = safe[b.p.intn(len(safe))]
		}
	case clsFxGap:
		gap, ok := b.gapFor("USD")
		if !ok {
			return model.APInvoice{}, fmt.Errorf("seed: no USD coverage gap was planted")
		}
		inv.CompanyCode = model.CompanyCodeCH10
		inv.Currency = "USD"
		inv.DocumentDate = gap.From
		inv.ReceiptDate, err = addDays(gap.From, 3)
		if err != nil {
			return model.APInvoice{}, err
		}
		if !b.isSafeCostCenter(inv.CostCenter, model.CompanyCodeCH10) {
			safe := b.safeCostCenters[model.CompanyCodeCH10]
			inv.CostCenter = safe[b.p.intn(len(safe))]
		}
	case clsCompanyCH20:
		inv.CompanyCode = model.CompanyCodeCH20
		inv.Currency = "EUR"
		safe := b.safeCostCenters[model.CompanyCodeCH20]
		inv.CostCenter = safe[b.p.intn(len(safe))]
		inv.PONumber = ""
	}

	// Foreign currency invoices must land on a date their currency is covered
	// on, unless the class is precisely about a missing rate.
	if class != clsFxGap && class != clsDeletedRateDKK && class != clsCompanyCH20 {
		if err := b.moveOntoCoveredDate(&inv); err != nil {
			return model.APInvoice{}, err
		}
	}

	// The positions leave this function in ascending line-number order, which is
	// what the customer's own document promises ("aufsteigend in Zehnerschritten")
	// and what the legacy writer therefore has to emit. It is not cosmetic: an
	// invoice re-delivered unchanged but with its positions in a different order
	// is a different payload, so the twin would see a content change and raise
	// the amended-invoice exception on a delivery that amended nothing.
	sort.Slice(inv.Lines, func(i, j int) bool { return inv.Lines[i].LineNo < inv.Lines[j].LineNo })

	// Fit the amounts to the referenced order, so the ERP's amount tolerance is
	// a designed outcome and not an artifact of the draw. Cross-currency
	// invoices are skipped because the ERP skips the comparison for them.
	if inv.PONumber != "" && inv.Currency == po.Currency && class != clsTotalsMismatch {
		if err := b.fitToPurchaseOrder(&inv, poLines, class); err != nil {
			return model.APInvoice{}, err
		}
	}
	return inv, nil
}

// fillInvoiceLines fills the invoice lines, mirroring the purchase order lines
// where there is a purchase order, and selecting the trap lines for the two
// unit-of-measure classes.
func (b *builder) fillInvoiceLines(inv *model.APInvoice, lineCount int, poLines []model.PurchaseOrderLine,
	class exceptionClass, credit bool) error {
	pick := poLines
	switch class {
	case clsPalletLine:
		// The pallet line must survive whatever the line count is, and the
		// 1000:3 line must not, so the pallet is placed first and the other
		// lines fill up behind it. Selecting by predicate rather than by slicing
		// keeps the trap independent of how many lines the invoice carries.
		pick = pickTrapLines(poLines,
			func(l model.PurchaseOrderLine) bool { return l.UoM == UoMPallet },
			func(l model.PurchaseOrderLine) bool { return l.Material == MaterialUnconvertible })
	case clsUoMUnconvertible:
		pick = pickTrapLines(poLines,
			func(l model.PurchaseOrderLine) bool { return l.Material == MaterialUnconvertible },
			func(l model.PurchaseOrderLine) bool { return l.UoM == UoMPallet })
	}
	if len(pick) > 0 {
		if lineCount > len(pick) {
			lineCount = len(pick)
		}
		for _, l := range pick[:lineCount] {
			qty := l.Quantity
			price := l.UnitPrice
			if credit {
				qty = qty.Neg()
			}
			amount, err := qty.MulRate(price, 1, 2)
			if err != nil {
				return err
			}
			line := model.APInvoiceLine{
				LineNo:     l.LineNo,
				GLAccount:  l.GLAccount,
				CostCenter: l.CostCenter,
				Quantity:   qty,
				UoM:        l.UoM,
				UnitPrice:  price,
				LineAmount: amount,
				TaxCode:    "",
			}
			// The undefined case: an empty position cost center. The customer's
			// document does not say whether it inherits from the header, so it
			// stays empty and raises ambiguous_field_semantics.
			if b.p.chance(150) {
				line.CostCenter = ""
			}
			inv.Lines = append(inv.Lines, line)
		}
		return nil
	}
	for j := 0; j < lineCount; j++ {
		qty := dec(int64(b.p.between(1, 25)*1000), 3)
		if credit {
			qty = qty.Neg()
		}
		price := dec(int64(b.p.between(500, 90000)), 4)
		amount, err := qty.MulRate(price, 1, 2)
		if err != nil {
			return err
		}
		line := model.APInvoiceLine{
			LineNo:     PadLeftZero(int64((j+1)*10), 5),
			GLAccount:  "0006" + PadLeftZero(int64(400+j*10), 3),
			CostCenter: inv.CostCenter,
			Quantity:   qty,
			UoM:        regularUoMs[(j+len(inv.Lines))%len(regularUoMs)],
			UnitPrice:  price,
			LineAmount: amount,
		}
		if b.p.chance(150) {
			line.CostCenter = ""
		}
		inv.Lines = append(inv.Lines, line)
	}
	return nil
}

// pickTrapLines returns the purchase order lines of a trap invoice: every line
// matching required first, then every line matching neither required nor
// forbidden, in line number order. Taking any prefix of the result therefore
// always contains the trap line and never the other trap.
func pickTrapLines(in []model.PurchaseOrderLine,
	required, forbidden func(model.PurchaseOrderLine) bool) []model.PurchaseOrderLine {
	out := make([]model.PurchaseOrderLine, 0, len(in))
	for _, l := range in {
		if required(l) {
			out = append(out, l)
		}
	}
	for _, l := range in {
		if required(l) || forbidden(l) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// poNetTotal is the net total of a purchase order's lines, which is what the ERP
// compares a posting against. Zero when the order has no lines.
func poNetTotal(poLines []model.PurchaseOrderLine) (model.Decimal, error) {
	total := dec(0, 2)
	for _, l := range poLines {
		amount, err := l.Quantity.MulRate(l.UnitPrice, 1, 2)
		if err != nil {
			return model.Decimal{}, err
		}
		total, err = total.Add(amount)
		if err != nil {
			return model.Decimal{}, err
		}
		if total, err = total.Rescale(2); err != nil {
			return model.Decimal{}, err
		}
	}
	return total, nil
}

// fitToPurchaseOrder scales an invoice's line amounts so the ERP's one-sided
// amount tolerance is a DESIGNED outcome rather than an artifact of the draw.
//
// The ERP refuses a posting whose net total exceeds the referenced order's net
// total by more than the published tolerance. Invoice amounts are drawn
// independently of the order they reference, so without this roughly two per
// cent of a five thousand invoice run over-billed by chance: eighty-two
// exceptions nobody designed, an expected set that moves with the seed, and a
// candidate seeing failures our own reference connector could not avoid.
//
// Every PO-referencing invoice is therefore scaled to between 40 and 100 percent
// of its order, which is also what accounts payable actually looks like: an
// invoice bills part of what was ordered. The clsAmountMismatch class is scaled
// to 130 percent instead, well past the tolerance, and it is the only class that
// reaches the ERP's refusal.
func (b *builder) fitToPurchaseOrder(inv *model.APInvoice, poLines []model.PurchaseOrderLine,
	class exceptionClass) error {
	if len(poLines) == 0 || len(inv.Lines) == 0 {
		return nil
	}
	poNet, err := poNetTotal(poLines)
	if err != nil {
		return err
	}
	if poNet.Sign() <= 0 {
		return nil
	}
	lineSum, err := inv.LineTotal()
	if err != nil {
		return err
	}
	if lineSum.Sign() <= 0 {
		return nil
	}
	// The target net total, in percent of the order.
	pct := int64(40 + b.p.intn(61)) // 40..100
	if class == clsAmountMismatch {
		pct = 130
	}
	target, err := poNet.MulRate(dec(pct, 2), 1, 2)
	if err != nil {
		return err
	}
	// factor = target / lineSum, at six decimals.
	//
	// MulRate(rate, divisor, scale) computes value*rate/divisor and already
	// accounts for both operands' scales, so dividing by a Decimal is expressed
	// as multiplying by 10^scale and dividing by its unscaled integer:
	//
	//	target * 10^s / unscaled(lineSum) = target * 10^s / (lineSum * 10^s) = target / lineSum
	factor, err := target.MulRate(pow10Dec(int(lineSum.Scale())), lineSum.Unscaled(), 6)
	if err != nil {
		return err
	}
	if factor.Sign() <= 0 {
		return nil
	}
	for i := range inv.Lines {
		scaled, err := inv.Lines[i].LineAmount.MulRate(factor, 1, 2)
		if err != nil {
			return err
		}
		if scaled.Sign() == 0 {
			scaled = dec(5, 2)
		}
		inv.Lines[i].LineAmount = scaled
		// The unit price follows the amount so quantity times price still
		// resembles the line, at the four decimals the format allows. Same
		// division trick as above: amount * 10^s / unscaled(quantity).
		if q := inv.Lines[i].Quantity; q.Sign() > 0 {
			if price, err := scaled.MulRate(pow10Dec(int(q.Scale())), q.Unscaled(), 4); err == nil &&
				price.Sign() > 0 {
				inv.Lines[i].UnitPrice = price
			}
		}
	}
	return nil
}

// pow10Int64 returns 10^n as an int64, for the divisor of a scale correction.
func pow10Int64(n int) int64 {
	out := int64(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}

// pow10Dec returns 10^n as a Decimal, for the multiplier of a scale correction.
func pow10Dec(n int) model.Decimal { return dec(pow10Int64(n), 0) }

// retotalInvoice recomputes the VAT amount and the gross total from the line
// amounts, so the header rule of the customer document holds exactly:
// Bruttobetrag equals the sum of the Positionsbeträge plus the MWST-Betrag.
//
// A pinned invoice keeps its hand-written totals and is only checked against the
// rule, because its amounts are published. The totals-mismatch class is the one
// place the rule is broken, and it is broken by a whole franc so the deviation
// can never be mistaken for a rounding artifact.
func (b *builder) retotalInvoice(inv *model.APInvoice, class exceptionClass, pinned bool) error {
	lineSum, err := inv.LineTotal()
	if err != nil {
		return err
	}
	lineSum, err = lineSum.Rescale(2)
	if err != nil {
		return err
	}
	if pinned {
		want, err := lineSum.Add(inv.VATAmount)
		if err != nil {
			return err
		}
		if !want.Equal(inv.GrossAmount) {
			return fmt.Errorf("seed: pinned invoice %s/%s violates the header total rule: %s + %s != %s",
				inv.SupplierNumber, inv.SupplierInvoiceNumber, lineSum.String(),
				inv.VATAmount.String(), inv.GrossAmount.String())
		}
	} else {
		var rate model.Decimal
		for _, vc := range vatCodes {
			if vc.code == inv.VATCode {
				rate = vc.rate
				break
			}
		}
		vat, err := lineSum.MulRate(rate, 1, 2)
		if err != nil {
			return err
		}
		gross, err := lineSum.Add(vat)
		if err != nil {
			return err
		}
		gross, err = gross.Rescale(2)
		if err != nil {
			return err
		}
		inv.VATAmount = vat
		inv.GrossAmount = gross
	}
	if class == clsTotalsMismatch {
		off, err := inv.GrossAmount.Add(dec(1000, 2))
		if err != nil {
			return err
		}
		off, err = off.Rescale(2)
		if err != nil {
			return err
		}
		inv.GrossAmount = off
	}
	// A currency whose minor unit is not the centime needs an amount that is
	// exact in its own minor units.
	scale, err := model.CurrencyScale(inv.Currency)
	if err != nil {
		return err
	}
	if _, err := inv.GrossAmount.MinorUnits(scale); err != nil {
		return fmt.Errorf("seed: invoice %s %s gross %s is not exact in %s minor units",
			inv.SupplierNumber, inv.SupplierInvoiceNumber, inv.GrossAmount.String(), inv.Currency)
	}
	return nil
}

// moveOntoCoveredDate shifts a foreign currency invoice forward until its
// document date is covered by a DAILY rate, so no clean invoice lands in the
// planted coverage hole by accident.
func (b *builder) moveOntoCoveredDate(inv *model.APInvoice) error {
	if inv.Currency == QuoteCurrency {
		return nil
	}
	for shift := 0; shift <= 40; shift++ {
		date, err := addDays(inv.DocumentDate, shift)
		if err != nil {
			return err
		}
		_, ok, err := b.dailyRateFor(inv.Currency, date)
		if err != nil {
			return err
		}
		if ok {
			inv.DocumentDate = date
			inv.ReceiptDate, err = addDays(date, 3)
			return err
		}
	}
	return fmt.Errorf("seed: no covered date near %s for %s", inv.DocumentDate, inv.Currency)
}

// decorateInvoice plants the text and date defects that are orthogonal to the
// exception classes: quoted fields, Windows-1252 high bytes and the two dates
// straddling the century pivot of the TTMMJJ format.
func (b *builder) decorateInvoice(inv *model.APInvoice, index int) {
	switch index {
	case 6:
		// A quoted field carrying an embedded CRLF.
		inv.Text = "Nachtrag zur Bestellung\r\nGenehmigt durch M. Bär"
	case 7:
		// Windows-1252 high bytes: umlauts and an eszett.
		inv.Text = "Rückvergütung Prüfgebühr, Größe 3, Weiß-Lack, Änderung Ölmenge, Übergabe offen"
	case 8:
		// The typographic apostrophe, the en dash and the euro sign, all in the
		// 0x80..0x9F block of Windows-1252.
		inv.Text = "Lieferung Q1 – Rabatt 2 % – Betrag in € gemäss Vertrag ’26"
	case 9:
		// TTMMJJ 010170: the two digit year 70 is 1970, the far side of the
		// documented pivot.
		inv.ReceiptDate = "1970-01-01"
	case 10:
		// TTMMJJ 311269: the two digit year 69 is 2069, the near side.
		inv.ReceiptDate = "2069-12-31"
	case 11:
		// A field containing both a semicolon and a doubled quote.
		inv.Text = `Rahmenvertrag; Zusatz "B"; Freigabe offen`
	}
	// Position texts live outside the canonical model, which has no text field
	// on an invoice line, so the generator keeps them in a side table the legacy
	// writer consults. Index 0 reproduces the doubled quotes of the customer
	// document's own example.
	switch index {
	case 0:
		b.setLineText(inv, "00010", `Ersatzteile, Charge "A-12"`)
		b.setLineText(inv, "00020", "Montage")
		b.setLineText(inv, "00030", "Kleinmaterial")
	case 12:
		b.setLineText(inv, "00010", `Charge "A-12"; Prüfprotokoll beigelegt`)
	}
}

// setLineText records the free text of one position, keyed by invoice and line
// number.
func (b *builder) setLineText(inv *model.APInvoice, lineNo, text string) {
	if b.lineTexts == nil {
		b.lineTexts = map[string]string{}
	}
	b.lineTexts[lineTextKey(inv.SupplierNumber, inv.SupplierInvoiceNumber, lineNo)] = text
}

// lineTextKey is the key of the position text side table.
func lineTextKey(supplierNumber, invoiceNumber, lineNo string) string {
	return supplierNumber + "\x1f" + invoiceNumber + "\x1f" + lineNo
}

// lineText returns the free text of a position: the planted one where there is
// one, otherwise a generated one.
func (b *builder) lineText(inv model.APInvoice, lineNo string) string {
	if t, ok := b.lineTexts[lineTextKey(inv.SupplierNumber, inv.SupplierInvoiceNumber, lineNo)]; ok {
		return t
	}
	return "Position " + lineNo
}

// poReference returns the spelling an invoice uses to refer to a purchase order.
// The same order is referred to in several spellings across the file: bare
// digits, extra leading zeros, a space padded form and, for the orders the ERP
// itself delivers with a prefix, the prefixed form. A line suffix appears on
// some of them.
func (b *builder) poReference(po model.PurchaseOrder, index int, poLines []model.PurchaseOrderLine) string {
	trimmed := strings.TrimSpace(po.PONumber)
	canon := CanonicalPONumber(po.PONumber)
	digitsOnly := allDigits(trimmed)
	ref := trimmed
	switch {
	case digitsOnly && index%9 == 2:
		// Extra leading zeros: the numeric comparison must strip them.
		ref = "0" + canon
	case digitsOnly && index%9 == 3:
		ref = " " + canon + " "
	case digitsOnly:
		ref = canon
	case index%9 == 4:
		// A prefixed number is compared as-is, so only the padding may vary.
		ref = " " + trimmed + " "
	}
	if index%5 == 0 && len(poLines) > 0 {
		ref += "/" + poLines[0].LineNo
	}
	return ref
}

// allDigits reports whether s is one or more ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// linesOf returns the purchase order lines of an order, in line number order.
func (b *builder) linesOf(poNumber string) []model.PurchaseOrderLine {
	idx := b.poLineIndex[CanonicalPONumber(poNumber)]
	out := make([]model.PurchaseOrderLine, 0, len(idx))
	for _, i := range idx {
		out = append(out, b.d.PurchaseOrderLines[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LineNo < out[j].LineNo })
	return out
}

// gapFor returns the planted coverage gap of a currency.
func (b *builder) gapFor(currency string) (FxGap, bool) {
	for _, g := range b.d.FxGaps {
		if g.Currency == currency {
			return g, true
		}
	}
	return FxGap{}, false
}

// dailyRateFor returns the winning DAILY rate for a currency on a calendar date:
// DELETED rows dropped, highest Sequence wins, half-open interval match on
// Europe/Zurich calendar dates.
func (b *builder) dailyRateFor(currency, date string) (model.FxRate, bool, error) {
	var best model.FxRate
	found := false
	for _, r := range b.resolvedByCurrency[currency] {
		in, err := model.DateInHalfOpenRange(date, r.ValidFrom, r.ValidTo)
		if err != nil {
			return model.FxRate{}, false, err
		}
		if !in {
			continue
		}
		if !found || r.Sequence > best.Sequence {
			best, found = r, true
		}
	}
	return best, found, nil
}
