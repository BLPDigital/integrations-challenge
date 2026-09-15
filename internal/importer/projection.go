package importer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// A projectedDocument is one document as the golden projection sees it: what the
// document IS, with nothing about where in the file it was found. Line numbers
// and ordinals are deliberately absent, so inserting a comment line or a blank
// line into a fixture does not invalidate every expectation in it.
type projectedDocument struct {
	Dataset string          `json:"dataset"`
	Key     string          `json:"key"`
	Payload json.RawMessage `json:"payload"`
}

// A projectedDiagnostic is the documented diagnostic tuple: code, line, field.
// Severity, message, value, record ordinal and document key are absent, because
// a message is prose that will be improved and a severity is implied by the
// code. Two findings that agree on the tuple are the same finding for grading
// purposes.
type projectedDiagnostic struct {
	Code  string `json:"code"`
	Line  int    `json:"line"`
	Field string `json:"field"`
}

// A projection is the whole comparable surface of a [Result].
type projection struct {
	Accepted    bool                  `json:"accepted"`
	Diagnostics []projectedDiagnostic `json:"diagnostics"`
	Documents   []projectedDocument   `json:"documents"`
	Totals      Totals                `json:"totals"`
}

// Projection returns the documented golden projection of r as canonical JSON:
// the canonical documents, the sorted diagnostic tuples (code, line, field), the
// totals, and accepted. It is what a golden fixture holds and what
// [GoldenDiff] compares.
//
// It is a projection and not the whole Result on purpose. A byte-identical
// whole-Result comparison would test JSON field ordering, empty-slice-versus-nil
// and the exact wording of a message far more than it tests parsing judgment: a
// candidate who improves a diagnostic message would see fourteen golden failures
// and learn nothing from them. What is compared is what the twin actually
// depends on - which documents came out, what they contain, which findings were
// reported and where, whether the control totals reconcile, and whether the file
// may be applied.
//
// Deliberately excluded, and each exclusion is a decision:
//
//   - [FileInfo], including the name and the sha256. A fixture renamed is the
//     same fixture.
//   - [Document.Line] and [Document.Ordinal]. Where a document was found is
//     provenance, not content.
//   - [Diagnostic.Severity], .Message, .Value, .Record and .Document. The code
//     fixes the severity; the rest is prose and evidence for a human.
//   - The order the documents came out in. Documents are sorted by dataset then
//     key, so a format that reads a file in a different but valid order still
//     matches.
//
// The diagnostic tuples are sorted by code, then line, then field - the order
// the tuple itself names - and duplicates are kept: two identical findings on
// one line are two findings, and collapsing them would hide a format that
// reports the same defect twice.
func Projection(r *Result) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("importer: Projection(nil)")
	}
	p := projection{
		Accepted:    r.Accepted,
		Diagnostics: make([]projectedDiagnostic, 0, len(r.Diagnostics)),
		Documents:   make([]projectedDocument, 0, len(r.Documents)),
		Totals:      r.Totals,
	}
	for _, d := range r.Diagnostics {
		p.Diagnostics = append(p.Diagnostics, projectedDiagnostic{Code: d.Code, Line: d.Line, Field: d.Field})
	}
	sort.SliceStable(p.Diagnostics, func(i, j int) bool {
		a, b := p.Diagnostics[i], p.Diagnostics[j]
		switch {
		case a.Code != b.Code:
			return a.Code < b.Code
		case a.Line != b.Line:
			return a.Line < b.Line
		default:
			return a.Field < b.Field
		}
	})
	for _, d := range r.Documents {
		p.Documents = append(p.Documents, projectedDocument{Dataset: d.Dataset, Key: d.Key, Payload: d.Payload})
	}
	sort.SliceStable(p.Documents, func(i, j int) bool {
		a, b := p.Documents[i], p.Documents[j]
		if a.Dataset != b.Dataset {
			return a.Dataset < b.Dataset
		}
		return a.Key < b.Key
	})
	return model.CanonicalJSON(p)
}

