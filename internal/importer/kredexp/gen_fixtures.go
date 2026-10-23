//go:build ignore

// Command gen_fixtures writes the four KRED-EXP 2.1 golden fixtures in testdata/kredexp.
//
// Run it from the repository root:
//
//	go run internal/importer/kredexp/gen_fixtures.go
//	go test ./internal/importer/kredexp/ -update   # then rewrite the expectations
//
// It exists because the fixtures must be genuine windows-1252 bytes with CRLF
// terminators, and a .txt file edited in an editor is UTF-8 with LF the moment
// anybody touches it. The encoder here is a small local one on purpose: the
// generator must not depend on the packages it produces test input for, and
// internal/seed's encoder is for the seeded datasets rather than for hand-written
// fixtures.
//
// The record content is spelled out record by record rather than generated from
// a model, because every fixture exists to pin one documented rule and a reader
// of this file has to be able to see which.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// crlf is the record terminator of the format. Every record ends with it, the
// last one included.
const crlf = "\r\n"

// invalidByteSentinel is replaced by the single byte 0x8D after encoding. It is
// how the one fixture that needs an undefined windows-1252 position gets it: no
// rune encodes to 0x8D, since the position is undefined, which is the whole
// point of the fixture.
const invalidByteSentinel = "@@8D@@"

// A fixture is one file to write.
type fixture struct {
	// dir is the directory, relative to the repository root.
	dir string
	// name is the file name.
	name string
	// why is a one-line note about what the fixture pins. It is printed by the
	// generator and is not written to disk.
	why string
	// records are the records, field by field, undelimited: the writer applies
	// the text delimiter where the customer document requires it.
	records [][]string
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	for _, f := range fixtures() {
		if err := write(root, f); err != nil {
			fmt.Fprintln(os.Stderr, "gen_fixtures:", err)
			os.Exit(1)
		}
		fmt.Printf("%-56s %s\n", filepath.Join(f.dir, f.name), f.why)
	}
}

