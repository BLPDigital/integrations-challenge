package model

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// MaxScale is the highest number of fraction digits a Decimal can carry.
const MaxScale uint8 = 9

// Decimal errors. They are sentinel values; callers compare with errors.Is.
var (
	// ErrDecimalSyntax reports a malformed decimal literal.
	ErrDecimalSyntax = errors.New("model: invalid decimal syntax")
	// ErrScaleRange reports a scale above MaxScale.
	ErrScaleRange = errors.New("model: decimal scale out of range")
	// ErrInexact reports a rescale that would drop a non-zero digit.
	ErrInexact = errors.New("model: inexact decimal rescale")
	// ErrOverflow reports a result outside the representable unscaled range.
	ErrOverflow = errors.New("model: decimal overflow")
	// ErrFactorRange reports a non-positive MulRate factor.
	ErrFactorRange = errors.New("model: rate factor must be positive")
	// ErrNotString reports a JSON value that is not a string where a decimal was
	// expected. A JSON number is always this error: money never travels as a
	// JSON number in this landscape.
	ErrNotString = errors.New("model: decimal must be a JSON string")
)

// pow10 holds 10^0..10^18, the largest power of ten that fits in an int64.
var pow10 = [19]int64{
	1, 10, 100, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9,
	1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18,
}

// Decimal is an exact base-10 fixed-point number: value = unscaled * 10^-scale.
//
// The scale is part of the value's identity and is preserved as written by
// ParseDecimal: "45.5000" has scale 4, "45.5" has scale 1. Equal compares
// values across scales, EqualStrict compares scales too.
//
// The representable unscaled range is [-(2^63-1), 2^63-1]: -2^63 is deliberately
// excluded so Neg is always exact and never silently wraps. Any operation that
// would produce it returns ErrOverflow.
//
// The zero Decimal is the exact value 0 with scale 0.
type Decimal struct {
	unscaled int64
	scale    uint8
}

// NewDecimal returns the Decimal unscaled*10^-scale. It reports ErrScaleRange for
// a scale above MaxScale and ErrOverflow for the unrepresentable unscaled value
// math.MinInt64.
func NewDecimal(unscaled int64, scale uint8) (Decimal, error) {
	if scale > MaxScale {
		return Decimal{}, ErrScaleRange
	}
	if unscaled == math.MinInt64 {
		return Decimal{}, ErrOverflow
	}
	return Decimal{unscaled: unscaled, scale: scale}, nil
}

// FromMinor returns the Decimal for an amount expressed in minor units of a
// currency with the given scale, e.g. FromMinor(10823, 2) is 108.23.
func FromMinor(minor int64, scale uint8) (Decimal, error) { return NewDecimal(minor, scale) }

// ParseDecimal parses a plain decimal literal: an optional leading '-', one or
// more integer digits, and optionally '.' followed by one to MaxScale fraction
// digits. The resulting scale is the number of fraction digits as written.
//
// Everything else is ErrDecimalSyntax: the empty string, surrounding whitespace
// (callers trim), '+', grouping separators, exponents, a bare '.', a missing
// integer part (".5") and a trailing '.' ("5."). A magnitude that does not fit
// the representable unscaled range is ErrOverflow, more than MaxScale fraction
// digits is ErrScaleRange.
func ParseDecimal(s string) (Decimal, error) {
	if s == "" {
		return Decimal{}, ErrDecimalSyntax
	}
	i := 0
	neg := false
	if s[0] == '-' {
		neg = true
		i = 1
	}
	const limit = uint64(math.MaxInt64)
	var mag uint64
	intDigits := 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		d := uint64(s[i] - '0')
		if mag > (limit-d)/10 {
			return Decimal{}, ErrOverflow
		}
		mag = mag*10 + d
		intDigits++
	}
	if intDigits == 0 {
		return Decimal{}, ErrDecimalSyntax
	}
	scale := 0
	if i < len(s) && s[i] == '.' {
		i++
		fracDigits := 0
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			d := uint64(s[i] - '0')
			if mag > (limit-d)/10 {
				return Decimal{}, ErrOverflow
			}
			mag = mag*10 + d
			fracDigits++
		}
		if fracDigits == 0 {
			return Decimal{}, ErrDecimalSyntax
		}
		if fracDigits > int(MaxScale) {
			return Decimal{}, ErrScaleRange
		}
		scale = fracDigits
	}
	if i != len(s) {
		return Decimal{}, ErrDecimalSyntax
	}
	v := int64(mag)
	if neg {
		v = -v
	}
	return Decimal{unscaled: v, scale: uint8(scale)}, nil
}

