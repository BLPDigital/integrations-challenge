package importer

import (
	"errors"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestParseAmountAcceptedShapes(t *testing.T) {
	// The accepted shapes of BUILD-SPEC 16.1, enumerated. This table is a
	// contract: a row removed from it is a published promise withdrawn.
	tests := []struct {
		name     string
		in       string
		format   NumericFormat
		want     string
		currency string
	}{
		{name: "plain decimal", in: "1234.5", format: NumericFormat{MaxFractionDigits: 2}, want: "1234.5"},
		{name: "apostrophe grouping", in: "1'234.50", format: NumericFormat{MaxFractionDigits: 2}, want: "1234.50"},
		{name: "apostrophe grouping two groups", in: "1'234'567.89", format: NumericFormat{MaxFractionDigits: 2}, want: "1234567.89"},
		{name: "typographic apostrophe U+2019", in: "12’345.67", format: NumericFormat{MaxFractionDigits: 2}, want: "12345.67"},
		{name: "swiss shorthand", in: "12'345.-", format: NumericFormat{MaxFractionDigits: 2}, want: "12345.00"},
		{name: "trailing minus", in: "1234.00-", format: NumericFormat{MaxFractionDigits: 2}, want: "-1234.00"},
		{name: "leading minus", in: "-1234.00", format: NumericFormat{MaxFractionDigits: 2}, want: "-1234.00"},
		{name: "accounting parentheses", in: "(1'234.50)", format: NumericFormat{MaxFractionDigits: 2}, want: "-1234.50"},
		{name: "currency prefix", in: "CHF 1'234.50", format: NumericFormat{MaxFractionDigits: 2}, want: "1234.50", currency: "CHF"},
		{name: "currency prefix with wide spacing", in: "EUR   99.00", format: NumericFormat{MaxFractionDigits: 2}, want: "99.00", currency: "EUR"},
		{name: "negative zero normalizes", in: "-0.00", format: NumericFormat{MaxFractionDigits: 2}, want: "0.00"},
		{name: "integer field", in: "12'345", format: NumericFormat{}, want: "12345"},
		{name: "rate with six digits", in: "1.082250", format: NumericFormat{MaxFractionDigits: 6}, want: "1.082250"},
		{name: "unit price with four digits", in: "45.5000", format: NumericFormat{MaxFractionDigits: 4}, want: "45.5000"},
		{name: "leading zeros in the integer part", in: "0000417.00", format: NumericFormat{MaxFractionDigits: 2}, want: "417.00"},
		{name: "comma decimal separator", in: "1'234,50", format: NumericFormat{DecimalSeparator: ",", MaxFractionDigits: 2}, want: "1234.50"},
		{name: "declared dot grouping with comma decimal", in: "1.234,50", format: NumericFormat{DecimalSeparator: ",", ThousandsSeparator: ".", MaxFractionDigits: 2}, want: "1234.50"},
		{name: "declared space grouping", in: "1 234.50", format: NumericFormat{ThousandsSeparator: " ", MaxFractionDigits: 2}, want: "1234.50"},
		{name: "shorthand negative", in: "12'345.-", format: NumericFormat{MaxFractionDigits: 2}, want: "12345.00"},
		{name: "any scale", in: "1.123456789", format: NumericFormat{}.AnyScale(), want: "1.123456789"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, cur, err := ParseAmount(tc.in, tc.format)
			if err != nil {
				t.Fatalf("ParseAmount(%q) error = %v, want an accepted value", tc.in, err)
			}
			if got.String() != tc.want {
				t.Errorf("ParseAmount(%q) = %s, want %s", tc.in, got.String(), tc.want)
			}
			if cur != tc.currency {
				t.Errorf("ParseAmount(%q) currency = %q, want %q", tc.in, cur, tc.currency)
			}
		})
	}
}

