package kredexp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer/kredexp"
)

// The records the tests build on: a valid one-document file. Every test states
// its input as raw byte strings rather than building it from a model, because
// what is under test is the reading of bytes, and a helper that renders records
// would test the helper's idea of the format instead of the document's.
const (
	vorlauf  = "VORLAUF;2.1;0100;3101260612;CHF;SUBSG01;BLP;P"
	kopf     = "KOPF;0004711;0000105;RE;150126;180126;;CHF;546.00;0.00;V81;30;;;0012340;Wartung"
	pos      = "POS;0004711;00010;0006400;0012340;12.000;STK;45.5000;546.00;;Ersatzteile"
	nachlauf = "NACHLAUF;1;1;546.00;546.00"
)

// file joins records with CRLF and terminates the last one, which is what the
// sender writes. The strings may carry raw windows-1252 bytes as \x escapes:
// that is the point of taking strings rather than runes.
func file(records ...string) []byte {
	return []byte(strings.Join(records, "\r\n") + "\r\n")
}

// A finding is the comparable part of a diagnostic: the tuple the golden
// projection compares, plus the severity, because a code's severity is part of
// this format's published contract and a projection does not carry it.
type finding struct {
	Code     string
	Severity importer.Severity
	Line     int
	Field    string
}

// findings returns the diagnostics of a result as comparable tuples, in the
// order the framework sorted them.
func findings(res *importer.Result) []finding {
	out := make([]finding, 0, len(res.Diagnostics))
	for _, d := range res.Diagnostics {
		out = append(out, finding{Code: d.Code, Severity: d.Severity, Line: d.Line, Field: d.Field})
	}
	return out
}

// parse parses raw with the importer and fails the test on the
// contract violations that are never data defects.
func parse(t *testing.T, raw []byte) *importer.Result {
	t.Helper()
	res, err := kredexp.New().Parse(context.Background(), raw, importer.Options{
		Filename: "KRED_0100_20260131_001.txt",
		Encoding: importer.EncodingCP1252,
	})
	if err != nil {
		t.Fatalf("Parse returned an error for a data defect: %v", err)
	}
	if res == nil {
		t.Fatal("Parse returned a nil Result and a nil error")
	}
	if res.Documents == nil || res.Diagnostics == nil {
		t.Error("Documents and Diagnostics must be non-nil so they encode as [] and never as null")
	}
	if res.Format != kredexp.ID {
		t.Errorf("Result.Format = %q, want %q", res.Format, kredexp.ID)
	}
	return res
}

func TestIDAndRegistration(t *testing.T) {
	if kredexp.ID != "kredexp-2.1" {
		t.Fatalf("ID = %q; a manifest names this exact string as its profile", kredexp.ID)
	}
	f, ok := importer.Lookup(kredexp.ID)
	if !ok {
		t.Fatal("the format did not register itself")
	}
	if f.ID() != kredexp.ID {
		t.Fatalf("the registry holds %q under %q", f.ID(), kredexp.ID)
	}
}

func TestParseRejectsANilContext(t *testing.T) {
	//lint:ignore SA1012 passing a nil context is what is under test.
	if _, err := kredexp.New().Parse(nil, file(vorlauf, kopf, pos, nachlauf), importer.Options{}); err == nil {
		t.Fatal("Parse accepted a nil context")
	}
}

func TestParseReportsACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := kredexp.New().Parse(ctx, file(vorlauf, kopf, pos, nachlauf), importer.Options{}); err == nil {
		t.Fatal("Parse accepted a cancelled context; a cancelled parse is an environment fault, not a data defect")
	}
}

