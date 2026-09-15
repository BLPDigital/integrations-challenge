package canonical

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestCSVRecoversPerRecord(t *testing.T) {
	// One bad row costs one record. The record separator of a delimited file is
	// unambiguous even when a field is not, so the other 11'999 rows of a
	// master-data extract still arrive.
	in := "supplier_number,name,currency\n" +
		"0000417,First,CHF\n" +
		"0000418,Missing a column\n" +
		"0000419,Third,CHF\n" +
		"0000420,Too,many,columns,here\n" +
		"0000421,Fifth,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("a file with rejected records was accepted")
	}
	if len(r.Documents) != 3 {
		t.Fatalf("%d documents, want the 3 well-formed records", len(r.Documents))
	}
	for _, want := range []string{"0000417", "0000419", "0000421"} {
		payload(t, r, want)
	}
	lines := []int{}
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeFieldInvalid {
			lines = append(lines, d.Line)
		}
	}
	if len(lines) != 2 || lines[0] != 3 || lines[1] != 5 {
		t.Errorf("field-count findings on lines %v, want lines 3 and 5", lines)
	}
}

func TestCSVQuotingAndEmbeddedLineBreaks(t *testing.T) {
	in := "supplier_number,name,currency\n" +
		"0000417,\"Muster, GmbH \"\"Nord\"\"\",CHF\n" +
		"0000418,\"Two\nlines\",EUR\n" +
		"0000419,Plain,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if got := payload(t, r, "0000417")["name"]; got != `Muster, GmbH "Nord"` {
		t.Errorf("name = %q, want the delimiter and the doubled quote decoded", got)
	}
	if got := payload(t, r, "0000418")["name"]; got != "Two\nlines" {
		t.Errorf("name = %q, want the embedded line break preserved", got)
	}
	// The record after one containing a line break is reported on its real
	// physical line, which is the line an operator searches for.
	for _, d := range r.Documents {
		if d.Key == "0000419" && d.Line != 5 {
			t.Errorf("the record after an embedded line break is on line %d, want 5", d.Line)
		}
	}
}

func TestCSVUnterminatedQuoteIsFatal(t *testing.T) {
	// The one CSV defect with no resync point: from the missing quote onwards,
	// every delimiter and every line break is inside a string or outside one
	// depending on a byte that is not there.
	in := "supplier_number,name,currency\n" +
		"0000417,First,CHF\n" +
		"0000418,\"never closed,EUR\n" +
		"0000419,Third,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted a file whose quoting never resolves")
	}
	d, ok := r.FirstFatal()
	if !ok || d.Code != importer.CodeMalformedDocument {
		t.Fatalf("diagnostics = %v, want a fatal E_MALFORMED_DOCUMENT", r.Diagnostics)
	}
	if len(r.Documents) != 1 {
		t.Errorf("%d documents, want the one record read before the defect", len(r.Documents))
	}
}

func TestCSVTrailingDataAfterAQuotedFieldIsRejectedNotGuessed(t *testing.T) {
	in := "supplier_number,name,currency\n0000417,\"quoted\"trailing,CHF\n0000418,ok,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted a value whose intent is not decidable")
	}
	if len(r.Documents) != 1 || r.Documents[0].Key != "0000418" {
		t.Errorf("documents = %v, want only the following well-formed record", r.Documents)
	}
}

func TestNDJSONRecoversPerLine(t *testing.T) {
	in := `{"supplier_number":"0000417","name":"First","currency":"CHF"}` + "\n" +
		`{"supplier_number":"0000418","name":"Broken"` + "\n" +
		`{"supplier_number":"0000419","name":"Third","currency":"CHF"}` + "\n"
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("a file with a rejected line was accepted")
	}
	if len(r.Documents) != 2 {
		t.Fatalf("%d documents, want the 2 well-formed lines", len(r.Documents))
	}
	var found bool
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeMalformedDocument {
			found = true
			if d.Severity != importer.SeverityReject {
				t.Errorf("severity = %s, want reject: a line terminator is a record separator NDJSON actually has", d.Severity)
			}
			if d.Line != 2 {
				t.Errorf("line = %d, want 2", d.Line)
			}
		}
	}
	if !found {
		t.Fatalf("want E_MALFORMED_DOCUMENT, got %v", r.Diagnostics)
	}
}

