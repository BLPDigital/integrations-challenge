package seed

import (
	"bytes"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// fingerprint reduces a dataset to the bytes that must be reproducible: the
// canonical JSON of every record collection plus the raw legacy bytes.
func fingerprint(t *testing.T, d *Dataset) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, part := range []any{
		d.Suppliers, d.CostCenters, d.PurchaseOrders, d.PurchaseOrderLines,
		d.FxRows, d.FxGaps, d.UoMConversions, d.ChangeSeqs, d.Invoices,
		d.AmendedInvoices, d.ExpectedExceptions,
	} {
		b, err := model.CanonicalJSON(part)
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	for _, del := range d.Deliveries {
		buf.WriteString(del.Name)
		buf.WriteByte('\n')
		buf.Write(del.Bytes)
	}
	return buf.Bytes()
}

func TestGenerateIsByteIdentical(t *testing.T) {
	for _, scenario := range Scenarios() {
		t.Run(scenario, func(t *testing.T) {
			if scenario == ScenarioX2 && testing.Short() {
				t.Skip("the scale scenario is large; -short covers the others")
			}
			const seed = 20260416
			a, err := Generate(scenario, seed)
			if err != nil {
				t.Fatal(err)
			}
			b, err := Generate(scenario, seed)
			if err != nil {
				t.Fatal(err)
			}
			fa, fb := fingerprint(t, a), fingerprint(t, b)
			if !bytes.Equal(fa, fb) {
				t.Fatalf("two generations of %s at the same seed differ (%d vs %d bytes)",
					scenario, len(fa), len(fb))
			}
			for i := range a.Deliveries {
				if !bytes.Equal(a.Deliveries[i].Bytes, b.Deliveries[i].Bytes) {
					t.Errorf("delivery %s is not byte-identical across generations", a.Deliveries[i].Name)
				}
			}
		})
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	for _, scenario := range []string{ScenarioS0, ScenarioS1} {
		t.Run(scenario, func(t *testing.T) {
			a, err := Generate(scenario, 1)
			if err != nil {
				t.Fatal(err)
			}
			b, err := Generate(scenario, 2)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(fingerprint(t, a), fingerprint(t, b)) {
				t.Fatal("two seeds produced the same dataset")
			}
			if bytes.Equal(a.LegacyFile, b.LegacyFile) {
				t.Fatal("two seeds produced the same legacy file bytes")
			}
			// The sizes are a property of the scenario, not of the seed.
			if len(a.Suppliers) != len(b.Suppliers) || len(a.FxRows) != len(b.FxRows) ||
				len(a.Invoices) != len(b.Invoices) {
				t.Fatal("the seed changed a published dataset size")
			}
		})
	}
}

func TestDeltaBaseDeliveryMatchesItsBaseScenario(t *testing.T) {
	// A delta scenario re-runs its base scenario's phases with the same seed and
	// the same code path, so its first delivery must be the base scenario's
	// delivery, byte for byte. That is what makes a re-delivered file a genuine
	// re-delivery rather than a lookalike.
	const seed = 991
	base, err := Generate(ScenarioS1, seed)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := Generate(ScenarioS2, seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Deliveries) <= len(base.Deliveries) {
		t.Fatalf("the delta scenario has %d deliveries, the base has %d",
			len(delta.Deliveries), len(base.Deliveries))
	}
	for i := range base.Deliveries {
		if base.Deliveries[i].Name != delta.Deliveries[i].Name {
			t.Fatalf("delivery %d: %q vs %q", i, base.Deliveries[i].Name, delta.Deliveries[i].Name)
		}
		if !bytes.Equal(base.Deliveries[i].Bytes, delta.Deliveries[i].Bytes) {
			t.Errorf("delivery %s differs between %s and %s", base.Deliveries[i].Name, ScenarioS1, ScenarioS2)
		}
	}
	if !bytes.Equal(base.LegacyFile, delta.LegacyFile) {
		t.Error("the primary legacy file differs between the base and the delta scenario")
	}
}

func TestNoScenarioDependsOnMapOrder(t *testing.T) {
	// Every collection that leaves the package is explicitly ordered. Generating
	// the same scenario many times in one process would surface a map-order
	// dependency, because Go randomizes map iteration per range statement.
	want := fingerprint(t, mustGenerate(t, ScenarioS0, 5))
	for i := 0; i < 12; i++ {
		if got := fingerprint(t, mustGenerate(t, ScenarioS0, 5)); !bytes.Equal(got, want) {
			t.Fatalf("generation %d differed: a map is being ranged over on an output path", i)
		}
	}
}

// testSeed is the seed the property tests share, so the generator runs once per
// scenario for the whole package. The tests that are ABOUT the seed - byte
// identity, seed independence, map-order independence - call [mustGenerate] or
// [Generate] directly with their own seeds instead.
const testSeed = 20260416

// genCache memoizes one dataset per scenario at [testSeed]. Generating the scale
// scenario costs over a second, and a dozen property tests do not each need their
// own copy. The datasets it hands out are read-only by convention: no test in
// this package mutates one.
var genCache = map[string]*Dataset{}

// generated returns the shared dataset of a scenario.
func generated(t *testing.T, scenario string) *Dataset {
	t.Helper()
	if d, ok := genCache[scenario]; ok {
		return d
	}
	d := mustGenerate(t, scenario, testSeed)
	genCache[scenario] = d
	return d
}

func mustGenerate(t *testing.T, scenario string, seed int64) *Dataset {
	t.Helper()
	d, err := Generate(scenario, seed)
	if err != nil {
		t.Fatalf("Generate(%s, %d): %v", scenario, seed, err)
	}
	return d
}
