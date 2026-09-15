package seed

import (
	"fmt"
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// Materials and units whose conversion behavior is graded.
const (
	// MaterialUnconvertible carries the 1000:3 conversion of the published rules.
	// Its base quantity is never exact at three decimals for an ordinary
	// alternative quantity, so a line on it is EXC_UOM_UNCONVERTIBLE and must
	// never be silently rounded.
	MaterialUnconvertible = "MAT-1000-3"
	// MaterialThreeDecimals carries a 1:8 conversion, so one carton is exactly
	// 0.125 base units: the base quantity needs exactly three decimals and the
	// line must convert, not fail.
	MaterialThreeDecimals = "MAT-EIGHTH"
	// UoMPallet has no entry in the conversion table at all, so a line in
	// pallets is EXC_UOM_UNMAPPABLE and must never be guessed at.
	UoMPallet = "PAL"
	// UoMTon maps 1000:1 to kilograms.
	UoMTon = "TON"
	// UoMStueck is the sender's own spelling of a piece and maps 1:1 to EA.
	UoMStueck = "STK"
	// AbsentCostCenter is a cost center code that deliberately does not exist in
	// the seeded table. Invoices carrying it are EXC_COST_CENTER_UNKNOWN.
	AbsentCostCenter = "0099999"
	// AnhangAPONumber is the purchase order of the customer document's own
	// worked example, reproduced verbatim so the published example is a real
	// record of the seeded landscape.
	AnhangAPONumber = "4500001234"
	// UoMTrapPONumber is the purchase order whose lines carry the two graded
	// conversion materials and the unmappable pallet line.
	UoMTrapPONumber = "4500001999"
	// CollisionSupplierCanonical and CollisionSupplierShort are two different
	// suppliers whose numbers become the same string once leading zeros are
	// stripped. Merging them is a data-loss bug a naive normalization causes;
	// they also share a legacy_id, which is why legacy_id is never a key.
	CollisionSupplierCanonical = "0000000417"
	CollisionSupplierShort     = "0000417"
)

// regularUoMs is the cycle of alternative units ordinary purchase order lines
// use. TON is included on purpose: it is an alternative unit of the sender, not
// a base unit of the ERP, and it needs the 1000:1 conversion.
var regularUoMs = []string{"EA", "PCE", UoMStueck, "CTN", "KG", UoMTon, "L", "M"}

// supplierNameStems and supplierNameSuffixes build creditor names that carry the
// Windows-1252 high bytes a real Swiss creditor master carries: umlauts, an
// eszett, a typographic apostrophe and an en dash.
var (
	supplierNameStems = []string{
		"Müller", "Bühler", "Schäfer", "Größle", "Küng", "Zürcher", "Häberli",
		"Widmer", "Brunner", "Steinbach", "Föllmi", "Oberhänsli", "Rüegg",
		"Bär", "Schürch", "Weiß", "Tröndle", "Käppeli", "Nüesch", "Bösch",
	}
	supplierNameSuffixes = []string{
		"Präzision AG", "Maschinenbau GmbH", "& Söhne AG", "Werkzeuge AG",
		"Anlagentechnik AG", "Logistik Sàrl", "Industrie–Service AG",
		"Oberflächentechnik AG", "Zerspanung GmbH", "Hydraulik AG",
	}
	supplierCountries  = []string{"CH", "CH", "CH", "CH", "DE", "DE", "AT", "IT", "FR", "LI"}
	supplierCurrencies = []string{"CHF", "CHF", "CHF", "CHF", "CHF", "EUR", "EUR", "USD"}
	supplierTerms      = []int{14, 30, 30, 30, 45, 60}
)

// buildCostCenters fills the cost center table. Cost centers are pre-seeded in
// the twin, as though another integration had already loaded them, so they carry
// no change sequence.
//
// The table opens with the documented significant-leading-zero exception: "0815"
// (Werk Wil) and "815" (Vertrieb DACH) are two different cost centers, and a
// client that strips leading zeros posts to the wrong one. It also contains an
// expired and a blocked cost center, and it deliberately does NOT contain
// [AbsentCostCenter].
func (b *builder) buildCostCenters() error {
	fixed := []model.CostCenter{
		{Code: "0815", Name: "Werk Wil", CompanyCode: model.CompanyCodeCH10, ValidFrom: "2019-01-01"},
		{Code: "815", Name: "Vertrieb DACH", CompanyCode: model.CompanyCodeCH10, ValidFrom: "2021-04-01"},
		{Code: "0012340", Name: "Instandhaltung Presse 3", CompanyCode: model.CompanyCodeCH10, ValidFrom: "2018-01-01"},
		{Code: "0012350", Name: "Montage", CompanyCode: model.CompanyCodeCH10, ValidFrom: "2018-01-01"},
		{Code: "CC-2200", Name: "Zentrale Dienste", CompanyCode: model.CompanyCodeCH10, ValidFrom: "2017-01-01"},
		{Code: "0012360", Name: "Werk Wil Logistik", CompanyCode: model.CompanyCodeCH20, ValidFrom: "2020-01-01"},
		{Code: "0012370", Name: "Vertrieb Süd", CompanyCode: model.CompanyCodeCH20, ValidFrom: "2020-01-01"},
		{Code: "CC-2900", Name: "Tochter Betrieb", CompanyCode: model.CompanyCodeCH20, ValidFrom: "2020-01-01"},
		// Expired: valid_to is in the past, so it is not valid at any invoice date.
		{Code: "0099998", Name: "Stilllegung Werk Bülach", CompanyCode: model.CompanyCodeCH10,
			ValidFrom: "2015-01-01", ValidTo: "2025-12-31"},
		// Blocked: exists and is in range, but must not be posted to.
		{Code: "0099997", Name: "Projekt Übergabe", CompanyCode: model.CompanyCodeCH10,
			ValidFrom: "2015-01-01", Blocked: true},
	}
	if b.spec.CostCenters < len(fixed) {
		return fmt.Errorf("seed: scenario %s wants %d cost centers, the fixed table needs %d",
			b.spec.ID, b.spec.CostCenters, len(fixed))
	}
	out := make([]model.CostCenter, 0, b.spec.CostCenters)
	out = append(out, fixed...)
	for i := len(fixed); i < b.spec.CostCenters; i++ {
		cc := model.CostCenter{ValidFrom: "2019-01-01"}
		if i%3 == 0 {
			cc.Code = "CC-" + PadLeftZero(int64(2200+i), 4)
			cc.Name = "Kostenstelle " + cc.Code
		} else {
			cc.Code = "0" + PadLeftZero(int64(20000+i), 6)
			cc.Name = "Kostenstelle " + cc.Code
		}
		cc.CompanyCode = model.CompanyCodeCH10
		if i%7 == 0 {
			cc.CompanyCode = model.CompanyCodeCH20
		}
		if i%53 == 0 {
			cc.Blocked = true
		}
		if i%89 == 0 {
			cc.ValidTo = "2025-12-31"
		}
		out = append(out, cc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	b.d.CostCenters = out
	b.costCenterByCode = make(map[string]int, len(out))
	for i := range out {
		if _, dup := b.costCenterByCode[out[i].Code]; dup {
			return fmt.Errorf("seed: duplicate cost center code %q", out[i].Code)
		}
		b.costCenterByCode[out[i].Code] = i
	}
	// Safe cost centers: not blocked and valid over the whole invoice date
	// range, so a clean invoice can never fail step 4 of the matching engine by
	// accident. Built by walking the sorted slice, never by ranging a map.
	b.safeCostCenters = map[string][]string{}
	for i := range out {
		cc := out[i]
		if cc.Blocked {
			continue
		}
		fromOK, err := model.CompareDates(cc.ValidFrom, b.spec.InvoiceDateFrom)
		if err != nil {
			return err
		}
		if fromOK > 0 {
			continue
		}
		if cc.ValidTo != "" {
			toOK, err := model.CompareDates(cc.ValidTo, b.spec.InvoiceDateTo)
			if err != nil {
				return err
			}
			if toOK <= 0 {
				continue
			}
		}
		b.safeCostCenters[cc.CompanyCode] = append(b.safeCostCenters[cc.CompanyCode], cc.Code)
	}
	for _, code := range []string{model.CompanyCodeCH10, model.CompanyCodeCH20} {
		if len(b.safeCostCenters[code]) == 0 {
			return fmt.Errorf("seed: no safe cost center for company code %s", code)
		}
	}
	if _, exists := b.costCenterByCode[AbsentCostCenter]; exists {
		return fmt.Errorf("seed: %q must not exist: it is the unknown cost center trap", AbsentCostCenter)
	}
	return nil
}

// buildSuppliers fills the creditor master.
//
// Supplier numbers are canonically ten characters, zero padded, and every
// generated record uses that form except one: the last slot is issued as
// [CollisionSupplierShort], a seven character number left behind by an older
// migration. Section 1.3 of the build specification uses exactly that spelling
// ("0000417") while section 16.1 calls ten characters canonical, and both forms
// therefore exist in the seeded landscape. Stripping leading zeros collapses it
// onto [CollisionSupplierCanonical], which is the data-loss bug the pair exists
// to catch; padding it to ten characters is the same bug from the other side.
func (b *builder) buildSuppliers() error {
	n := b.spec.Suppliers
	if n < 32 {
		return fmt.Errorf("seed: scenario %s wants %d suppliers, at least 32 are needed for the planted pairs", b.spec.ID, n)
	}
	out := make([]Supplier, 0, n)
	for i := 0; i < n; i++ {
		numeric := int64(401 + i)
		number := PadLeftZero(numeric, 10)
		if i == n-1 {
			// The migration remnant: seven characters, same numeric value as
			// CollisionSupplierCanonical, therefore the same legacy_id.
			number = CollisionSupplierShort
			numeric = 417
		}
		s := Supplier{LegacyID: numeric}
		s.SupplierNumber = number
		s.Name = b.p.pickString(supplierNameStems) + " " + b.p.pickString(supplierNameSuffixes)
		s.Country = b.p.pickString(supplierCountries)
		s.Currency = b.p.pickString(supplierCurrencies)
		s.IBAN = fmt.Sprintf("CH%02d00762%012d", b.p.between(10, 97), numeric*7919%1000000000000)
		s.VATNumber = fmt.Sprintf("CHE-%03d.%03d.%03d MWST", b.p.between(100, 999), b.p.between(100, 999), b.p.between(100, 999))
		s.PaymentTermsDays = supplierTerms[b.p.intn(len(supplierTerms))]
		// Roughly three percent of creditors are payment blocked.
		s.Blocked = b.p.chance(30)
		if number == CollisionSupplierCanonical || number == CollisionSupplierShort {
			// Both halves of the collision pair are referenced by invoices, so
			// neither may be blocked: the collision has to be reachable without
			// tripping EXC_SUPPLIER_BLOCKED first.
			s.Blocked = false
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SupplierNumber < out[j].SupplierNumber })
	b.d.Suppliers = out
	b.supplierByNumber = make(map[string]int, len(out))
	for i := range out {
		if _, dup := b.supplierByNumber[out[i].SupplierNumber]; dup {
			return fmt.Errorf("seed: duplicate supplier number %q", out[i].SupplierNumber)
		}
		b.supplierByNumber[out[i].SupplierNumber] = i
	}
	for _, want := range []string{CollisionSupplierCanonical, CollisionSupplierShort} {
		if _, ok := b.supplierByNumber[want]; !ok {
			return fmt.Errorf("seed: planted supplier %q missing", want)
		}
	}
	for i := range out {
		if !out[i].Blocked {
			b.safeSuppliers = append(b.safeSuppliers, out[i].SupplierNumber)
		} else {
			b.blockedSuppliers = append(b.blockedSuppliers, out[i].SupplierNumber)
		}
	}
	if len(b.blockedSuppliers) == 0 {
		return fmt.Errorf("seed: no blocked supplier was planted")
	}
	return nil
}

// buildUoMConversions fills the conversion table served by
// GET /erp/v1/uom-conversions. It is a fixed table: no draw from the generator
// touches it, so every scenario publishes the same conversions.
func (b *builder) buildUoMConversions() {
	out := []UoMConversion{
		{AltUoM: "EA", Numerator: 1, Denominator: 1, BaseUoM: "EA"},
		{AltUoM: "PCE", Numerator: 1, Denominator: 1, BaseUoM: "EA"},
		{AltUoM: UoMStueck, Numerator: 1, Denominator: 1, BaseUoM: "EA"},
		{AltUoM: "CTN", Numerator: 12, Denominator: 1, BaseUoM: "EA"},
		{AltUoM: "KG", Numerator: 1, Denominator: 1, BaseUoM: "KG"},
		{AltUoM: UoMTon, Numerator: 1000, Denominator: 1, BaseUoM: "KG"},
		{AltUoM: "L", Numerator: 1, Denominator: 1, BaseUoM: "L"},
		{AltUoM: "M", Numerator: 1, Denominator: 1, BaseUoM: "M"},
		// The units the subsidiary's legacy export delivers on service and
		// lump-sum lines. They are their own base unit: an hour converts to an
		// hour. Without these rows every service line on a legacy invoice would
		// become EXC_UOM_UNMAPPABLE, which would drown the one line that is
		// supposed to.
		{AltUoM: "STD", Numerator: 1, Denominator: 1, BaseUoM: "STD"},
		{AltUoM: "H", Numerator: 1, Denominator: 1, BaseUoM: "STD"},
		{AltUoM: "PAU", Numerator: 1, Denominator: 1, BaseUoM: "PAU"},
		{AltUoM: "M2", Numerator: 1, Denominator: 1, BaseUoM: "M2"},
		{AltUoM: "G", Numerator: 1, Denominator: 1000, BaseUoM: "KG"},
		// Material specific overrides of the CTN rule.
		{Material: MaterialUnconvertible, AltUoM: "CTN", Numerator: 1000, Denominator: 3, BaseUoM: "EA"},
		{Material: MaterialThreeDecimals, AltUoM: "CTN", Numerator: 1, Denominator: 8, BaseUoM: "EA"},
		// PAL is deliberately absent.
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Material != out[j].Material {
			return out[i].Material < out[j].Material
		}
		return out[i].AltUoM < out[j].AltUoM
	})
	b.d.UoMConversions = out
	b.uomByMaterialUnit = make(map[string]int, len(out))
	for i := range out {
		b.uomByMaterialUnit[out[i].Material+"\x1f"+out[i].AltUoM] = i
	}
}

// lookupConversion returns the conversion for a material and alternative unit,
// preferring a material specific row over the generic one.
func (b *builder) lookupConversion(material, altUoM string) (UoMConversion, bool) {
	if i, ok := b.uomByMaterialUnit[material+"\x1f"+altUoM]; ok {
		return b.d.UoMConversions[i], true
	}
	if i, ok := b.uomByMaterialUnit["\x1f"+altUoM]; ok {
		return b.d.UoMConversions[i], true
	}
	return UoMConversion{}, false
}

// poSpelling returns the spelling a purchase order number is delivered in.
// Three spellings occur, exactly as the published identifier rule describes:
// bare digits, a "PO-" prefix, and a space padded form. The prefixed form does
// not match the digits numerically, so it must be compared as-is; the space
// padded form must be trimmed before it is compared at all.
func poSpelling(index int, digits string) string {
	switch {
	case index%4 == 1:
		return "PO-" + digits
	case index%37 == 5:
		return " " + digits + " "
	default:
		return digits
	}
}

// buildPurchaseOrders fills the purchase order headers and lines.
//
// Index 0 is the customer document's own worked example, [AnhangAPONumber], so
// the published example resolves against real data. Index 1 is
// [UoMTrapPONumber], whose three lines carry the 1000:3 material, the 1:8
// material and an unmappable pallet line.
func (b *builder) buildPurchaseOrders() error {
	n := b.spec.PurchaseOrders
	if n < 8 {
		return fmt.Errorf("seed: scenario %s wants %d purchase orders, at least 8 are needed", b.spec.ID, n)
	}
	linesPerPO := make([]int, n)
	base := 3 * n
	extra := b.spec.PurchaseOrderLines - base
	if extra < 0 || extra > n {
		return fmt.Errorf("seed: scenario %s wants %d purchase order lines for %d orders, which is not %d..%d",
			b.spec.ID, b.spec.PurchaseOrderLines, n, base, base+n)
	}
	for i := range linesPerPO {
		linesPerPO[i] = 3
		if i < extra {
			linesPerPO[i] = 4
		}
	}

	headers := make([]model.PurchaseOrder, 0, n)
	lines := make([]model.PurchaseOrderLine, 0, b.spec.PurchaseOrderLines)
	for i := 0; i < n; i++ {
		var po model.PurchaseOrder
		switch i {
		case 0:
			po.PONumber = AnhangAPONumber
			po.SupplierNumber = CollisionSupplierCanonical
			po.CompanyCode = model.CompanyCodeCH10
			po.Currency = "EUR"
			po.Status = model.POStatusOpen
			po.OrderDate = "2026-01-12"
			po.CostCenter = "0012340"
		case 1:
			po.PONumber = UoMTrapPONumber
			po.SupplierNumber = CollisionSupplierCanonical
			po.CompanyCode = model.CompanyCodeCH10
			po.Currency = "CHF"
			po.Status = model.POStatusOpen
			po.OrderDate = "2026-01-19"
			po.CostCenter = "0012350"
		default:
			digits := PadLeftZero(int64(4500002000+i), 10)
			po.PONumber = poSpelling(i, digits)
			po.SupplierNumber = b.safeSuppliers[b.p.intn(len(b.safeSuppliers))]
			po.CompanyCode = model.CompanyCodeCH10
			if i%11 == 0 {
				po.CompanyCode = model.CompanyCodeCH20
			}
			po.Currency = b.d.Suppliers[b.supplierByNumber[po.SupplierNumber]].Currency
			switch {
			case i%13 == 0:
				po.Status = model.POStatusClosed
			case i%29 == 0:
				po.Status = model.POStatusCancelled
			default:
				po.Status = model.POStatusOpen
			}
			// Some orders predate the closed fiscal period boundary. That is
			// what a real order book looks like; it is not a rejection.
			if i%17 == 0 {
				po.OrderDate = "2026-01-" + PadLeftZero(int64(1+i%28), 2)
			} else {
				po.OrderDate = "2026-0" + PadLeftZero(int64(2+i%2), 1) + "-" + PadLeftZero(int64(1+i%28), 2)
			}
			safe := b.safeCostCenters[po.CompanyCode]
			po.CostCenter = safe[b.p.intn(len(safe))]
		}
		headers = append(headers, po)

		for j := 0; j < linesPerPO[i]; j++ {
			l := model.PurchaseOrderLine{
				PONumber:   po.PONumber,
				LineNo:     PadLeftZero(int64((j+1)*10), 5),
				Currency:   po.Currency,
				GLAccount:  "0006" + PadLeftZero(int64(400+(i+j)%9*10), 3),
				CostCenter: po.CostCenter,
			}
			switch {
			case i == 0:
				// The three lines of Anhang A, verbatim.
				anhang := []struct {
					material, desc, qty, unit, price string
				}{
					{"MAT-100641", "Ersatzteile, Charge A-12", "12.000", UoMStueck, "45.5000"},
					{"MAT-100681", "Montage", "6.500", "PCE", "105.0000"},
					{"MAT-100683", "Kleinmaterial", "1.000", "EA", "16.3000"},
				}
				if j < len(anhang) {
					a := anhang[j]
					l.Material, l.Description, l.UoM = a.material, a.desc, a.unit
					l.Quantity = mustParseDec(a.qty)
					l.UnitPrice = mustParseDec(a.price)
				} else {
					b.fillRegularPOLine(&l, i, j)
				}
			case i == 1:
				trap := []struct {
					material, desc, qty, unit, price string
				}{
					{MaterialUnconvertible, "Rollenware, 1000:3 Umrechnung", "2.000", "CTN", "18.5000"},
					{MaterialThreeDecimals, "Achtelpalette", "1.000", "CTN", "96.0000"},
					{"MAT-100999", "Ganzpalette", "1.000", UoMPallet, "480.0000"},
				}
				if j < len(trap) {
					t := trap[j]
					l.Material, l.Description, l.UoM = t.material, t.desc, t.unit
					l.Quantity = mustParseDec(t.qty)
					l.UnitPrice = mustParseDec(t.price)
				} else {
					b.fillRegularPOLine(&l, i, j)
				}
			default:
				b.fillRegularPOLine(&l, i, j)
			}
			lines = append(lines, l)
		}
	}

	sort.Slice(headers, func(i, j int) bool { return headers[i].PONumber < headers[j].PONumber })
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].PONumber != lines[j].PONumber {
			return lines[i].PONumber < lines[j].PONumber
		}
		return lines[i].LineNo < lines[j].LineNo
	})
	b.d.PurchaseOrders = headers
	b.d.PurchaseOrderLines = lines

	b.poByCanonical = make(map[string]int, len(headers))
	b.poByDelivered = make(map[string]int, len(headers))
	for i := range headers {
		c := CanonicalPONumber(headers[i].PONumber)
		if _, dup := b.poByCanonical[c]; dup {
			return fmt.Errorf("seed: purchase order numbers %q collide canonically as %q", headers[i].PONumber, c)
		}
		b.poByCanonical[c] = i
		b.poByDelivered[headers[i].PONumber] = i
	}
	b.poLineIndex = map[string][]int{}
	for i := range lines {
		c := CanonicalPONumber(lines[i].PONumber)
		b.poLineIndex[c] = append(b.poLineIndex[c], i)
	}

	// Safe purchase orders: OPEN, owned by an unblocked supplier, with a cost
	// center that is valid for the whole invoice window and lines whose units
	// all convert exactly. A clean invoice may only reference one of these.
	for i := range headers {
		po := headers[i]
		if po.Status != model.POStatusOpen {
			continue
		}
		si, ok := b.supplierByNumber[po.SupplierNumber]
		if !ok || b.d.Suppliers[si].Blocked {
			continue
		}
		if !b.isSafeCostCenter(po.CostCenter, po.CompanyCode) {
			continue
		}
		clean := true
		for _, li := range b.poLineIndex[CanonicalPONumber(po.PONumber)] {
			l := lines[li]
			conv, ok := b.lookupConversion(l.Material, l.UoM)
			if !ok {
				clean = false
				break
			}
			if _, err := convertQuantity(l.Quantity, conv); err != nil {
				clean = false
				break
			}
		}
		if clean {
			b.safePOs = append(b.safePOs, po.PONumber)
		}
	}
	if len(b.safePOs) == 0 {
		return fmt.Errorf("seed: no safe purchase order was generated")
	}
	// The two planted purchase orders must be usable.
	for _, want := range []string{AnhangAPONumber, UoMTrapPONumber} {
		if _, ok := b.poByDelivered[want]; !ok {
			return fmt.Errorf("seed: planted purchase order %q missing", want)
		}
	}
	// At least one CLOSED or CANCELLED order and one order whose supplier
	// differs from a chosen invoice supplier must exist, or two exception
	// classes are unreachable.
	for i := range headers {
		switch headers[i].Status {
		case model.POStatusClosed, model.POStatusCancelled:
			b.closedPOs = append(b.closedPOs, headers[i].PONumber)
			// A closed order is only usable for the closed-order class when its
			// creditor is unblocked: otherwise the supplier check fires first
			// and the invoice carries the wrong exception.
			if si, ok := b.supplierByNumber[headers[i].SupplierNumber]; ok && !b.d.Suppliers[si].Blocked {
				b.closedPOsUsableSupplier = append(b.closedPOsUsableSupplier, headers[i].PONumber)
			}
		}
	}
	if len(b.closedPOs) == 0 {
		return fmt.Errorf("seed: no closed or cancelled purchase order was generated")
	}
	if len(b.closedPOsUsableSupplier) == 0 {
		return fmt.Errorf("seed: no closed purchase order with an unblocked creditor was generated")
	}
	return nil
}

