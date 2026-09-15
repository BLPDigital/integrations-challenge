package importer

import (
	"strings"
	"testing"
)

// sampleResult builds a Result to project.
func sampleResult(mutate func(*Result)) *Result {
	b := NewBuilder("blp-csv-v1", Options{Filename: "suppliers.csv", DeclaredRecords: 2})
	b.SetSourceBytes([]byte("whatever"))
	b.AddDocument(Document{Dataset: "supplier", Key: "0000418", Line: 3, Payload: []byte(`{"name":"B","supplier_number":"0000418"}`)})
	b.AddDocument(Document{Dataset: "supplier", Key: "0000417", Line: 2, Payload: []byte(`{"name":"A","supplier_number":"0000417"}`)})
	b.Add(Diagnostic{Code: CodeAmbiguousFieldSemantics, Severity: SeverityWarn, Line: 2, Record: 1, Field: "discount_raw", Message: "m"})
	t := b.Totals()
	t.ComputedDocuments = 2
	t.TrailerVerified = true
	b.SetTotals(t)
	r := b.Result()
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestProjectionIsCanonicalAndStable(t *testing.T) {
	a, err := Projection(sampleResult(nil))
	if err != nil {
		t.Fatal(err)
	}
	bb, err := Projection(sampleResult(nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(bb) {
		t.Fatalf("two projections of equal Results differ:\n%s\n%s", a, bb)
	}
	// Documents sort by (dataset, key), not by delivery order.
	if i, j := strings.Index(string(a), "0000417"), strings.Index(string(a), "0000418"); i > j {
		t.Errorf("documents are not sorted by key:\n%s", a)
	}
}

func TestProjectionExcludesProvenance(t *testing.T) {
	// Renaming a fixture, or moving a record to a different line, must not
	// invalidate an expectation: what the record IS is graded, where it was
	// found is not.
	base, err := Projection(sampleResult(nil))
	if err != nil {
		t.Fatal(err)
	}
	moved, err := Projection(sampleResult(func(r *Result) {
		r.File.Name = "renamed.csv"
		r.File.SHA256 = "0000"
		r.File.Lines = 999
		for i := range r.Documents {
			r.Documents[i].Line += 100
			r.Documents[i].Ordinal += 100
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if string(base) != string(moved) {
		t.Fatalf("the projection is sensitive to provenance:\n%s", GoldenDiff(moved, base))
	}
	for _, absent := range []string{"suppliers.csv", "sha256", "ordinal"} {
		if strings.Contains(string(base), absent) {
			t.Errorf("the projection carries %q, which is provenance and not content", absent)
		}
	}
}

func TestProjectionKeepsDiagnosticMultiplicity(t *testing.T) {
	twice, err := Projection(sampleResult(func(r *Result) {
		r.Diagnostics = append(r.Diagnostics, r.Diagnostics[0])
	}))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(twice), CodeAmbiguousFieldSemantics); n != 2 {
		t.Fatalf("the projection collapsed a repeated finding: found %d occurrences, want 2", n)
	}
}

func TestGoldenDiffEmptyWhenEqual(t *testing.T) {
	a, _ := Projection(sampleResult(nil))
	b, _ := Projection(sampleResult(nil))
	if d := GoldenDiff(a, b); d != "" {
		t.Fatalf("GoldenDiff of equal projections = %q", d)
	}
}

func TestGoldenDiffNamesTheDocumentTheFieldAndBothValues(t *testing.T) {
	want, _ := Projection(sampleResult(nil))
	got, _ := Projection(sampleResult(func(r *Result) {
		r.Documents[1].Payload = []byte(`{"name":"WRONG","supplier_number":"0000417"}`)
	}))
	d := GoldenDiff(got, want)
	for _, must := range []string{"supplier/0000417", "name", "WRONG", `"A"`} {
		if !strings.Contains(d, must) {
			t.Errorf("the diff does not mention %q:\n%s", must, d)
		}
	}
}

func TestGoldenDiffDistinguishesMissingFromUnexpected(t *testing.T) {
	full, _ := Projection(sampleResult(nil))
	short, _ := Projection(sampleResult(func(r *Result) {
		r.Documents = r.Documents[:1]
	}))
	if d := GoldenDiff(short, full); !strings.Contains(d, "missing document supplier/0000417") {
		t.Errorf("a document that should exist and does not is not reported as missing:\n%s", d)
	}
	if d := GoldenDiff(full, short); !strings.Contains(d, "unexpected document supplier/0000417") {
		t.Errorf("a document that should not exist is not reported as unexpected:\n%s", d)
	}
}

func TestGoldenDiffReportsDiagnosticsAndTotalsAndAcceptance(t *testing.T) {
	want, _ := Projection(sampleResult(nil))
	got, _ := Projection(sampleResult(func(r *Result) {
		r.Accepted = false
		r.Totals.ComputedDocuments = 1
		r.Totals.ComputedGross = "1345.63"
		r.Diagnostics = []Diagnostic{{Code: CodeDecimalFormat, Severity: SeverityReject, Line: 7, Field: "gross_amount", Message: "m"}}
	}))
	d := GoldenDiff(got, want)
	checks := []string{
		"accepted: got false, want true",
		"totals.computed_documents: got 1, want 2",
		"totals.computed_gross:",
		"unexpected diagnostic E_DECIMAL_FORMAT at line 7 on field gross_amount",
		"missing diagnostic ambiguous_field_semantics at line 2 on field discount_raw",
	}
	for _, c := range checks {
		if !strings.Contains(d, c) {
			t.Errorf("the diff does not contain %q:\n%s", c, d)
		}
	}
}

func TestGoldenDiffReportsRepeatCounts(t *testing.T) {
	want, _ := Projection(sampleResult(nil))
	got, _ := Projection(sampleResult(func(r *Result) {
		r.Diagnostics = append(r.Diagnostics, r.Diagnostics[0])
	}))
	if d := GoldenDiff(got, want); !strings.Contains(d, "reported 2 time(s), want 1") {
		t.Errorf("a finding reported twice where it belongs once is not called out:\n%s", d)
	}
}

func TestGoldenDiffReportsUnreadableProjections(t *testing.T) {
	good, _ := Projection(sampleResult(nil))
	if d := GoldenDiff([]byte("{not json"), good); !strings.Contains(d, "produced projection is unreadable") {
		t.Errorf("an unreadable produced projection is not reported: %q", d)
	}
	if d := GoldenDiff(good, []byte("{not json")); !strings.Contains(d, "expected projection is unreadable") {
		t.Errorf("an unreadable expectation is not reported: %q", d)
	}
	if d := GoldenDiff(nil, nil); !strings.Contains(d, "both projections are unreadable") {
		t.Errorf("two empty projections are not reported: %q", d)
	}
}

func TestGoldenDiffIsNotADigestComparison(t *testing.T) {
	// The point of the diff is that a candidate learns what is wrong. A report
	// that only says two hashes differ is what this test forbids.
	want, _ := Projection(sampleResult(nil))
	got, _ := Projection(sampleResult(func(r *Result) {
		r.Documents[0].Payload = []byte(`{"name":"B","supplier_number":"0000418","country":"DE"}`)
	}))
	d := GoldenDiff(got, want)
	if !strings.Contains(d, "country") || !strings.Contains(d, "absent") {
		t.Errorf("a field present in the output and not in the expectation is not named:\n%s", d)
	}
	if strings.Contains(d, "sha256") || strings.Contains(strings.ToLower(d), "hash mismatch") {
		t.Errorf("the diff reports a digest rather than a difference:\n%s", d)
	}
}

func TestProjectionHash(t *testing.T) {
	a := ProjectionHash(sampleResult(nil))
	b := ProjectionHash(sampleResult(nil))
	if a == "" || a != b {
		t.Fatalf("ProjectionHash = %q and %q, want one stable non-empty value", a, b)
	}
	if ProjectionHash(sampleResult(func(r *Result) { r.Accepted = false })) == a {
		t.Error("ProjectionHash ignored a change in acceptance")
	}
}

func TestProjectionRejectsNil(t *testing.T) {
	if _, err := Projection(nil); err == nil {
		t.Fatal("Projection(nil) returned no error")
	}
	if ProjectionHash(nil) != "" {
		t.Error("ProjectionHash(nil) returned a hash")
	}
}

func TestGoldenDiffFlattensNestedPayloads(t *testing.T) {
	// A payload path is dotted for objects and indexed for arrays, so a
	// difference inside the third line of an invoice names that line.
	want, _ := Projection(sampleResult(func(r *Result) {
		r.Documents[0].Payload = []byte(`{"lines":[{"line_no":"00010","amount":"546.00"},{"line_no":"00020","amount":"682.50"}],` +
			`"money":{"amount_minor":134563,"currency":"CHF"},"blocked":false,"note":null,"tags":[],"meta":{}}`)
	}))
	got, _ := Projection(sampleResult(func(r *Result) {
		r.Documents[0].Payload = []byte(`{"lines":[{"line_no":"00010","amount":"546.00"},{"line_no":"00020","amount":"682.51"}],` +
			`"money":{"amount_minor":134564,"currency":"CHF"},"blocked":true,"note":"x","tags":[],"meta":{}}`)
	}))
	d := GoldenDiff(got, want)
	for _, must := range []string{
		"lines[1].amount",
		"money.amount_minor",
		`blocked: got "true", want "false"`,
		"note",
	} {
		if !strings.Contains(d, must) {
			t.Errorf("the diff does not name %q:\n%s", must, d)
		}
	}
	if strings.Contains(d, "lines[0]") {
		t.Errorf("the diff reports a line that did not change:\n%s", d)
	}
}

func TestGoldenDiffHandlesAScalarPayload(t *testing.T) {
	want, _ := Projection(sampleResult(func(r *Result) {
		r.Documents[0].Payload = []byte(`"a scalar"`)
	}))
	got, _ := Projection(sampleResult(func(r *Result) {
		r.Documents[0].Payload = []byte(`"another scalar"`)
	}))
	if d := GoldenDiff(got, want); !strings.Contains(d, "(payload)") {
		t.Errorf("a scalar payload difference is not reported:\n%s", d)
	}
}

func TestGoldenDiffRejectsAMalformedExpectation(t *testing.T) {
	good, _ := Projection(sampleResult(nil))
	for _, bad := range []string{`[]`, `{"diagnostics":[{"line":"two"}]}`, `{"documents":["not an object"]}`, `{"diagnostics":["x"]}`} {
		if d := GoldenDiff(good, []byte(bad)); !strings.Contains(d, "unreadable") {
			t.Errorf("GoldenDiff accepted the malformed expectation %s: %q", bad, d)
		}
	}
}
