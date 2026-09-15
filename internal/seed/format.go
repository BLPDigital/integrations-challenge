package seed

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// ErrScaleInexact reports an amount that cannot be written at the scale the
// target field demands without dropping a non-zero digit. The generator never
// rounds an input value, so it fails loudly instead.
var ErrScaleInexact = errors.New("seed: value does not fit the field scale exactly")

// dec builds a [model.Decimal] from an unscaled value and a scale. It panics on
// an out-of-range scale, which can only be a bug in a literal in this package,
// and exists so fixed rates and amounts can be written without a parse step.
func dec(unscaled int64, scale uint8) model.Decimal {
	d, err := model.NewDecimal(unscaled, scale)
	if err != nil {
		panic(err)
	}
	return d
}

// mustParseDec parses a plain decimal literal of this package. It panics on a
// malformed literal, which can only be a bug in this package.
func mustParseDec(s string) model.Decimal {
	d, err := model.ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// FormatSwissAmount renders an amount the way the KRED-EXP sender does: exactly
// scale fraction digits, a decimal point, an optional apostrophe as the
// thousands separator and, for a negative value, a minus sign AFTER the last
// digit.
//
// group selects whether the integer part carries apostrophes. The customer
// document calls the thousands separator optional, so the generator emits both
// spellings in the same file on purpose. The apostrophe is the ASCII one
// (U+0027); the typographic apostrophe U+2019 appears only in text fields.
//
// A value that does not fit the scale exactly is [ErrScaleInexact] rather than a
// rounded value: rounding an amount on the way into a file is how a supplier
// gets paid the wrong sum.
func FormatSwissAmount(d model.Decimal, scale uint8, group bool) (string, error) {
	r, err := d.Rescale(scale)
	if err != nil {
		return "", fmt.Errorf("%w: %s at scale %d: %v", ErrScaleInexact, d.String(), scale, err)
	}
	s := r.String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if group {
		intPart = groupApostrophe(intPart)
	}
	var b strings.Builder
	b.WriteString(intPart)
	if fracPart != "" {
		b.WriteByte('.')
		b.WriteString(fracPart)
	}
	if neg {
		b.WriteByte('-')
	}
	return b.String(), nil
}

// groupApostrophe inserts an ASCII apostrophe every three digits from the right.
func groupApostrophe(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	lead := len(digits) % 3
	if lead == 0 {
		lead = 3
	}
	var b strings.Builder
	b.WriteString(digits[:lead])
	for i := lead; i < len(digits); i += 3 {
		b.WriteByte('\'')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// FormatTTMMJJ renders a YYYY-MM-DD date as the six digit TTMMJJ (DDMMYY) of
// the customer document. Only the last two digits of the year survive, which is
// the whole point of the century pivot trap: the generator plants dates on both
// sides of it.
func FormatTTMMJJ(date string) (string, error) {
	d, err := model.ParseDate(date)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d%02d%02d", d.Day, d.Month, d.Year%100), nil
}

// ParseTTMMJJ is the inverse of [FormatTTMMJJ] and applies the documented pivot:
// a two digit year from 70 to 99 is 19xx, from 00 to 69 it is 20xx. It exists so
// the generator's own tests can prove the pivot dates it writes read back to the
// centuries it intended.
func ParseTTMMJJ(s string) (string, error) {
	if len(s) != 6 {
		return "", fmt.Errorf("seed: TTMMJJ must be six digits, got %q", s)
	}
	for i := 0; i < 6; i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", fmt.Errorf("seed: TTMMJJ must be six digits, got %q", s)
		}
	}
	dd := int(s[0]-'0')*10 + int(s[1]-'0')
	mm := int(s[2]-'0')*10 + int(s[3]-'0')
	yy := int(s[4]-'0')*10 + int(s[5]-'0')
	year := 2000 + yy
	if yy >= 70 {
		year = 1900 + yy
	}
	out := fmt.Sprintf("%04d-%02d-%02d", year, mm, dd)
	if _, err := model.ParseDate(out); err != nil {
		return "", err
	}
	return out, nil
}

// FormatTTMMJJHHMM renders the Vorlaufsatz creation timestamp: TTMMJJ followed
// by a zero padded hour and minute. The clock value is a constant of the
// scenario, never a reading of the wall clock.
func FormatTTMMJJHHMM(date string, hour, minute int) (string, error) {
	d, err := FormatTTMMJJ(date)
	if err != nil {
		return "", err
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return "", fmt.Errorf("seed: invalid clock %02d%02d", hour, minute)
	}
	return fmt.Sprintf("%s%02d%02d", d, hour, minute), nil
}

// FormatHostRate renders an exchange rate in the ERP host locale, which is the
// spelling the SOAP channel uses: a decimal comma, exactly six fraction digits
// and a dot as the thousands separator where the integer part needs one. It is
// deliberately a different number contract from the REST surface (plain decimal
// point) and from the legacy file (apostrophe grouping): one landscape with
// three number contracts is the actual job.
func FormatHostRate(d model.Decimal) (string, error) {
	r, err := d.Rescale(6)
	if err != nil {
		return "", fmt.Errorf("%w: rate %s at scale 6: %v", ErrScaleInexact, d.String(), err)
	}
	s := r.String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(groupDot(intPart))
	b.WriteByte(',')
	b.WriteString(fracPart)
	return b.String(), nil
}

// groupDot inserts a dot every three digits from the right, the host locale's
// thousands separator.
func groupDot(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	lead := len(digits) % 3
	if lead == 0 {
		lead = 3
	}
	var b strings.Builder
	b.WriteString(digits[:lead])
	for i := lead; i < len(digits); i += 3 {
		b.WriteByte('.')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// QuoteLegacyField applies the text delimiter rules of the customer document: a
// field is quoted only when it contains a semicolon, a double quote or a line
// break, and a double quote inside a quoted field is doubled.
func QuoteLegacyField(s string) string {
	if !strings.ContainsAny(s, ";\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// PadLeftZero renders a non-negative integer as a zero padded string of exactly
// width characters. It is the canonical spelling of every numeric key in this
// landscape, where leading zeros are significant.
func PadLeftZero(v int64, width int) string {
	s := fmt.Sprintf("%d", v)
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

// CanonicalPONumber normalizes a purchase order number for comparison, exactly
// as the published rule states: trim, and if the remainder is all digits compare
// numerically with leading zeros stripped, otherwise compare the trimmed string
// as-is and case-sensitively. An all-zero number canonicalizes to "0".
//
// It is used by the generator to resolve the three spellings it plants
// ("4500001234", "PO-4500001234" and the space padded form) back to one purchase
// order when it derives the expected exception set.
func CanonicalPONumber(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return t
		}
	}
	stripped := strings.TrimLeft(t, "0")
	if stripped == "" {
		return "0"
	}
	return stripped
}

// SplitPOReference splits a purchase order reference into its header number and
// an optional line number: "4500001234/00010" carries both, "4500001234" only
// the header. The line suffix keeps its leading zeros, because a purchase order
// line number is a string key.
func SplitPOReference(ref string) (poNumber, lineNo string) {
	t := strings.TrimSpace(ref)
	if i := strings.LastIndexByte(t, '/'); i >= 0 {
		return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+1:])
	}
	return t, ""
}
