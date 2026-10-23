package seed

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestLegacyFileName(t *testing.T) {
	tests := []struct {
		mandant, date string
		run           int
		want          string
	}{
		{MandantCH10, "2026-04-16", 1, "KRED_0100_20260416_001.txt"},
		{MandantCH20, "2026-04-16", 2, "KRED_0200_20260416_002.txt"},
		{MandantCH10, "2026-01-31", 999, "KRED_0100_20260131_999.txt"},
	}
	for _, tc := range tests {
		got, err := LegacyFileName(tc.mandant, tc.date, tc.run)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("LegacyFileName(%s, %s, %d) = %q, want %q", tc.mandant, tc.date, tc.run, got, tc.want)
		}
		if ok := OKFileNameFor(got); ok != strings.TrimSuffix(tc.want, ".txt")+".ok" {
			t.Errorf("OKFileNameFor(%q) = %q", got, ok)
		}
	}
	for _, run := range []int{0, 1000, -1} {
		if _, err := LegacyFileName(MandantCH10, "2026-04-16", run); err == nil {
			t.Errorf("run number %d was accepted", run)
		}
	}
}

// legacyRecords splits a delivery into its records and their fields, honoring
// the quoting rules, so tests can assert on structure rather than on substrings.
func legacyRecords(t *testing.T, raw []byte) [][]string {
	t.Helper()
	text := DecodeCP1252(raw)
	var out [][]string
	var fields []string
	var cur strings.Builder
	inQuotes := false
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case inQuotes && c == '"':
			if i+1 < len(text) && text[i+1] == '"' {
				cur.WriteByte('"')
				i += 2
				continue
			}
			inQuotes = false
			i++
		case !inQuotes && c == '"' && cur.Len() == 0:
			inQuotes = true
			i++
		case !inQuotes && c == ';':
			fields = append(fields, cur.String())
			cur.Reset()
			i++
		case !inQuotes && c == '\r' && i+1 < len(text) && text[i+1] == '\n':
			fields = append(fields, cur.String())
			cur.Reset()
			out = append(out, fields)
			fields = nil
			i += 2
		default:
			cur.WriteByte(c)
			i++
		}
	}
	if inQuotes {
		t.Fatal("the delivery ends inside a quoted field")
	}
	if cur.Len() != 0 || len(fields) != 0 {
		t.Fatal("the delivery does not end with a record terminator")
	}
	return out
}

func TestLegacyFileStructure(t *testing.T) {
	for _, scenario := range []string{ScenarioS0, ScenarioS1, ScenarioS2} {
		t.Run(scenario, func(t *testing.T) {
			d := generated(t, scenario)
			for _, del := range d.Deliveries {
				recs := legacyRecords(t, del.Bytes)
				if len(recs) < 3 {
					t.Fatalf("%s has only %d records", del.Name, len(recs))
				}
				vor := recs[0]
				if vor[0] != RecordVorlauf {
					t.Fatalf("%s does not open with a Vorlaufsatz: %q", del.Name, vor[0])
				}
				if len(vor) != 8 {
					t.Errorf("%s Vorlaufsatz has %d fields, want 8", del.Name, len(vor))
				}
				if vor[1] != FormatVersion {
					t.Errorf("%s declares format version %q", del.Name, vor[1])
				}
				if vor[2] != del.Mandant {
					t.Errorf("%s Vorlaufsatz Mandant is %q, the delivery says %q", del.Name, vor[2], del.Mandant)
				}
				if vor[4] != QuoteCurrency || vor[5] != Sender || vor[6] != Receiver || vor[7] != TestIndicator {
					t.Errorf("%s Vorlaufsatz identities changed: %v", del.Name, vor)
				}
				if _, err := ParseTTMMJJ(vor[3][:6]); err != nil {
					t.Errorf("%s Vorlaufsatz timestamp %q is malformed: %v", del.Name, vor[3], err)
				}
				nach := recs[len(recs)-1]
				if nach[0] != RecordNachlauf {
					t.Fatalf("%s does not end with a Nachlaufsatz: %q", del.Name, nach[0])
				}
				if len(nach) != 5 {
					t.Errorf("%s Nachlaufsatz has %d fields, want 5", del.Name, len(nach))
				}
				kopf, pos := 0, 0
				lastKopfNumber := ""
				for _, r := range recs[1 : len(recs)-1] {
					switch r[0] {
					case RecordKopf:
						kopf++
						if len(r) != 16 {
							t.Fatalf("%s Kopfsatz has %d fields, want 16: %v", del.Name, len(r), r)
						}
						lastKopfNumber = r[1]
					case RecordPos:
						pos++
						if len(r) != 11 {
							t.Fatalf("%s Positionssatz has %d fields, want 11: %v", del.Name, len(r), r)
						}
						if r[1] != lastKopfNumber {
							t.Fatalf("%s: a Positionssatz of %q follows Kopfsatz %q: positions of different documents must never interleave",
								del.Name, r[1], lastKopfNumber)
						}
					default:
						t.Fatalf("%s carries unknown record type %q", del.Name, r[0])
					}
				}
				if kopf != del.KopfCount || pos != del.PosCount {
					t.Errorf("%s has %d/%d records, the delivery says %d/%d",
						del.Name, kopf, pos, del.KopfCount, del.PosCount)
				}
			}
		})
	}
}