// write renders one fixture and its sibling options file.
func write(root string, f fixture) error {
	dir := filepath.Join(root, f.dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	for _, rec := range f.records {
		for i, field := range rec {
			if i > 0 {
				sb.WriteString(";")
			}
			sb.WriteString(quote(field))
		}
		sb.WriteString(crlf)
	}
	raw, err := encodeCP1252(sb.String())
	if err != nil {
		return fmt.Errorf("%s: %w", f.name, err)
	}
	raw = injectRawBytes(raw)
	if err := os.WriteFile(filepath.Join(dir, f.name), raw, 0o644); err != nil {
		return err
	}
	// The options file declares the encoding the manifest would declare, so the
	// golden runner parses the fixture the way the twin parses a delivery. The
	// record count is deliberately absent: this format declares its own control
	// totals in the Nachlaufsatz, and a manifest count would add a second,
	// unrelated declaration to verify.
	opts, err := json.Marshal(map[string]string{"encoding": "windows-1252", "dataset": "invoice"})
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(f.name, filepath.Ext(f.name))
	return os.WriteFile(filepath.Join(dir, base+".options.json"), append(opts, '\n'), 0o644)
}

// quote applies the text delimiter rules of section 3: a field is delimited only
// when it contains a separator, a delimiter or a line break, and a delimiter
// inside a delimited field is doubled.
func quote(s string) string {
	if !strings.ContainsAny(s, ";\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// injectRawBytes replaces the sentinel with the raw byte it stands for.
func injectRawBytes(raw []byte) []byte {
	return []byte(strings.ReplaceAll(string(raw), invalidByteSentinel, "\x8d"))
}

// cp1252High maps the runes of the windows-1252 0x80..0x9F block to their bytes.
// The five undefined positions are absent: a generator that could emit one by
// accident could produce a fixture nobody meant to write.
var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87,
	'ˆ': 0x88, '‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E,
	'‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B, 'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// encodeCP1252 encodes a UTF-8 string as windows-1252 bytes, refusing any rune
// the character set cannot represent rather than substituting a replacement
// character.
func encodeCP1252(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			b, ok := cp1252High[r]
			if !ok {
				return nil, fmt.Errorf("rune %q (U+%04X) has no windows-1252 representation", r, r)
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// vorlauf returns a Vorlaufsatz with the given creation timestamp and currency.
func vorlauf(stamp, currency string) []string {
	return []string{"VORLAUF", "2.1", "0100", stamp, currency, "SUBSG01", "BLP", "P"}
}

// fixtures returns every fixture, public suite first.
func fixtures() []fixture {
	return []fixture{
		{
			dir:  "testdata/kredexp",
			name: "01_minimal_ok.txt",
			why:  "one Kopfsatz, one Positionssatz, ASCII only, trailer matching, no diagnostics",
			records: [][]string{
				vorlauf("3101260612", "CHF"),
				{"KOPF", "0004711", "0000105", "RE", "150126", "180126", "4500001234/00010", "CHF",
					"546.00", "0.00", "V81", "30", "", "", "0012340", "Wartung Presse 3"},
				{"POS", "0004711", "00010", "0006400", "0012340", "12.000", "STK", "45.5000", "546.00", "", "Ersatzteile"},
				{"NACHLAUF", "1", "1", "546.00", "546.00"},
			},
		},
		{
			dir:  "testdata/kredexp",
			name: "02_swiss_numbers_leading_zeros_ok.txt",
			why:  "two Belege whose numbers differ only in leading zeros, apostrophe grouping, 3 and 4 decimal Einzelpreis, empty MWST-Betrag",
			records: [][]string{
				vorlauf("0102260845", "CHF"),
				// Währung is empty, so the currency of the Vorlaufsatz applies;
				// MWST-Betrag is empty, which section 5 defines as 0.00.
				{"KOPF", "0004711", "0000105", "RE", "010226", "030226", "", "",
					"4'725.00", "", "V0", "30", "", "", "0012340", "Materiallieferung Januar"},
				{"POS", "0004711", "00010", "0006400", "0012340", "0.125", "STK", "12'800.0000", "1'600.00", "", "Spezialwerkzeug"},
				{"POS", "0004711", "00020", "0000123", "0012340", "1'250.000", "KG", "2.500", "3'125.00", "", "Rohmaterial"},
				// "4711" is a different Beleg from "0004711": a Belegnummer is a
				// string and its leading zeros are part of the value.
				{"KOPF", "4711", "0000105", "RE", "020226", "040226", "", "CHF",
					"1'076.85", "76.85", "V81", "30", "", "", "0012340", "Nachlieferung"},
				{"POS", "4711", "00010", "0006400", "0012340", "1.000", "STK", "1000.0000", "1'000.00", "", "Ersatzteil"},
				{"NACHLAUF", "2", "3", "5'801.85", "5'725.00"},
			},
		},
		{
			dir:  "testdata/kredexp",
			name: "03_quoted_fields_ok.txt",
			why:  "quoted fields with a separator, doubled quotes, an embedded CRLF and an embedded lone LF, plus windows-1252 high bytes",
			records: [][]string{
				vorlauf("3101261430", "CHF"),
				// The Bruttobetrag is grouped with the typographic apostrophe
				// U+2019 (byte 0x92), which section 9.7 says arrives regularly
				// from manual entry; the text carries an en dash, doubled
				// quotes, a semicolon and an embedded CRLF.
				{"KOPF", "RE-2026-0001", "0000317", "RE", "120226", "140226", "", "EUR",
					"1’234.50", "88.15", "V81", "30", "", "", "0012340",
					"Wartung Presse 3; Rahmenvertrag 2026 – Charge \"A-12\"\r\nNachtrag: Ersatzteile für Presse 3"},
				{"POS", "RE-2026-0001", "00010", "0006400", "0012340", "2.000", "STK", "573.1750", "1'146.35", "", "Ersatzteile"},
				// The second text carries a euro sign, an umlaut and an embedded
				// LONE LF: a sender whose text field lost its carriage return
				// still delivered one field, not two records.
				{"KOPF", "RE-2026-0002", "0000317", "RE", "130226", "", "", "EUR",
					"540.00", "", "V0", "", "", "", "", "Rabatt 40.00 € gemäss Absprache\nZeile zwei ohne CR"},
				{"POS", "RE-2026-0002", "00010", "0000765", "0012350", "1.000", "PAU", "540.0000", "540.00", "", "Pauschale"},
				{"NACHLAUF", "2", "2", "1'774.50", "1'686.35"},
			},
		},
		{
			dir:  "testdata/kredexp",
			name: "04_gutschrift_trailing_minus_ok.txt",
			why:  "a Gutschrift whose trailing minus the trailer sums confirm, plus both ambiguity warnings",
			records: [][]string{
				vorlauf("2801260930", "CHF"),
				// Skonto 2.00 with Skontotage 10 is the value of Anhang A: the
				// one the field table calls an amount and the glossary calls a
				// percentage rate.
				{"KOPF", "0004815", "0000105", "RE", "200126", "220126", "", "CHF",
					"1'077.00", "77.00", "V81", "30", "2.00", "10", "0012340", "Wartungsvertrag Q1"},
				{"POS", "0004815", "00010", "0006400", "0012340", "1.000", "PAU", "1000.0000", "1'000.00", "V81", "Wartung"},
				// The credit note carries its sign explicitly, as a minus after
				// the last digit. The trailer sums below are the evidence: a
				// receiver that derived a second flip from Belegart GU would
				// compute 2'369.40 and 2'200.00 and fail the trailer.
				{"KOPF", "0004816", "0000105", "GU", "250126", "270126", "", "CHF",
					"1'292.40-", "92.40-", "V81", "", "", "", "", "Gutschrift Fehllieferung"},
				// The Kostenstelle of this position is empty, which section 9.3
				// leaves undefined and which is therefore reported rather than
				// inherited from the Kopfsatz.
				{"POS", "0004816", "00010", "0006400", "", "8.000", "STK", "150.0000-", "1'200.00-", "V81", "Rücksendung"},
				{"NACHLAUF", "2", "2", "215.40-", "200.00-"},
			},
		},
	}
}