// fillRegularPOLine fills an ordinary purchase order line: a cycling unit of
// measure, a quantity at three decimals and a unit price at four.
func (b *builder) fillRegularPOLine(l *model.PurchaseOrderLine, poIndex, lineIndex int) {
	l.Material = "MAT-" + PadLeftZero(int64(100000+(poIndex*7+lineIndex)%899999), 6)
	l.Description = "Position " + l.LineNo
	l.UoM = regularUoMs[(poIndex+lineIndex)%len(regularUoMs)]
	l.Quantity = dec(int64(b.p.between(1, 40)*1000), 3)
	l.UnitPrice = dec(int64(b.p.between(500, 250000)), 4)
}

// isSafeCostCenter reports whether a cost center is one of the codes a clean
// invoice may use for a company code.
func (b *builder) isSafeCostCenter(code, companyCode string) bool {
	for _, c := range b.safeCostCenters[companyCode] {
		if c == code {
			return true
		}
	}
	return false
}

// convertQuantity returns the base quantity of an alternative quantity at
// exactly three decimals, or an error when the conversion does not terminate
// there. It computes at scale nine and then rescales exactly, so a
// non-terminating conversion is a reported error and never a silent round.
func convertQuantity(qty model.Decimal, conv UoMConversion) (model.Decimal, error) {
	if conv.Denominator == 0 {
		return model.Decimal{}, fmt.Errorf("seed: conversion %s/%s has a zero denominator", conv.Material, conv.AltUoM)
	}
	wide, err := qty.MulRate(dec(conv.Numerator, 0), conv.Denominator, 9)
	if err != nil {
		return model.Decimal{}, err
	}
	return wide.Rescale(3)
}
