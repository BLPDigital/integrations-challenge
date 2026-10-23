package seed

import (
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestSupplierNumberCollisionPair(t *testing.T) {
	// Two different creditors whose numbers become the same string once leading
	// zeros are stripped, and which therefore also share a legacy_id. Merging
	// them loses a creditor; using legacy_id as a key loses one too.
	d := generated(t, ScenarioS0)
	var canonical, short *Supplier
	for i := range d.Suppliers {
		switch d.Suppliers[i].SupplierNumber {
		case CollisionSupplierCanonical:
			canonical = &d.Suppliers[i]
		case CollisionSupplierShort:
			short = &d.Suppliers[i]
		}
	}
	if canonical == nil || short == nil {
		t.Fatalf("the collision pair is missing: %v / %v", canonical, short)
	}
	if canonical.SupplierNumber == short.SupplierNumber {
		t.Fatal("the pair must be two distinct keys")
	}
	if strings.TrimLeft(canonical.SupplierNumber, "0") != strings.TrimLeft(short.SupplierNumber, "0") {
		t.Errorf("%q and %q do not collide when leading zeros are stripped",
			canonical.SupplierNumber, short.SupplierNumber)
	}
	if canonical.LegacyID != short.LegacyID {
		t.Errorf("legacy_id %d and %d differ; the pair should show why legacy_id is not a key",
			canonical.LegacyID, short.LegacyID)
	}
	if canonical.Blocked || short.Blocked {
		t.Error("neither half of the collision pair may be payment blocked: the collision must be reachable")
	}
}

func TestLegacyIDIsANumberAndNeverTheKey(t *testing.T) {
	d := generated(t, ScenarioS0)
	byLegacy := map[int64]int{}
	for i := range d.Suppliers {
		if d.Suppliers[i].LegacyID <= 0 {
			t.Fatalf("supplier %q has legacy_id %d", d.Suppliers[i].SupplierNumber, d.Suppliers[i].LegacyID)
		}
		byLegacy[d.Suppliers[i].LegacyID]++
	}
	dupes := 0
	for _, n := range byLegacy {
		if n > 1 {
			dupes++
		}
	}
	if dupes == 0 {
		t.Error("no legacy_id is shared, so nothing proves it cannot be used as a key")
	}
	// The field must serialize as a JSON number, which is the whole trap.
	b, err := model.CanonicalJSON(d.Suppliers[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"legacy_id":`) {
		t.Fatalf("legacy_id is absent from the supplier JSON: %s", b)
	}
	if strings.Contains(string(b), `"legacy_id":"`) {
		t.Errorf("legacy_id must be a JSON number, not a string: %s", b)
	}
	if !strings.Contains(string(b), `"supplier_number":"`) {
		t.Errorf("supplier_number must be a JSON string: %s", b)
	}
}

func TestCostCenterLeadingZeroException(t *testing.T) {
	// The documented exception to the identifier rule: cost center leading zeros
	// are significant, so "0815" and "815" are two different cost centers.
	d := generated(t, ScenarioS0)
	found := map[string]model.CostCenter{}
	for _, cc := range d.CostCenters {
		if cc.Code == "0815" || cc.Code == "815" {
			found[cc.Code] = cc
		}
	}
	if len(found) != 2 {
		t.Fatalf("want both 0815 and 815, got %d: %v", len(found), found)
	}
	if found["0815"].Name == found["815"].Name {
		t.Error("the two cost centers must be distinguishable, not two spellings of one")
	}
	if found["0815"].Name != "Werk Wil" || found["815"].Name != "Vertrieb DACH" {
		t.Errorf("the documented names changed: %q / %q", found["0815"].Name, found["815"].Name)
	}
	if _, exists := found[AbsentCostCenter]; exists {
		t.Error("the unknown-cost-center trap code must not exist")
	}
}

func TestPurchaseOrderSpellings(t *testing.T) {
	d := generated(t, ScenarioS1)
	var plain, prefixed, padded int
	statuses := map[string]int{}
	for _, po := range d.PurchaseOrders {
		statuses[po.Status]++
		switch {
		case po.PONumber != strings.TrimSpace(po.PONumber):
			padded++
		case strings.HasPrefix(po.PONumber, "PO-"):
			prefixed++
		default:
			plain++
		}
	}
	if plain == 0 || prefixed == 0 || padded == 0 {
		t.Errorf("want all three spellings, got plain=%d prefixed=%d padded=%d", plain, prefixed, padded)
	}
	for _, want := range []string{model.POStatusOpen, model.POStatusClosed, model.POStatusCancelled} {
		if statuses[want] == 0 {
			t.Errorf("no purchase order carries status %s", want)
		}
	}
	// Invoices refer to purchase orders in several spellings, including a line
	// suffix, and every reference that is meant to resolve must resolve.
	forms := map[string]int{}
	for _, inv := range d.Invoices {
		if inv.PONumber == "" {
			continue
		}
		switch {
		case strings.Contains(inv.PONumber, "PO-"):
			forms["prefixed"]++
		case inv.PONumber != strings.TrimSpace(inv.PONumber):
			forms["padded"]++
		case strings.HasPrefix(inv.PONumber, "0"):
			forms["extra zeros"]++
		default:
			forms["plain"]++
		}
		if strings.Contains(inv.PONumber, "/") {
			forms["line suffix"]++
		}
	}
	for _, want := range []string{"prefixed", "padded", "extra zeros", "plain", "line suffix"} {
		if forms[want] == 0 {
			t.Errorf("no invoice refers to a purchase order in the %q form", want)
		}
	}
}

func TestUoMTrapsArePresent(t *testing.T) {
	d := generated(t, ScenarioS0)
	units := map[string]int{}
	materials := map[string]int{}
	for _, l := range d.PurchaseOrderLines {
		units[l.UoM]++
		materials[l.Material]++
	}
	for _, want := range []string{"EA", "PCE", "CTN", "KG", UoMTon, "L", "M", UoMPallet} {
		if units[want] == 0 {
			t.Errorf("no purchase order line carries unit %s", want)
		}
	}
	for _, want := range []string{MaterialUnconvertible, MaterialThreeDecimals} {
		if materials[want] == 0 {
			t.Errorf("no purchase order line carries material %s", want)
		}
	}
	// The conversion table: a carton is twelve, a ton is a thousand kilograms,
	// the 1:8 material needs exactly three decimals and PAL has no entry at all.
	byKey := map[string]UoMConversion{}
	for _, c := range d.UoMConversions {
		byKey[c.Material+"|"+c.AltUoM] = c
	}
	if c := byKey["|CTN"]; c.Numerator != 12 || c.Denominator != 1 || c.BaseUoM != "EA" {
		t.Errorf("the carton conversion is %v", c)
	}
	if c := byKey["|"+UoMTon]; c.Numerator != 1000 || c.Denominator != 1 || c.BaseUoM != "KG" {
		t.Errorf("the ton conversion is %v", c)
	}
	if c := byKey[MaterialUnconvertible+"|CTN"]; c.Numerator != 1000 || c.Denominator != 3 {
		t.Errorf("the 1000:3 conversion is %v", c)
	}
	if c, ok := byKey[MaterialThreeDecimals+"|CTN"]; !ok || c.Numerator != 1 || c.Denominator != 8 {
		t.Errorf("the three-decimal conversion is %v", c)
	}
	if _, exists := byKey["|"+UoMPallet]; exists {
		t.Error("PAL must have no conversion at all: it is EXC_UOM_UNMAPPABLE, not a guess")
	}
	// The three-decimal material really needs three decimals, and the 1000:3
	// material really does not terminate there.
	three, _ := byKey[MaterialThreeDecimals+"|CTN"]
	got, err := convertQuantity(dec(1000, 3), three)
	if err != nil {
		t.Fatalf("the three-decimal conversion failed: %v", err)
	}
	if got.String() != "0.125" {
		t.Errorf("one carton of %s is %s, want 0.125", MaterialThreeDecimals, got.String())
	}
	if _, err := convertQuantity(dec(2000, 3), byKey[MaterialUnconvertible+"|CTN"]); err == nil {
		t.Errorf("the 1000:3 conversion of two cartons must not terminate at three decimals")
	}
}

func TestBlockedSuppliersAndClosedPeriodArePresent(t *testing.T) {
	d := generated(t, ScenarioS0)
	blocked := 0
	for i := range d.Suppliers {
		if d.Suppliers[i].Blocked {
			blocked++
		}
	}
	if blocked == 0 {
		t.Error("no blocked creditor was planted")
	}
	if d.ClosedPeriodBefore != "2026-02-01" {
		t.Errorf("the closed period boundary is %q, want 2026-02-01", d.ClosedPeriodBefore)
	}
	// The boundary is a fact of the landscape: order dates straddle it, and no
	// invoice is dated into the closed period, so it never fires by accident.
	before := 0
	for _, po := range d.PurchaseOrders {
		c, err := model.CompareDates(po.OrderDate, d.ClosedPeriodBefore)
		if err != nil {
			t.Fatal(err)
		}
		if c < 0 {
			before++
		}
	}
	if before == 0 {
		t.Error("no purchase order predates the closed fiscal period boundary")
	}
	for _, inv := range d.Invoices {
		c, err := model.CompareDates(inv.DocumentDate, d.ClosedPeriodBefore)
		if err != nil {
			t.Fatal(err)
		}
		if c < 0 {
			t.Fatalf("invoice %s/%s is dated %s, inside the closed period",
				inv.SupplierNumber, inv.SupplierInvoiceNumber, inv.DocumentDate)
		}
	}
}

func TestAmbiguityMarkersFire(t *testing.T) {
	// The two documented non-decisions must both be reachable: a non-empty
	// Skonto and an empty position cost center.
	d := generated(t, ScenarioS0)
	skonto, emptyPositionCC := 0, 0
	for _, inv := range d.Invoices {
		if strings.TrimSpace(inv.DiscountRaw) != "" {
			skonto++
		}
		for _, l := range inv.Lines {
			if l.CostCenter == "" {
				emptyPositionCC++
			}
		}
	}
	if skonto == 0 {
		t.Error("no document carries a non-empty Skonto")
	}
	if emptyPositionCC == 0 {
		t.Error("no position carries an empty Kostenstelle")
	}
}

func TestCreditNotesCarryAnExplicitSign(t *testing.T) {
	d := generated(t, ScenarioS0)
	credits, negative := 0, 0
	for _, inv := range d.Invoices {
		if inv.DocumentType != model.DocumentTypeCreditNote {
			continue
		}
		credits++
		if inv.GrossAmount.Sign() < 0 {
			negative++
		}
	}
	if credits == 0 {
		t.Fatal("no credit note was planted")
	}
	if negative != credits {
		t.Errorf("%d of %d credit notes carry an explicit negative amount; the sign must never be derived from the document type",
			negative, credits)
	}
}

func TestBothCompanyCodesAreDelivered(t *testing.T) {
	// The CH20 delivery must exist, because that subsidiary's rate table faults
	// permanently and the fault has to be reachable just by processing the input.
	d := generated(t, ScenarioS0)
	seen := map[string]int{}
	for _, del := range d.Deliveries {
		seen[del.CompanyCode] += del.KopfCount
		if del.Mandant != MandantCH10 && del.Mandant != MandantCH20 {
			t.Errorf("delivery %s carries Mandant %q", del.Name, del.Mandant)
		}
	}
	if seen[model.CompanyCodeCH10] == 0 || seen[model.CompanyCodeCH20] == 0 {
		t.Fatalf("want invoices for both company codes, got %v", seen)
	}
	foreignCH20 := 0
	for _, inv := range d.Invoices {
		if inv.CompanyCode == model.CompanyCodeCH20 && inv.Currency != QuoteCurrency {
			foreignCH20++
		}
	}
	if foreignCH20 == 0 {
		t.Error("no CH20 invoice needs a rate, so the permanent SOAP fault never bites")
	}
}

func TestLeadingZeroInvoiceNumberPair(t *testing.T) {
	// "0004711" and "4711" are two different invoices of the same creditor.
	d := generated(t, ScenarioS0)
	var padded, bare *model.APInvoice
	for i := range d.Invoices {
		if d.Invoices[i].SupplierNumber != CollisionSupplierCanonical {
			continue
		}
		switch d.Invoices[i].SupplierInvoiceNumber {
		case "0004711":
			padded = &d.Invoices[i]
		case "4711":
			bare = &d.Invoices[i]
		}
	}
	if padded == nil || bare == nil {
		t.Fatal("the invoice number pair 0004711 / 4711 is missing")
	}
	if padded.Key() == bare.Key() {
		t.Fatal("the pair must be two distinct natural keys")
	}
	if model.ContentHash(*padded) == model.ContentHash(*bare) {
		t.Error("the two invoices must differ in content, or merging them would be harmless")
	}
}

func TestPinnedGradedAmounts(t *testing.T) {
	// The four published conversions. Changing any of them changes a published
	// expected amount, so this test is a contract and not a unit test.
	tests := []struct {
		invoice  string
		currency string
		gross    string
		rate     string
		factor   int64
		wantCHF  string
	}{
		{"0004711", "EUR", "1345.63", "0.931000", 1, "1252.78"},
		{"0004713", "GBP", "100.00", "1.082250", 1, "108.23"},
		{"0004714", "JPY", "250000.00", "0.556300", 100, "1390.75"},
		{"0004715", "GBP", "-100.00", "1.082250", 1, "-108.23"},
	}
	for _, scenario := range []string{ScenarioS0, ScenarioS1} {
		d := generated(t, scenario)
		rates := map[string][]model.FxRate{}
		for _, r := range d.ResolvedDailyRates() {
			rates[r.Base] = append(rates[r.Base], r)
		}
		for _, tc := range tests {
			var inv *model.APInvoice
			for i := range d.Invoices {
				if d.Invoices[i].SupplierInvoiceNumber == tc.invoice &&
					d.Invoices[i].SupplierNumber == CollisionSupplierCanonical {
					inv = &d.Invoices[i]
				}
			}
			if inv == nil {
				t.Fatalf("%s: pinned invoice %s is missing", scenario, tc.invoice)
			}
			if inv.Currency != tc.currency {
				t.Errorf("%s: invoice %s is in %s, want %s", scenario, tc.invoice, inv.Currency, tc.currency)
			}
			if inv.GrossAmount.String() != tc.gross {
				t.Errorf("%s: invoice %s gross is %s, want %s", scenario, tc.invoice, inv.GrossAmount.String(), tc.gross)
			}
			row, ok, err := winningRate(rates[inv.Currency], inv.DocumentDate)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("%s: no rate covers %s on %s", scenario, inv.Currency, inv.DocumentDate)
			}
			if row.Rate.String() != tc.rate || row.RateFactor != tc.factor {
				t.Errorf("%s: invoice %s resolves to rate %s factor %d, want %s / %d",
					scenario, tc.invoice, row.Rate.String(), row.RateFactor, tc.rate, tc.factor)
			}
			got, err := inv.GrossAmount.MulRate(row.Rate, row.RateFactor, 2)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.wantCHF {
				t.Errorf("%s: invoice %s converts to %s CHF, want %s",
					scenario, tc.invoice, got.String(), tc.wantCHF)
			}
		}
	}
}

func TestTrapsSurviveEverySeed(t *testing.T) {
	// The planted traps are structural, not statistical: they must be present at
	// every seed, not merely at the one the other tests share. This runs over the
	// smoke scenario, which is cheap, and asserts the whole checklist.
	for _, seed := range []int64{0, 1, -1, 20260329, 987654321} {
		d := mustGenerate(t, ScenarioS0, seed)
		if err := d.Validate(); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		numbers := map[string]bool{}
		blockedCreditors := 0
		for i := range d.Suppliers {
			numbers[d.Suppliers[i].SupplierNumber] = true
			if d.Suppliers[i].Blocked {
				blockedCreditors++
			}
		}
		if !numbers[CollisionSupplierCanonical] || !numbers[CollisionSupplierShort] {
			t.Errorf("seed %d: the supplier collision pair is missing", seed)
		}
		if blockedCreditors == 0 {
			t.Errorf("seed %d: no blocked creditor", seed)
		}
		codes := map[string]bool{}
		for _, cc := range d.CostCenters {
			codes[cc.Code] = true
		}
		if !codes["0815"] || !codes["815"] || codes[AbsentCostCenter] {
			t.Errorf("seed %d: the cost center leading-zero exception is broken", seed)
		}
		units := map[string]bool{}
		for _, l := range d.PurchaseOrderLines {
			units[l.UoM] = true
		}
		if !units[UoMPallet] || !units[UoMTon] || !units["CTN"] {
			t.Errorf("seed %d: the unit-of-measure mix is incomplete", seed)
		}
		dst, dkk := 0, 0
		for _, r := range d.FxRows {
			if r.Base == "EUR" && r.ValidFromDate == DSTSwitchDate {
				dst++
			}
			if r.Base == "DKK" && r.Status != model.FxStatusDeleted {
				dkk++
			}
		}
		if dst < 2 {
			t.Errorf("seed %d: %d rows on the summer time switch, want the winner and its superseded row", seed, dst)
		}
		if dkk != 0 {
			t.Errorf("seed %d: %d active DKK rows", seed, dkk)
		}
		mandanten := map[string]bool{}
		for _, del := range d.Deliveries {
			mandanten[del.Mandant] = true
		}
		if !mandanten[MandantCH10] || !mandanten[MandantCH20] {
			t.Errorf("seed %d: both Mandanten must be delivered, got %v", seed, mandanten)
		}
		byCode := map[string]int{}
		for _, e := range d.ExpectedExceptions {
			byCode[e.Code]++
		}
		for _, code := range []string{
			ExcPONotFound, ExcPOClosed, ExcPOSupplierMismatch, ExcSupplierBlocked,
			ExcTotalsMismatch, ExcCostCenterUnknown, ExcFxRateMissing,
			ExcUoMUnmappable, ExcUoMUnconvertible, CodeAckRejected,
		} {
			if byCode[code] == 0 {
				t.Errorf("seed %d: no invoice carries %s", seed, code)
			}
		}
		// One per class, plus the two extra rate-gap invoices the FX classes
		// produce. The exact number matters: an expected set whose size moves
		// with the seed is not an expected set.
		if len(d.ExpectedExceptions) != 14 {
			t.Errorf("seed %d: %d expected exceptions, want 14: %v", seed, len(d.ExpectedExceptions), byCode)
		}
	}
}

// TestExclusiveWatermarkLosesExactlyOneRecord pins the record that makes the
// watermark rule testable.
//
// The ERP filters change_seq > changed_since, so a client sends the watermark it
// persisted and a client with an off-by-one sends watermark + 1 and loses the
// record at that sequence. That only costs something if such a record exists: the
// delta's sequences all sit above the GLOBAL base maximum, so unless the dataset
// that owns that boundary is also the one whose delta starts there, watermark + 1
// names a gap and the mistake is free. It was free until this test existed.
func TestExclusiveWatermarkLosesExactlyOneRecord(t *testing.T) {
	d := generated(t, ScenarioS2)
	baseMax := map[string]int64{}
	deltaMin := map[string]int64{}
	for _, e := range d.ChangeSeqs {
		if e.ChangeSeq <= d.DeltaFromChangeSeq {
			if e.ChangeSeq > baseMax[e.Dataset] {
				baseMax[e.Dataset] = e.ChangeSeq
			}
			continue
		}
		if cur, ok := deltaMin[e.Dataset]; !ok || e.ChangeSeq < cur {
			deltaMin[e.Dataset] = e.ChangeSeq
		}
	}
	adjacent := ""
	for ds, min := range deltaMin {
		if baseMax[ds]+1 == min {
			adjacent = ds
			break
		}
	}
	if adjacent == "" {
		t.Fatalf("no dataset's delta begins at its own watermark + 1, so an exclusive "+
			"watermark loses nothing and the rule is untested.\n base maxima %v\n delta minima %v",
			baseMax, deltaMin)
	}
	t.Logf("dataset %s: base watermark %d, first changed record %d",
		adjacent, baseMax[adjacent], deltaMin[adjacent])

	// And exactly one record sits there, so the defect costs one record and not a
	// whole page: a trap that loses everything is a different trap.
	at := 0
	for _, e := range d.ChangeSeqs {
		if e.Dataset == adjacent && e.ChangeSeq == baseMax[adjacent]+1 {
			at++
		}
	}
	if at != 1 {
		t.Errorf("%d records sit at the boundary sequence, want exactly 1", at)
	}
}
