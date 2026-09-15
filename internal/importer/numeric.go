package importer

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// Numeric and date errors. They are data defects, so a caller turns them into a
// [Diagnostic] rather than returning them: [ErrDecimalFormat] is
// [CodeDecimalFormat], [ErrMoneyScale] is [CodeMoneyScale], and both date errors
// are [CodeDateFormat].
var (
	// ErrDecimalFormat reports a value outside the accepted numeric shapes of
	// BUILD-SPEC 16.1.
	ErrDecimalFormat = errors.New("importer: value is not an accepted numeric shape")
	// ErrMoneyScale reports more fraction digits than the field allows. It is
	// never resolved by rounding: rounding an input is how a supplier gets paid
	// the wrong amount.
	ErrMoneyScale = errors.New("importer: too many fraction digits for this field")
	// ErrDateFormat reports a value that is neither an RFC 3339 calendar date
	// nor the declared pattern.
	ErrDateFormat = errors.New("importer: value is not a date in the declared format")
	// ErrDatePatternUnknown reports a declared date pattern this package does
	// not implement. It is deliberately not a fallback to sniffing: guessing
	// between 03/04/2026 and 04/03/2026 moves a due date by a month.
	ErrDatePatternUnknown = errors.New("importer: unknown date pattern")
)

// The typographic and plain apostrophes Swiss senders use for grouping. Both are
// always accepted, whatever the manifest declares, because BUILD-SPEC 16.1
// enumerates both unconditionally: text fields are typed by hand on Windows and
// the autocorrect that turns ' into ’ does not stop at the amount column.
const (
	// GroupApostrophe is U+0027 APOSTROPHE.
	GroupApostrophe = "'"
	// GroupTypographicApostrophe is U+2019 RIGHT SINGLE QUOTATION MARK.
	GroupTypographicApostrophe = "’"
)

// A NumericFormat is the numeric dialect of one field: the separators its values
// use and how many fraction digits it allows. The zero value is the Swiss
// default of these feeds - dot decimal separator, apostrophe grouping - for an
// integer field; a field with a fraction sets MaxFractionDigits, and one whose
// scale its own currency decides uses [NumericFormat.AnyScale].
type NumericFormat struct {
	// DecimalSeparator is the separator between the integer and the fraction
	// part. Empty means ".". The dialect allows "." or ","; anything else is
	// [ErrDecimalFormat] for every value.
	DecimalSeparator string
	// ThousandsSeparator is the grouping separator the manifest declared, or ""
	// for none. Whatever it says, [GroupApostrophe] and
	// [GroupTypographicApostrophe] are also accepted; a declared separator is
	// accepted in addition to them, never instead of them.
	ThousandsSeparator string
	// MaxFractionDigits is how many fraction digits the field allows. A
	// negative value means [model.MaxScale]; zero means the field is an
	// integer. A value with more is [ErrMoneyScale].
	MaxFractionDigits int
}

// AnyScale returns f with the fraction-digit cap lifted to [model.MaxScale]. It
// is for fields whose scale the record's own currency decides.
func (f NumericFormat) AnyScale() NumericFormat {
	f.MaxFractionDigits = -1
	return f
}

