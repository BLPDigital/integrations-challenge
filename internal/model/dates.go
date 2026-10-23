package model

import (
	"errors"
	"strconv"
	"strings"
)

// Date errors.
var (
	// ErrDateFormat reports a value that is not a calendar date in YYYY-MM-DD
	// form, or that names a day the calendar does not have. Record-level callers
	// surface it as E_DATE_FORMAT.
	ErrDateFormat = errors.New("model: invalid date, want YYYY-MM-DD")
	// ErrTimestampFormat reports a value that is not an RFC 3339 instant.
	ErrTimestampFormat = errors.New("model: invalid RFC3339 timestamp")
)

// Date is a proleptic Gregorian calendar date with no time and no zone. It is
// the only date type in the model: the domain speaks in calendar dates, and
// carrying a time zone into an FX validity interval is how off-by-one-day
// posting errors are made.
//
// The zero Date is invalid and reported by IsZero.
type Date struct {
	Year  int
	Month int // 1..12
	Day   int // 1..31, valid for Year and Month
}

// ParseDate parses a calendar date in RFC 3339 full-date form, YYYY-MM-DD. The
// format is strict: exactly ten characters, four-digit year, zero-padded month
// and day, no surrounding whitespace, no time part and no zone. A syntactically
// well-formed but non-existent day such as "2026-02-30" is ErrDateFormat.
func ParseDate(s string) (Date, error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, ErrDateFormat
	}
	y, ok := atoiFixed(s[0:4])
	if !ok {
		return Date{}, ErrDateFormat
	}
	m, ok := atoiFixed(s[5:7])
	if !ok {
		return Date{}, ErrDateFormat
	}
	d, ok := atoiFixed(s[8:10])
	if !ok {
		return Date{}, ErrDateFormat
	}
	if m < 1 || m > 12 || d < 1 || d > daysInMonth(y, m) {
		return Date{}, ErrDateFormat
	}
	return Date{Year: y, Month: m, Day: d}, nil
}

// MustDate is ParseDate for literals known to be valid. It panics on error and
// is intended for tests and constant tables.
func MustDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic("model: MustDate(" + strconv.Quote(s) + "): " + err.Error())
	}
	return d
}

// IsValidDate reports whether s is a calendar date in YYYY-MM-DD form.
func IsValidDate(s string) bool {
	_, err := ParseDate(s)
	return err == nil
}

// String returns the date in YYYY-MM-DD form, zero-padded. Years outside
// 0..9999 are rendered with as many digits as they need and are therefore not
// round-trippable; the domain has no such dates.
func (d Date) String() string {
	var b strings.Builder
	b.Grow(10)
	writePadded(&b, d.Year, 4)
	b.WriteByte('-')
	writePadded(&b, d.Month, 2)
	b.WriteByte('-')
	writePadded(&b, d.Day, 2)
	return b.String()
}

// IsZero reports whether d is the zero Date.
func (d Date) IsZero() bool { return d == Date{} }

// DaysSinceEpoch returns the number of days between d and 1970-01-01, negative
// for earlier dates.
func (d Date) DaysSinceEpoch() int64 { return daysFromCivil(d.Year, d.Month, d.Day) }

// Compare returns -1, 0 or +1 as d is before, equal to or after o.
func (d Date) Compare(o Date) int {
	a, b := d.DaysSinceEpoch(), o.DaysSinceEpoch()
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Before reports whether d is earlier than o.
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After reports whether d is later than o.
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }

// AddDays returns the date n days after d; n may be negative.
func (d Date) AddDays(n int) Date { return dateFromDays(d.DaysSinceEpoch() + int64(n)) }

// Weekday returns the day of the week with Sunday as 0, matching the ISO
// convention used by the EU daylight saving rule ("last Sunday").
func (d Date) Weekday() int { return weekdayOfDays(d.DaysSinceEpoch()) }

// CompareDates compares two dates in YYYY-MM-DD form. Either value being
// malformed is ErrDateFormat: dates are never compared as raw strings elsewhere
// in the codebase, even though this format happens to sort lexically.
func CompareDates(a, b string) (int, error) {
	da, err := ParseDate(a)
	if err != nil {
		return 0, err
	}
	db, err := ParseDate(b)
	if err != nil {
		return 0, err
	}
	return da.Compare(db), nil
}