// ProjectionHash returns the [model.ContentHash] of the projection of r. It is
// for a caller that wants one comparable value, such as a receipt or a
// determinism assertion that reruns a parse. A golden test never uses it: a
// digest mismatch tells a candidate that something is wrong and nothing about
// what, which is the failure mode [GoldenDiff] exists to avoid.
func ProjectionHash(r *Result) string {
	b, err := Projection(r)
	if err != nil {
		return ""
	}
	return model.Sha256Hex(b)
}

// GoldenDiff compares two projections and returns a human-readable report of
// how they differ, or "" when they are equal. It is what a candidate sees from
// `make golden-diff`, so it names the document, the field and both values rather
// than reporting that two digests are unequal.
//
// The report has up to four sections, in this order: acceptance, totals,
// documents, diagnostics. Documents are matched by (dataset, key), so a missing
// document, a document that should not exist and a document whose contents are
// wrong are three different findings with three different remedies. Payloads are
// compared field by field, dotted for nested objects and indexed for arrays, and
// each difference names the path and both values.
//
// A projection that is not valid JSON is itself reported, because a fixture
// edited by hand into invalid JSON is a common and confusing failure.
func GoldenDiff(gotProjection, wantProjection []byte) string {
	got, gotErr := decodeProjection(gotProjection)
	want, wantErr := decodeProjection(wantProjection)
	switch {
	case gotErr != nil && wantErr != nil:
		return fmt.Sprintf("both projections are unreadable:\n  got:  %v\n  want: %v\n", gotErr, wantErr)
	case gotErr != nil:
		return fmt.Sprintf("the produced projection is unreadable: %v\n", gotErr)
	case wantErr != nil:
		return fmt.Sprintf("the expected projection is unreadable: %v\n", wantErr)
	}

	var b strings.Builder
	if got.Accepted != want.Accepted {
		fmt.Fprintf(&b, "accepted: got %t, want %t\n", got.Accepted, want.Accepted)
	}
	diffTotals(&b, got.Totals, want.Totals)
	diffDocuments(&b, got.Documents, want.Documents)
	diffDiagnostics(&b, got.Diagnostics, want.Diagnostics)
	return b.String()
}

// goldenProjection is the projection as it reads back from JSON. Payload is a
// generic tree so it can be flattened and compared field by field.
type goldenProjection struct {
	Accepted    bool
	Totals      Totals
	Documents   []goldenDocument
	Diagnostics []projectedDiagnostic
}

// goldenDocument is one document read back from a projection.
type goldenDocument struct {
	Dataset string
	Key     string
	Payload any
}

// decodeProjection reads a projection without letting a JSON number become a
// float64, so an amount in an expectation file compares as the string it was
// written as.
func decodeProjection(b []byte) (goldenProjection, error) {
	var out goldenProjection
	if len(b) == 0 {
		return out, fmt.Errorf("empty projection")
	}
	tree, err := model.DecodeJSONTree(b)
	if err != nil {
		return out, err
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return out, fmt.Errorf("projection is not a JSON object")
	}
	if v, ok := obj["accepted"].(bool); ok {
		out.Accepted = v
	}
	if raw, ok := obj["totals"]; ok {
		enc, err := json.Marshal(raw)
		if err != nil {
			return out, fmt.Errorf("totals: %w", err)
		}
		if err := json.Unmarshal(enc, &out.Totals); err != nil {
			return out, fmt.Errorf("totals: %w", err)
		}
	}
	for i, raw := range asSlice(obj["documents"]) {
		d, ok := raw.(map[string]any)
		if !ok {
			return out, fmt.Errorf("documents[%d] is not a JSON object", i)
		}
		out.Documents = append(out.Documents, goldenDocument{
			Dataset: asString(d["dataset"]),
			Key:     asString(d["key"]),
			Payload: d["payload"],
		})
	}
	for i, raw := range asSlice(obj["diagnostics"]) {
		d, ok := raw.(map[string]any)
		if !ok {
			return out, fmt.Errorf("diagnostics[%d] is not a JSON object", i)
		}
		line, err := asInt(d["line"])
		if err != nil {
			return out, fmt.Errorf("diagnostics[%d].line: %w", i, err)
		}
		out.Diagnostics = append(out.Diagnostics, projectedDiagnostic{
			Code:  asString(d["code"]),
			Line:  line,
			Field: asString(d["field"]),
		})
	}
	return out, nil
}

