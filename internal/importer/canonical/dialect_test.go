package canonical

import (
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// boolPtr returns a pointer to b, for the dialect's tri-state header field.
func boolPtr(b bool) *bool { return &b }

func TestCSVHonoursTheDeclaredDialect(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		dialect httpx.CSVDialect
		want    map[string]any
	}{
		{
			name:    "semicolon delimiter",
			in:      "code;name;company_code;valid_from\n0815;Werk Wil;CH10;2020-01-01\n",
			dialect: httpx.CSVDialect{Delimiter: ";"},
			want:    map[string]any{"code": "0815", "name": "Werk Wil"},
		},
		{
			name:    "tab delimiter",
			in:      "code\tname\tcompany_code\tvalid_from\n0815\tWerk Wil\tCH10\t2020-01-01\n",
			dialect: httpx.CSVDialect{Delimiter: "\t"},
			want:    map[string]any{"code": "0815"},
		},
		{
			name:    "pipe quote",
			in:      "code,name,company_code,valid_from\n0815,|Werk, Wil|,CH10,2020-01-01\n",
			dialect: httpx.CSVDialect{Quote: "|"},
			want:    map[string]any{"name": "Werk, Wil"},
		},
		{
			name:    "crlf line endings are read whatever is declared",
			in:      "code,name,company_code,valid_from\r\n0815,Werk Wil,CH10,2020-01-01\r\n",
			dialect: httpx.CSVDialect{LineEnding: "LF"},
			want:    map[string]any{"name": "Werk Wil"},
		},
		{
			name:    "trim both",
			in:      "code,name,company_code,valid_from\n  0815  ,  Werk Wil  ,CH10,2020-01-01\n",
			dialect: httpx.CSVDialect{Trim: "both"},
			want:    map[string]any{"code": "0815", "name": "Werk Wil"},
		},
		{
			name:    "null token",
			in:      "code,name,company_code,valid_from,valid_to\n0815,Werk Wil,CH10,2020-01-01,\\N\n",
			dialect: httpx.CSVDialect{NullToken: `\N`},
			want:    map[string]any{"valid_to": ""},
		},
		{
			name:    "declared date format",
			in:      "code,name,company_code,valid_from\n0815,Werk Wil,CH10,01.01.2020\n",
			dialect: httpx.CSVDialect{DateFormat: "DD.MM.YYYY"},
			want:    map[string]any{"valid_from": "2020-01-01"},
		},
		{
			name:    "compact date format",
			in:      "code,name,company_code,valid_from\n0815,Werk Wil,CH10,20200101\n",
			dialect: httpx.CSVDialect{DateFormat: "YYYYMMDD"},
			want:    map[string]any{"valid_from": "2020-01-01"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := parse(t, NewCSV(), tc.in, importer.Options{Dataset: "cost_center", Dialect: tc.dialect})
			if !r.Accepted {
				t.Fatalf("not accepted: %v", r.Diagnostics)
			}
			p := payload(t, r, "0815")
			for k, want := range tc.want {
				if p[k] != want {
					t.Errorf("%s = %v, want %v", k, p[k], want)
				}
			}
		})
	}
}

func TestCSVMoneyUsesTheDeclaredSeparators(t *testing.T) {
	tests := []struct {
		name    string
		amount  string
		dialect httpx.CSVDialect
		want    string
		code    string
	}{
		{name: "apostrophe grouping", amount: "1'345.63", want: "1345.63"},
		{name: "typographic apostrophe", amount: "1’345.63", want: "1345.63"},
		{name: "currency prefix", amount: "CHF 1'345.63", want: "1345.63"},
		{name: "trailing minus", amount: "1345.63-", want: "-1345.63"},
		{name: "accounting parentheses", amount: "(1345.63)", want: "-1345.63"},
		{name: "swiss shorthand", amount: "1'345.-", want: "1345.00"},
		{name: "comma decimal separator",
			amount: "1'345,63", dialect: httpx.CSVDialect{DecimalSeparator: ","}, want: "1345.63"},
		{name: "declared dot grouping",
			amount: "1.345,63", dialect: httpx.CSVDialect{DecimalSeparator: ",", ThousandsSeparator: "."}, want: "1345.63"},

		{name: "us grouping is rejected", amount: "1,345.63", code: importer.CodeDecimalFormat},
		{name: "three fraction digits are rejected, never rounded", amount: "1345.634", code: importer.CodeMoneyScale},
		{name: "a word is rejected", amount: "n/a", code: importer.CodeDecimalFormat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.dialect
			// A semicolon delimiter throughout, so that a comma decimal
			// separator is a property of the amount and not of the framing.
			d.Delimiter = ";"
			zero := "0.00"
			if d.DecimalSeparator == "," {
				zero = "0,00"
			}
			in := "supplier_number;supplier_invoice_number;company_code;document_type;document_date;currency;gross_amount;vat_amount\n" +
				"0000105;0004711;CH10;RE;2026-01-15;CHF;" + tc.amount + ";" + zero + "\n"
			r := parse(t, NewCSV(), in, importer.Options{Dataset: "invoice", Dialect: d})
			if tc.code != "" {
				if !hasDiag(r, tc.code, "gross_amount") {
					t.Fatalf("want %s on gross_amount, got %v", tc.code, r.Diagnostics)
				}
				if r.Accepted {
					t.Error("a file with a rejected record was accepted")
				}
				if len(r.Documents) != 0 {
					t.Error("a rejected record produced a document")
				}
				return
			}
			if !r.Accepted {
				t.Fatalf("not accepted: %v", r.Diagnostics)
			}
			p := payload(t, r, model.InvoiceKey("0000105", "0004711"))
			if p["gross_amount"] != tc.want {
				t.Errorf("gross_amount = %v, want %v", p["gross_amount"], tc.want)
			}
		})
	}
}