func TestLegacyTrailerTotalsAreCorrect(t *testing.T) {
	// The generated file must be valid: its control totals agree with its own
	// records. The invalid variants live only in the fixtures of the Go task.
	for _, scenario := range Scenarios() {
		d := generated(t, scenario)
		for _, del := range d.Deliveries {
			recs := legacyRecords(t, del.Bytes)
			nach := recs[len(recs)-1]
			kopf, pos := 0, 0
			gross, lines := dec(0, 2), dec(0, 2)
			for _, r := range recs[1 : len(recs)-1] {
				switch r[0] {
				case RecordKopf:
					kopf++
					v, err := parseSwissAmount(r[8])
					if err != nil {
						t.Fatalf("%s: Bruttobetrag %q: %v", del.Name, r[8], err)
					}
					sum, err := gross.Add(v)
					if err != nil {
						t.Fatal(err)
					}
					if gross, err = sum.Rescale(2); err != nil {
						t.Fatal(err)
					}
				case RecordPos:
					pos++
					v, err := parseSwissAmount(r[8])
					if err != nil {
						t.Fatalf("%s: Positionsbetrag %q: %v", del.Name, r[8], err)
					}
					sum, err := lines.Add(v)
					if err != nil {
						t.Fatal(err)
					}
					if lines, err = sum.Rescale(2); err != nil {
						t.Fatal(err)
					}
				}
			}
			wantGross, err := FormatSwissAmount(gross, 2, true)
			if err != nil {
				t.Fatal(err)
			}
			wantLines, err := FormatSwissAmount(lines, 2, true)
			if err != nil {
				t.Fatal(err)
			}
			if nach[1] != itoa(kopf) || nach[2] != itoa(pos) {
				t.Errorf("%s trailer counts are %q/%q, the file has %d/%d",
					del.Name, nach[1], nach[2], kopf, pos)
			}
			if nach[3] != wantGross || nach[4] != wantLines {
				t.Errorf("%s trailer sums are %q/%q, the file sums to %q/%q",
					del.Name, nach[3], nach[4], wantGross, wantLines)
			}
		}
	}
}

// parseSwissAmount reads back an amount the way a receiver must: optional
// apostrophe grouping and a trailing minus.
func parseSwissAmount(s string) (model.Decimal, error) {
	t := strings.ReplaceAll(strings.TrimSpace(s), "'", "")
	if strings.HasSuffix(t, "-") {
		t = "-" + strings.TrimSuffix(t, "-")
	}
	return model.ParseDecimal(t)
}

func itoa(v int) string { return PadLeftZero(int64(v), 1) }