// diffTotals reports every control-total field that differs, naming the field.
func diffTotals(b *strings.Builder, got, want Totals) {
	type row struct {
		name        string
		gotS, wantS string
		gotI, wantI int
		numeric     bool
	}
	rows := []row{
		{name: "declared_documents", gotI: got.DeclaredDocuments, wantI: want.DeclaredDocuments, numeric: true},
		{name: "computed_documents", gotI: got.ComputedDocuments, wantI: want.ComputedDocuments, numeric: true},
		{name: "declared_lines", gotI: got.DeclaredLines, wantI: want.DeclaredLines, numeric: true},
		{name: "computed_lines", gotI: got.ComputedLines, wantI: want.ComputedLines, numeric: true},
		{name: "declared_gross", gotS: got.DeclaredGross, wantS: want.DeclaredGross},
		{name: "computed_gross", gotS: got.ComputedGross, wantS: want.ComputedGross},
		{name: "declared_line_sum", gotS: got.DeclaredLineSum, wantS: want.DeclaredLineSum},
		{name: "computed_line_sum", gotS: got.ComputedLineSum, wantS: want.ComputedLineSum},
	}
	for _, r := range rows {
		if r.numeric {
			if r.gotI != r.wantI {
				fmt.Fprintf(b, "totals.%s: got %d, want %d\n", r.name, r.gotI, r.wantI)
			}
			continue
		}
		if r.gotS != r.wantS {
			fmt.Fprintf(b, "totals.%s: got %s, want %s\n", r.name, quoteOrEmpty(r.gotS), quoteOrEmpty(r.wantS))
		}
	}
	if got.TrailerDeclared != want.TrailerDeclared {
		fmt.Fprintf(b, "totals.trailer_declared: got %t, want %t\n", got.TrailerDeclared, want.TrailerDeclared)
	}
	if got.TrailerVerified != want.TrailerVerified {
		fmt.Fprintf(b, "totals.trailer_verified: got %t, want %t\n", got.TrailerVerified, want.TrailerVerified)
	}
}

// diffDocuments matches documents by (dataset, key) and reports the three
// distinct failures separately: a document that is missing, one that should not
// be there, and one whose contents differ.
func diffDocuments(b *strings.Builder, got, want []goldenDocument) {
	index := func(ds []goldenDocument) (map[string]goldenDocument, []string) {
		m := make(map[string]goldenDocument, len(ds))
		order := make([]string, 0, len(ds))
		for _, d := range ds {
			id := d.Dataset + "/" + d.Key
			if _, dup := m[id]; dup {
				id = fmt.Sprintf("%s#%d", id, len(order))
			}
			m[id] = d
			order = append(order, id)
		}
		sort.Strings(order)
		return m, order
	}
	gotIdx, gotIDs := index(got)
	wantIdx, wantIDs := index(want)

	for _, id := range wantIDs {
		if _, ok := gotIdx[id]; !ok {
			fmt.Fprintf(b, "missing document %s\n", id)
		}
	}
	for _, id := range gotIDs {
		if _, ok := wantIdx[id]; !ok {
			fmt.Fprintf(b, "unexpected document %s\n", id)
		}
	}
	for _, id := range wantIDs {
		g, ok := gotIdx[id]
		if !ok {
			continue
		}
		w := wantIdx[id]
		gf := flatten(g.Payload)
		wf := flatten(w.Payload)
		paths := unionPaths(gf, wf)
		var lines []string
		for _, path := range paths {
			gv, gok := gf[path]
			wv, wok := wf[path]
			switch {
			case gok && wok && gv != wv:
				lines = append(lines, fmt.Sprintf("    %s: got %s, want %s", path, quoteOrEmpty(gv), quoteOrEmpty(wv)))
			case gok && !wok:
				lines = append(lines, fmt.Sprintf("    %s: got %s, want the field to be absent", path, quoteOrEmpty(gv)))
			case !gok && wok:
				lines = append(lines, fmt.Sprintf("    %s: missing, want %s", path, quoteOrEmpty(wv)))
			}
		}
		if len(lines) > 0 {
			fmt.Fprintf(b, "document %s differs:\n%s\n", id, strings.Join(lines, "\n"))
		}
	}
}