// ParseAmount parses one of the accepted numeric shapes of BUILD-SPEC 16.1 and
// returns the exact value and the currency prefix the value carried, "" when it
// carried none. The scale of the result is the number of fraction digits as
// written, which is why an input is never silently rounded: the value's own
// precision survives into the store.
//
// The accepted shapes, and nothing else:
//
//	1234.5             a plain decimal
//	1'234.50           apostrophe grouping, groups of three
//	12’345.67          the typographic apostrophe U+2019
//	12'345.-           Swiss shorthand for .00
//	1234.00-           trailing minus
//	-1234.00           leading minus
//	(1'234.50)         accounting parentheses, negative
//	CHF 1'234.50       a currency prefix, separated by at least one space
//	-0.00              normalizes to 0.00, because a zero carries no sign
//
// Everything else is [ErrDecimalFormat]. In particular: US grouping
// ("1,234,567.89") is invalid for these feeds; a leading "+" is invalid; an
// exponent is invalid; two signs are invalid; grouping in anything but groups of
// three is invalid; an empty string is invalid, because absence is the caller's
// question to answer, not the parser's.
//
// A value with more fraction digits than [NumericFormat.MaxFractionDigits]
// allows is [ErrMoneyScale] and is never rounded.
func ParseAmount(s string, f NumericFormat) (model.Decimal, string, error) {
	dec := f.DecimalSeparator
	if dec == "" {
		dec = "."
	}
	if dec != "." && dec != "," {
		return model.Decimal{}, "", ErrDecimalFormat
	}
	max := f.MaxFractionDigits
	if max < 0 || max > int(model.MaxScale) {
		max = int(model.MaxScale)
	}

	rest := s
	if rest == "" {
		return model.Decimal{}, "", ErrDecimalFormat
	}

	// A currency prefix: exactly three uppercase ASCII letters, then at least
	// one space. The code itself is returned untouched; whether it is a known
	// currency is the record's question, not the number's.
	currency := ""
	if len(rest) > 4 && isUpperAlpha(rest[0]) && isUpperAlpha(rest[1]) && isUpperAlpha(rest[2]) && rest[3] == ' ' {
		currency = rest[:3]
		rest = strings.TrimLeft(rest[3:], " ")
		if rest == "" {
			return model.Decimal{}, "", ErrDecimalFormat
		}
	}

	// The Swiss shorthand for a whole amount, "12'345.-", is normalized before
	// the sign is read, because its trailing minus is not a sign: it is how a
	// handwritten amount says "and no rappen". Reading it as a sign would turn
	// a positive invoice into a credit note.
	if strings.HasSuffix(rest, dec+"-") {
		rest = rest[:len(rest)-1] + "00"
	}

	// Exactly one sign, in exactly one of the three documented positions.
	neg := false
	switch {
	case strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")"):
		neg = true
		rest = rest[1 : len(rest)-1]
		if strings.HasSuffix(rest, dec+"-") {
			rest = rest[:len(rest)-1] + "00"
		}
	case strings.HasPrefix(rest, "-"):
		neg = true
		rest = rest[1:]
	case strings.HasSuffix(rest, "-"):
		neg = true
		rest = rest[:len(rest)-1]
	}
	if rest == "" {
		return model.Decimal{}, "", ErrDecimalFormat
	}

	intPart, fracPart := rest, ""
	if i := strings.Index(rest, dec); i >= 0 {
		intPart, fracPart = rest[:i], rest[i+len(dec):]
		if fracPart == "" || strings.Contains(fracPart, dec) {
			return model.Decimal{}, "", ErrDecimalFormat
		}
	}

	digits, err := ungroup(intPart, f.ThousandsSeparator, dec)
	if err != nil {
		return model.Decimal{}, "", err
	}
	if !allDigits(fracPart) {
		return model.Decimal{}, "", ErrDecimalFormat
	}
	if len(fracPart) > max {
		return model.Decimal{}, "", ErrMoneyScale
	}

	lit := digits
	if fracPart != "" {
		lit += "." + fracPart
	}
	if neg {
		lit = "-" + lit
	}
	d, err := model.ParseDecimal(lit)
	if err != nil {
		if errors.Is(err, model.ErrScaleRange) {
			return model.Decimal{}, currency, ErrMoneyScale
		}
		if errors.Is(err, model.ErrOverflow) {
			return model.Decimal{}, currency, err
		}
		return model.Decimal{}, currency, ErrDecimalFormat
	}
	return d, currency, nil
}

// ungroup removes the grouping separators from an integer part and checks their
// placement: the first group has one to three digits and every later group has
// exactly three. A misplaced separator is [ErrDecimalFormat], because
// "1'23'456" is far more likely a corrupted field than a novel convention.
func ungroup(s, declared, dec string) (string, error) {
	if s == "" {
		return "", ErrDecimalFormat
	}
	seps := groupSeparators(declared, dec)
	var groups []string
	cur := s
	for {
		idx, sep := firstIndexOfAny(cur, seps)
		if idx < 0 {
			groups = append(groups, cur)
			break
		}
		groups = append(groups, cur[:idx])
		cur = cur[idx+len(sep):]
	}
	for i, g := range groups {
		if !allDigits(g) || g == "" {
			return "", ErrDecimalFormat
		}
		if i == 0 {
			if len(g) > 3 && len(groups) > 1 {
				return "", ErrDecimalFormat
			}
			continue
		}
		if len(g) != 3 {
			return "", ErrDecimalFormat
		}
	}
	return strings.Join(groups, ""), nil
}