// DateInHalfOpenRange reports whether date lies in the half-open interval
// [from, to): from is included, to is excluded, and an empty to is open-ended.
// This is the validity semantics of FX rate rows and cost centers, so a rate
// that expires on the 1st does not apply on the 1st.
func DateInHalfOpenRange(date, from, to string) (bool, error) {
	d, err := ParseDate(date)
	if err != nil {
		return false, err
	}
	f, err := ParseDate(from)
	if err != nil {
		return false, err
	}
	if d.Before(f) {
		return false, nil
	}
	if to == "" {
		return true, nil
	}
	t, err := ParseDate(to)
	if err != nil {
		return false, err
	}
	return d.Before(t), nil
}

// Europe/Zurich offsets, in minutes east of UTC.
const (
	// ZurichOffsetWinter is CET, UTC+01:00.
	ZurichOffsetWinter = 60
	// ZurichOffsetSummer is CEST, UTC+02:00.
	ZurichOffsetSummer = 120
)

// ZurichOffsetMinutes returns the Europe/Zurich UTC offset in minutes that
// applies to the calendar date s.
//
// The offset is derived from the EU rule, not from the operating system: summer
// time runs from the last Sunday in March at 01:00 UTC to the last Sunday in
// October at 01:00 UTC. The runtime may ship without tzdata, and a missing zone
// database must not silently move a posting date by a day.
//
// A whole calendar date has one offset here, so the two transition days need a
// convention: the offset in force after the transition wins, because it covers
// all but the first two local hours of the day. The last Sunday in March is
// therefore +02:00 and the last Sunday in October +01:00.
func ZurichOffsetMinutes(date string) (int, error) {
	d, err := ParseDate(date)
	if err != nil {
		return 0, err
	}
	start := lastSundayOfMonth(d.Year, 3)
	end := lastSundayOfMonth(d.Year, 10)
	if !d.Before(start) && d.Before(end) {
		return ZurichOffsetSummer, nil
	}
	return ZurichOffsetWinter, nil
}

// ZurichOffsetMinutesAt returns the Europe/Zurich UTC offset in minutes in force
// at the given RFC 3339 instant. Unlike ZurichOffsetMinutes it needs no
// convention: the transition happens at a known instant, 01:00 UTC.
func ZurichOffsetMinutesAt(instantRFC3339 string) (int, error) {
	t, err := ParseInstantUTC(instantRFC3339)
	if err != nil {
		return 0, err
	}
	return zurichOffsetAtUnix(t), nil
}

// ZurichDateOf returns the Europe/Zurich calendar date of an RFC 3339 instant,
// in YYYY-MM-DD form. It is how a UTC or offset-bearing timestamp becomes the
// posting date that FX validity intervals are matched against: 2026-03-29T00:30:00Z
// is still 2026-03-29 locally, and 2026-03-28T23:30:00Z is 2026-03-29 too.
func ZurichDateOf(instantRFC3339 string) (string, error) {
	t, err := ParseInstantUTC(instantRFC3339)
	if err != nil {
		return "", err
	}
	local := t + int64(zurichOffsetAtUnix(t))*60
	return dateFromDays(floorDiv(local, 86400)).String(), nil
}

// DateFromUnixUTC returns the UTC calendar date of a Unix second.
//
// It is deliberately NOT what a validity boundary should be read with: this
// landscape's boundaries carry a Europe/Zurich offset and the calendar that
// decides them is the Zurich one, which is what [ZurichDateOf] returns. This
// exists so the difference between the two is expressible, and it is the exact
// difference the summer time switch turns into money.
func DateFromUnixUTC(unixSeconds int64) string {
	return dateFromDays(floorDiv(unixSeconds, 86400)).String()
}

// ParseInstantUTC parses an RFC 3339 timestamp and returns the number of seconds
// since the Unix epoch. The offset is mandatory: "Z", "z" or ±HH:MM. Fractional
// seconds are accepted and truncated toward the second, since nothing in this
// landscape is sub-second. A leap second (:60) is rejected.
func ParseInstantUTC(s string) (int64, error) {
	if len(s) < 20 {
		return 0, ErrTimestampFormat
	}
	d, err := ParseDate(s[:10])
	if err != nil {
		return 0, ErrTimestampFormat
	}
	if s[10] != 'T' && s[10] != 't' {
		return 0, ErrTimestampFormat
	}
	rest := s[11:]
	if len(rest) < 9 || rest[2] != ':' || rest[5] != ':' {
		return 0, ErrTimestampFormat
	}
	hh, ok1 := atoiFixed(rest[0:2])
	mm, ok2 := atoiFixed(rest[3:5])
	ss, ok3 := atoiFixed(rest[6:8])
	if !ok1 || !ok2 || !ok3 || hh > 23 || mm > 59 || ss > 59 {
		return 0, ErrTimestampFormat
	}
	rest = rest[8:]
	if len(rest) > 0 && rest[0] == '.' {
		i := 1
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 1 {
			return 0, ErrTimestampFormat
		}
		rest = rest[i:]
	}
	offset := 0
	switch {
	case rest == "Z" || rest == "z":
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':':
		oh, ok1 := atoiFixed(rest[1:3])
		om, ok2 := atoiFixed(rest[4:6])
		if !ok1 || !ok2 || oh > 23 || om > 59 {
			return 0, ErrTimestampFormat
		}
		offset = oh*3600 + om*60
		if rest[0] == '-' {
			offset = -offset
		}
	default:
		return 0, ErrTimestampFormat
	}
	return d.DaysSinceEpoch()*86400 + int64(hh)*3600 + int64(mm)*60 + int64(ss) - int64(offset), nil
}

