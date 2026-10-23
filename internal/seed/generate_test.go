package seed

import (
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestGenerateRejectsUnknownScenario(t *testing.T) {
	if _, err := Generate("nope", 1); err == nil {
		t.Fatal("an unknown scenario was accepted")
	}
	if _, err := SpecOf(""); err == nil {
		t.Fatal("an empty scenario was accepted")
	}
}

func TestPublishedSizesAreExact(t *testing.T) {
	// These numbers are published. A generated dataset that misses one of them by
	// a single record is a changed contract, not a rounding difference.
	tests := []struct {
		scenario                                                         string
		suppliers, costCenters, pos, poLines, fxRows, invoices, invLines int
	}{
		{ScenarioS0, 240, 60, 160, 520, 120, 100, 300},
		{ScenarioS1, 12000, 1400, 8000, 26400, 600, 5000, 14800},
		{ScenarioS2, 12000, 1400, 8000, 26400, 660, 6100, 18100},
		{ScenarioX2, 36000, 4200, 24000, 79200, 600, 15000, 44400},
	}
	for _, tc := range tests {
		t.Run(tc.scenario, func(t *testing.T) {
			d := generated(t, tc.scenario)
			got := []struct {
				name       string
				have, want int
			}{
				{"suppliers", len(d.Suppliers), tc.suppliers},
				{"cost centers", len(d.CostCenters), tc.costCenters},
				{"purchase orders", len(d.PurchaseOrders), tc.pos},
				{"purchase order lines", len(d.PurchaseOrderLines), tc.poLines},
				{"fx rows", len(d.FxRows), tc.fxRows},
				{"distinct invoices", len(d.Invoices), tc.invoices},
				{"invoice lines", d.InvoiceLineCount(), tc.invLines},
			}
			for _, g := range got {
				if g.have != g.want {
					t.Errorf("%s: got %d, want %d", g.name, g.have, g.want)
				}
			}
		})
	}
}

func TestScenarioSizesAreSeedIndependent(t *testing.T) {
	for _, seed := range []int64{0, 1, 7, 20260329, -5} {
		d := mustGenerate(t, ScenarioS0, seed)
		if len(d.Suppliers) != 240 || len(d.FxRows) != 120 || len(d.Invoices) != 100 {
			t.Fatalf("seed %d changed a published size: %v", seed, d.Counts())
		}
	}
}

func TestChangeSequencesAreStrictlyIncreasingAndComplete(t *testing.T) {
	for _, scenario := range Scenarios() {
		t.Run(scenario, func(t *testing.T) {
			d := generated(t, scenario)
			want := len(d.Suppliers) + len(d.PurchaseOrders) + len(d.PurchaseOrderLines)
			if len(d.ChangeSeqs) != want {
				t.Fatalf("got %d change sequences for %d master records", len(d.ChangeSeqs), want)
			}
			seen := map[int64]bool{}
			var prev int64
			for i, e := range d.ChangeSeqs {
				if e.ChangeSeq <= prev {
					t.Fatalf("entry %d has change sequence %d after %d", i, e.ChangeSeq, prev)
				}
				if seen[e.ChangeSeq] {
					t.Fatalf("change sequence %d is not unique", e.ChangeSeq)
				}
				seen[e.ChangeSeq] = true
				prev = e.ChangeSeq
				if !model.IsKnownDataset(e.Dataset) {
					t.Fatalf("entry %d names unknown dataset %q", i, e.Dataset)
				}
			}
			if d.MaxChangeSeq != prev {
				t.Errorf("MaxChangeSeq is %d, the highest assigned is %d", d.MaxChangeSeq, prev)
			}
			// Every record carries the sequence it was assigned.
			byKey := map[string]int64{}
			for _, e := range d.ChangeSeqs {
				byKey[e.Dataset+"|"+e.Key] = e.ChangeSeq
			}
			for i := range d.Suppliers {
				if got := byKey[model.DatasetSupplier.String()+"|"+d.Suppliers[i].Key()]; got != d.Suppliers[i].ChangeSeq {
					t.Fatalf("supplier %q carries %d, the index says %d",
						d.Suppliers[i].SupplierNumber, d.Suppliers[i].ChangeSeq, got)
				}
			}
		})
	}
}

func TestSomeRecordsAreUpdatedOutOfNaturalKeyOrder(t *testing.T) {
	// A connector that persists "the last key I saw" instead of the highest
	// change sequence has to lose records, and that only bites if the data really
	// contains a record whose change sequence disagrees with its key order.
	for _, scenario := range Scenarios() {
		d := generated(t, scenario)
		monotone := true
		var prev int64
		for i := range d.Suppliers {
			if d.Suppliers[i].ChangeSeq < prev {
				monotone = false
				break
			}
			prev = d.Suppliers[i].ChangeSeq
		}
		if monotone {
			t.Errorf("%s: supplier change sequences follow key order exactly, so no out-of-order update was planted",
				scenario)
		}
	}
}

func TestDeltaSeparatesItselfByWatermark(t *testing.T) {
	d := generated(t, ScenarioS2)
	if d.DeltaFromChangeSeq == 0 {
		t.Fatal("the delta scenario carries no base watermark")
	}
	spec, err := SpecOf(ScenarioS2)
	if err != nil {
		t.Fatal(err)
	}
	above := 0
	for _, e := range d.ChangeSeqs {
		if e.ChangeSeq > d.DeltaFromChangeSeq {
			above++
		}
	}
	want := spec.ChangedSuppliers + spec.ChangedPurchaseOrders + spec.ChangedPurchaseOrderLines
	if above != want {
		t.Errorf("%d records sit above the watermark, want %d", above, want)
	}
	if d.MaxChangeSeq <= d.DeltaFromChangeSeq {
		t.Error("the delta did not advance the watermark")
	}
}

func TestColdLoadScenariosHaveNoWatermark(t *testing.T) {
	for _, scenario := range []string{ScenarioS0, ScenarioS1, ScenarioX2} {
		d := generated(t, scenario)
		if d.DeltaFromChangeSeq != 0 {
			t.Errorf("%s is a cold load but carries watermark %d", scenario, d.DeltaFromChangeSeq)
		}
	}
}

func TestDatasetValidates(t *testing.T) {
	for _, scenario := range Scenarios() {
		d := generated(t, scenario)
		if err := d.Validate(); err != nil {
			t.Errorf("%s: %v", scenario, err)
		}
	}
}