func TestJSONArrayIsFatalAtTheBreakingByte(t *testing.T) {
	// The documented asymmetry with CSV and NDJSON. A JSON array has no record
	// separator to resume at: a missing brace makes the following objects
	// members of the wrong container, and skipping forward to the next "}," is a
	// guess about data.
	in := `[{"supplier_number":"0000417","name":"First","currency":"CHF"},` + "\n" +
		`{"supplier_number":"0000418","name":"Broken",` + "\n" +
		`{"supplier_number":"0000419","name":"Third","currency":"CHF"}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted a structurally broken array")
	}
	d, ok := r.FirstFatal()
	if !ok || d.Code != importer.CodeMalformedDocument {
		t.Fatalf("diagnostics = %v, want a fatal E_MALFORMED_DOCUMENT", r.Diagnostics)
	}
	if !strings.HasPrefix(d.Value, "offset ") {
		t.Errorf("Value = %q, want a byte offset: an array that stops balancing has no line a parser may trust", d.Value)
	}
	off, err := strconv.Atoi(strings.TrimPrefix(d.Value, "offset "))
	if err != nil || off <= 0 || off > len(in) {
		t.Errorf("Value = %q, want an offset inside the file (0..%d)", d.Value, len(in))
	}
	if len(r.Documents) != 1 {
		t.Errorf("%d documents, want the one element read before the break", len(r.Documents))
	}
	// The same defect one line later in a NDJSON file costs one record, and that
	// difference is a property of the two grammars, not a policy.
	nd := `{"supplier_number":"0000417","name":"First","currency":"CHF"}` + "\n" +
		`{"supplier_number":"0000418","name":"Broken",` + "\n" +
		`{"supplier_number":"0000419","name":"Third","currency":"CHF"}` + "\n"
	rn := parse(t, NewJSON(), nd, importer.Options{Dataset: "supplier"})
	if _, fatal := rn.FirstFatal(); fatal {
		t.Error("the same defect is fatal in NDJSON; recovery per line is what a line terminator buys")
	}
	if len(rn.Documents) != 2 {
		t.Errorf("NDJSON produced %d documents, want 2", len(rn.Documents))
	}
}

func TestPrettyPrintedObjectIsNotNDJSON(t *testing.T) {
	in := "{\n  \"supplier_number\": \"0000417\",\n  \"name\": \"First\",\n  \"currency\": \"CHF\"\n}\n"
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted a document that is neither an array nor one value per line")
	}
	// Diagnostics are sorted by line, so the first finding is the one on the
	// opening brace, and its message has to name the remedy.
	var msg string
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeMalformedDocument {
			msg = d.Message
			break
		}
	}
	if !strings.Contains(msg, "wrap the records in an array") {
		t.Errorf("the first finding does not name the remedy: %q", msg)
	}
}

func TestJSONEnvelope(t *testing.T) {
	in := `{"records":[{"supplier_number":"0000417","name":"A","currency":"CHF"},{"supplier_number":"0000418","name":"B","currency":"EUR"}]}` + "\n"
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if len(r.Documents) != 2 {
		t.Fatalf("%d documents, want 2 from the envelope", len(r.Documents))
	}
}

func TestJSONSingleObjectIsOneRecord(t *testing.T) {
	in := `{"supplier_number":"0000417","name":"A","currency":"CHF"}` + "\n"
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted || len(r.Documents) != 1 {
		t.Fatalf("documents = %d, diagnostics = %v", len(r.Documents), r.Diagnostics)
	}
}

func TestEmptyJSONFile(t *testing.T) {
	r := parse(t, NewJSON(), "", importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted an empty file")
	}
	if _, ok := r.FirstFatal(); !ok {
		t.Errorf("diagnostics = %v, want a fatal finding", r.Diagnostics)
	}
}

func TestXMLIsFatalAtTheBreakingByte(t *testing.T) {
	in := `<records>
	<record><supplier_number>0000417</supplier_number><name>First</name><currency>CHF</currency></record>
	<record><supplier_number>0000418</supplier_number><name>Broken</name
	<record><supplier_number>0000419</supplier_number><name>Third</name><currency>CHF</currency></record>
	</records>`
	r := parse(t, NewXML(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted a document that is not well-formed")
	}
	d, ok := r.FirstFatal()
	if !ok || d.Code != importer.CodeMalformedDocument {
		t.Fatalf("diagnostics = %v, want a fatal E_MALFORMED_DOCUMENT", r.Diagnostics)
	}
	if !strings.HasPrefix(d.Value, "offset ") {
		t.Errorf("Value = %q, want a byte offset", d.Value)
	}
	if d.Line < 3 {
		t.Errorf("Line = %d, want the line the document stops making sense on", d.Line)
	}
	if len(r.Documents) != 1 {
		t.Errorf("%d documents, want the one record read before the break", len(r.Documents))
	}
}

func TestXMLPrologEncodingConflict(t *testing.T) {
	in := `<?xml version="1.0" encoding="ISO-8859-1"?><records><record><supplier_number>0000417</supplier_number><name>A</name><currency>CHF</currency></record></records>`
	r := parse(t, NewXML(), in, importer.Options{Dataset: "supplier", Encoding: importer.EncodingUTF8})
	if r.Accepted {
		t.Error("accepted a file whose prolog and manifest disagree about how to read it")
	}
	if !hasDiag(r, importer.CodeEncodingConflict, "encoding") {
		t.Fatalf("diagnostics = %v, want ENCODING_CONFLICT", r.Diagnostics)
	}

	// Agreement, including a spelling the prolog and the manifest write
	// differently, parses.
	agree := `<?xml version="1.0" encoding="cp1252"?><records><record><supplier_number>0000417</supplier_number><name>A</name><currency>CHF</currency></record></records>`
	r = parse(t, NewXML(), agree, importer.Options{Dataset: "supplier", Encoding: importer.EncodingCP1252})
	if !r.Accepted {
		t.Fatalf("cp1252 and windows-1252 are the same code page: %v", r.Diagnostics)
	}
}

func TestXMLNonRecordElementIsSkippedWithAWarning(t *testing.T) {
	in := `<records><metadata><producer>acme-connector/1.4.2</producer></metadata>` +
		`<record><supplier_number>0000417</supplier_number><name>A</name><currency>CHF</currency></record></records>`
	r := parse(t, NewXML(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if len(r.Documents) != 1 {
		t.Fatalf("%d documents, want 1", len(r.Documents))
	}
	if !hasDiag(r, importer.CodeFieldUnknown, "metadata") {
		t.Errorf("the skipped element is not reported: %v", r.Diagnostics)
	}
}

func TestXMLDatasetNamedRecordElement(t *testing.T) {
	in := `<suppliers><supplier><supplier_number>0000417</supplier_number><name>A</name><currency>CHF</currency></supplier></suppliers>`
	r := parse(t, NewXML(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted || len(r.Documents) != 1 {
		t.Fatalf("documents = %d, diagnostics = %v", len(r.Documents), r.Diagnostics)
	}
}

func TestXMLNamespacePrefixIsNotCoupledToTheName(t *testing.T) {
	// A middleware that rewrites a prefix changes nothing about the data. A
	// parser that couples the two breaks the first time one does.
	in := `<ns:records xmlns:ns="urn:blp:canonical"><ns:record>` +
		`<ns:supplier_number>0000417</ns:supplier_number><ns:name>A</ns:name><ns:currency>CHF</ns:currency>` +
		`</ns:record></ns:records>`
	r := parse(t, NewXML(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted || len(r.Documents) != 1 {
		t.Fatalf("documents = %d, diagnostics = %v", len(r.Documents), r.Diagnostics)
	}
}

func TestDeclaredRecordCountMismatchIsRejected(t *testing.T) {
	in := "supplier_number,name,currency\n0000417,A,CHF\n0000418,B,EUR\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier", DeclaredRecords: 3})
	if r.Accepted {
		t.Error("accepted a file whose record count does not match the manifest; this is the truncated-transfer guard")
	}
	if !hasDiag(r, importer.CodeTrailerCountMismatch, "record_count") {
		t.Fatalf("diagnostics = %v, want trailer_count_mismatch", r.Diagnostics)
	}
	if r.Totals.DeclaredDocuments != 3 || r.Totals.ComputedDocuments != 2 || r.Totals.TrailerVerified {
		t.Errorf("Totals = %+v, want declared 3, computed 2, unverified", r.Totals)
	}

	// A matching count verifies the control block.
	r = parse(t, NewCSV(), in, importer.Options{Dataset: "supplier", DeclaredRecords: 2})
	if !r.Accepted || !r.Totals.TrailerVerified {
		t.Errorf("Totals = %+v, accepted = %v; want a verified trailer", r.Totals, r.Accepted)
	}
}

func TestARejectedRecordLeavesTheTrailerUnverifiedRatherThanMismatched(t *testing.T) {
	// A control total computed over an incomplete set of records is not
	// evidence. Reporting a count mismatch derived from it would send an
	// operator looking for a second problem that does not exist.
	in := "supplier_number,name,currency\n0000417,A,CHF\n0000418,B\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier", DeclaredRecords: 2})
	if hasDiag(r, importer.CodeTrailerCountMismatch, "record_count") {
		t.Error("a count mismatch was reported over an incomplete count")
	}
	if !hasDiag(r, importer.CodeTrailerNotVerified, "record_count") {
		t.Fatalf("diagnostics = %v, want trailer_not_verified", r.Diagnostics)
	}
	if r.Totals.TrailerVerified {
		t.Error("the trailer is marked verified although a record was rejected")
	}
}

func TestParseAndParseStreamAgree(t *testing.T) {
	inputs := []struct {
		name    string
		format  importer.Format
		dataset string
		body    string
	}{
		{name: "csv", format: NewCSV(), dataset: "supplier",
			body: "supplier_number,name,currency\n0000417,\"Muster, GmbH \"\"Nord\"\"\",CHF\n0000418,\"Two\nlines\",EUR\n0000419,Bad\n"},
		{name: "json array", format: NewJSON(), dataset: "supplier",
			body: `[{"supplier_number":"0000417","name":"A","currency":"CHF"},{"supplier_number":417,"name":"B","currency":"CHF"}]`},
		{name: "ndjson", format: NewJSON(), dataset: "supplier",
			body: "{\"supplier_number\":\"0000417\",\"name\":\"A\",\"currency\":\"CHF\"}\n{broken\n"},
		{name: "xml", format: NewXML(), dataset: "supplier",
			body: "<records>\n<record><supplier_number>0000417</supplier_number><name>Zürich</name><currency>CHF</currency></record>\n</records>"},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			opt := importer.Options{Filename: "f", Dataset: tc.dataset}
			whole, err := tc.format.Parse(context.Background(), []byte(tc.body), opt)
			if err != nil {
				t.Fatal(err)
			}
			streamed, err := importer.ParseStream(context.Background(), tc.format, &chunkReader{rest: []byte(tc.body), size: 3}, opt)
			if err != nil {
				t.Fatal(err)
			}
			a, err := importer.Projection(whole)
			if err != nil {
				t.Fatal(err)
			}
			b, err := importer.Projection(streamed)
			if err != nil {
				t.Fatal(err)
			}
			if d := importer.GoldenDiff(b, a); d != "" {
				t.Fatalf("the streaming path disagrees with the whole-file path:\n%s", d)
			}
			if streamed.File.SHA256 != whole.File.SHA256 || streamed.File.Bytes != whole.File.Bytes {
				t.Errorf("FileInfo differs: %+v vs %+v", streamed.File, whole.File)
			}
		})
	}
}

func TestParseIsDeterministic(t *testing.T) {
	in := "supplier_number,name,currency,legacy_id,extra\n" +
		"0000417,A,CHF,417,x\n0000418,B,EUR,418,y\n0000419,,ZZZ,419,z\n"
	opt := importer.Options{Filename: "f", Dataset: "supplier"}
	var first string
	for i := 0; i < 20; i++ {
		r := parse(t, NewCSV(), in, opt)
		p, err := importer.Projection(r)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = string(p)
			continue
		}
		if string(p) != first {
			t.Fatalf("run %d produced a different projection:\n%s\n%s", i, p, first)
		}
	}
}

func TestNilContextIsAProgrammingFault(t *testing.T) {
	for _, f := range []importer.Format{NewCSV(), NewXML(), NewJSON()} {
		//lint:ignore SA1012 passing a nil context is exactly what is under test.
		if _, err := f.Parse(nil, []byte("x"), importer.Options{}); err == nil {
			t.Errorf("%s accepted a nil context", f.ID())
		}
	}
}

func TestCancelledContextStopsTheParse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := "supplier_number,name,currency\n0000417,A,CHF\n"
	for _, f := range []importer.Format{NewCSV(), NewXML(), NewJSON()} {
		if _, err := f.Parse(ctx, []byte(in), importer.Options{Dataset: "supplier"}); err == nil {
			t.Errorf("%s ignored a cancelled context", f.ID())
		}
	}
}

// chunkReader hands over size bytes at a time, so a record boundary, a quoted
// line break and a multi-byte character are each split across reads.
type chunkReader struct {
	rest []byte
	size int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.rest) == 0 {
		return 0, io.EOF
	}
	n := r.size
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.rest) {
		n = len(r.rest)
	}
	copy(p, r.rest[:n])
	r.rest = r.rest[n:]
	return n, nil
}

// generatedCSV yields a supplier CSV of n records without ever holding the file:
// a test that materialized a 50'000-record extract would prove nothing about a
// parser that does the same.
type generatedCSV struct {
	n    int
	i    int
	buf  bytes.Buffer
	done bool
}

func (g *generatedCSV) Read(p []byte) (int, error) {
	for g.buf.Len() < len(p) && !g.done {
		if g.i == 0 {
			g.buf.WriteString("supplier_number,name,currency,change_seq\n")
		}
		if g.i >= g.n {
			g.done = true
			break
		}
		key := strconv.Itoa(g.i)
		for len(key) < 7 {
			key = "0" + key
		}
		g.buf.WriteString(key + ",Supplier " + key + ",CHF," + strconv.Itoa(100000+g.i) + "\n")
		g.i++
	}
	if g.buf.Len() == 0 {
		return 0, io.EOF
	}
	return g.buf.Read(p)
}

func TestLargeFileStreams(t *testing.T) {
	const n = 50000
	res, err := importer.ParseStream(context.Background(), NewCSV(), &generatedCSV{n: n},
		importer.Options{Filename: "big.csv", Dataset: "supplier", DeclaredRecords: n})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted {
		t.Fatalf("not accepted: %v", res.Diagnostics[:min(5, len(res.Diagnostics))])
	}
	if len(res.Documents) != n {
		t.Fatalf("%d documents, want %d", len(res.Documents), n)
	}
	if !res.Totals.TrailerVerified {
		t.Error("the declared record count did not verify")
	}
	if res.Documents[n-1].Key != "0049999" {
		t.Errorf("last key = %q, want 0049999", res.Documents[n-1].Key)
	}
}

// endlessQuote is an infinite stream: an opening quote and then bytes forever. A
// parser that waits for the closing quote before deciding anything never
// returns, which is the failure this test exists to prevent.
type endlessQuote struct{ sent int }

func (e *endlessQuote) Read(p []byte) (int, error) {
	const header = "supplier_number,name,currency\n0000417,\""
	if e.sent < len(header) {
		n := copy(p, header[e.sent:])
		e.sent += n
		return n, nil
	}
	for i := range p {
		p[i] = 'x'
	}
	e.sent += len(p)
	return len(p), nil
}

func TestUnterminatedQuoteIsCappedRatherThanBuffered(t *testing.T) {
	// The record cap is what makes the "bounded memory" claim true for a
	// grammar in which one record may be arbitrarily long. Without it, a file
	// with a single unclosed quote is read into memory in its entirety, and a
	// stream with no end is read until the process dies.
	src := &endlessQuote{}
	res, err := importer.ParseStream(context.Background(), NewCSV(), src,
		importer.Options{Filename: "endless.csv", Dataset: "supplier"})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := res.FirstFatal()
	if !ok || d.Code != importer.CodeMalformedDocument {
		t.Fatalf("diagnostics = %v, want a fatal E_MALFORMED_DOCUMENT", res.Diagnostics)
	}
	if d.Value != strconv.Itoa(MaxRecordBytes) {
		t.Errorf("Value = %q, want the record cap %d", d.Value, MaxRecordBytes)
	}
	if src.sent > 4*MaxRecordBytes {
		t.Errorf("the parser read %d bytes before giving up, which is more than the %d byte cap justifies", src.sent, MaxRecordBytes)
	}
}

func TestKeysKeepTheirLeadingZeros(t *testing.T) {
	// 0815 (Werk Wil) and 815 (Vertrieb DACH) are two different cost centers.
	in := "code,name,company_code,valid_from\n0815,Werk Wil,CH10,2020-01-01\n815,Vertrieb DACH,CH10,2020-01-01\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "cost_center"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if len(r.Documents) != 2 {
		t.Fatalf("%d documents, want 2 distinct cost centers", len(r.Documents))
	}
	if payload(t, r, "0815")["name"] != "Werk Wil" || payload(t, r, "815")["name"] != "Vertrieb DACH" {
		t.Error("the two cost centers were confused")
	}
}

func TestNumericKeyIsRejected(t *testing.T) {
	in := `[{"supplier_number":417,"name":"A","currency":"CHF"}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "supplier"})
	if r.Accepted || !hasDiag(r, importer.CodeKeyNotString, "supplier_number") {
		t.Fatalf("diagnostics = %v, want E_KEY_NOT_STRING", r.Diagnostics)
	}
	if len(r.Documents) != 0 {
		t.Error("a record whose key lost its leading zeros produced a document")
	}
}