func TestLegacyFileCarriesItsPlantedDefects(t *testing.T) {
	d := generated(t, ScenarioS0)
	var all bytes.Buffer
	for _, del := range d.Deliveries {
		all.Write(del.Bytes)
	}
	raw := all.Bytes()
	text := DecodeCP1252(raw)

	t.Run("cp1252 high bytes", func(t *testing.T) {
		// Ordered explicitly so a failure names the same byte every run.
		want := []struct {
			name string
			b    byte
		}{
			{"euro sign", 0x80},
			{"typographic apostrophe", 0x92},
			{"en dash", 0x96},
			{"eszett", 0xDF},
			{"lower case a diaeresis", 0xE4},
			{"lower case o diaeresis", 0xF6},
			{"lower case u diaeresis", 0xFC},
			{"upper case a diaeresis", 0xC4},
			{"upper case o diaeresis", 0xD6},
			{"upper case u diaeresis", 0xDC},
		}
		for _, w := range want {
			if bytes.IndexByte(raw, w.b) < 0 {
				t.Errorf("the delivery carries no %s (%#x)", w.name, w.b)
			}
		}
	})
	t.Run("no undefined bytes", func(t *testing.T) {
		for _, b := range []byte{0x81, 0x8D, 0x8F, 0x90, 0x9D} {
			if bytes.IndexByte(raw, b) >= 0 {
				t.Errorf("the delivery carries the undefined Windows-1252 byte %#x", b)
			}
		}
	})
	t.Run("quoting", func(t *testing.T) {
		if !strings.Contains(text, `""`) {
			t.Error("no field carries a doubled quote")
		}
		if !strings.Contains(text, `; `) {
			t.Error("no quoted field carries a semicolon")
		}
		// An embedded CRLF inside a quoted field: a naive line splitter breaks
		// exactly here.
		found := false
		for _, del := range d.Deliveries {
			recs := legacyRecords(t, del.Bytes)
			for _, r := range recs {
				for _, f := range r {
					if strings.Contains(f, "\r\n") {
						found = true
					}
				}
			}
		}
		if !found {
			t.Error("no quoted field carries an embedded CRLF")
		}
	})
	t.Run("amount spellings", func(t *testing.T) {
		if !strings.Contains(text, "'") {
			t.Error("no amount uses apostrophe grouping")
		}
		if !strings.Contains(text, "-;") && !strings.Contains(text, "-\r\n") {
			t.Error("no amount carries a trailing minus")
		}
	})
	t.Run("century pivot dates", func(t *testing.T) {
		want := map[string]bool{"010170": false, "311269": false}
		for _, del := range d.Deliveries {
			for _, r := range legacyRecords(t, del.Bytes) {
				if r[0] != RecordKopf {
					continue
				}
				if _, ok := want[r[5]]; ok {
					want[r[5]] = true
				}
			}
		}
		for k, seen := range want {
			if !seen {
				t.Errorf("no Kopfsatz carries the pivot date %s", k)
			}
		}
	})
	t.Run("optional fields left empty", func(t *testing.T) {
		emptyCurrency, emptyVAT, emptyPositionCC, skonto := 0, 0, 0, 0
		for _, del := range d.Deliveries {
			for _, r := range legacyRecords(t, del.Bytes) {
				switch r[0] {
				case RecordKopf:
					if r[7] == "" {
						emptyCurrency++
					}
					if r[9] == "" {
						emptyVAT++
					}
					if r[12] != "" {
						skonto++
					}
				case RecordPos:
					if r[4] == "" {
						emptyPositionCC++
					}
				}
			}
		}
		if emptyCurrency == 0 {
			t.Error("no Kopfsatz omits the currency, so the Vorlauf default is never exercised")
		}
		if emptyVAT == 0 {
			t.Error("no Kopfsatz omits the VAT amount, so 'empty means 0.00' is never exercised")
		}
		if emptyPositionCC == 0 {
			t.Error("no Positionssatz omits the cost center, so the inheritance ambiguity never fires")
		}
		if skonto == 0 {
			t.Error("no Kopfsatz carries a Skonto, so the amount-or-percentage ambiguity never fires")
		}
	})
	t.Run("unit price scales", func(t *testing.T) {
		scales := map[int]int{}
		for _, del := range d.Deliveries {
			for _, r := range legacyRecords(t, del.Bytes) {
				if r[0] != RecordPos {
					continue
				}
				if i := strings.IndexByte(r[7], '.'); i >= 0 {
					scales[len(r[7])-i-1]++
				}
			}
		}
		if scales[2] == 0 || scales[4] == 0 {
			t.Errorf("Einzelpreis must occur with two and with four decimals, got %v", scales)
		}
	})
	t.Run("quantity and amount scales", func(t *testing.T) {
		for _, del := range d.Deliveries {
			for _, r := range legacyRecords(t, del.Bytes) {
				if r[0] != RecordPos {
					continue
				}
				q := strings.TrimSuffix(r[5], "-")
				if i := strings.IndexByte(q, '.'); i < 0 || len(q)-i-1 != 3 {
					t.Fatalf("Menge %q does not carry three decimals", r[5])
				}
				a := strings.TrimSuffix(strings.ReplaceAll(r[8], "'", ""), "-")
				if i := strings.IndexByte(a, '.'); i < 0 || len(a)-i-1 != 2 {
					t.Fatalf("Positionsbetrag %q does not carry exactly two decimals", r[8])
				}
			}
		}
	})
	t.Run("anhang a is reproduced", func(t *testing.T) {
		if !strings.Contains(text,
			"KOPF;0004711;0000000417;RE;290326;020426;4500001234/00010;EUR;1'345.63;100.83;V81;30;2.00;10;0012340;") {
			t.Error("the customer document's own worked example is not in the delivery")
		}
	})
}