func TestParseNeverReturnsAnErrorForBadData(t *testing.T) {
	// The load-bearing rule of the framework: a defect in the file is a
	// diagnostic, never an error, so one bad file cannot take a batch down.
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"not this format at all", []byte("{\"supplier_number\":\"0000417\"}\n")},
		{"a byte order mark", append(append([]byte{}, importer.UTF8BOM...), file(vorlauf, kopf, pos, nachlauf)...)},
		{"an undefined windows-1252 byte", file(vorlauf, "KOPF;\x81")},
		{"an unterminated quote", file(vorlauf, "KOPF;0004711;\"never closed")},
		{"only a trailer", file(nachlauf)},
		{"binary", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}},
		{"one very long unterminated quoted field", file(vorlauf, "KOPF;\""+strings.Repeat("x", 4096))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := parse(t, tc.raw)
			if res.Accepted {
				t.Error("a file this broken must not be Accepted")
			}
			if len(res.Diagnostics) == 0 {
				t.Error("a rejected file must say why")
			}
		})
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want bool
	}{
		{"a Vorlaufsatz", file(vorlauf, kopf, pos, nachlauf), true},
		{"a Vorlaufsatz of the unsupported v2.0", []byte("VORLAUF;2.0;0100;3101260612;CHF;SUBSG01;BLP;P\r\n"), true},
		{"a Vorlaufsatz behind a byte order mark", append(append([]byte{}, importer.UTF8BOM...), []byte(vorlauf)...), true},
		{"the record type alone", []byte("VORLAUF"), false},
		{"a canonical semicolon CSV header", []byte("supplier_number;name;country\r\n0000417;ACME;CH\r\n"), false},
		{"an XML prolog", []byte("<?xml version=\"1.0\" encoding=\"windows-1252\"?><records/>"), false},
		{"NDJSON", []byte("{\"supplier_number\":\"0000417\"}\n"), false},
		{"a Kopfsatz without its Vorlaufsatz", []byte(kopf), false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kredexp.New().Detect(importer.Head(tc.head), importer.Options{}); got != tc.want {
				t.Errorf("Detect = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestFileLevelFatalPrecedence(t *testing.T) {
	// BUILD-SPEC 10 fixes the order and the first one found wins. The order is a
	// precedence and not a position: an undecodable byte anywhere in the file
	// outranks a missing Vorlaufsatz on line 1, because a file whose bytes
	// cannot be read has not been read.
	cases := []struct {
		name string
		raw  []byte
		want finding
	}{
		{
			name: "a byte order mark outranks everything after it",
			raw:  append(append([]byte{}, importer.UTF8BOM...), file("KOPF;\x8d", "POS;\"unterminated")...),
			want: finding{kredexp.CodeEncodingBOMPresent, importer.SeverityFatal, 1, ""},
		},
		{
			name: "an undefined byte outranks a missing Vorlaufsatz",
			raw:  file("KOPF;0004711;\x8d", "POS;\"unterminated"),
			want: finding{kredexp.CodeEncodingInvalidByte, importer.SeverityFatal, 1, ""},
		},
		{
			name: "an undefined byte on the last line outranks a missing Vorlaufsatz on the first",
			raw:  file("KOPF;0004711", pos, "NACHLAUF;1;1;546.00;546.00\x90"),
			want: finding{kredexp.CodeEncodingInvalidByte, importer.SeverityFatal, 3, ""},
		},
		{
			name: "a control byte is an undefined byte too",
			raw:  file(vorlauf, "KOPF;0004711;\x00;RE", pos, nachlauf),
			want: finding{kredexp.CodeEncodingInvalidByte, importer.SeverityFatal, 2, ""},
		},
		{
			name: "unresolved quoting outranks a missing Vorlaufsatz",
			raw:  file("KOPF;0004711;\"never closed"),
			want: finding{kredexp.CodeMalformedQuoting, importer.SeverityFatal, 1, ""},
		},
		{
			name: "a missing Vorlaufsatz outranks an unsupported version elsewhere",
			raw:  file(kopf, "VORLAUF;9.9;0100;3101260612;CHF;SUBSG01;BLP;P", nachlauf),
			want: finding{kredexp.CodeVorlaufMissing, importer.SeverityFatal, 1, "Satzart"},
		},
		{
			name: "an empty file has no Vorlaufsatz",
			raw:  nil,
			want: finding{kredexp.CodeVorlaufMissing, importer.SeverityFatal, 0, ""},
		},
		{
			name: "an unsupported version outranks a missing trailer",
			raw:  file("VORLAUF;2.0;0100;3101260612;CHF;SUBSG01;BLP;P", kopf),
			want: finding{kredexp.CodeFormatVersionUnsupported, importer.SeverityFatal, 1, "Formatversion"},
		},
		{
			name: "a missing trailer",
			raw:  file(vorlauf, kopf, pos),
			want: finding{kredexp.CodeTrailerMissing, importer.SeverityFatal, 0, "Satzart"},
		},
		{
			name: "a trailer that is not the last record",
			raw:  file(vorlauf, kopf, nachlauf, pos),
			want: finding{kredexp.CodeTrailerMissing, importer.SeverityFatal, 0, "Satzart"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := parse(t, tc.raw)
			if got := findings(res); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("findings = %+v, want exactly [%+v]", got, tc.want)
			}
			if len(res.Documents) != 0 {
				t.Errorf("a fatal file-level finding yields no documents, got %d", len(res.Documents))
			}
			if res.Accepted {
				t.Error("Accepted must be false")
			}
		})
	}
}

func TestEncodingOfEveryByte(t *testing.T) {
	// The whole windows-1252 table, one byte at a time, in a text field. The
	// five undefined positions and the control bytes are findings; everything
	// else decodes, and 0x80..0x9F decodes to the typographic set rather than
	// to the Latin-1 C1 controls.
	undefined := map[byte]bool{0x81: true, 0x8D: true, 0x8F: true, 0x90: true, 0x9D: true}
	for b := 0; b <= 0xFF; b++ {
		by := byte(b)
		control := by < 0x20 || by == 0x7F
		if by == '\t' {
			control = false
		}
		// CR, LF, the separator and the delimiter are structural rather than
		// text, and are covered by the quoting and dispatch tests.
		if by == '\r' || by == '\n' || by == ';' || by == '"' {
			continue
		}
		// The byte sits between two letters: canonical JSON trims a string's
		// leading and trailing whitespace, so a TAB at the edge of a field would
		// disappear for a reason that has nothing to do with the encoding.
		raw := file(vorlauf, "KOPF;0004711;0000105;RE;150126;180126;;CHF;546.00;0.00;V81;30;;;0012340;T"+string([]byte{by})+"U", pos, nachlauf)
		res := parse(t, raw)
		want := undefined[by] || control
		got := len(res.Diagnostics) == 1 && res.Diagnostics[0].Code == kredexp.CodeEncodingInvalidByte
		if got != want {
			t.Fatalf("byte 0x%02X: invalid = %t, want %t (diagnostics %+v)", by, got, want, findings(res))
		}
		if want {
			continue
		}
		r, ok := importer.CP1252Rune(by)
		if !ok {
			t.Fatalf("byte 0x%02X decoded but the framework calls it undefined", by)
		}
		if text := documentText(t, res); text != "T"+string(r)+"U" {
			t.Fatalf("byte 0x%02X decoded to %q, want %q", by, text, "T"+string(r)+"U")
		}
	}
}

// documentText returns the Text of the single document of a result.
func documentText(t *testing.T, res *importer.Result) string {
	t.Helper()
	if len(res.Documents) != 1 {
		t.Fatalf("want exactly one document, got %d", len(res.Documents))
	}
	var inv struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(res.Documents[0].Payload, &inv); err != nil {
		t.Fatalf("decoding the payload: %v", err)
	}
	return inv.Text
}

func TestQuoting(t *testing.T) {
	cases := []struct {
		name     string
		kopfText string
		wantText string
		wantBad  bool
	}{
		{"a quoted field with a separator", `"Wartung; Rahmenvertrag"`, "Wartung; Rahmenvertrag", false},
		{"a doubled delimiter", `"Charge ""A-12"""`, `Charge "A-12"`, false},
		{"a doubled delimiter alone", `""""`, `"`, false},
		{"an empty quoted field", `""`, "", false},
		{"an embedded CRLF", "\"eins\r\nzwei\"", "eins\r\nzwei", false},
		{"an embedded lone LF", "\"eins\nzwei\"", "eins\nzwei", false},
		{"a delimiter inside an unquoted field", `Charge "A-12"`, "", true},
		{"content after the closing delimiter", `"Charge"x`, "", true},
		{"an unterminated quoted field", `"Charge`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := file(vorlauf,
				"KOPF;0004711;0000105;RE;150126;180126;;CHF;546.00;0.00;V81;30;;;0012340;"+tc.kopfText,
				pos, nachlauf)
			res := parse(t, raw)
			if tc.wantBad {
				want := finding{kredexp.CodeMalformedQuoting, importer.SeverityFatal, 2, ""}
				if got := findings(res); len(got) != 1 || got[0] != want {
					t.Fatalf("findings = %+v, want exactly [%+v]", got, want)
				}
				return
			}
			if len(res.Diagnostics) != 0 {
				t.Fatalf("unexpected findings %+v", findings(res))
			}
			if got := documentText(t, res); got != tc.wantText {
				t.Errorf("text = %q, want %q", got, tc.wantText)
			}
			if !res.Accepted {
				t.Error("the file is valid and must be Accepted")
			}
		})
	}
}

func TestRecordSpanningLinesIsReportedOnItsFirstLine(t *testing.T) {
	// A Kopfsatz whose text carries two line breaks is one record on three
	// physical lines, and the Positionssatz after it is on line 5. A finding on
	// that Positionssatz must name line 5: the line an operator searches for is
	// the line the record starts on.
	raw := file(vorlauf,
		"KOPF;0004711;0000105;RE;150126;180126;;CHF;546.00;0.00;V81;30;;;0012340;\"eins\r\nzwei\r\ndrei\"",
		"POS;0004711;00010;0006400;0012340;12.000;STK;45.5000;546,00;;Ersatzteile",
		nachlauf)
	res := parse(t, raw)
	want := []finding{
		{kredexp.CodeInvalidAmount, importer.SeverityReject, 5, "Positionsbetrag"},
		{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 6, ""},
	}
	if got := findings(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
	if res.File.Lines != 6 {
		t.Errorf("File.Lines = %d, want 6", res.File.Lines)
	}
}

func TestAmounts(t *testing.T) {
	// Per-field scales: an amount carries exactly two fraction digits, an
	// Einzelpreis two to four, a Menge up to three. More is a finding and never
	// a rounded value; fewer loses nothing.
	cases := []struct {
		name                        string
		gross, menge, preis, betrag string
		wantCode                    string
		wantField                   string
	}{
		{name: "apostrophe grouping", gross: "1'092.00", menge: "24.000", preis: "45.5000", betrag: "1'092.00"},
		// Byte 0x92 is the windows-1252 typographic apostrophe, which
		// section 9.7 says arrives from manual entry and which the
		// framework accepts as a grouping separator.
		{name: "typographic apostrophe grouping", gross: "1\x92092.00", menge: "24.000", preis: "45.5000", betrag: "1\x92092.00"},
		{name: "no grouping", gross: "1092.00", menge: "24.000", preis: "45.5000", betrag: "1092.00"},
		{name: "trailing minus", gross: "1'092.00-", menge: "24.000", preis: "45.5000-", betrag: "1'092.00-"},
		{name: "an amount with one fraction digit", gross: "546.0", menge: "12.000", preis: "45.5000", betrag: "546.0"},
		{name: "an amount with no fraction digits", gross: "546", menge: "12.000", preis: "45.5000", betrag: "546"},
		{name: "an amount with three fraction digits", gross: "546.000", menge: "12.000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Bruttobetrag"},
		{name: "a Positionsbetrag with three fraction digits", gross: "546.00", menge: "12.000", preis: "45.5000", betrag: "546.000",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Positionsbetrag"},
		{name: "an Einzelpreis with five fraction digits", gross: "546.00", menge: "12.000", preis: "45.50000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Einzelpreis"},
		{name: "a Menge with four fraction digits", gross: "546.00", menge: "12.0000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Menge"},
		{name: "US grouping", gross: "1,092.00", menge: "24.000", preis: "45.5000", betrag: "1092.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Bruttobetrag"},
		{name: "a decimal comma", gross: "546,00", menge: "12.000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Bruttobetrag"},
		{name: "a leading minus", gross: "-546.00", menge: "12.000", preis: "45.5000", betrag: "-546.00"},
		{name: "a currency prefix", gross: "CHF 546.00", menge: "12.000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Bruttobetrag"},
		{name: "a padded amount", gross: " 546.00", menge: "12.000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Bruttobetrag"},
		{name: "misplaced grouping", gross: "10'92.00", menge: "12.000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeInvalidAmount, wantField: "Bruttobetrag"},
		{name: "an empty mandatory amount", gross: "", menge: "12.000", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeMissingRequiredField, wantField: "Bruttobetrag"},
		{name: "an empty Menge", gross: "546.00", menge: "", preis: "45.5000", betrag: "546.00",
			wantCode: kredexp.CodeMissingRequiredField, wantField: "Menge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := file(vorlauf,
				"KOPF;0004711;0000105;RE;150126;180126;;CHF;"+tc.gross+";0.00;V81;30;;;0012340;Wartung",
				"POS;0004711;00010;0006400;0012340;"+tc.menge+";STK;"+tc.preis+";"+tc.betrag+";;Ersatzteile",
				"NACHLAUF;1;1;0.00;0.00")
			res := parse(t, raw)
			if tc.wantCode == "" {
				for _, d := range res.Diagnostics {
					if d.Code != kredexp.CodeTrailerSumMismatch {
						t.Fatalf("unexpected finding %+v", d)
					}
				}
				return
			}
			// A rejected Kopfsatz rejects its Positionssatz, and a rejected
			// record leaves the trailer unverified. Both cascades have tests of
			// their own; what is under test here is the amount itself.
			var got []finding
			for _, d := range findings(res) {
				if d.Code == kredexp.CodeTrailerNotVerified || d.Code == kredexp.CodeOrphanLine {
					continue
				}
				got = append(got, d)
			}
			want := []finding{{tc.wantCode, importer.SeverityReject, wantLine(tc.wantField), tc.wantField}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("findings = %+v, want %+v", got, want)
			}
		})
	}
}

// wantLine returns the line a Kopfsatz or Positionssatz field of the amount
// table sits on.
func wantLine(field string) int {
	switch field {
	case "Bruttobetrag", "MWST-Betrag":
		return 2
	default:
		return 3
	}
}

func TestEmptyVATAmountIsZero(t *testing.T) {
	// Section 5, field 10: "VAT amount. Empty means 0.00." It is the only
	// reading under which a control total stays addable.
	raw := file(vorlauf,
		"KOPF;0004711;0000105;RE;150126;180126;;CHF;546.00;;V81;30;;;0012340;Wartung",
		pos, nachlauf)
	res := parse(t, raw)
	if len(res.Diagnostics) != 0 {
		t.Fatalf("unexpected findings %+v", findings(res))
	}
	var inv struct {
		VATAmount string `json:"vat_amount"`
	}
	if err := json.Unmarshal(res.Documents[0].Payload, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.VATAmount != "0.00" {
		t.Errorf("vat_amount = %q, want \"0.00\"", inv.VATAmount)
	}
}

func TestDates(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"150126", "2026-01-15"},
		{"010100", "2000-01-01"},
		{"311269", "2069-12-31"},
		{"010170", "1970-01-01"},
		{"311299", "1999-12-31"},
		{"290224", "2024-02-29"},
		{"280226", "2026-02-28"},
		{"290200", "2000-02-29"},
		{"", ""},
		{"29022", ""},
		{"2902261", ""},
		{"290226", ""},
		{"290299", ""},
		{"320126", ""},
		{"001226", ""},
		{"151326", ""},
		{"150026", ""},
		{"15012a", ""},
		{"15.01.26", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := kredexp.ParseTTMMJJ(tc.in)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("ParseTTMMJJ(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTTMMJJ(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseTTMMJJ(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTimestamps(t *testing.T) {
	cases := []struct {
		in         string
		date, hhmm string
	}{
		{"3101260612", "2026-01-31", "0612"},
		{"0101702359", "1970-01-01", "2359"},
		{"3101262400", "", ""},
		{"3101260660", "", ""},
		{"31012606", "", ""},
		{"310126061a", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			date, hhmm, err := kredexp.ParseTTMMJJHHMM(tc.in)
			if tc.date == "" {
				if err == nil {
					t.Fatalf("ParseTTMMJJHHMM(%q) = %q %q, want an error", tc.in, date, hhmm)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTTMMJJHHMM(%q): %v", tc.in, err)
			}
			if date != tc.date || hhmm != tc.hhmm {
				t.Errorf("ParseTTMMJJHHMM(%q) = %q %q, want %q %q", tc.in, date, hhmm, tc.date, tc.hhmm)
			}
		})
	}
}

func TestRecordLevelRejects(t *testing.T) {
	cases := []struct {
		name    string
		records []string
		want    []finding
		docs    int
	}{
		{
			name:    "an unknown Satzart",
			records: []string{vorlauf, kopf, pos, "ZWISCHENSUMME;546.00", nachlauf},
			want: []finding{
				{kredexp.CodeUnknownRecordType, importer.SeverityReject, 4, "Satzart"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 5, ""},
			},
			docs: 1,
		},
		{
			name:    "a second Vorlaufsatz in the body",
			records: []string{vorlauf, vorlauf, kopf, pos, nachlauf},
			want: []finding{
				{kredexp.CodeUnknownRecordType, importer.SeverityReject, 2, "Satzart"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 5, ""},
			},
			docs: 1,
		},
		{
			name:    "a Kopfsatz with fifteen fields",
			records: []string{vorlauf, "KOPF;0004711;0000105;RE;150126;180126;;CHF;546.00;0.00;V81;30;;;0012340", pos, nachlauf},
			want: []finding{
				{kredexp.CodeFieldCountMismatch, importer.SeverityReject, 2, ""},
				{kredexp.CodeOrphanLine, importer.SeverityReject, 3, "Belegnummer"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
		},
		{
			name:    "a Positionssatz with ten fields, the v2.0 layout",
			records: []string{vorlauf, kopf, "POS;0004711;00010;0006400;0012340;12.000;STK;45.5000;546.00;Ersatzteile", nachlauf},
			want: []finding{
				{kredexp.CodeFieldCountMismatch, importer.SeverityReject, 3, ""},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
			docs: 1,
		},
		{
			name:    "a Vorlaufsatz with a missing Testkennzeichen",
			records: []string{"VORLAUF;2.1;0100;3101260612;CHF;SUBSG01;BLP;", kopf, pos, nachlauf},
			want: []finding{
				{kredexp.CodeMissingRequiredField, importer.SeverityReject, 1, "Testkennzeichen"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
			docs: 1,
		},
		{
			name:    "a Vorlaufsatz with an unreadable creation timestamp",
			records: []string{"VORLAUF;2.1;0100;31012699;CHF;SUBSG01;BLP;P", kopf, pos, nachlauf},
			want: []finding{
				{kredexp.CodeInvalidDate, importer.SeverityReject, 1, "Erstellungszeitpunkt"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
			docs: 1,
		},
		{
			name:    "a Kopfsatz with several defects reports all of them",
			records: []string{vorlauf, "KOPF;;0000105;RE;300226;180126;;CHF;546.00;0.00;V81;dreissig;;;0012340;Wartung", pos, nachlauf},
			// Sorted by field within the line, which is what the framework's
			// order by (line, record, field, code) produces.
			want: []finding{
				{kredexp.CodeInvalidDate, importer.SeverityReject, 2, "Belegdatum"},
				{kredexp.CodeMissingRequiredField, importer.SeverityReject, 2, "Belegnummer"},
				{kredexp.CodeInvalidAmount, importer.SeverityReject, 2, "Zahlungsziel"},
				{kredexp.CodeOrphanLine, importer.SeverityReject, 3, "Belegnummer"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
		},
		{
			name:    "a Positionssatz before any Kopfsatz",
			records: []string{vorlauf, pos, kopf, pos, nachlauf},
			want: []finding{
				{kredexp.CodeOrphanLine, importer.SeverityReject, 2, "Belegnummer"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 5, ""},
			},
			docs: 1,
		},
		{
			name:    "a Positionssatz whose Belegnummer disagrees with its Kopfsatz",
			records: []string{vorlauf, kopf, "POS;4711;00010;0006400;0012340;12.000;STK;45.5000;546.00;;Ersatzteile", nachlauf},
			want: []finding{
				{kredexp.CodeOrphanLine, importer.SeverityReject, 3, "Belegnummer"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
			docs: 1,
		},
		{
			name: "a rejected Kopfsatz costs one finding per Positionssatz line",
			records: []string{vorlauf, "KOPF;0004711;0000105;RE;300226;180126;;CHF;546.00;0.00;V81;30;;;0012340;Wartung",
				pos, pos, pos, nachlauf},
			want: []finding{
				{kredexp.CodeInvalidDate, importer.SeverityReject, 2, "Belegdatum"},
				{kredexp.CodeOrphanLine, importer.SeverityReject, 3, "Belegnummer"},
				{kredexp.CodeOrphanLine, importer.SeverityReject, 4, "Belegnummer"},
				{kredexp.CodeOrphanLine, importer.SeverityReject, 5, "Belegnummer"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 6, ""},
			},
		},
		{
			name:    "a Positionssatz with no Sachkonto",
			records: []string{vorlauf, kopf, "POS;0004711;00010;;0012340;12.000;STK;45.5000;546.00;;Ersatzteile", nachlauf},
			want: []finding{
				{kredexp.CodeMissingRequiredField, importer.SeverityReject, 3, "Sachkonto"},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
			docs: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := parse(t, file(tc.records...))
			if got := findings(res); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("findings = %+v, want %+v", got, tc.want)
			}
			if len(res.Documents) != tc.docs {
				t.Errorf("documents = %d, want %d", len(res.Documents), tc.docs)
			}
			if res.Accepted {
				t.Error("a file with a reject must not be Accepted")
			}
			if res.Totals.TrailerVerified {
				t.Error("a file with a reject must not report a verified trailer")
			}
		})
	}
}

func TestTrailerVerification(t *testing.T) {
	cases := []struct {
		name     string
		trailer  string
		want     []finding
		accepted bool
	}{
		{name: "matching", trailer: nachlauf, accepted: true},
		{
			name:    "a Kopfsatz count that is too high",
			trailer: "NACHLAUF;2;1;546.00;546.00",
			want:    []finding{{kredexp.CodeTrailerCountMismatch, importer.SeverityFatal, 4, "Anzahl Kopfsätze"}},
		},
		{
			name:    "a Positionssatz count that is too low",
			trailer: "NACHLAUF;1;0;546.00;546.00",
			want:    []finding{{kredexp.CodeTrailerCountMismatch, importer.SeverityFatal, 4, "Anzahl Positionssätze"}},
		},
		{
			name:    "a gross sum one Rappen out",
			trailer: "NACHLAUF;1;1;546.01;546.00",
			want:    []finding{{kredexp.CodeTrailerSumMismatch, importer.SeverityFatal, 4, "Summe Bruttobetrag"}},
		},
		{
			name:    "a line sum one Rappen out",
			trailer: "NACHLAUF;1;1;546.00;545.99",
			want:    []finding{{kredexp.CodeTrailerSumMismatch, importer.SeverityFatal, 4, "Summe Positionsbetrag"}},
		},
		{
			name:    "every control total wrong at once",
			trailer: "NACHLAUF;9;9;9.00;9.00",
			want: []finding{
				{kredexp.CodeTrailerCountMismatch, importer.SeverityFatal, 4, "Anzahl Kopfsätze"},
				{kredexp.CodeTrailerCountMismatch, importer.SeverityFatal, 4, "Anzahl Positionssätze"},
				{kredexp.CodeTrailerSumMismatch, importer.SeverityFatal, 4, "Summe Bruttobetrag"},
				{kredexp.CodeTrailerSumMismatch, importer.SeverityFatal, 4, "Summe Positionsbetrag"},
			},
		},
		{
			name:    "a trailer with four fields is rejected and then not verified",
			trailer: "NACHLAUF;1;1;546.00",
			want: []finding{
				{kredexp.CodeFieldCountMismatch, importer.SeverityReject, 4, ""},
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
			},
		},
		{
			name:    "a trailer whose sum is unreadable is rejected and then not verified",
			trailer: "NACHLAUF;1;1;546,00;546.00",
			want: []finding{
				{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
				{kredexp.CodeInvalidAmount, importer.SeverityReject, 4, "Summe Bruttobetrag"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := parse(t, file(vorlauf, kopf, pos, tc.trailer))
			if got := findings(res); !reflect.DeepEqual(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("findings = %+v, want %+v", got, tc.want)
			}
			if res.Accepted != tc.accepted {
				t.Errorf("Accepted = %t, want %t", res.Accepted, tc.accepted)
			}
			if !res.Totals.TrailerDeclared {
				t.Error("a file with a Nachlaufsatz declares a trailer")
			}
			if res.Totals.TrailerVerified != tc.accepted {
				t.Errorf("TrailerVerified = %t, want %t", res.Totals.TrailerVerified, tc.accepted)
			}
			if len(res.Documents) != 1 {
				t.Errorf("documents = %d, want 1: a trailer finding is about the file, and the documents already read are the evidence", len(res.Documents))
			}
		})
	}
}

func TestTrailerNotVerifiedIsReportedOnce(t *testing.T) {
	// Several rejects, several documents, one warning. A warning per reject
	// would say the same thing five times and bury the five real findings.
	raw := file(vorlauf,
		"KOPF;0004711;0000105;RE;300226;;;CHF;546.00;0.00;V81;;;;;Wartung",
		pos,
		"KOPF;0004712;0000105;RE;300226;;;CHF;546.00;0.00;V81;;;;;Wartung",
		pos,
		"NACHLAUF;2;2;1'092.00;1'092.00")
	res := parse(t, raw)
	n := 0
	for _, d := range res.Diagnostics {
		if d.Code == kredexp.CodeTrailerNotVerified {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("trailer_not_verified appears %d times, want exactly 1 (findings %+v)", n, findings(res))
	}
	for _, d := range res.Diagnostics {
		if d.Code == kredexp.CodeTrailerCountMismatch || d.Code == kredexp.CodeTrailerSumMismatch {
			t.Errorf("the trailer was compared anyway: %+v", d)
		}
	}
}

// invoice is the part of a document payload the ambiguity tests assert on.
type invoice struct {
	DocumentType string `json:"document_type"`
	GrossAmount  string `json:"gross_amount"`
	VATAmount    string `json:"vat_amount"`
	DiscountRaw  string `json:"discount_raw"`
	CostCenter   string `json:"cost_center"`
	VATCode      string `json:"vat_code"`
	Lines        []struct {
		CostCenter string `json:"cost_center"`
		LineAmount string `json:"line_amount"`
		UnitPrice  string `json:"unit_price"`
		TaxCode    string `json:"tax_code"`
	} `json:"lines"`
}

// decodeInvoice decodes the document at index i of a result.
func decodeInvoice(t *testing.T, res *importer.Result, i int) invoice {
	t.Helper()
	if len(res.Documents) <= i {
		t.Fatalf("want at least %d documents, got %d", i+1, len(res.Documents))
	}
	var inv invoice
	if err := json.Unmarshal(res.Documents[i].Payload, &inv); err != nil {
		t.Fatalf("decoding the payload: %v", err)
	}
	return inv
}

func TestSkontoIsCarriedVerbatimAndReportedOnce(t *testing.T) {
	// The first documented contradiction. Two Positionssätze, one warning: the
	// ambiguity is a property of the document, not of its lines.
	cases := []struct {
		name  string
		value string
		warns int
	}{
		{"the value from Anhang A", "2.00", 1},
		{"a percentage spelling", "2,5%", 1},
		{"a spelling no amount parser accepts", "2 %", 1},
		{"empty", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := file(vorlauf,
				"KOPF;0004711;0000105;RE;150126;180126;;CHF;1'092.00;0.00;V81;30;"+tc.value+";10;0012340;Wartung",
				pos,
				"POS;0004711;00020;0006400;0012340;12.000;STK;45.5000;546.00;;Ersatzteile",
				"NACHLAUF;1;2;1'092.00;1'092.00")
			res := parse(t, raw)
			var warns []finding
			for _, d := range findings(res) {
				if d.Code == kredexp.CodeAmbiguousFieldSemantics {
					warns = append(warns, d)
				}
			}
			if len(warns) != tc.warns {
				t.Fatalf("ambiguity warnings = %+v, want %d", warns, tc.warns)
			}
			if tc.warns == 1 && (warns[0].Field != "Skonto" || warns[0].Severity != importer.SeverityWarn || warns[0].Line != 2) {
				t.Errorf("the warning is %+v, want a warn on Skonto at line 2", warns[0])
			}
			if got := decodeInvoice(t, res, 0).DiscountRaw; got != tc.value {
				t.Errorf("discount_raw = %q, want the source value %q carried verbatim", got, tc.value)
			}
			if !res.Accepted {
				t.Error("an ambiguity is a warning and must not block acceptance")
			}
		})
	}
}

func TestEmptyPositionKostenstelleIsSurfacedAndNeverInherited(t *testing.T) {
	// The second documented contradiction, plus its counterpart: an empty
	// Steuercode inherits the Kopfsatz's MWST-Code, because section 6 says so,
	// and an empty Kostenstelle does not, because nothing says so.
	raw := file(vorlauf,
		"KOPF;0004711;0000105;RE;150126;180126;;CHF;1'092.00;0.00;V81;30;;;0012340;Wartung",
		"POS;0004711;00010;0006400;;12.000;STK;45.5000;546.00;;Ersatzteile",
		"POS;0004711;00020;0006400;;12.000;STK;45.5000;546.00;V26;Ersatzteile",
		"NACHLAUF;1;2;1'092.00;1'092.00")
	res := parse(t, raw)
	want := []finding{
		{kredexp.CodeAmbiguousFieldSemantics, importer.SeverityWarn, 3, "Kostenstelle"},
		{kredexp.CodeAmbiguousFieldSemantics, importer.SeverityWarn, 4, "Kostenstelle"},
	}
	if got := findings(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v (one warning per Positionssatz, none for the document's own empty fields)", got, want)
	}
	inv := decodeInvoice(t, res, 0)
	if inv.CostCenter != "0012340" {
		t.Errorf("the document's cost_center = %q, want it unchanged", inv.CostCenter)
	}
	for i, l := range inv.Lines {
		if l.CostCenter != "" {
			t.Errorf("lines[%d].cost_center = %q, want it left empty rather than inherited", i, l.CostCenter)
		}
	}
	if inv.Lines[0].TaxCode != "V81" {
		t.Errorf("lines[0].tax_code = %q, want the documented inheritance of the Kopfsatz MWST-Code", inv.Lines[0].TaxCode)
	}
	if inv.Lines[1].TaxCode != "V26" {
		t.Errorf("lines[1].tax_code = %q, want the position's own Steuercode", inv.Lines[1].TaxCode)
	}
	if !res.Accepted {
		t.Error("an ambiguity is a warning and must not block acceptance")
	}
}

func TestAmbiguityWarningsAreDroppedForRejectedRecords(t *testing.T) {
	// A warning describes a document that will exist. A Positionssatz that was
	// rejected produces none, so a warning about its cost center would send an
	// operator looking for a line that is not there.
	raw := file(vorlauf, kopf,
		"POS;0004711;00010;0006400;;12.000;STK;45.5000;546.000;;Ersatzteile",
		nachlauf)
	res := parse(t, raw)
	want := []finding{
		{kredexp.CodeInvalidAmount, importer.SeverityReject, 3, "Positionsbetrag"},
		{kredexp.CodeTrailerNotVerified, importer.SeverityWarn, 4, ""},
	}
	if got := findings(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
}

func TestGutschriftSignIsNeverDerived(t *testing.T) {
	// The third documented contradiction. The Belegart is carried verbatim and
	// the sign is the explicit one, whatever the Belegart says: a GU with
	// positive amounts stays positive, a RE with a trailing minus stays
	// negative, and both trailers verify, which is the evidence that no flip is
	// wanted.
	cases := []struct {
		name                  string
		belegart              string
		gross, vat, betrag    string
		trailer               string
		wantGross, wantBetrag string
	}{
		{
			name: "a GU whose amounts carry a trailing minus", belegart: "GU",
			gross: "623.10-", vat: "23.10-", betrag: "600.00-",
			trailer: "NACHLAUF;1;1;623.10-;600.00-", wantGross: "-623.10", wantBetrag: "-600.00",
		},
		{
			name: "a GU whose amounts carry no sign at all", belegart: "GU",
			gross: "623.10", vat: "23.10", betrag: "600.00",
			trailer: "NACHLAUF;1;1;623.10;600.00", wantGross: "623.10", wantBetrag: "600.00",
		},
		{
			name: "a RE whose amounts carry a trailing minus", belegart: "RE",
			gross: "623.10-", vat: "23.10-", betrag: "600.00-",
			trailer: "NACHLAUF;1;1;623.10-;600.00-", wantGross: "-623.10", wantBetrag: "-600.00",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := file(vorlauf,
				"KOPF;0004711;0000105;"+tc.belegart+";150126;180126;;CHF;"+tc.gross+";"+tc.vat+";V81;30;;;0012340;Wartung",
				"POS;0004711;00010;0006400;0012340;4.000;STK;150.0000;"+tc.betrag+";;Ersatzteile",
				tc.trailer)
			res := parse(t, raw)
			if len(res.Diagnostics) != 0 {
				t.Fatalf("unexpected findings %+v", findings(res))
			}
			if !res.Accepted || !res.Totals.TrailerVerified {
				t.Fatal("the trailer sums agree with the explicit signs, so the file verifies")
			}
			inv := decodeInvoice(t, res, 0)
			if inv.DocumentType != tc.belegart {
				t.Errorf("document_type = %q, want the Belegart verbatim %q", inv.DocumentType, tc.belegart)
			}
			if inv.GrossAmount != tc.wantGross {
				t.Errorf("gross_amount = %q, want %q", inv.GrossAmount, tc.wantGross)
			}
			if inv.Lines[0].LineAmount != tc.wantBetrag {
				t.Errorf("lines[0].line_amount = %q, want %q", inv.Lines[0].LineAmount, tc.wantBetrag)
			}
		})
	}
}

func TestDerivedSignWouldFailTheTrailer(t *testing.T) {
	// The evidence itself, stated as a test: the public Gutschrift fixture's
	// trailer only adds up if the explicit signs are kept. A reader that flipped
	// the sign of a GU would compute a different sum, which is why the trailer is
	// the answer to the third contradiction.
	raw, err := os.ReadFile(filepath.Join(publicDir, "04_gutschrift_trailing_minus_ok.txt"))
	if err != nil {
		t.Fatal(err)
	}
	res := parse(t, raw)
	if !res.Accepted {
		t.Fatalf("the fixture must be accepted, findings %+v", findings(res))
	}
	credit := decodeInvoice(t, res, 1)
	if credit.DocumentType != "GU" {
		t.Fatalf("the second document is %q, want the Gutschrift", credit.DocumentType)
	}
	gross := mustDecimal(t, credit.GrossAmount)
	if gross.Sign() >= 0 {
		t.Fatalf("the Gutschrift gross amount is %s, want the explicit negative", gross)
	}
	// Flip it, as a reader who derived the sign from Belegart GU would, and the
	// declared control total no longer matches.
	invoiceGross := mustDecimal(t, decodeInvoice(t, res, 0).GrossAmount)
	flipped, err := invoiceGross.Add(gross.Neg())
	if err != nil {
		t.Fatal(err)
	}
	declared := mustDecimal(t, res.Totals.DeclaredGross)
	if flipped.Cmp(declared) == 0 {
		t.Fatal("the fixture does not discriminate between the two readings; a derived sign flip must break its trailer")
	}
	if got := mustDecimal(t, res.Totals.ComputedGross); got.Cmp(declared) != 0 {
		t.Fatalf("computed %s, declared %s: the explicit signs must reproduce the sender's total", got, declared)
	}
}

// mustDecimal parses an exact decimal a test controls.
func mustDecimal(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseDecimal(s)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", s, err)
	}
	return d
}

func TestLeadingZerosAndKeysSurvive(t *testing.T) {
	// "0004711" and "4711" are different Belege of the same supplier, and both
	// numbers stay strings: a natural key that became a number lost its leading
	// zeros before anybody could compare it.
	raw := file(vorlauf,
		"KOPF;0004711;0000105;RE;150126;;;CHF;546.00;0.00;V81;;;;0012340;Erster",
		pos,
		"KOPF;4711;0000105;RE;150126;;;CHF;546.00;0.00;V81;;;;0012340;Zweiter",
		"POS;4711;00010;0006400;0012340;12.000;STK;45.5000;546.00;;Ersatzteile",
		"NACHLAUF;2;2;1'092.00;1'092.00")
	res := parse(t, raw)
	if len(res.Diagnostics) != 0 {
		t.Fatalf("unexpected findings %+v", findings(res))
	}
	want := []string{
		model.InvoiceKey("0000105", "0004711"),
		model.InvoiceKey("0000105", "4711"),
	}
	got := []string{res.Documents[0].Key, res.Documents[1].Key}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if got[0] == got[1] {
		t.Fatal("the two Belege collapsed into one key")
	}
}

func TestMandantTravelsAsTheCompanyCode(t *testing.T) {
	// The Mandant of the Vorlaufsatz is the only company code the file carries,
	// and it is delivered as the file spelled it. Mapping 0100 to CH10 is master
	// data the twin holds.
	res := parse(t, file(vorlauf, kopf, pos, nachlauf))
	var inv struct {
		CompanyCode string `json:"company_code"`
		Currency    string `json:"currency"`
	}
	if err := json.Unmarshal(res.Documents[0].Payload, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.CompanyCode != "0100" {
		t.Errorf("company_code = %q, want the Mandant verbatim", inv.CompanyCode)
	}
}

func TestDocumentCurrencyFallsBackToTheRun(t *testing.T) {
	// Section 5, field 8: "if empty, the currency of the Vorlaufsatz applies".
	// This inheritance is documented, so it happens silently.
	cases := []struct{ field, want string }{
		{"", "CHF"},
		{"EUR", "EUR"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			res := parse(t, file(vorlauf,
				"KOPF;0004711;0000105;RE;150126;180126;"+";"+tc.field+";546.00;0.00;V81;30;;;0012340;Wartung",
				pos, nachlauf))
			if len(res.Diagnostics) != 0 {
				t.Fatalf("unexpected findings %+v", findings(res))
			}
			var inv struct {
				Currency string `json:"currency"`
			}
			if err := json.Unmarshal(res.Documents[0].Payload, &inv); err != nil {
				t.Fatal(err)
			}
			if inv.Currency != tc.want {
				t.Errorf("currency = %q, want %q", inv.Currency, tc.want)
			}
		})
	}
}

func TestBlankLinesAreIgnored(t *testing.T) {
	// A blank line carries no Satzart, so there is nothing to reject, and a
	// stray one at the end of a transfer is not a data defect.
	raw := []byte(vorlauf + "\r\n\r\n" + kopf + "\r\n" + pos + "\r\n\r\n" + nachlauf + "\r\n\r\n")
	res := parse(t, raw)
	if len(res.Diagnostics) != 0 {
		t.Fatalf("unexpected findings %+v", findings(res))
	}
	if !res.Accepted {
		t.Error("the file is valid and must be Accepted")
	}
}

func TestLoneLFTerminatorIsAccepted(t *testing.T) {
	// A transfer that lost its carriage returns is still unambiguous, and
	// refusing it would turn a recoverable delivery into a support ticket.
	raw := []byte(strings.Join([]string{vorlauf, kopf, pos, nachlauf}, "\n") + "\n")
	res := parse(t, raw)
	if len(res.Diagnostics) != 0 {
		t.Fatalf("unexpected findings %+v", findings(res))
	}
	if !res.Accepted {
		t.Error("the file is valid and must be Accepted")
	}
}

func TestMissingFinalTerminator(t *testing.T) {
	raw := []byte(strings.Join([]string{vorlauf, kopf, pos, nachlauf}, "\r\n"))
	res := parse(t, raw)
	if len(res.Diagnostics) != 0 {
		t.Fatalf("unexpected findings %+v", findings(res))
	}
	if !res.Accepted {
		t.Error("the last record is complete without its terminator and must be read")
	}
}

func TestParseIsDeterministic(t *testing.T) {
	// Identical bytes and identical options produce a byte-identical
	// projection, and so does the streaming path the framework wraps around
	// Parse. Everything downstream - a content hash, a receipt, a state digest -
	// depends on it.
	for _, dir := range []string{publicDir} {
		cases, err := importer.LoadGoldenCases(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			t.Run(c.Name, func(t *testing.T) {
				raw, err := os.ReadFile(c.InputPath)
				if err != nil {
					t.Fatal(err)
				}
				first := ""
				for i := 0; i < 3; i++ {
					res := parse(t, raw)
					proj, err := importer.Projection(res)
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						first = string(proj)
						continue
					}
					if string(proj) != first {
						t.Fatalf("run %d produced a different projection", i+1)
					}
				}
			})
		}
	}
}

func TestDiagnosticsAreCappedByTheFramework(t *testing.T) {
	// The cap is the framework's and the count that decides Accepted is taken
	// before it, so a file with more findings than the cap is still rejected for
	// all of them.
	records := []string{vorlauf}
	for i := 0; i < 12; i++ {
		records = append(records, "ZWISCHENSUMME;546.00")
	}
	records = append(records, "NACHLAUF;0;0;0.00;0.00")
	res, err := kredexp.New().Parse(context.Background(), file(records...), importer.Options{
		Filename:       "KRED_0100_20260131_001.txt",
		Encoding:       importer.EncodingCP1252,
		MaxDiagnostics: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diagnostics) != 6 {
		t.Fatalf("diagnostics = %d, want the 5 kept plus one truncation warning", len(res.Diagnostics))
	}
	if last := res.Diagnostics[len(res.Diagnostics)-1]; last.Code != importer.CodeDiagnosticsTruncated {
		t.Errorf("the last diagnostic is %s, want %s", last.Code, importer.CodeDiagnosticsTruncated)
	}
	if res.Accepted {
		t.Error("Accepted is computed over the untruncated findings")
	}
}

func TestFileInfoDescribesTheSource(t *testing.T) {
	raw := file(vorlauf, kopf, pos, nachlauf)
	res := parse(t, raw)
	if res.File.Bytes != len(raw) {
		t.Errorf("File.Bytes = %d, want %d", res.File.Bytes, len(raw))
	}
	if res.File.SHA256 != model.Sha256Hex(raw) {
		t.Error("File.SHA256 must be the digest of the source bytes, so the raw store can hand them back")
	}
	if res.File.Lines != 4 {
		t.Errorf("File.Lines = %d, want 4", res.File.Lines)
	}
	if res.File.Encoding != importer.EncodingCP1252 {
		t.Errorf("File.Encoding = %q, want the declared %q", res.File.Encoding, importer.EncodingCP1252)
	}
}

// anhangA is the customer document's own example, section 8, as the framework
// ships it for the detection tests. Parsing it is the sharpest available check
// on the importer: it must agree with the example in the customer's own document.
const anhangA = "../../../internal/importer/testdata/formats/kredexp-2.1/anhang-a.txt"

func TestAnhangAFromTheCustomerDocument(t *testing.T) {
	raw, err := os.ReadFile(anhangA)
	if err != nil {
		t.Skipf("the framework's example fixture is not present: %v", err)
	}
	res := parse(t, raw)
	// Two ambiguities, both from the example itself: Skonto 2.00 with
	// Skontotage 10, and the first Positionssatz with an empty Kostenstelle.
	want := []finding{
		{kredexp.CodeAmbiguousFieldSemantics, importer.SeverityWarn, 2, "Skonto"},
		{kredexp.CodeAmbiguousFieldSemantics, importer.SeverityWarn, 3, "Kostenstelle"},
	}
	if got := findings(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %+v, want %+v", got, want)
	}
	if !res.Accepted || !res.Totals.TrailerVerified {
		t.Fatal("the example in the specification must verify and be accepted")
	}
	if len(res.Documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(res.Documents))
	}
	inv := decodeInvoice(t, res, 0)
	if len(inv.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(inv.Lines))
	}
	if inv.DiscountRaw != "2.00" {
		t.Errorf("discount_raw = %q, want the example's value verbatim", inv.DiscountRaw)
	}
	if inv.Lines[0].CostCenter != "" {
		t.Errorf("lines[0].cost_center = %q, want it left empty", inv.Lines[0].CostCenter)
	}
	if inv.Lines[2].TaxCode != "V81" {
		t.Errorf("lines[2].tax_code = %q, want the documented inheritance of the Kopfsatz MWST-Code", inv.Lines[2].TaxCode)
	}
	if got, want := res.Totals.ComputedLineSum, "1244.80"; got != want {
		t.Errorf("computed_line_sum = %q, want %q", got, want)
	}
	if got, want := res.Totals.ComputedGross, "1345.63"; got != want {
		t.Errorf("computed_gross = %q, want %q", got, want)
	}
}
