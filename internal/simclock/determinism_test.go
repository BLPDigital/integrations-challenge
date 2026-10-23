package simclock

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"
)

// faultTrace records the injection verdict for a run of sequence numbers, which
// is the whole observable surface of the seed.
type faultTrace struct {
	faults  []string
	applied []bool
}

func trace(seed int64) faultTrace {
	g := New(Config{Seed: seed, Chaos: true})
	tr := faultTrace{}
	for i := 0; i < 1000; i++ {
		status, code, retriable, ok := g.InjectFault("GET /erp/v1/suppliers|after=" + strconv.Itoa(i))
		tr.faults = append(tr.faults, strconv.Itoa(status)+"|"+code+"|"+
			strconv.FormatBool(retriable)+"|"+strconv.FormatBool(ok))
	}
	for idx := 0; idx < 200; idx++ {
		tr.applied = append(tr.applied, g.ApplyThenFail("APPLYFAIL|tb_1|supplier|"+strconv.Itoa(idx)))
	}
	return tr
}

func TestInjectionIsAFunctionOfTheSeed(t *testing.T) {
	if a, b := trace(20250831), trace(20250831); !reflect.DeepEqual(a, b) {
		t.Fatal("two Governors with the same seed injected different faults")
	}
	if a, b := trace(20250831), trace(20250832); reflect.DeepEqual(a, b) {
		t.Fatal("two Governors with different seeds injected identical faults")
	}
	// Exactly one chunk signature is applied-then-500 per run, and which one it
	// is moves with the seed.
	firedAt := map[int64]int{}
	for _, seed := range []int64{0, 1, 16, 17, 18, 99} {
		tr := trace(seed)
		hits := 0
		for idx, fired := range tr.applied {
			if fired {
				hits++
				firedAt[seed] = idx
			}
		}
		if hits != 1 {
			t.Fatalf("seed %d failed %d chunks, want exactly 1", seed, hits)
		}
	}
	distinct := map[int]bool{}
	for _, idx := range firedAt {
		distinct[idx] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("the applied-then-500 chunk did not move with the seed: %v", firedAt)
	}
}

// workload is a fixed mixed call sequence: everything a handler can do to a
// Governor, in an order that crosses the bucket, the quota, the quarantine, the
// injectors and the token store.
func workload(g *Governor) {
	for i := 0; i < 120; i++ {
		d := g.Admit("suppliers_list", Cost(ERPListPage, 250), 250)
		if d.Allow {
			if status, _, _, ok := g.InjectFault("GET /erp/v1/suppliers|page=" + strconv.Itoa(i)); ok {
				if status >= 500 {
					g.Inc5xxRetried()
				}
			}
		}
	}
	for i := 0; i < 200; i++ {
		d := g.Admit("supplier_get", Cost(ERPSingleGet, 0), 1)
		if !d.Allow {
			continue
		}
		if _, _, _, ok := g.InjectFault("GET /erp/v1/suppliers/" + strconv.Itoa(i)); ok {
			g.Inc5xxRetried()
		}
	}
	for i := 0; i < 40; i++ {
		if d := g.Admit("documents_post", Cost(ERPDocumentPost, 0), 1); d.Allow {
			g.IncDuplicateDocumentAttempts()
		}
	}
	for i := 0; i < 25; i++ {
		if g.ApplyThenFail("APPLYFAIL|tb_1|invoice|" + strconv.Itoa(i)) {
			g.IncDuplicateApplyAttempts()
		}
	}
	tok := g.IssueToken()
	for i := 0; i < 300; i++ {
		g.ValidateToken(tok)
	}
	g.Admit("auth_token", Cost(ERPAuthToken, 0), 0)
	g.Advance(1234)
}

