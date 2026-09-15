package seed

import (
	"errors"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestFormatSwissAmount(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		scale uint8
		group bool
		want  string
	}{
		{"plain two decimals", "45.50", 2, false, "45.50"},
		{"zero keeps its decimals", "0", 2, false, "0.00"},
		{"grouping above a thousand", "1345.63", 2, true, "1'345.63"},
		{"grouping suppressed", "1345.63", 2, false, "1345.63"},
		{"grouping of six digits", "250000.00", 2, true, "250'000.00"},
		{"grouping of seven digits", "1234567.89", 2, true, "1'234'567.89"},
		{"no grouping under a thousand", "999.99", 2, true, "999.99"},
		{"trailing minus", "-1234.50", 2, true, "1'234.50-"},
		{"trailing minus small", "-0.05", 2, false, "0.05-"},
		{"quantity at three decimals", "12", 3, false, "12.000"},
		{"negative quantity", "-1.5", 3, false, "1.500-"},
		{"unit price at four decimals", "45.5", 4, false, "45.5000"},
		{"unit price narrowed to two", "45.5000", 2, false, "45.50"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FormatSwissAmount(mustParseDec(tc.in), tc.scale, tc.group)
			if err != nil {
				t.Fatalf("FormatSwissAmount(%s): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("FormatSwissAmount(%s, %d, %v) = %q, want %q", tc.in, tc.scale, tc.group, got, tc.want)
			}
		})
	}
}

func TestFormatSwissAmountRefusesToRound(t *testing.T) {
	// An input value is never rounded on the way into a file: rounding an amount
	// is how a supplier gets paid the wrong sum.
	if _, err := FormatSwissAmount(mustParseDec("45.505"), 2, false); !errors.Is(err, ErrScaleInexact) {
		t.Fatalf("want ErrScaleInexact for 45.505 at scale 2, got %v", err)
	}
}

func TestTTMMJJRoundTrip(t *testing.T) {
	tests := []struct {
		date, ttmmjj string
	}{
		{"2026-01-15", "150126"},
		{"2026-03-29", "290326"},
		{"2026-12-31", "311226"},
		// The century pivot: 70..99 are 19xx, 00..69 are 20xx.
		{"1970-01-01", "010170"},
		{"1999-12-31", "311299"},
		{"2069-12-31", "311269"},
		{"2000-01-01", "010100"},
	}
	for _, tc := range tests {
		got, err := FormatTTMMJJ(tc.date)
		if err != nil {
			t.Fatalf("FormatTTMMJJ(%s): %v", tc.date, err)
		}
		if got != tc.ttmmjj {
			t.Errorf("FormatTTMMJJ(%s) = %q, want %q", tc.date, got, tc.ttmmjj)
		}
		back, err := ParseTTMMJJ(tc.ttmmjj)
		if err != nil {
			t.Fatalf("ParseTTMMJJ(%s): %v", tc.ttmmjj, err)
		}
		if back != tc.date {
			t.Errorf("ParseTTMMJJ(%q) = %q, want %q", tc.ttmmjj, back, tc.date)
		}
	}
}

func TestParseTTMMJJRejects(t *testing.T) {
	for _, in := range []string{"", "15012", "1501267", "15O126", "320126", "151326", "000126"} {
		if _, err := ParseTTMMJJ(in); err == nil {
			t.Errorf("ParseTTMMJJ(%q) accepted an invalid value", in)
		}
	}
}

func TestFormatTTMMJJHHMM(t *testing.T) {
	got, err := FormatTTMMJJHHMM("2026-04-16", 6, 19)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1604260619" {
		t.Errorf("got %q, want %q", got, "1604260619")
	}
	if _, err := FormatTTMMJJHHMM("2026-04-16", 24, 0); err == nil {
		t.Error("hour 24 was accepted")
	}
}

