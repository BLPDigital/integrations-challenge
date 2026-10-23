package seed

import (
	"sort"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestExpectedExceptionsAreSortedAndPublished(t *testing.T) {
	published := map[string]bool{}
	for _, c := range ExceptionCodes() {
		published[c] = true
	}
	for _, scenario := range Scenarios() {
		t.Run(scenario, func(t *testing.T) {
			d := generated(t, scenario)
			seen := map[string]bool{}
			for i, e := range d.ExpectedExceptions {
				if !published[e.Code] {
					t.Fatalf("entry %d carries unpublished code %q", i, e.Code)
				}
				if e.SubjectType != SubjectTypeInvoice {
					t.Fatalf("entry %d has subject type %q", i, e.SubjectType)
				}
				if e.SubjectKey == "" {
					t.Fatalf("entry %d has no subject key", i)
				}
				k := e.SubjectKey + "|" + e.Code
				if seen[k] {
					t.Fatalf("entry %d duplicates (%q, %q)", i, e.SubjectKey, e.Code)
				}
				seen[k] = true
				if i > 0 {
					prev := d.ExpectedExceptions[i-1]
					if prev.SubjectKey > e.SubjectKey ||
						(prev.SubjectKey == e.SubjectKey && prev.Code >= e.Code) {
						t.Fatalf("entry %d is out of order after %v", i, prev)
					}
				}
			}
		})
	}
}

func TestExceptionMixMatchesThePlantedPlan(t *testing.T) {
	// Every class must be present, and the total must be the published share of
	// the invoices. Under-planting silently weakens a graded assertion; over-
	// planting means an invoice became an exception by accident.
	tests := []struct {
		scenario string
		want     int
	}{
		{ScenarioS0, 14},
		{ScenarioS1, 700},
		{ScenarioS2, 700 + 154 + 12},
		{ScenarioX2, 2100},
	}
	for _, tc := range tests {
		t.Run(tc.scenario, func(t *testing.T) {
			d := generated(t, tc.scenario)
			if len(d.ExpectedExceptions) != tc.want {
				byCode := map[string]int{}
				for _, e := range d.ExpectedExceptions {
					byCode[e.Code]++
				}
				t.Fatalf("got %d expected exceptions, want %d (%v)", len(d.ExpectedExceptions), tc.want, byCode)
			}
			byCode := map[string]int{}
			for _, e := range d.ExpectedExceptions {
				byCode[e.Code]++
			}
			mustHave := []string{
				ExcPONotFound, ExcPOClosed, ExcPOSupplierMismatch, ExcSupplierBlocked,
				ExcTotalsMismatch, ExcCostCenterUnknown, ExcFxRateMissing,
				ExcUoMUnmappable, ExcUoMUnconvertible,
			}
			for _, code := range mustHave {
				if byCode[code] == 0 {
					t.Errorf("no invoice carries %s", code)
				}
			}
			if tc.scenario == ScenarioS2 && byCode[ExcDuplicateInvoiceAmend] != 12 {
				t.Errorf("%s carries %d amended-duplicate exceptions, want 12",
					tc.scenario, byCode[ExcDuplicateInvoiceAmend])
			}
			if tc.scenario != ScenarioS2 && byCode[ExcDuplicateInvoiceAmend] != 0 {
				t.Errorf("%s carries an amended-duplicate exception but re-delivers nothing", tc.scenario)
			}
		})
	}
}

func TestExceptionShareIsAboutFourteenPercent(t *testing.T) {
	for _, scenario := range []string{ScenarioS0, ScenarioS1} {
		d := generated(t, scenario)
		spec, err := SpecOf(scenario)
		if err != nil {
			t.Fatal(err)
		}
		perMille := len(d.ExpectedExceptions) * 1000 / len(d.Invoices)
		if perMille < spec.ExceptionPerMille-5 || perMille > spec.ExceptionPerMille+5 {
			t.Errorf("%s: %d per mille of the invoices are exceptions, want about %d",
				scenario, perMille, spec.ExceptionPerMille)
		}
	}
}

func TestDeriveExceptionsIsIdempotent(t *testing.T) {
	// The golden set is derived from the data, not recorded while planting, so
	// deriving it again must return the same set.
	d := generated(t, ScenarioS1)
	again, err := d.DeriveExceptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(d.ExpectedExceptions) {
		t.Fatalf("re-derivation gave %d entries, the dataset carries %d", len(again), len(d.ExpectedExceptions))
	}
	for i := range again {
		if again[i] != d.ExpectedExceptions[i] {
			t.Fatalf("entry %d differs: %v vs %v", i, again[i], d.ExpectedExceptions[i])
		}
	}
}

func TestEveryExceptionSubjectIsADeliveredInvoice(t *testing.T) {
	d := generated(t, ScenarioS2)
	keys := map[string]bool{}
	for _, inv := range d.Invoices {
		keys[inv.Key()] = true
	}
	for _, e := range d.ExpectedExceptions {
		if !keys[e.SubjectKey] {
			t.Errorf("exception %v names a subject that is not a delivered invoice", e)
		}
	}
}

func TestCleanInvoicesReallyAreClean(t *testing.T) {
	// The complement of the golden set must be postable end to end: an existing
	// unblocked creditor, a resolvable open purchase order, exact totals, a valid
	// cost center, a usable rate and convertible units. If a "clean" invoice were
	// not clean, the exception set would be bigger than the plan and the mix test
	// would already have failed; this test names the reason instead of the count.
	d := generated(t, ScenarioS1)
	bad := map[string]bool{}
	for _, e := range d.ExpectedExceptions {
		bad[e.SubjectKey] = true
	}
	suppliers := map[string]Supplier{}
	for i := range d.Suppliers {
		suppliers[d.Suppliers[i].SupplierNumber] = d.Suppliers[i]
	}
	pos := map[string]model.PurchaseOrder{}
	for i := range d.PurchaseOrders {
		pos[CanonicalPONumber(d.PurchaseOrders[i].PONumber)] = d.PurchaseOrders[i]
	}
	ccs := map[string]model.CostCenter{}
	for i := range d.CostCenters {
		ccs[d.CostCenters[i].Code] = d.CostCenters[i]
	}
	rates := map[string][]model.FxRate{}
	for _, r := range d.ResolvedDailyRates() {
		rates[r.Base] = append(rates[r.Base], r)
	}
	checked := 0
	for _, inv := range d.Invoices {
		if bad[inv.Key()] {
			continue
		}
		checked++
		s, ok := suppliers[inv.SupplierNumber]
		if !ok || s.Blocked {
			t.Fatalf("clean invoice %q has creditor %q (found=%v blocked=%v)",
				inv.Key(), inv.SupplierNumber, ok, s.Blocked)
		}
		code := inv.CostCenter
		if inv.PONumber != "" {
			num, _ := SplitPOReference(inv.PONumber)
			po, ok := pos[CanonicalPONumber(num)]
			if !ok {
				t.Fatalf("clean invoice %q refers to unknown purchase order %q", inv.Key(), inv.PONumber)
			}
			if po.SupplierNumber != inv.SupplierNumber {
				t.Fatalf("clean invoice %q refers to a purchase order of another creditor", inv.Key())
			}
			if po.Status != model.POStatusOpen {
				t.Fatalf("clean invoice %q refers to a %s purchase order", inv.Key(), po.Status)
			}
			if code == "" {
				code = po.CostCenter
			}
		}
		cc, ok := ccs[code]
		if !ok || cc.Blocked || cc.CompanyCode != inv.CompanyCode {
			t.Fatalf("clean invoice %q resolves to cost center %q (found=%v)", inv.Key(), code, ok)
		}
		if inv.Currency != QuoteCurrency {
			if inv.CompanyCode == model.CompanyCodeCH20 {
				t.Fatalf("clean invoice %q is a foreign currency CH20 document", inv.Key())
			}
			if _, ok, err := winningRate(rates[inv.Currency], inv.DocumentDate); err != nil {
				t.Fatal(err)
			} else if !ok {
				t.Fatalf("clean invoice %q has no rate for %s on %s", inv.Key(), inv.Currency, inv.DocumentDate)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no clean invoice was checked")
	}
}

func TestExceptionCodesAreSortedAndClosed(t *testing.T) {
	got := ExceptionCodes()
	want := append([]string(nil), got...)
	sort.Strings(want)
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ExceptionCodes is not sorted at %d: %q", i, got[i])
		}
	}
	if len(got) != 12 {
		t.Errorf("the published exception set has %d codes, want 12", len(got))
	}
}