func TestEmptyKeyIsReportedOnce(t *testing.T) {
	in := "supplier_number,name,currency\n,A,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
	n := 0
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeKeyMissing {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("E_KEY_MISSING reported %d times, want exactly once: %v", n, r.Diagnostics)
	}
}

func TestMoneyWireForms(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
		code string
	}{
		{name: "money object", json: `{"amount_minor":134563,"currency":"CHF","scale":2}`, want: "1345.63"},
		{name: "decimal string", json: `"1'345.63"`, want: "1345.63"},
		{name: "decimal string with a currency prefix", json: `"CHF 1'345.63"`, want: "1345.63"},
		{name: "money object for a zero-scale currency", json: `{"amount_minor":250000,"currency":"JPY","scale":0}`, want: "250000", code: importer.CodeFieldInvalid},

		{name: "a float is not minor units", json: `1345.63`, code: importer.CodeMoneyNotIntegerMinor},
		{name: "an integer has no currency and therefore no scale", json: `134563`, code: importer.CodeMoneyNotIntegerMinor},
		{name: "an incomplete money object", json: `{"amount_minor":134563,"currency":"CHF"}`, code: importer.CodeMoneyNotIntegerMinor},
		{name: "a money object with a float amount", json: `{"amount_minor":1345.63,"currency":"CHF","scale":2}`, code: importer.CodeMoneyNotIntegerMinor},
		{name: "a money object whose scale contradicts its currency", json: `{"amount_minor":250000,"currency":"JPY","scale":2}`, code: importer.CodeFieldInvalid},
		{name: "an unknown currency", json: `{"amount_minor":100,"currency":"XBT","scale":2}`, code: importer.CodeCurrencyUnknown},
		{name: "three fraction digits", json: `"1345.634"`, code: importer.CodeMoneyScale},
		{name: "a currency contradicting the record", json: `"EUR 1345.63"`, code: importer.CodeFieldInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := `[{"supplier_number":"0000105","supplier_invoice_number":"0004711","company_code":"CH10",` +
				`"document_type":"RE","document_date":"2026-01-15","currency":"CHF",` +
				`"gross_amount":` + tc.json + `,"vat_amount":"0.00"}]`
			r := parse(t, NewJSON(), in, importer.Options{Dataset: "invoice"})
			if tc.code != "" {
				found := false
				for _, d := range r.Diagnostics {
					if d.Code == tc.code && strings.HasPrefix(d.Field, "gross_amount") {
						found = true
					}
				}
				if !found {
					t.Fatalf("want %s on gross_amount, got %v", tc.code, r.Diagnostics)
				}
				if r.Accepted {
					t.Error("the file was accepted")
				}
				return
			}
			if !r.Accepted {
				t.Fatalf("not accepted: %v", r.Diagnostics)
			}
			if got := payload(t, r, model.InvoiceKey("0000105", "0004711"))["gross_amount"]; got != tc.want {
				t.Errorf("gross_amount = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNonMonetaryDecimalRejectsAJSONNumber(t *testing.T) {
	in := `[{"base":"EUR","quote":"CHF","valid_from":"2026-01-15","rate_type":"DAILY","rate":0.931,"rate_factor":1,"sequence":1,"status":"ACTIVE"}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "fx_rate"})
	if r.Accepted || !hasDiag(r, importer.CodeDecimalFormat, "rate") {
		t.Fatalf("diagnostics = %v, want E_DECIMAL_FORMAT on rate: a rate delivered as a JSON number has already lost its exact value", r.Diagnostics)
	}
}

func TestAmbiguousFieldsAreSurfacedAndNeverResolved(t *testing.T) {
	// BUILD-SPEC 5: the two documented non-decisions. Both are warnings, both
	// leave the value alone, and both keep the record.
	in := `[{"supplier_number":"0000105","supplier_invoice_number":"0004711","company_code":"CH10",
	  "document_type":"GU","document_date":"2026-01-15","currency":"CHF",
	  "gross_amount":"-100.00","vat_amount":"0.00","discount_raw":"2.00",
	  "lines":[{"line_no":"00010","gl_account":"0006400","quantity":"1.000","uom":"EA",
	            "unit_price":"100.0000","line_amount":"-100.00"}]}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "invoice"})
	if !r.Accepted {
		t.Fatalf("an ambiguity is a warning, not a rejection: %v", r.Diagnostics)
	}
	if !hasDiag(r, importer.CodeAmbiguousFieldSemantics, "discount_raw") {
		t.Error("the discount field's contradictory documentation is not surfaced")
	}
	if !hasDiag(r, importer.CodeAmbiguousFieldSemantics, "lines[0].cost_center") {
		t.Error("the empty line cost center is not surfaced")
	}
	p := payload(t, r, model.InvoiceKey("0000105", "0004711"))
	if p["discount_raw"] != "2.00" {
		t.Errorf("discount_raw = %v; the value must be carried verbatim, never converted to money", p["discount_raw"])
	}
	lines, _ := p["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", p["lines"])
	}
	line, _ := lines[0].(map[string]any)
	if line["cost_center"] != "" {
		t.Errorf("cost_center = %v; an absent line cost center is left empty, never inherited from the header", line["cost_center"])
	}
	// The explicit sign wins; the document type never implies one.
	if p["gross_amount"] != "-100.00" || p["document_type"] != "GU" {
		t.Errorf("gross_amount = %v, document_type = %v; the explicit sign is trusted and never derived", p["gross_amount"], p["document_type"])
	}
}

func TestNegativeAmountIsNotFlippedByDocumentType(t *testing.T) {
	// A credit note delivered with a positive amount stays positive: deriving a
	// sign flip from the document type contradicts the trailer sums, which are
	// the available evidence.
	in := `[{"supplier_number":"0000105","supplier_invoice_number":"0004712","company_code":"CH10",
	  "document_type":"GU","document_date":"2026-01-15","currency":"CHF",
	  "gross_amount":"100.00","vat_amount":"0.00"}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "invoice"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if got := payload(t, r, model.InvoiceKey("0000105", "0004712"))["gross_amount"]; got != "100.00" {
		t.Errorf("gross_amount = %v, want 100.00 unchanged", got)
	}
}

func TestStandaloneInvoiceLine(t *testing.T) {
	in := "supplier_number,supplier_invoice_number,line_no,gl_account,cost_center,quantity,uom,unit_price,line_amount,tax_code,currency\n" +
		"0000105,0004711,00010,0006400,0012350,12.000,EA,45.5000,546.00,V81,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "invoice_line"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	want := model.InvoiceLineKey("0000105", "0004711", "00010")
	p := payload(t, r, want)
	if p["line_amount"] != "546.00" || p["supplier_invoice_number"] != "0004711" {
		t.Errorf("payload = %v", p)
	}
}

func TestStandaloneInvoiceLineNeedsItsInvoice(t *testing.T) {
	in := "supplier_number,supplier_invoice_number,line_no,gl_account,quantity,uom,unit_price,line_amount\n" +
		",,00010,0006400,12.000,EA,45.5000,546.00\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "invoice_line"})
	if r.Accepted {
		t.Fatal("a line with no invoice was accepted")
	}
	if !hasDiag(r, importer.CodeKeyMissing, "supplier_number") {
		t.Errorf("diagnostics = %v, want E_KEY_MISSING on supplier_number", r.Diagnostics)
	}
}

func TestAckValidation(t *testing.T) {
	tests := []struct {
		name  string
		row   string
		code  string
		field string
	}{
		{name: "posted with a document number", row: "prop-1,posted,AP-2026-0004311"},
		{name: "failed with no document number", row: "prop-1,failed,"},
		{name: "posted with no document number", row: "prop-1,posted,", code: importer.CodeFieldRequired, field: "external_document_number"},
		{name: "unknown status", row: "prop-1,accepted,AP-1", code: importer.CodeEnumUnknown, field: "status"},
		{name: "no status", row: "prop-1,,AP-1", code: importer.CodeFieldRequired, field: "status"},
		{name: "no proposal", row: ",posted,AP-1", code: importer.CodeKeyMissing, field: "proposal_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := "proposal_id,status,external_document_number\n" + tc.row + "\n"
			r := parse(t, NewCSV(), in, importer.Options{Dataset: "outbox_ack"})
			if tc.code == "" {
				if !r.Accepted {
					t.Fatalf("not accepted: %v", r.Diagnostics)
				}
				return
			}
			if r.Accepted || !hasDiag(r, tc.code, tc.field) {
				t.Fatalf("diagnostics = %v, want %s on %s", r.Diagnostics, tc.code, tc.field)
			}
		})
	}
}

func TestModelValidationReachesTheDiagnostics(t *testing.T) {
	tests := []struct {
		name    string
		dataset string
		in      string
		code    string
		field   string
	}{
		{name: "unknown currency", dataset: "supplier",
			in:   "supplier_number,name,currency\n0000417,A,XBT\n",
			code: importer.CodeCurrencyUnknown, field: "currency"},
		{name: "unknown po status", dataset: "purchase_order",
			in:   "po_number,supplier_number,company_code,currency,status,order_date\n4500001234,0000417,CH10,CHF,PARKED,2026-01-02\n",
			code: importer.CodeEnumUnknown, field: "status"},
		// PAL is a KNOWN unit that has no conversion, so it is accepted here and
		// becomes EXC_UOM_UNMAPPABLE at matching time: a business exception a
		// clerk can act on, not a parse reject. A genuinely unknown unit is
		// still rejected at ingest.
		{name: "unknown unit of measure", dataset: "purchase_order_line",
			in:   "po_number,line_no,quantity,uom,unit_price,currency,gl_account\n4500001234,00010,1.000,ZZZ,10.0000,CHF,0006400\n",
			code: importer.CodeUoMUnknown, field: "uom"},
		{name: "non-positive rate factor", dataset: "fx_rate",
			in:   "base,quote,valid_from,rate_type,rate,rate_factor,sequence,status\nEUR,CHF,2026-01-15,DAILY,0.931000,-1,1,ACTIVE\n",
			code: importer.CodeFieldInvalid, field: "rate_factor"},
		{name: "empty validity range", dataset: "cost_center",
			in:   "code,name,company_code,valid_from,valid_to\n0815,Werk Wil,CH10,2020-01-01,2020-01-01\n",
			code: importer.CodeFieldInvalid, field: "valid_to"},
		{name: "missing supplier name", dataset: "supplier",
			in:   "supplier_number,name,currency\n0000417,,CHF\n",
			code: importer.CodeFieldRequired, field: "name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := parse(t, NewCSV(), tc.in, importer.Options{Dataset: tc.dataset})
			if r.Accepted {
				t.Fatal("the file was accepted")
			}
			if !hasDiag(r, tc.code, tc.field) {
				t.Fatalf("diagnostics = %v, want %s on %s", r.Diagnostics, tc.code, tc.field)
			}
		})
	}
}

func TestFxRateFactorDefaultsToOne(t *testing.T) {
	// An absent column produces zero, and dividing by it is a division by zero
	// waiting for the first JPY invoice.
	in := "base,quote,valid_from,rate_type,rate,sequence,status\nEUR,CHF,2026-01-15,DAILY,0.931000,1,ACTIVE\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "fx_rate"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if got := payload(t, r, "EUR"+model.KeySeparator+"CHF"+model.KeySeparator+"2026-01-15"+model.KeySeparator+"DAILY")["rate_factor"]; got == nil || got.(interface{ String() string }).String() != "1" {
		t.Errorf("rate_factor = %v, want 1", got)
	}
}

func TestBooleanSpellings(t *testing.T) {
	tests := []struct {
		in      string
		want    bool
		wantErr bool
	}{
		{in: "true", want: true},
		{in: "TRUE", want: true},
		{in: "1", want: true},
		{in: "false"},
		{in: "0"},
		{in: ""},
		{in: "maybe", wantErr: true},
		{in: "yes", wantErr: true},
		{in: "J", wantErr: true},
	}
	for _, tc := range tests {
		t.Run("blocked="+tc.in, func(t *testing.T) {
			in := "supplier_number,name,currency,blocked\n0000417,A,CHF," + tc.in + "\n"
			r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
			if tc.wantErr {
				// A "blocked" column that reads "maybe" must not quietly become
				// "not blocked": posting to a blocked creditor is what the flag
				// prevents.
				if r.Accepted || !hasDiag(r, importer.CodeFieldInvalid, "blocked") {
					t.Fatalf("diagnostics = %v, want E_FIELD_INVALID on blocked", r.Diagnostics)
				}
				return
			}
			if !r.Accepted {
				t.Fatalf("not accepted: %v", r.Diagnostics)
			}
			if got := payload(t, r, "0000417")["blocked"]; got != tc.want {
				t.Errorf("blocked = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFileInfoIsRecorded(t *testing.T) {
	in := "supplier_number,name,currency\n0000417,A,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Filename: "suppliers.csv", Dataset: "supplier"})
	if r.File.Name != "suppliers.csv" || r.File.Bytes != len(in) {
		t.Errorf("FileInfo = %+v", r.File)
	}
	if r.File.SHA256 != model.Sha256Hex([]byte(in)) {
		t.Errorf("SHA256 = %q, want the digest of the source bytes", r.File.SHA256)
	}
	if r.File.Encoding != importer.EncodingUTF8 || r.File.Lines != 2 {
		t.Errorf("FileInfo = %+v, want UTF-8 and 2 lines", r.File)
	}
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
