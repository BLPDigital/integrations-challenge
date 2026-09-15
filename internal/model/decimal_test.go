package model

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestParseDecimal(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		unscaled int64
		scale    uint8
	}{
		{"integer", "45", 45, 0},
		{"zero", "0", 0, 0},
		{"one fraction digit", "45.5", 455, 1},
		{"trailing zeros are kept", "45.5000", 455000, 4},
		{"negative", "-1.05", -105, 2},
		{"negative zero", "-0.00", 0, 2},
		{"leading zeros", "007.50", 750, 2},
		{"nine fraction digits", "0.123456789", 123456789, 9},
		{"rate", "1.082250", 1082250, 6},
		{"max int64", "9223372036854775807", math.MaxInt64, 0},
		{"min representable", "-9223372036854775807", -math.MaxInt64, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDecimal(tc.in)
			if err != nil {
				t.Fatalf("ParseDecimal(%q) = %v", tc.in, err)
			}
			if got.Unscaled() != tc.unscaled || got.Scale() != tc.scale {
				t.Fatalf("ParseDecimal(%q) = {%d,%d}, want {%d,%d}",
					tc.in, got.Unscaled(), got.Scale(), tc.unscaled, tc.scale)
			}
		})
	}
}

func TestParseDecimalErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrDecimalSyntax},
		{"leading space", " 1", ErrDecimalSyntax},
		{"trailing space", "1 ", ErrDecimalSyntax},
		{"explicit plus", "+1", ErrDecimalSyntax},
		{"comma decimal", "1,5", ErrDecimalSyntax},
		{"apostrophe grouping", "12'345.00", ErrDecimalSyntax},
		{"comma grouping", "12,345.00", ErrDecimalSyntax},
		{"exponent", "1e3", ErrDecimalSyntax},
		{"exponent upper", "1E3", ErrDecimalSyntax},
		{"no integer part", ".5", ErrDecimalSyntax},
		{"trailing point", "5.", ErrDecimalSyntax},
		{"bare point", ".", ErrDecimalSyntax},
		{"bare minus", "-", ErrDecimalSyntax},
		{"two points", "1.2.3", ErrDecimalSyntax},
		{"letters", "abc", ErrDecimalSyntax},
		{"nbsp", "1 5", ErrDecimalSyntax},
		{"ten fraction digits", "0.1234567890", ErrScaleRange},
		{"magnitude overflow", "9223372036854775808", ErrOverflow},
		{"min int64 excluded", "-9223372036854775808", ErrOverflow},
		{"unscaled overflow via scale", "92233720368547758.08", ErrOverflow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDecimal(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("ParseDecimal(%q) = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

func TestDecimalString(t *testing.T) {
	tests := []struct{ in, want string }{
		{"45", "45"},
		{"45.5", "45.5"},
		{"45.5000", "45.5000"},
		{"-1.05", "-1.05"},
		{"-0.00", "0.00"},
		{"0.001", "0.001"},
		{"007.50", "7.50"},
		{"-9223372036854775807", "-9223372036854775807"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := MustDecimal(tc.in).String(); got != tc.want {
				t.Fatalf("MustDecimal(%q).String() = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// A negative unscaled value smaller than the scale still renders padded.
	d, err := NewDecimal(-5, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.String(); got != "-0.005" {
		t.Fatalf("String() = %q, want %q", got, "-0.005")
	}
}

func TestRescale(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		scale uint8
		want  string
		err   error
	}{
		{"up", "45.5", 4, "45.5000", nil},
		{"same", "45.50", 2, "45.50", nil},
		{"down exact", "45.5000", 1, "45.5", nil},
		{"down to integer", "45.000", 0, "45", nil},
		{"down inexact", "45.505", 2, "", ErrInexact},
		{"down inexact tiny", "0.001", 2, "", ErrInexact},
		{"scale out of range", "1.0", 10, "", ErrScaleRange},
		{"up overflow", "9223372036854775807", 1, "", ErrOverflow},
		{"negative down exact", "-45.500", 1, "-45.5", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MustDecimal(tc.in).Rescale(tc.scale)
			if !errors.Is(err, tc.err) {
				t.Fatalf("Rescale(%d) error = %v, want %v", tc.scale, err, tc.err)
			}
			if tc.err == nil && got.String() != tc.want {
				t.Fatalf("Rescale(%d) = %q, want %q", tc.scale, got.String(), tc.want)
			}
		})
	}
}

func TestAddSub(t *testing.T) {
	tests := []struct {
		name    string
		a, b    string
		wantAdd string
		wantSub string
	}{
		{"same scale", "1.05", "2.05", "3.10", "-1.00"},
		{"mixed scale", "1.5", "2.005", "3.505", "-0.505"},
		{"integer and fraction", "10", "0.01", "10.01", "9.99"},
		{"negative", "-1.05", "-2.05", "-3.10", "1.00"},
		{"to zero", "1.50", "1.5", "3.00", "0.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := MustDecimal(tc.a), MustDecimal(tc.b)
			sum, err := a.Add(b)
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if sum.String() != tc.wantAdd {
				t.Fatalf("Add = %q, want %q", sum.String(), tc.wantAdd)
			}
			diff, err := a.Sub(b)
			if err != nil {
				t.Fatalf("Sub: %v", err)
			}
			if diff.String() != tc.wantSub {
				t.Fatalf("Sub = %q, want %q", diff.String(), tc.wantSub)
			}
		})
	}
}

func TestAddSubOverflow(t *testing.T) {
	max := MustDecimal("9223372036854775807")
	one := MustDecimal("1")
	if _, err := max.Add(one); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add overflow = %v, want ErrOverflow", err)
	}
	min := MustDecimal("-9223372036854775807")
	if _, err := min.Sub(one); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Sub overflow = %v, want ErrOverflow", err)
	}
	// Rescaling to the common scale overflows before the addition does.
	if _, err := max.Add(MustDecimal("0.01")); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add mixed-scale overflow = %v, want ErrOverflow", err)
	}
}

func TestCmpAndEquality(t *testing.T) {
	tests := []struct {
		a, b        string
		cmp         int
		equal       bool
		equalStrict bool
	}{
		{"1.50", "1.5", 0, true, false},
		{"1.5", "1.5", 0, true, true},
		{"1.50", "1.51", -1, false, false},
		{"-1.50", "1.50", -1, false, false},
		{"-1.5", "-1.50", 0, true, false},
		{"0", "0.000", 0, true, false},
		{"9223372036854775807", "9223372036854775806", 1, false, false},
		{"0.000000001", "0", 1, false, false},
		{"-0.000000001", "0", -1, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			a, b := MustDecimal(tc.a), MustDecimal(tc.b)
			if got := a.Cmp(b); got != tc.cmp {
				t.Fatalf("Cmp = %d, want %d", got, tc.cmp)
			}
			if got := b.Cmp(a); got != -tc.cmp {
				t.Fatalf("reverse Cmp = %d, want %d", got, -tc.cmp)
			}
			if got := a.Equal(b); got != tc.equal {
				t.Fatalf("Equal = %v, want %v", got, tc.equal)
			}
			if got := a.EqualStrict(b); got != tc.equalStrict {
				t.Fatalf("EqualStrict = %v, want %v", got, tc.equalStrict)
			}
		})
	}
}

func TestSignZeroNegAbs(t *testing.T) {
	if got := MustDecimal("-0.00").Sign(); got != 0 {
		t.Fatalf("Sign(-0.00) = %d, want 0", got)
	}
	if !MustDecimal("-0.00").IsZero() {
		t.Fatal("IsZero(-0.00) = false")
	}
	if got := MustDecimal("-1.05").Neg().String(); got != "1.05" {
		t.Fatalf("Neg = %q, want 1.05", got)
	}
	if got := MustDecimal("1.05").Neg().String(); got != "-1.05" {
		t.Fatalf("Neg = %q, want -1.05", got)
	}
	if got := MustDecimal("-1.05").Abs().String(); got != "1.05" {
		t.Fatalf("Abs = %q, want 1.05", got)
	}
	// The unrepresentable -2^63 cannot be constructed, so Neg is always exact.
	if _, err := NewDecimal(math.MinInt64, 0); !errors.Is(err, ErrOverflow) {
		t.Fatalf("NewDecimal(MinInt64) = %v, want ErrOverflow", err)
	}
}

func TestMulRate(t *testing.T) {
	tests := []struct {
		name        string
		value, rate string
		factor      int64
		targetScale uint8
		want        string
	}{
		// The graded case: half away from zero, not banker's rounding.
		{"GBP half up not banker", "100.00", "1.082250", 1, 2, "108.23"},
		{"negative half away from zero", "-100.00", "1.082250", 1, 2, "-108.23"},
		{"JPY quoted per 100 units", "250000", "0.612300", 100, 2, "1530.75"},
		{"JPY factor ignored would be 100x", "250000", "0.612300", 1, 2, "153075.00"},
		{"exact half at scale 0", "2.5", "1.000000", 1, 0, "3"},
		{"exact negative half at scale 0", "-2.5", "1.000000", 1, 0, "-3"},
		{"third decimal half", "1.005", "1.000000", 1, 2, "1.01"},
		{"eighth", "0.125", "1.000000", 1, 2, "0.13"},
		{"below half rounds down", "1.004", "1.000000", 1, 2, "1.00"},
		{"identity keeps value", "9223372036854775.807", "1.000000", 1, 3, "9223372036854775.807"},
		{"128 bit intermediate", "1000000000000000000", "1.500000", 1, 0, "1500000000000000000"},
		{"factor seven", "12345.678", "0.123456", 7, 4, "217.7354"},
		{"repeating fraction", "1", "3.000000", 7, 9, "0.428571429"},
		{"zero value", "0.00", "1.082250", 1, 2, "0.00"},
		{"scale up target", "1.00", "1.500000", 1, 4, "1.5000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MustDecimal(tc.value).MulRate(MustDecimal(tc.rate), tc.factor, tc.targetScale)
			if err != nil {
				t.Fatalf("MulRate: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("MulRate = %q, want %q", got.String(), tc.want)
			}
			if got.Scale() != tc.targetScale {
				t.Fatalf("scale = %d, want %d", got.Scale(), tc.targetScale)
			}
		})
	}
}

func TestMulRateBankersRoundingIsWrong(t *testing.T) {
	// Banker's rounding would produce 108.22 and 2 respectively; commercial
	// rounding must not.
	got, err := MustDecimal("100.00").MulRate(MustDecimal("1.082250"), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() == "108.22" {
		t.Fatal("MulRate used banker's rounding")
	}
	got, err = MustDecimal("2.5").MulRate(MustDecimal("1.000000"), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "3" {
		t.Fatalf("MulRate(2.5) = %q, want 3", got.String())
	}
}

func TestMulRateErrors(t *testing.T) {
	tests := []struct {
		name        string
		value, rate string
		factor      int64
		targetScale uint8
		want        error
	}{
		{"zero factor", "1.00", "1.000000", 0, 2, ErrFactorRange},
		{"negative factor", "1.00", "1.000000", -1, 2, ErrFactorRange},
		{"scale out of range", "1.00", "1.000000", 1, 10, ErrScaleRange},
		{"result overflows int64", "9223372036854775.807", "1.000000", 1, 4, ErrOverflow},
		{"product overflows int64 but not 128 bits", "9223372036854775807", "2.000000", 1, 0, ErrOverflow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MustDecimal(tc.value).MulRate(MustDecimal(tc.rate), tc.factor, tc.targetScale)
			if !errors.Is(err, tc.want) {
				t.Fatalf("MulRate error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMinorUnits(t *testing.T) {
	tests := []struct {
		in    string
		scale uint8
		want  int64
		err   error
	}{
		{"45.50", 2, 4550, nil},
		{"45.5", 2, 4550, nil},
		{"45.5000", 2, 4550, nil},
		{"250000", 0, 250000, nil},
		{"1.5", 0, 0, ErrInexact},
		{"45.505", 2, 0, ErrInexact},
		{"-1.05", 2, -105, nil},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := MustDecimal(tc.in).MinorUnits(tc.scale)
			if !errors.Is(err, tc.err) {
				t.Fatalf("MinorUnits error = %v, want %v", err, tc.err)
			}
			if tc.err == nil && got != tc.want {
				t.Fatalf("MinorUnits = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDecimalJSON(t *testing.T) {
	type wrapper struct {
		Amount Decimal `json:"amount"`
	}
	b, err := json.Marshal(wrapper{Amount: MustDecimal("45.5000")})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"amount":"45.5000"}` {
		t.Fatalf("Marshal = %s, want {\"amount\":\"45.5000\"}", b)
	}

	var w wrapper
	if err := json.Unmarshal([]byte(`{"amount":"45.5000"}`), &w); err != nil {
		t.Fatal(err)
	}
	if !w.Amount.EqualStrict(MustDecimal("45.5000")) {
		t.Fatalf("Unmarshal = %v, want 45.5000 at scale 4", w.Amount)
	}

	for _, in := range []string{`{"amount":45.5}`, `{"amount":45}`, `{"amount":null}`, `{"amount":true}`, `{"amount":[]}`} {
		var w wrapper
		err := json.Unmarshal([]byte(in), &w)
		if !errors.Is(err, ErrNotString) {
			t.Fatalf("Unmarshal(%s) = %v, want ErrNotString", in, err)
		}
	}
	var w2 wrapper
	if err := json.Unmarshal([]byte(`{"amount":"1,5"}`), &w2); !errors.Is(err, ErrDecimalSyntax) {
		t.Fatalf("Unmarshal(\"1,5\") = %v, want ErrDecimalSyntax", err)
	}
}

func TestMustDecimalPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustDecimal did not panic")
		}
	}()
	MustDecimal("1,5")
}

func TestFromMinor(t *testing.T) {
	d, err := FromMinor(10823, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "108.23" {
		t.Fatalf("FromMinor = %q, want 108.23", d.String())
	}
	if _, err := FromMinor(1, 10); !errors.Is(err, ErrScaleRange) {
		t.Fatalf("FromMinor scale 10 = %v, want ErrScaleRange", err)
	}
}