func TestPrimaryDeliveryIsTheCH10File(t *testing.T) {
	for _, scenario := range Scenarios() {
		d := generated(t, scenario)
		if !strings.HasPrefix(d.LegacyFileName, "KRED_0100_") {
			t.Errorf("%s: the primary delivery is %q", scenario, d.LegacyFileName)
		}
		if d.OKFileName != OKFileNameFor(d.LegacyFileName) {
			t.Errorf("%s: sentinel %q does not match %q", scenario, d.OKFileName, d.LegacyFileName)
		}
		if !bytes.Equal(d.LegacyFile, d.Deliveries[0].Bytes) {
			t.Errorf("%s: LegacyFile is not Deliveries[0]", scenario)
		}
	}
}

func TestDeltaDeliveryRedelivers(t *testing.T) {
	spec, err := SpecOf(ScenarioS2)
	if err != nil {
		t.Fatal(err)
	}
	d := generated(t, ScenarioS2)
	firstSeen := map[string]string{}
	resent, amended := 0, 0
	for i, del := range d.Deliveries {
		for _, inv := range del.Invoices {
			key := inv.Key()
			hash := model.ContentHash(inv)
			prev, ok := firstSeen[key]
			if !ok {
				firstSeen[key] = hash
				continue
			}
			if i == 0 {
				t.Fatalf("invoice %q appears twice in the first delivery", key)
			}
			if prev == hash {
				resent++
			} else {
				amended++
			}
		}
	}
	if resent != spec.ResentInvoices {
		t.Errorf("%d invoices were re-sent unchanged, want %d", resent, spec.ResentInvoices)
	}
	if amended != spec.AmendedInvoices {
		t.Errorf("%d invoices were re-sent amended, want %d", amended, spec.AmendedInvoices)
	}
	if len(d.AmendedInvoices) != spec.AmendedInvoices {
		t.Errorf("AmendedInvoices holds %d, want %d", len(d.AmendedInvoices), spec.AmendedInvoices)
	}
	for _, inv := range d.AmendedInvoices {
		lineSum, err := inv.LineTotal()
		if err != nil {
			t.Fatal(err)
		}
		want, err := lineSum.Add(inv.VATAmount)
		if err != nil {
			t.Fatal(err)
		}
		if !want.Equal(inv.GrossAmount) {
			t.Errorf("amended invoice %q breaks the header total rule as well; it must break only the amount", inv.Key())
		}
	}
}