func TestUndeclaredDatePatternIsNeverGuessed(t *testing.T) {
	in := "code,name,company_code,valid_from\n0815,Werk Wil,CH10,01.01.2020\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "cost_center"})
	if !hasDiag(r, importer.CodeDateFormat, "valid_from") {
		t.Fatalf("want E_DATE_FORMAT on valid_from, got %v", r.Diagnostics)
	}
	if r.Accepted || len(r.Documents) != 0 {
		t.Error("a record with an unreadable date was accepted")
	}
}

func TestBadDialectIsReportedOnceAtFileLevel(t *testing.T) {
	tests := []struct {
		name    string
		dialect httpx.CSVDialect
		field   string
	}{
		{name: "two-character delimiter", dialect: httpx.CSVDialect{Delimiter: ";;"}, field: "delimiter"},
		{name: "delimiter equal to quote", dialect: httpx.CSVDialect{Delimiter: `"`}, field: "quote"},
		{name: "backslash escape", dialect: httpx.CSVDialect{Escape: "backslash"}, field: "escape"},
		{name: "unknown line ending", dialect: httpx.CSVDialect{LineEnding: "CR"}, field: "line_ending"},
		{name: "unknown decimal separator", dialect: httpx.CSVDialect{DecimalSeparator: "'"}, field: "decimal_separator"},
		{name: "grouping equal to the decimal separator", dialect: httpx.CSVDialect{ThousandsSeparator: "."}, field: "thousands_separator"},
		{name: "unknown trim", dialect: httpx.CSVDialect{Trim: "outer"}, field: "trim"},
		{name: "unknown date format", dialect: httpx.CSVDialect{DateFormat: "TTMMJJ"}, field: "date_format"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := "code,name,company_code,valid_from\n0815,Werk Wil,CH10,2020-01-01\n"
			r := parse(t, NewCSV(), in, importer.Options{Dataset: "cost_center", Dialect: tc.dialect})
			if r.Accepted {
				t.Fatal("a file with an unusable dialect was accepted")
			}
			if len(r.Diagnostics) != 1 {
				t.Fatalf("got %d diagnostics, want exactly one file-level finding: %v", len(r.Diagnostics), r.Diagnostics)
			}
			d := r.Diagnostics[0]
			if d.Severity != importer.SeverityFatal || d.Field != tc.field || d.Line != 0 {
				t.Errorf("diagnostic = %+v, want a fatal file-level finding on %s", d, tc.field)
			}
		})
	}
}

func TestHeaderlessCSVIsRejected(t *testing.T) {
	r := parse(t, NewCSV(), "0815,Werk Wil,CH10,2020-01-01\n",
		importer.Options{Dataset: "cost_center", Dialect: httpx.CSVDialect{Header: boolPtr(false)}})
	if r.Accepted || len(r.Diagnostics) != 1 || r.Diagnostics[0].Field != "header" {
		t.Fatalf("diagnostics = %v, want one fatal finding on header", r.Diagnostics)
	}
}

