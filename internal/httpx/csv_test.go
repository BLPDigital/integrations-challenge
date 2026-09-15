package httpx

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCSV(t *testing.T) {
	tests := []struct {
		name    string
		dialect CSVDialect
		in      string
		want    [][]string
	}{
		{"simple", CSVDialect{}, "a,b\n1,2\n", [][]string{{"a", "b"}, {"1", "2"}}},
		{"no trailing newline", CSVDialect{}, "a,b\n1,2", [][]string{{"a", "b"}, {"1", "2"}}},
		{"crlf", CSVDialect{}, "a,b\r\n1,2\r\n", [][]string{{"a", "b"}, {"1", "2"}}},
		{"empty fields", CSVDialect{}, "a,b,c\n,,\n", [][]string{{"a", "b", "c"}, {"", "", ""}}},
		{"trailing delimiter", CSVDialect{}, "a,b\n1,\n", [][]string{{"a", "b"}, {"1", ""}}},
		{"trailing delimiter at eof", CSVDialect{}, "a,b\n1,", [][]string{{"a", "b"}, {"1", ""}}},
		{"quoted delimiter", CSVDialect{}, "a\n\"x,y\"\n", [][]string{{"a"}, {"x,y"}}},
		{"doubled quote", CSVDialect{}, "a\n\"say \"\"hi\"\"\"\n", [][]string{{"a"}, {`say "hi"`}}},
		{"embedded newline", CSVDialect{}, "a\n\"one\ntwo\"\n", [][]string{{"a"}, {"one\ntwo"}}},
		{"embedded crlf", CSVDialect{}, "a\n\"one\r\ntwo\"\n", [][]string{{"a"}, {"one\r\ntwo"}}},
		{"semicolon and apostrophe quote", CSVDialect{Delimiter: ";", Quote: "'"},
			"a;b\n'x;y';2\n", [][]string{{"a", "b"}, {"x;y", "2"}}},
		{"tab delimiter", CSVDialect{Delimiter: "\t"}, "a\tb\n1\t2\n", [][]string{{"a", "b"}, {"1", "2"}}},
		{"multibyte delimiter", CSVDialect{Delimiter: "¦"}, "a¦b\n1¦2\n", [][]string{{"a", "b"}, {"1", "2"}}},
		{"empty input", CSVDialect{}, "", nil},
		{"blank line is a record", CSVDialect{}, "a\n\n", [][]string{{"a"}, {""}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := tc.dialect.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got, perr := parseCSV(tc.in, d)
			if perr != nil {
				t.Fatalf("parseCSV: %v", perr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestWriteCSVQuoting(t *testing.T) {
	tests := []struct {
		name    string
		dialect CSVDialect
		header  []string
		rows    [][]string
		want    string
	}{
		{"plain", CSVDialect{}, []string{"a", "b"}, [][]string{{"1", "2"}}, "a,b\n1,2\n"},
		{"quotes only where needed", CSVDialect{}, []string{"a"}, [][]string{{"x,y"}, {`q"q`}, {"n\nn"}, {"plain"}},
			"a\n\"x,y\"\n\"q\"\"q\"\n\"n\nn\"\nplain\n"},
		{"crlf line ending", CSVDialect{LineEnding: "CRLF"}, []string{"a"}, [][]string{{"1"}}, "a\r\n1\r\n"},
		{"exotic dialect", CSVDialect{Delimiter: ";", Quote: "'"}, []string{"a"}, [][]string{{"x;y"}, {"it's"}},
			"a\n'x;y'\n'it''s'\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := tc.dialect.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			var sb strings.Builder
			writeCSV(&sb, d, tc.header, tc.rows)
			if got := sb.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCSVNumberTransforms(t *testing.T) {
	tests := []struct {
		name        string
		dialect     CSVDialect
		cell        string
		wantCanon   string
		wantDialect string
	}{
		{"canonical dialect is identity", CSVDialect{}, "1234.56", "1234.56", "1234.56"},
		{"comma decimal", CSVDialect{DecimalSeparator: ","}, "1234,56", "1234.56", "1234,56"},
		{"swiss grouping", CSVDialect{DecimalSeparator: ",", ThousandsSeparator: "'"}, "12'345,67", "12345.67", "12345,67"},
		{"negative", CSVDialect{DecimalSeparator: ","}, "-0,05", "-0.05", "-0,05"},
		{"integer keeps leading zeros", CSVDialect{DecimalSeparator: ","}, "0000417", "0000417", "0000417"},
		{"not a number is untouched", CSVDialect{DecimalSeparator: ","}, "PO-004417", "PO-004417", "PO-004417"},
		{"two decimal separators", CSVDialect{DecimalSeparator: ","}, "1,2,3", "1,2,3", "1,2,3"},
		// A dot is not a decimal point in a comma dialect, so decoding leaves
		// it alone; encoding a canonical decimal always produces the dialect
		// form.
		{"dot in a comma dialect", CSVDialect{DecimalSeparator: ","}, "1.5", "1.5", "1,5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := tc.dialect.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got, _ := numberFromDialect(tc.cell, d)
			if got != tc.wantCanon {
				t.Errorf("numberFromDialect(%q) = %q, want %q", tc.cell, got, tc.wantCanon)
			}
			back, _ := numberToDialect(tc.wantCanon, d)
			if back != tc.wantDialect {
				t.Errorf("numberToDialect(%q) = %q, want %q", tc.wantCanon, back, tc.wantDialect)
			}
		})
	}
}

func TestCSVDateTransforms(t *testing.T) {
	tests := []struct {
		format string
		wire   string
		canon  string
	}{
		{"RFC3339", "2026-03-29", "2026-03-29"},
		{"DD.MM.YYYY", "29.03.2026", "2026-03-29"},
		{"DD/MM/YYYY", "29/03/2026", "2026-03-29"},
		{"MM/DD/YYYY", "03/29/2026", "2026-03-29"},
		{"YYYYMMDD", "20260329", "2026-03-29"},
	}
	for _, tc := range tests {
		t.Run(tc.format, func(t *testing.T) {
			d, err := CSVDialect{DateFormat: tc.format}.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got, _ := dateFromDialect(tc.wire, d); got != tc.canon {
				t.Errorf("dateFromDialect(%q) = %q, want %q", tc.wire, got, tc.canon)
			}
			if got, _ := dateToDialect(tc.canon, d); got != tc.wire {
				t.Errorf("dateToDialect(%q) = %q, want %q", tc.canon, got, tc.wire)
			}
			// A cell that is not a date in the declared format passes through.
			if got, ok := dateFromDialect("not-a-date", d); ok || got != "not-a-date" {
				t.Errorf("dateFromDialect(non-date) = %q, %v", got, ok)
			}
		})
	}
}

func TestCSVDialectDefaults(t *testing.T) {
	d, err := CSVDialect{}.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if d.delim != ',' || d.quote != '"' || d.crlf || d.dec != "." || d.dateFmt != "RFC3339" {
		t.Errorf("zero dialect resolved to %+v", d)
	}
	spelled, err := DefaultCSVDialect().resolve()
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if !reflect.DeepEqual(d, spelled) {
		t.Errorf("zero dialect %+v differs from DefaultCSVDialect %+v", d, spelled)
	}
}

func TestCSVDialectRejections(t *testing.T) {
	tests := []struct {
		name    string
		dialect CSVDialect
		reason  string
	}{
		{"same delimiter and quote", CSVDialect{Delimiter: ",", Quote: ","}, "csv_dialect_unsupported"},
		{"unsupported escape", CSVDialect{Escape: "backslash"}, "csv_dialect_unsupported"},
		{"header off", CSVDialect{Header: boolPtr(false)}, "csv_header_required"},
		{"line ending", CSVDialect{LineEnding: "CR"}, "csv_dialect_unsupported"},
		{"decimal separator", CSVDialect{DecimalSeparator: "'"}, "csv_dialect_unsupported"},
		{"thousands equals decimal", CSVDialect{DecimalSeparator: ",", ThousandsSeparator: ","}, "csv_dialect_unsupported"},
		{"trim", CSVDialect{Trim: "middle"}, "csv_dialect_unsupported"},
		{"date format", CSVDialect{DateFormat: "TTMMJJ"}, "csv_dialect_unsupported"},
		{"multi rune quote", CSVDialect{Quote: "ab"}, "csv_dialect_unsupported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.dialect.resolve()
			if err == nil {
				t.Fatal("want an error")
			}
			if err.Details["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", err.Details["reason"], tc.reason)
			}
		})
	}
}

func TestCSVTrim(t *testing.T) {
	tests := []struct {
		trim string
		want string
	}{
		{"", "  x  "},
		{"none", "  x  "},
		{"both", "x"},
		{"left", "x  "},
		{"right", "  x"},
	}
	for _, tc := range tests {
		t.Run(tc.trim, func(t *testing.T) {
			d, err := CSVDialect{Trim: tc.trim}.resolve()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got := d.trimCell("  x  "); got != tc.want {
				t.Errorf("trimCell = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsPlainDecimal(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"0", true},
		{"0000417", true},
		{"-1", true},
		{"1.5", true},
		{"-0.05", true},
		{"", false},
		{"-", false},
		{".5", false},
		{"5.", false},
		{"1.2.3", false},
		{"1'234", false},
		{"1e5", false},
		{"+1", false},
		{"12345678901234567890123", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isPlainDecimal(tc.in); got != tc.want {
				t.Errorf("isPlainDecimal(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsXMLName(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"a", true},
		{"supplier_number", true},
		{"a-b.c1", true},
		{"", false},
		{"1a", false},
		{"a b", false},
		{"a:b", false},
		{"amount(chf)", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isXMLName(tc.in); got != tc.want {
				t.Errorf("isXMLName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