// MustDecimal is ParseDecimal for literals known to be valid. It panics on
// error and is intended for tests and constant tables, never for a parse path.
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic("model: MustDecimal(" + strconv.Quote(s) + "): " + err.Error())
	}
	return d
}

// Unscaled returns the unscaled integer value.
func (d Decimal) Unscaled() int64 { return d.unscaled }

// Scale returns the number of fraction digits.
func (d Decimal) Scale() uint8 { return d.scale }

// IsZero reports whether d is exactly zero, at any scale.
func (d Decimal) IsZero() bool { return d.unscaled == 0 }

// Sign returns -1, 0 or +1 as d is negative, zero or positive.
func (d Decimal) Sign() int {
	switch {
	case d.unscaled < 0:
		return -1
	case d.unscaled > 0:
		return 1
	}
	return 0
}

// Neg returns -d at the same scale. It is always exact because -2^63 is not a
// representable unscaled value.
func (d Decimal) Neg() Decimal { return Decimal{unscaled: -d.unscaled, scale: d.scale} }

// Abs returns |d| at the same scale.
func (d Decimal) Abs() Decimal {
	if d.unscaled < 0 {
		return d.Neg()
	}
	return d
}

// Rescale returns d with exactly scale fraction digits. Increasing the scale is
// exact unless the widened unscaled value overflows (ErrOverflow); decreasing it
// succeeds only when every dropped digit is zero, else ErrInexact. Rescale never
// rounds: use MulRate for that.
func (d Decimal) Rescale(scale uint8) (Decimal, error) {
	if scale > MaxScale {
		return Decimal{}, ErrScaleRange
	}
	switch {
	case scale == d.scale:
		return d, nil
	case scale > d.scale:
		f := pow10[scale-d.scale]
		v := d.unscaled * f
		if d.unscaled != 0 && (v/f != d.unscaled || v == math.MinInt64) {
			return Decimal{}, ErrOverflow
		}
		return Decimal{unscaled: v, scale: scale}, nil
	default:
		f := pow10[d.scale-scale]
		if d.unscaled%f != 0 {
			return Decimal{}, ErrInexact
		}
		return Decimal{unscaled: d.unscaled / f, scale: scale}, nil
	}
}

// Add returns d+o at max(d.scale, o.scale). Overflow is ErrOverflow; the result
// is never saturated.
func (d Decimal) Add(o Decimal) (Decimal, error) {
	a, b, scale, err := align(d, o)
	if err != nil {
		return Decimal{}, err
	}
	sum := a + b
	if (a > 0 && b > 0 && sum <= 0) || (a < 0 && b < 0 && sum >= 0) || sum == math.MinInt64 {
		return Decimal{}, ErrOverflow
	}
	return Decimal{unscaled: sum, scale: scale}, nil
}

// Sub returns d-o at max(d.scale, o.scale). Overflow is ErrOverflow.
func (d Decimal) Sub(o Decimal) (Decimal, error) {
	a, b, scale, err := align(d, o)
	if err != nil {
		return Decimal{}, err
	}
	diff := a - b
	if (b < 0 && diff < a) || (b > 0 && diff > a) || diff == math.MinInt64 {
		return Decimal{}, ErrOverflow
	}
	return Decimal{unscaled: diff, scale: scale}, nil
}

// align returns both unscaled values at the common scale max(d.scale, o.scale).
func align(d, o Decimal) (a, b int64, scale uint8, err error) {
	scale = d.scale
	if o.scale > scale {
		scale = o.scale
	}
	da, err := d.Rescale(scale)
	if err != nil {
		return 0, 0, 0, err
	}
	ob, err := o.Rescale(scale)
	if err != nil {
		return 0, 0, 0, err
	}
	return da.unscaled, ob.unscaled, scale, nil
}

// Cmp returns -1, 0 or +1 as d is less than, equal to or greater than o. The
// comparison is exact and scale-insensitive; it never overflows because the
// aligned magnitudes are compared in 128 bits.
func (d Decimal) Cmp(o Decimal) int {
	sd, so := d.Sign(), o.Sign()
	switch {
	case sd != so:
		if sd < so {
			return -1
		}
		return 1
	case sd == 0:
		return 0
	}
	scale := d.scale
	if o.scale > scale {
		scale = o.scale
	}
	a, _ := u128From(magnitude(d.unscaled)).mulPow10(int(scale - d.scale))
	b, _ := u128From(magnitude(o.unscaled)).mulPow10(int(scale - o.scale))
	c := a.cmp(b)
	if sd < 0 {
		c = -c
	}
	return c
}

// Equal reports value equality across scales: 1.50 equals 1.5.
func (d Decimal) Equal(o Decimal) bool { return d.Cmp(o) == 0 }