// diffDiagnostics compares the diagnostic tuples as a multiset, so a finding
// reported twice where it should be reported once is a difference.
func diffDiagnostics(b *strings.Builder, got, want []projectedDiagnostic) {
	count := func(ds []projectedDiagnostic) map[projectedDiagnostic]int {
		m := make(map[projectedDiagnostic]int, len(ds))
		for _, d := range ds {
			m[d]++
		}
		return m
	}
	gotN, wantN := count(got), count(want)
	keys := make([]projectedDiagnostic, 0, len(gotN)+len(wantN))
	seen := make(map[projectedDiagnostic]bool, len(gotN)+len(wantN))
	for _, m := range []map[projectedDiagnostic]int{wantN, gotN} {
		for d := range m {
			if !seen[d] {
				seen[d] = true
				keys = append(keys, d)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		switch {
		case a.Code != c.Code:
			return a.Code < c.Code
		case a.Line != c.Line:
			return a.Line < c.Line
		default:
			return a.Field < c.Field
		}
	})
	for _, d := range keys {
		g, w := gotN[d], wantN[d]
		if g == w {
			continue
		}
		switch {
		case g == 0:
			fmt.Fprintf(b, "missing diagnostic %s\n", describeDiagnostic(d))
		case w == 0:
			fmt.Fprintf(b, "unexpected diagnostic %s\n", describeDiagnostic(d))
		default:
			fmt.Fprintf(b, "diagnostic %s reported %d time(s), want %d\n", describeDiagnostic(d), g, w)
		}
	}
}

// describeDiagnostic renders a tuple the way an operator reads it: what, where,
// which field.
func describeDiagnostic(d projectedDiagnostic) string {
	out := d.Code
	if d.Line > 0 {
		out += fmt.Sprintf(" at line %d", d.Line)
	} else {
		out += " at file level"
	}
	if d.Field != "" {
		out += " on field " + d.Field
	}
	return out
}

// flatten renders a decoded JSON payload as a map from dotted, indexed path to a
// rendered value, so two payloads can be compared field by field. A nested
// object becomes "a.b" and an array element "a[0]". A JSON null renders as
// "(null)" rather than "null", so it cannot be confused with the string "null" -
// which is a value a free-text field really does carry sometimes.
func flatten(v any) map[string]string {
	out := make(map[string]string)
	flattenInto(out, "", v)
	return out
}

// flattenInto is the recursive half of flatten.
func flattenInto(out map[string]string, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			out[orRoot(prefix)] = "{}"
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flattenInto(out, p, t[k])
		}
	case []any:
		if len(t) == 0 {
			out[orRoot(prefix)] = "[]"
			return
		}
		for i, e := range t {
			flattenInto(out, fmt.Sprintf("%s[%d]", prefix, i), e)
		}
	case nil:
		out[orRoot(prefix)] = "(null)"
	case bool:
		out[orRoot(prefix)] = fmt.Sprintf("%t", t)
	case json.Number:
		out[orRoot(prefix)] = t.String()
	case string:
		out[orRoot(prefix)] = t
	default:
		out[orRoot(prefix)] = fmt.Sprintf("%v", t)
	}
}

// orRoot names the payload itself when a scalar payload has no field path.
func orRoot(prefix string) string {
	if prefix == "" {
		return "(payload)"
	}
	return prefix
}

// unionPaths returns every path in either map, ascending.
func unionPaths(a, b map[string]string) []string {
	set := make(map[string]bool, len(a)+len(b))
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quoteOrEmpty renders a value for a diff line, spelling out an empty string so
// "got , want CHF" cannot be misread.
func quoteOrEmpty(s string) string {
	if s == "" {
		return "an empty value"
	}
	return strconv.Quote(s)
}

// asSlice returns v as a JSON array, or nil.
func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// asString returns v as a JSON string, or "".
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asInt returns v as an int, accepting a JSON number and an absent value.
func asInt(v any) (int, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, fmt.Errorf("%q is not an integer", t.String())
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("%v is not a number", v)
	}
}