func TestFormatHostRate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0.9245", "0,924500"},
		{"0.931", "0,931000"},
		{"1.08225", "1,082250"},
		{"0.5563", "0,556300"},
		{"1234.56789", "1.234,567890"},
		{"1234567.1", "1.234.567,100000"},
	}
	for _, tc := range tests {
		got, err := FormatHostRate(mustParseDec(tc.in))
		if err != nil {
			t.Fatalf("FormatHostRate(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("FormatHostRate(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := FormatHostRate(mustParseDec("0.1234567")); !errors.Is(err, ErrScaleInexact) {
		t.Errorf("a seven digit rate must not be truncated to six, got %v", err)
	}
}

func TestQuoteLegacyField(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Montage", "Montage"},
		{"Wartung Presse 3; Rahmenvertrag", `"Wartung Presse 3; Rahmenvertrag"`},
		{`Charge "A-12"`, `"Charge ""A-12"""`},
		{"Zeile 1\r\nZeile 2", "\"Zeile 1\r\nZeile 2\""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := QuoteLegacyField(tc.in); got != tc.want {
			t.Errorf("QuoteLegacyField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalPONumber(t *testing.T) {
	tests := []struct{ in, want string }{
		{"4500001234", "4500001234"},
		{" 4500001234 ", "4500001234"},
		{"04500001234", "4500001234"},
		{"0000004711", "4711"},
		{"PO-4500001234", "PO-4500001234"},
		{" PO-4500001234 ", "PO-4500001234"},
		{"po-4500001234", "po-4500001234"},
		{"0000", "0"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := CanonicalPONumber(tc.in); got != tc.want {
			t.Errorf("CanonicalPONumber(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The prefixed spelling is compared as-is, so it must NOT collapse onto the
	// bare digits.
	if CanonicalPONumber("PO-4500001234") == CanonicalPONumber("4500001234") {
		t.Error("the prefixed and the bare spelling must not canonicalize to the same key")
	}
}

func TestSplitPOReference(t *testing.T) {
	tests := []struct{ in, po, line string }{
		{"4500001234", "4500001234", ""},
		{"4500001234/00010", "4500001234", "00010"},
		{" PO-4500001234 /00020", "PO-4500001234", "00020"},
		{"", "", ""},
	}
	for _, tc := range tests {
		po, line := SplitPOReference(tc.in)
		if po != tc.po || line != tc.line {
			t.Errorf("SplitPOReference(%q) = (%q, %q), want (%q, %q)", tc.in, po, line, tc.po, tc.line)
		}
	}
}

func TestPadLeftZero(t *testing.T) {
	tests := []struct {
		v     int64
		width int
		want  string
	}{
		{417, 10, "0000000417"},
		{417, 7, "0000417"},
		{4711, 7, "0004711"},
		{4500001234, 10, "4500001234"},
		{12345, 3, "12345"},
	}
	for _, tc := range tests {
		if got := PadLeftZero(tc.v, tc.width); got != tc.want {
			t.Errorf("PadLeftZero(%d, %d) = %q, want %q", tc.v, tc.width, got, tc.want)
		}
	}
}

func TestZurichMidnightAcrossTheSwitch(t *testing.T) {
	// This is the whole DST trap in one table: local midnight on the switch day
	// is still +01:00, and only the next day is +02:00.
	tests := []struct{ date, want string }{
		{"2026-02-01", "2026-02-01T00:00:00+01:00"},
		{"2026-03-28", "2026-03-28T00:00:00+01:00"},
		{"2026-03-29", "2026-03-29T00:00:00+01:00"},
		{"2026-03-30", "2026-03-30T00:00:00+02:00"},
		{"2026-10-25", "2026-10-25T00:00:00+02:00"},
		{"2026-10-26", "2026-10-26T00:00:00+01:00"},
	}
	for _, tc := range tests {
		got, err := zurichMidnight(tc.date)
		if err != nil {
			t.Fatalf("zurichMidnight(%s): %v", tc.date, err)
		}
		if got != tc.want {
			t.Errorf("zurichMidnight(%s) = %q, want %q", tc.date, got, tc.want)
		}
		back, err := model.ZurichDateOf(got)
		if err != nil {
			t.Fatal(err)
		}
		if back != tc.date {
			t.Errorf("ZurichDateOf(%q) = %q, want %q", got, back, tc.date)
		}
	}
}

func TestConvertQuantity(t *testing.T) {
	tests := []struct {
		name    string
		qty     string
		conv    UoMConversion
		want    string
		wantErr bool
	}{
		{"carton of twelve", "1.500", UoMConversion{AltUoM: "CTN", Numerator: 12, Denominator: 1, BaseUoM: "EA"}, "18.000", false},
		{"ton to kilogram", "2.500", UoMConversion{AltUoM: UoMTon, Numerator: 1000, Denominator: 1, BaseUoM: "KG"}, "2500.000", false},
		{"identity", "7.250", UoMConversion{AltUoM: "KG", Numerator: 1, Denominator: 1, BaseUoM: "KG"}, "7.250", false},
		{"exactly three decimals", "1.000", UoMConversion{Material: MaterialThreeDecimals, AltUoM: "CTN", Numerator: 1, Denominator: 8, BaseUoM: "EA"}, "0.125", false},
		{"non terminating is an error, never a round", "2.000", UoMConversion{Material: MaterialUnconvertible, AltUoM: "CTN", Numerator: 1000, Denominator: 3, BaseUoM: "EA"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := convertQuantity(mustParseDec(tc.qty), tc.conv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %s", got.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Errorf("convertQuantity(%s) = %s, want %s", tc.qty, got.String(), tc.want)
			}
		})
	}
}