func TestHeaderDefects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "duplicate column", in: "code,code,name\n0815,0815,x\n", want: "code"},
		{name: "empty column name", in: "code,,name\n0815,,x\n", want: "column 2"},
		{name: "no header at all", in: "\n", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := parse(t, NewCSV(), tc.in, importer.Options{Dataset: "cost_center"})
			if r.Accepted {
				t.Fatal("accepted a file whose header does not name its fields")
			}
			if len(r.Diagnostics) == 0 || r.Diagnostics[0].Severity != importer.SeverityFatal {
				t.Fatalf("diagnostics = %v, want a fatal header finding", r.Diagnostics)
			}
			if r.Diagnostics[0].Field != tc.want {
				t.Errorf("field = %q, want %q", r.Diagnostics[0].Field, tc.want)
			}
		})
	}
}

func TestMissingOrUnknownDatasetIsFatal(t *testing.T) {
	in := "code,name,company_code,valid_from\n0815,Werk Wil,CH10,2020-01-01\n"
	for _, tc := range []struct {
		name    string
		dataset string
		code    string
	}{
		{name: "absent", dataset: "", code: importer.CodeFieldRequired},
		{name: "unknown", dataset: "creditors", code: importer.CodeEnumUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parse(t, NewCSV(), in, importer.Options{Dataset: tc.dataset})
			if r.Accepted || !hasDiag(r, tc.code, "dataset") {
				t.Fatalf("diagnostics = %v, want %s on dataset", r.Diagnostics, tc.code)
			}
		})
	}
}

func TestUnknownColumnIsAWarningNotARejection(t *testing.T) {
	// An extra column is a sender adding a field. Dropping twelve thousand
	// supplier records over it turns a compatible change into an outage.
	in := "supplier_number,name,currency,legacy_id\n0000417,Steinbach,CHF,417\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if !hasDiag(r, importer.CodeFieldUnknown, "legacy_id") {
		t.Fatalf("want a warning on legacy_id, got %v", r.Diagnostics)
	}
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeFieldUnknown && d.Severity != importer.SeverityWarn {
			t.Errorf("unknown field %q reported as %s, want warn", d.Field, d.Severity)
		}
	}
	if p := payload(t, r, "0000417"); p["legacy_id"] != nil {
		t.Error("the unknown field reached the stored payload")
	}
}

func TestEncodings(t *testing.T) {
	// The name column carries "Zürich – 1’234 €": an umlaut from Latin-1 and
	// three characters that exist only in the windows-1252 C1 range.
	header := "supplier_number,name,currency\n"
	cp1252 := append([]byte(header), 0x30, 0x30, 0x30, 0x30, 0x34, 0x31, 0x37, ',')
	cp1252 = append(cp1252, 'Z', 0xFC, 'r', 'i', 'c', 'h', ' ', 0x96, ' ', '1', 0x92, '2', '3', '4', ' ', 0x80)
	cp1252 = append(cp1252, ',', 'C', 'H', 'F', '\n')

	r := parse(t, NewCSV(), string(cp1252), importer.Options{Dataset: "supplier", Encoding: importer.EncodingCP1252})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if got := payload(t, r, "0000417")["name"]; got != "Zürich – 1’234 €" {
		t.Errorf("name = %q, want %q", got, "Zürich – 1’234 €")
	}

	// The same bytes read as ISO 8859-1 decode without error and produce
	// mojibake, which is the documented consequence of a sender declaring
	// Latin-1 for a file their Windows system wrote as windows-1252.
	r = parse(t, NewCSV(), string(cp1252), importer.Options{Dataset: "supplier", Encoding: importer.EncodingLatin1})
	if !r.Accepted {
		t.Fatalf("ISO 8859-1 defines every byte, so this must parse: %v", r.Diagnostics)
	}
	if got := payload(t, r, "0000417")["name"]; got == "Zürich – 1’234 €" {
		t.Error("ISO 8859-1 produced the windows-1252 reading; the two encodings are being confused")
	}
}