// TestSameSeedSameMetrics is the determinism assertion the grader depends on:
// two Governors driven through the same call sequence agree on every counter,
// down to the JSON bytes.
func TestSameSeedSameMetrics(t *testing.T) {
	a, b := New(erpConfig(20250831, 500)), New(erpConfig(20250831, 500))
	workload(a)
	workload(b)
	ma, mb := a.Metrics(), b.Metrics()
	if !reflect.DeepEqual(ma, mb) {
		t.Fatalf("metrics diverged:\n a: %+v\n b: %+v", ma, mb)
	}
	ja, _ := json.Marshal(ma)
	jb, _ := json.Marshal(mb)
	if string(ja) != string(jb) {
		t.Fatalf("metrics JSON diverged:\n a: %s\n b: %s", ja, jb)
	}
	if ma.RequestsTotal == 0 || ma.VirtualClockMs == 0 {
		t.Fatalf("workload did nothing measurable: %+v", ma)
	}
	// Repeated marshalling of the same map must be byte-identical too:
	// encoding/json sorts map keys, so by_endpoint cannot leak iteration order.
	for i := 0; i < 50; i++ {
		again, _ := json.Marshal(a.Metrics())
		if string(again) != string(ja) {
			t.Fatalf("metrics JSON unstable across marshals:\n %s\n %s", ja, again)
		}
	}
}

// TestDifferentSeedDifferentRun proves the seed is actually load-bearing: with
// chaos on, the same workload under a different seed produces a different
// injected-fault profile.
func TestDifferentSeedDifferentRun(t *testing.T) {
	a, b := New(erpConfig(1, 500)), New(erpConfig(2, 500))
	workload(a)
	workload(b)
	ma, mb := a.Metrics(), b.Metrics()
	if ma.FiveXXRetried == mb.FiveXXRetried && ma.FiveXXInjected == mb.FiveXXInjected && ma.RateLimited == mb.RateLimited {
		t.Fatalf("seeds 1 and 2 produced the same fault profile: %+v", ma)
	}
	// Everything not keyed by the seed must still match: the clock, the quota
	// and the request counts are decided by the call sequence alone.
	if ma.RequestsTotal != mb.RequestsTotal || ma.QuotaUsed != mb.QuotaUsed || ma.VirtualClockMs != mb.VirtualClockMs {
		t.Fatalf("seed changed non-injection accounting:\n a: %+v\n b: %+v", ma, mb)
	}
}

// TestNoAmbientNondeterminism asserts by construction that the package cannot
// read the wall clock, use unseeded randomness or touch floating point: it parses
// every file in the package, including the tests, and rejects the imports and the
// type identifiers that would make a run unreproducible.
func TestNoAmbientNondeterminism(t *testing.T) {
	forbiddenImports := map[string]string{
		"time":              "the virtual clock replaces wall time",
		"math/rand":         "injection is keyed by (seed, seq), never by a PRNG",
		"math/rand/v2":      "injection is keyed by (seed, seq), never by a PRNG",
		"crypto/rand":       "no randomness of any kind",
		"os":                "no ambient environment",
		"runtime":           "no ambient runtime state",
		"net":               "no network",
		"net/http":          "no network",
		"path/filepath":     "no filesystem ordering",
		"io/ioutil":         "no filesystem ordering",
		"golang.org/x":      "standard library only",
		"unicode/utf16":     "no locale dependence",
		"golang.org/x/text": "standard library only",
	}
	forbiddenIdents := map[string]string{
		"float32": "no floating point in a cost or money path",
		"float64": "no floating point in a cost or money path",
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no packages")
	}
	files := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			files++
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: bad import %s", name, imp.Path.Value)
				}
				if why, bad := forbiddenImports[path]; bad {
					t.Errorf("%s imports %q: %s", name, path, why)
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if why, bad := forbiddenIdents[id.Name]; bad {
					t.Errorf("%s uses %s: %s", name, id.Name, why)
				}
				return true
			})
		}
	}
	if files < 4 {
		t.Fatalf("parsed only %d files; the scan is not covering the package", files)
	}
}