// zurichOffsetAtUnix returns the Europe/Zurich offset in minutes at a Unix time,
// applying the EU rule at its exact transition instants, 01:00 UTC.
func zurichOffsetAtUnix(t int64) int {
	y, _, _ := civilFromDays(floorDiv(t, 86400))
	start := lastSundayOfMonth(y, 3).DaysSinceEpoch()*86400 + 3600
	end := lastSundayOfMonth(y, 10).DaysSinceEpoch()*86400 + 3600
	if t >= start && t < end {
		return ZurichOffsetSummer
	}
	return ZurichOffsetWinter
}

// lastSundayOfMonth returns the last Sunday of the given month.
func lastSundayOfMonth(year, month int) Date {
	last := daysFromCivil(year, month, daysInMonth(year, month))
	return dateFromDays(last - int64(weekdayOfDays(last)))
}

// daysInMonth returns the length of the month, honoring the Gregorian leap
// year rule.
func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isLeap(year) {
			return 29
		}
		return 28
	}
	return 0
}

// isLeap reports whether year is a Gregorian leap year.
func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// weekdayOfDays returns the weekday of a day count since the epoch, Sunday as 0.
// 1970-01-01 was a Thursday.
func weekdayOfDays(days int64) int {
	return int(mod(days+4, 7))
}

// daysFromCivil converts a proleptic Gregorian date to days since 1970-01-01.
// The algorithm shifts the year to start in March so leap days land last.
func daysFromCivil(y, m, d int) int64 {
	yy := int64(y)
	if m <= 2 {
		yy--
	}
	era := floorDiv(yy, 400)
	yoe := yy - era*400              // [0, 399]
	mp := int64((m + 9) % 12)        // March is 0
	doy := (153*mp+2)/5 + int64(d-1) // [0, 365]
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

// civilFromDays converts days since 1970-01-01 back to a calendar date.
func civilFromDays(z int64) (y, m, d int) {
	z += 719468
	era := floorDiv(z, 146097)
	doe := z - era*146097                                  // [0, 146096]
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365 // [0, 399]
	yy := yoe + era*400                                    //
	doy := doe - (365*yoe + yoe/4 - yoe/100)               // [0, 365]
	mp := (5*doy + 2) / 153                                // [0, 11]
	dd := doy - (153*mp+2)/5 + 1                           // [1, 31]
	mm := mp + 3                                           //
	if mm > 12 {
		mm -= 12
		yy++
	}
	return int(yy), int(mm), int(dd)
}

// dateFromDays converts days since the epoch to a Date.
func dateFromDays(days int64) Date {
	y, m, d := civilFromDays(days)
	return Date{Year: y, Month: m, Day: d}
}

// floorDiv divides rounding toward negative infinity, unlike Go's truncating
// division, so dates before 1970 behave.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// mod returns the non-negative remainder of a/b for positive b.
func mod(a, b int64) int64 {
	r := a % b
	if r < 0 {
		r += b
	}
	return r
}

// atoiFixed parses a string of ASCII digits. It reports false for anything else,
// including signs and spaces, which strconv.Atoi would accept or ignore.
func atoiFixed(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// writePadded writes v zero-padded to at least width digits. Negative values are
// written with a leading minus and are not padded.
func writePadded(b *strings.Builder, v, width int) {
	s := strconv.Itoa(v)
	if s != "" && s[0] == '-' {
		b.WriteString(s)
		return
	}
	for n := width - len(s); n > 0; n-- {
		b.WriteByte('0')
	}
	b.WriteString(s)
}