func TestParseAmountRejects(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		format NumericFormat
		want   error
	}{
		{name: "US grouping", in: "1,234,567.89", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "US grouping with declared comma decimal", in: "1,234,567.89", format: NumericFormat{DecimalSeparator: ",", MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "empty", in: "", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "leading plus", in: "+1234.00", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "exponent", in: "1.2e3", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "two signs", in: "-1234.00-", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "parentheses and minus", in: "(1234.00-)", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "misplaced grouping", in: "1'23'456.00", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "grouping in the fraction", in: "1234.00'0", format: NumericFormat{MaxFractionDigits: 4}, want: ErrDecimalFormat},
		{name: "trailing separator", in: "1234.", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "no integer part", in: ".50", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "two decimal separators", in: "1.2.3", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "surrounding whitespace is the caller's to trim", in: " 1234.00 ", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "currency prefix without a space", in: "CHF1234.00", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "lowercase currency prefix", in: "chf 1234.00", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "words", in: "one thousand", format: NumericFormat{MaxFractionDigits: 2}, want: ErrDecimalFormat},
		{name: "three digits where two are allowed", in: "45.505", format: NumericFormat{MaxFractionDigits: 2}, want: ErrMoneyScale},
		{name: "four digits where three are allowed", in: "12.0001", format: NumericFormat{MaxFractionDigits: 3}, want: ErrMoneyScale},
		{name: "a fraction in an integer field", in: "30.5", format: NumericFormat{}, want: ErrMoneyScale},
		{name: "ten fraction digits", in: "1.0123456789", format: NumericFormat{}.AnyScale(), want: ErrMoneyScale},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseAmount(tc.in, tc.format)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseAmount(%q) error = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

func TestParseAmountNeverRounds(t *testing.T) {
	// The single most important property of this parser. A value that does not
	// fit its field is a defect the sender fixes, not a number this code
	// improves: rounding an input is how a supplier gets paid the wrong amount.
	for _, in := range []string{"45.505", "45.5049", "0.001", "-0.005"} {
		got, _, err := ParseAmount(in, NumericFormat{MaxFractionDigits: 2})
		if !errors.Is(err, ErrMoneyScale) {
			t.Fatalf("ParseAmount(%q) error = %v, want ErrMoneyScale", in, err)
		}
		if !got.IsZero() {
			t.Fatalf("ParseAmount(%q) returned %s with an error; a rejected value must not be usable", in, got)
		}
	}
}

func TestParseAmountOverflow(t *testing.T) {
	_, _, err := ParseAmount("99999999999999999999.00", NumericFormat{MaxFractionDigits: 2})
	if !errors.Is(err, model.ErrOverflow) {
		t.Fatalf("error = %v, want model.ErrOverflow", err)
	}
}

func TestParseInteger(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "30", want: 30},
		{in: "0", want: 0},
		{in: "007", want: 7},
		{in: "-14", want: -14},
		{in: "", wantErr: true},
		{in: "+7", wantErr: true},
		{in: "1'000", wantErr: true},
		{in: "30.0", wantErr: true},
		{in: "30-", wantErr: true},
		{in: " 30", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseInteger(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseInteger(%q) = %d, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInteger(%q) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseInteger(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		pattern string
		want    string
		wantErr error
	}{
		{name: "canonical", in: "2026-01-15", pattern: "", want: "2026-01-15"},
		{name: "canonical under a declared pattern", in: "2026-01-15", pattern: DatePatternDotted, want: "2026-01-15"},
		{name: "dotted", in: "15.01.2026", pattern: DatePatternDotted, want: "2026-01-15"},
		{name: "slashed day first", in: "15/01/2026", pattern: DatePatternSlashedDMY, want: "2026-01-15"},
		{name: "slashed month first", in: "01/15/2026", pattern: DatePatternSlashedMDY, want: "2026-01-15"},
		{name: "compact", in: "20260115", pattern: DatePatternCompact, want: "2026-01-15"},
		{name: "leap day", in: "29.02.2024", pattern: DatePatternDotted, want: "2024-02-29"},

		{name: "an undeclared pattern is never guessed", in: "15.01.2026", pattern: DatePatternRFC3339, wantErr: ErrDateFormat},
		{name: "the other slashed pattern is never guessed", in: "15/01/2026", pattern: DatePatternSlashedMDY, wantErr: ErrDateFormat},
		{name: "a day the calendar has not", in: "30.02.2026", pattern: DatePatternDotted, wantErr: ErrDateFormat},
		{name: "two digit year", in: "15.01.26", pattern: DatePatternDotted, wantErr: ErrDateFormat},
		{name: "empty", in: "", pattern: DatePatternDotted, wantErr: ErrDateFormat},
		{name: "unknown pattern", in: "2026-01-15", pattern: "TTMMJJ", wantErr: ErrDatePatternUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDate(tc.in, tc.pattern)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseDate(%q, %q) error = %v, want %v", tc.in, tc.pattern, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDate(%q, %q) error = %v", tc.in, tc.pattern, err)
			}
			if got != tc.want {
				t.Errorf("ParseDate(%q, %q) = %q, want %q", tc.in, tc.pattern, got, tc.want)
			}
		})
	}
}

func TestSlashedPatternsAreDistinguishedOnlyByDeclaration(t *testing.T) {
	// 03/04/2026 is the third of April and the fourth of March, and nothing in
	// the value says which. Both readings are available and each is reachable
	// only by declaring it, which is the whole point of refusing to guess.
	dmy, err := ParseDate("03/04/2026", DatePatternSlashedDMY)
	if err != nil {
		t.Fatal(err)
	}
	mdy, err := ParseDate("03/04/2026", DatePatternSlashedMDY)
	if err != nil {
		t.Fatal(err)
	}
	if dmy != "2026-04-03" || mdy != "2026-03-04" {
		t.Fatalf("dmy = %q, mdy = %q; want 2026-04-03 and 2026-03-04", dmy, mdy)
	}
}

func TestDatePatternsCoversEveryAcceptedPattern(t *testing.T) {
	patterns := DatePatterns()
	if len(patterns) != 5 {
		t.Fatalf("DatePatterns() = %v, want the five declarable patterns", patterns)
	}
	for _, p := range patterns {
		if _, err := ParseDate("2026-01-15", p); err != nil {
			t.Errorf("the canonical form is not accepted under the declared pattern %s: %v", p, err)
		}
	}
	for i := 1; i < len(patterns); i++ {
		if patterns[i-1] >= patterns[i] {
			t.Errorf("DatePatterns() is not sorted: %v", patterns)
		}
	}
}

func TestNumericFormatAnyScale(t *testing.T) {
	f := NumericFormat{MaxFractionDigits: 2}
	if f.AnyScale().MaxFractionDigits >= 0 {
		t.Errorf("AnyScale did not lift the cap: %+v", f.AnyScale())
	}
	if f.MaxFractionDigits != 2 {
		t.Error("AnyScale mutated its receiver")
	}
}