func TestInvalidUTF8IsReportedNotReplaced(t *testing.T) {
	in := "supplier_number,name,currency\n0000417,Z\xC3\x28rich,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier", Encoding: importer.EncodingUTF8})
	if r.Accepted {
		t.Fatal("a file with an undecodable byte was accepted")
	}
	found := false
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeEncoding {
			found = true
			if d.Severity != importer.SeverityFatal {
				t.Errorf("E_ENCODING severity = %s, want fatal for a stream that cannot be decoded further", d.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("want E_ENCODING, got %v", r.Diagnostics)
	}
	for _, doc := range r.Documents {
		if containsRune(string(doc.Payload), '�') {
			t.Error("the parser emitted U+FFFD; an invalid byte is reported, never replaced")
		}
	}
}

func TestUndefinedCP1252ByteIsReported(t *testing.T) {
	in := "supplier_number,name,currency\n0000417,Z\x81rich,CHF\n"
	r := parse(t, NewCSV(), in, importer.Options{Dataset: "supplier", Encoding: importer.EncodingCP1252})
	if r.Accepted || !hasDiag(r, importer.CodeEncoding, "") {
		t.Fatalf("diagnostics = %v, want E_ENCODING for an undefined windows-1252 position", r.Diagnostics)
	}
}

func TestUnknownDeclaredEncodingIsFatal(t *testing.T) {
	r := parse(t, NewCSV(), "supplier_number,name\n0000417,x\n",
		importer.Options{Dataset: "supplier", Encoding: "EBCDIC"})
	if r.Accepted || !hasDiag(r, importer.CodeEncoding, "encoding") {
		t.Fatalf("diagnostics = %v, want a fatal finding on encoding", r.Diagnostics)
	}
}

// containsRune reports whether s contains r.
func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func TestWrongJSONTypesInScalarFields(t *testing.T) {
	// JSON is the only one of the three formats where a field can be the wrong
	// type. Each wrong type gets a code that says what the field is, so a sender
	// can fix their serializer rather than guess.
	tests := []struct {
		name  string
		field string
		value string
		code  string
	}{
		{name: "a number in a text field", field: "name", value: `417`, code: importer.CodeFieldInvalid},
		{name: "an object in a text field", field: "name", value: `{"a":1}`, code: importer.CodeFieldInvalid},
		{name: "an array in a text field", field: "name", value: `["a"]`, code: importer.CodeFieldInvalid},
		{name: "a boolean in a text field", field: "name", value: `true`, code: importer.CodeFieldInvalid},
		{name: "a text in an integer field", field: "payment_terms_days", value: `"thirty"`, code: importer.CodeNumberFormat},
		{name: "an object in an integer field", field: "payment_terms_days", value: `{}`, code: importer.CodeNumberFormat},
		{name: "a number in a boolean field", field: "blocked", value: `2`, code: importer.CodeFieldInvalid},
		{name: "an object in a date field", field: "valid_from", value: `{}`, code: importer.CodeDateFormat},
		{name: "a boolean key", field: "supplier_number", value: `true`, code: importer.CodeKeyNotString},
		{name: "an object key", field: "supplier_number", value: `{"n":417}`, code: importer.CodeKeyNotString},
		{name: "an array key", field: "supplier_number", value: `[417]`, code: importer.CodeKeyNotString},
		{name: "an object in a wide integer field", field: "change_seq", value: `{}`, code: importer.CodeNumberFormat},
		{name: "a grouped wide integer", field: "change_seq", value: `"118'422"`, code: importer.CodeNumberFormat},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dataset, base := "supplier", `"name":"A","currency":"CHF"`
			if tc.field == "valid_from" {
				dataset, base = "cost_center", `"code":"0815","name":"A","company_code":"CH10"`
			}
			if dataset == "supplier" && tc.field != "supplier_number" {
				base = `"supplier_number":"0000417",` + base
			}
			in := `[{` + base + `,"` + tc.field + `":` + tc.value + `}]`
			r := parse(t, NewJSON(), in, importer.Options{Dataset: dataset})
			if r.Accepted || !hasDiag(r, tc.code, tc.field) {
				t.Fatalf("diagnostics = %v, want %s on %s", r.Diagnostics, tc.code, tc.field)
			}
		})
	}
}

func TestBooleanFromJSONAndText(t *testing.T) {
	// A JSON boolean and the text "true" mean the same thing, so the two
	// channels agree.
	js := parse(t, NewJSON(), `[{"supplier_number":"0000417","name":"A","currency":"CHF","blocked":true}]`,
		importer.Options{Dataset: "supplier"})
	csv := parse(t, NewCSV(), "supplier_number,name,currency,blocked\n0000417,A,CHF,true\n",
		importer.Options{Dataset: "supplier"})
	if string(js.Documents[0].Payload) != string(csv.Documents[0].Payload) {
		t.Errorf("a JSON boolean and the text \"true\" produced different records:\n %s\n %s",
			js.Documents[0].Payload, csv.Documents[0].Payload)
	}
}