// groupSeparators returns the accepted grouping separators in ascending byte
// order: the two apostrophes always, plus the declared one when it is something
// else. The decimal separator is never a grouping separator.
func groupSeparators(declared, dec string) []string {
	set := map[string]bool{GroupApostrophe: true, GroupTypographicApostrophe: true}
	if declared != "" {
		set[declared] = true
	}
	delete(set, dec)
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// firstIndexOfAny returns the index of the earliest of seps in s, and which one
// it was. seps is sorted, so a tie - impossible for distinct separators - would
// resolve deterministically anyway.
func firstIndexOfAny(s string, seps []string) (int, string) {
	best, bestSep := -1, ""
	for _, sep := range seps {
		if i := strings.Index(s, sep); i >= 0 && (best < 0 || i < best) {
			best, bestSep = i, sep
		}
	}
	return best, bestSep
}

// allDigits reports whether s consists only of ASCII digits. The empty string
// reports true: an absent fraction part is not a malformed one.
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isUpperAlpha reports whether b is an ASCII uppercase letter.
func isUpperAlpha(b byte) bool { return b >= 'A' && b <= 'Z' }

// ParseInteger parses a whole number: an optional leading "-" and one or more
// ASCII digits, with no grouping, no sign suffix and no decimal part. Leading
// zeros are accepted and carry no meaning, because an integer field is a count -
// a payment term in days, a discount period - and not an identifier. Identifiers
// are strings, and their leading zeros are significant.
func ParseInteger(s string) (int, error) {
	body := s
	if strings.HasPrefix(body, "-") {
		body = body[1:]
	}
	if body == "" || !allDigits(body) {
		return 0, ErrDecimalFormat
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ErrDecimalFormat
	}
	return n, nil
}

// The date patterns a manifest may declare. They are the same set the twin's
// HTTP surface accepts (see [httpx.CSVDialect]), so a dataset delivered as a
// file and the same dataset delivered over HTTP read dates identically.
const (
	// DatePatternRFC3339 is the canonical YYYY-MM-DD and the default.
	DatePatternRFC3339 = "RFC3339"
	// DatePatternDotted is DD.MM.YYYY, what a German-language system writes.
	DatePatternDotted = "DD.MM.YYYY"
	// DatePatternSlashedDMY is DD/MM/YYYY.
	DatePatternSlashedDMY = "DD/MM/YYYY"
	// DatePatternSlashedMDY is MM/DD/YYYY.
	DatePatternSlashedMDY = "MM/DD/YYYY"
	// DatePatternCompact is YYYYMMDD.
	DatePatternCompact = "YYYYMMDD"
)

// DatePatterns returns the declarable date patterns in ascending byte order.
func DatePatterns() []string {
	out := []string{
		DatePatternCompact, DatePatternDotted, DatePatternRFC3339,
		DatePatternSlashedDMY, DatePatternSlashedMDY,
	}
	sort.Strings(out)
	return out
}

// ParseDate parses s as a calendar date and returns it in canonical YYYY-MM-DD
// form.
//
// The canonical form is always accepted. The declared pattern is accepted in
// addition, and no other pattern ever is: an undeclared layout is
// [ErrDateFormat], never a guess, because DD/MM and MM/DD are indistinguishable
// on the twelfth of the month and distinguishable everywhere else, which is the
// worst possible failure mode. An empty pattern means [DatePatternRFC3339]; a
// pattern outside [DatePatterns] is [ErrDatePatternUnknown].
//
// The two accepted layouts never collide - they differ in length, in separator
// or in both - so accepting the canonical form alongside the declared one
// introduces no ambiguity.
func ParseDate(s, pattern string) (string, error) {
	if pattern == "" {
		pattern = DatePatternRFC3339
	}
	switch pattern {
	case DatePatternRFC3339, DatePatternDotted, DatePatternSlashedDMY, DatePatternSlashedMDY, DatePatternCompact:
	default:
		return "", ErrDatePatternUnknown
	}
	if s == "" {
		return "", ErrDateFormat
	}
	if d, err := model.ParseDate(s); err == nil {
		return d.String(), nil
	}
	var y, m, day string
	switch pattern {
	case DatePatternRFC3339:
		return "", ErrDateFormat
	case DatePatternDotted:
		p := strings.Split(s, ".")
		if len(p) != 3 || len(p[0]) != 2 || len(p[1]) != 2 || len(p[2]) != 4 {
			return "", ErrDateFormat
		}
		day, m, y = p[0], p[1], p[2]
	case DatePatternSlashedDMY:
		p := strings.Split(s, "/")
		if len(p) != 3 || len(p[0]) != 2 || len(p[1]) != 2 || len(p[2]) != 4 {
			return "", ErrDateFormat
		}
		day, m, y = p[0], p[1], p[2]
	case DatePatternSlashedMDY:
		p := strings.Split(s, "/")
		if len(p) != 3 || len(p[0]) != 2 || len(p[1]) != 2 || len(p[2]) != 4 {
			return "", ErrDateFormat
		}
		m, day, y = p[0], p[1], p[2]
	case DatePatternCompact:
		if len(s) != 8 || !allDigits(s) {
			return "", ErrDateFormat
		}
		y, m, day = s[:4], s[4:6], s[6:8]
	}
	if !allDigits(y) || !allDigits(m) || !allDigits(day) {
		return "", ErrDateFormat
	}
	d, err := model.ParseDate(y + "-" + m + "-" + day)
	if err != nil {
		return "", ErrDateFormat
	}
	return d.String(), nil
}