// EqualStrict reports equality of both value and scale: 1.50 does not equal 1.5.
func (d Decimal) EqualStrict(o Decimal) bool { return d.unscaled == o.unscaled && d.scale == o.scale }

// MulRate computes d*rate/factor rounded half away from zero to targetScale.
// It is the single money conversion primitive: every FX conversion goes through
// it, and its rounding is commercial rounding ("kaufmännisch"), not banker's
// rounding, so GBP 100.00 at rate 1.082250 with factor 1 yields 108.23.
//
// factor is the per-unit quotation factor of the rate: a JPY rate quoted per 100
// units has factor 100, and passing 1 there would be wrong by 100x. It must be
// positive, else ErrFactorRange.
//
// The intermediate product is computed in 128 bits over two uint64 halves, so no
// int64 multiplication can overflow silently. A rounded result that does not fit
// the representable unscaled range is ErrOverflow.
func (d Decimal) MulRate(rate Decimal, factor int64, targetScale uint8) (Decimal, error) {
	if targetScale > MaxScale {
		return Decimal{}, ErrScaleRange
	}
	if factor <= 0 {
		return Decimal{}, ErrFactorRange
	}
	neg := (d.Sign() < 0) != (rate.Sign() < 0)
	// numerator = |d.unscaled| * |rate.unscaled| * 10^targetScale
	num, ok := mul64(magnitude(d.unscaled), magnitude(rate.unscaled)).mulPow10(int(targetScale))
	if !ok {
		return Decimal{}, ErrOverflow
	}
	// denominator = factor * 10^(d.scale+rate.scale)
	den, ok := u128From(uint64(factor)).mulPow10(int(d.scale) + int(rate.scale))
	if !ok {
		return Decimal{}, ErrOverflow
	}
	q, r := num.divMod(den)
	// Half away from zero: round up when the remainder is at least half the
	// divisor. Compared as r >= den-r to avoid doubling r.
	if !r.isZero() && r.cmp(den.sub(r)) >= 0 {
		if q, ok = q.add(u128From(1)); !ok {
			return Decimal{}, ErrOverflow
		}
	}
	if q.hi != 0 || q.lo > uint64(math.MaxInt64) {
		return Decimal{}, ErrOverflow
	}
	v := int64(q.lo)
	if neg {
		v = -v
	}
	return Decimal{unscaled: v, scale: targetScale}, nil
}

// MinorUnits returns the value in minor units of a currency with the given
// scale. It is an exact rescale, so a value with more fraction digits than the
// currency has minor digits is ErrInexact rather than a silent rounding.
func (d Decimal) MinorUnits(currencyScale uint8) (int64, error) {
	r, err := d.Rescale(currencyScale)
	if err != nil {
		return 0, err
	}
	return r.unscaled, nil
}

// String returns the plain decimal representation with exactly Scale fraction
// digits and no exponent, grouping or plus sign. Negative zero is normalized
// to "0": a zero value never carries a sign.
func (d Decimal) String() string {
	digits := strconv.FormatUint(magnitude(d.unscaled), 10)
	var b strings.Builder
	b.Grow(len(digits) + 3)
	if d.unscaled < 0 {
		b.WriteByte('-')
	}
	if d.scale == 0 {
		b.WriteString(digits)
		return b.String()
	}
	if n := int(d.scale) + 1 - len(digits); n > 0 {
		b.WriteString("0.")
		for ; n > 1; n-- {
			b.WriteByte('0')
		}
		b.WriteString(digits)
		return b.String()
	}
	cut := len(digits) - int(d.scale)
	b.WriteString(digits[:cut])
	b.WriteByte('.')
	b.WriteString(digits[cut:])
	return b.String()
}

// magnitude returns |v| as a uint64. It is safe for every representable
// unscaled value; math.MinInt64 cannot occur (see Decimal).
func magnitude(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

// MarshalJSON encodes the decimal as a JSON string, never as a JSON number, so
// no consumer can round-trip it through a float64.
func (d Decimal) MarshalJSON() ([]byte, error) {
	s := d.String()
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	out = append(out, s...)
	return append(out, '"'), nil
}

// UnmarshalJSON decodes a decimal from a JSON string. Any other JSON value,
// including a number and null, is ErrNotString: callers translate that into
// E_MONEY_NOT_INTEGER_MINOR or E_KEY_NOT_STRING as appropriate for the field.
func (d *Decimal) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return ErrNotString
	}
	lit, err := strconv.Unquote(s)
	if err != nil {
		return ErrDecimalSyntax
	}
	v, err := ParseDecimal(lit)
	if err != nil {
		return err
	}
	*d = v
	return nil
}