func TestJSONNullIsAnAbsentValue(t *testing.T) {
	// A canonical record has no nulls: an absent value is an empty string, so an
	// absent field and a null field hash identically.
	withNull := parse(t, NewJSON(), `[{"supplier_number":"0000417","name":"A","currency":"CHF","country":null,"iban":null}]`,
		importer.Options{Dataset: "supplier"})
	without := parse(t, NewJSON(), `[{"supplier_number":"0000417","name":"A","currency":"CHF"}]`,
		importer.Options{Dataset: "supplier"})
	if !withNull.Accepted {
		t.Fatalf("not accepted: %v", withNull.Diagnostics)
	}
	if string(withNull.Documents[0].Payload) != string(without.Documents[0].Payload) {
		t.Errorf("a null field and an absent field produced different records:\n %s\n %s",
			withNull.Documents[0].Payload, without.Documents[0].Payload)
	}
}

func TestNonObjectArrayElementIsRejected(t *testing.T) {
	r := parse(t, NewJSON(), `[{"supplier_number":"0000417","name":"A","currency":"CHF"},"not a record",42]`,
		importer.Options{Dataset: "supplier"})
	if r.Accepted {
		t.Error("accepted an array carrying values that are not records")
	}
	if len(r.Documents) != 1 {
		t.Errorf("%d documents, want the one real record", len(r.Documents))
	}
	n := 0
	for _, d := range r.Diagnostics {
		if d.Code == importer.CodeFieldInvalid {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d findings, want one per non-record element", n)
	}
}

func TestEmbeddedLinesMustBeObjects(t *testing.T) {
	in := `[{"supplier_number":"0000105","supplier_invoice_number":"0004711","company_code":"CH10",
	  "document_type":"RE","document_date":"2026-01-15","currency":"CHF",
	  "gross_amount":"100.00","vat_amount":"0.00","lines":"00010"}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "invoice"})
	if r.Accepted || !hasDiag(r, importer.CodeFieldInvalid, "lines") {
		t.Fatalf("diagnostics = %v, want E_FIELD_INVALID on lines", r.Diagnostics)
	}
}

func TestUnknownFieldInsideAnEmbeddedLineCarriesItsPath(t *testing.T) {
	in := `[{"supplier_number":"0000105","supplier_invoice_number":"0004711","company_code":"CH10",
	  "document_type":"RE","document_date":"2026-01-15","currency":"CHF",
	  "gross_amount":"100.00","vat_amount":"0.00",
	  "lines":[{"line_no":"00010","gl_account":"0006400","cost_center":"0012350","quantity":"1.000",
	            "uom":"EA","unit_price":"100.0000","line_amount":"100.00","delivery_note":"LS-1"}]}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "invoice"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if !hasDiag(r, importer.CodeFieldUnknown, "lines[0].delivery_note") {
		t.Fatalf("diagnostics = %v, want the finding to carry the line's path", r.Diagnostics)
	}
}

func TestRejectedEmbeddedLineRejectsTheInvoice(t *testing.T) {
	// An invoice is one document. A line whose amount cannot be read makes the
	// invoice's own total unverifiable, so the invoice is rejected rather than
	// stored with a line missing.
	in := `[{"supplier_number":"0000105","supplier_invoice_number":"0004711","company_code":"CH10",
	  "document_type":"RE","document_date":"2026-01-15","currency":"CHF",
	  "gross_amount":"100.00","vat_amount":"0.00",
	  "lines":[{"line_no":"00010","gl_account":"0006400","cost_center":"0012350","quantity":"1.000",
	            "uom":"EA","unit_price":"100.0000","line_amount":"1,00.00"}]}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "invoice"})
	if r.Accepted {
		t.Error("the invoice was accepted with an unreadable line amount")
	}
	if len(r.Documents) != 0 {
		t.Error("a rejected invoice produced a document")
	}
	if !hasDiag(r, importer.CodeDecimalFormat, "lines[0].line_amount") {
		t.Fatalf("diagnostics = %v, want the finding on the line's amount", r.Diagnostics)
	}
}

func TestObjectInADecimalFieldIsRejected(t *testing.T) {
	in := `[{"base":"EUR","quote":"CHF","valid_from":"2026-01-15","rate_type":"DAILY","rate":{"v":"0.931"},"rate_factor":1,"sequence":1,"status":"ACTIVE"}]`
	r := parse(t, NewJSON(), in, importer.Options{Dataset: "fx_rate"})
	if r.Accepted || !hasDiag(r, importer.CodeDecimalFormat, "rate") {
		t.Fatalf("diagnostics = %v, want E_DECIMAL_FORMAT on rate", r.Diagnostics)
	}
}
