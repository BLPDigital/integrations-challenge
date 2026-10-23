package model

import (
	"errors"
	"testing"
)

func TestParseDate(t *testing.T) {
	tests := []struct {
		in    string
		y     int
		m     int
		d     int
		valid bool
	}{
		{"2026-08-31", 2026, 8, 31, true},
		{"1970-01-01", 1970, 1, 1, true},
		{"2024-02-29", 2024, 2, 29, true},
		{"2000-02-29", 2000, 2, 29, true},
		{"0001-01-01", 1, 1, 1, true},
		{"2026-02-30", 0, 0, 0, false},
		{"2100-02-29", 0, 0, 0, false},
		{"2026-13-01", 0, 0, 0, false},
		{"2026-00-01", 0, 0, 0, false},
		{"2026-01-00", 0, 0, 0, false},
		{"2026-01-32", 0, 0, 0, false},
		{"2026-1-01", 0, 0, 0, false},
		{"2026-01-1", 0, 0, 0, false},
		{"26-01-01", 0, 0, 0, false},
		{"2026/01/01", 0, 0, 0, false},
		{"2026-01-01T00:00:00Z", 0, 0, 0, false},
		{" 2026-01-01", 0, 0, 0, false},
		{"2026-01-01 ", 0, 0, 0, false},
		{"", 0, 0, 0, false},
		{"+026-01-01", 0, 0, 0, false},
	}
	for _, tc := range tests {
		name := tc.in
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			got, err := ParseDate(tc.in)
			if tc.valid != (err == nil) {
				t.Fatalf("ParseDate(%q) error = %v, want valid=%v", tc.in, err, tc.valid)
			}
			if !tc.valid {
				if !errors.Is(err, ErrDateFormat) {
					t.Fatalf("ParseDate(%q) = %v, want ErrDateFormat", tc.in, err)
				}
				return
			}
			if got.Year != tc.y || got.Month != tc.m || got.Day != tc.d {
				t.Fatalf("ParseDate(%q) = %+v", tc.in, got)
			}
			if got.String() != tc.in {
				t.Fatalf("String() = %q, want %q", got.String(), tc.in)
			}
		})
	}
}

func TestDateArithmetic(t *testing.T) {
	tests := []struct {
		date    string
		days    int64
		weekday int
	}{
		{"1970-01-01", 0, 4},     // Thursday
		{"1969-12-31", -1, 3},    // Wednesday
		{"2000-02-29", 11016, 2}, // Tuesday
		{"2026-03-29", 20541, 0}, // Sunday
		{"2026-08-31", 20696, 1}, // Monday
	}
	for _, tc := range tests {
		t.Run(tc.date, func(t *testing.T) {
			d := MustDate(tc.date)
			if got := d.DaysSinceEpoch(); got != tc.days {
				t.Fatalf("DaysSinceEpoch = %d, want %d", got, tc.days)
			}
			if got := d.Weekday(); got != tc.weekday {
				t.Fatalf("Weekday = %d, want %d", got, tc.weekday)
			}
			// Round-tripping through the day count must be lossless.
			if got := d.AddDays(0); got != d {
				t.Fatalf("AddDays(0) = %+v, want %+v", got, d)
			}
		})
	}
	if got := MustDate("2026-02-28").AddDays(1).String(); got != "2026-03-01" {
		t.Fatalf("AddDays across February = %q", got)
	}
	if got := MustDate("2024-02-28").AddDays(1).String(); got != "2024-02-29" {
		t.Fatalf("AddDays into a leap day = %q", got)
	}
	if got := MustDate("2026-01-01").AddDays(-1).String(); got != "2025-12-31" {
		t.Fatalf("AddDays backwards across a year = %q", got)
	}
	if got := MustDate("1970-01-01").AddDays(-1).String(); got != "1969-12-31" {
		t.Fatalf("AddDays before the epoch = %q", got)
	}
}

func TestCompareDatesAndRange(t *testing.T) {
	if c, err := CompareDates("2026-01-01", "2026-01-02"); err != nil || c != -1 {
		t.Fatalf("CompareDates = %d, %v", c, err)
	}
	if c, err := CompareDates("2026-01-02", "2026-01-02"); err != nil || c != 0 {
		t.Fatalf("CompareDates = %d, %v", c, err)
	}
	if _, err := CompareDates("2026-01-02", "nope"); !errors.Is(err, ErrDateFormat) {
		t.Fatalf("CompareDates error = %v, want ErrDateFormat", err)
	}

	tests := []struct {
		name             string
		date, from, to   string
		want             bool
		wantErrDateParse bool
	}{
		{"inside", "2026-06-15", "2026-01-01", "2026-12-31", true, false},
		{"on from is included", "2026-01-01", "2026-01-01", "2026-12-31", true, false},
		{"on to is excluded", "2026-12-31", "2026-01-01", "2026-12-31", false, false},
		{"before from", "2025-12-31", "2026-01-01", "2026-12-31", false, false},
		{"open ended", "2099-01-01", "2026-01-01", "", true, false},
		{"open ended before from", "2025-01-01", "2026-01-01", "", false, false},
		{"malformed date", "nope", "2026-01-01", "", false, true},
		{"malformed from", "2026-01-01", "nope", "", false, true},
		{"malformed to", "2026-01-01", "2026-01-01", "nope", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DateInHalfOpenRange(tc.date, tc.from, tc.to)
			if tc.wantErrDateParse {
				if !errors.Is(err, ErrDateFormat) {
					t.Fatalf("error = %v, want ErrDateFormat", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("DateInHalfOpenRange = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestZurichOffsetMinutes(t *testing.T) {
	tests := []struct {
		date string
		want int
	}{
		// 2026: last Sunday in March is the 29th, last Sunday in October the 25th.
		{"2026-01-15", ZurichOffsetWinter},
		{"2026-03-28", ZurichOffsetWinter}, // the Saturday before the switch
		{"2026-03-29", ZurichOffsetSummer}, // the transition day itself
		{"2026-03-30", ZurichOffsetSummer},
		{"2026-07-01", ZurichOffsetSummer},
		{"2026-10-24", ZurichOffsetSummer},
		{"2026-10-25", ZurichOffsetWinter}, // the transition day itself
		{"2026-12-31", ZurichOffsetWinter},
		// Other years, to pin the "last Sunday" arithmetic.
		{"2024-03-30", ZurichOffsetWinter},
		{"2024-03-31", ZurichOffsetSummer},
		{"2024-10-26", ZurichOffsetSummer},
		{"2024-10-27", ZurichOffsetWinter},
		{"2025-03-29", ZurichOffsetWinter},
		{"2025-03-30", ZurichOffsetSummer},
		{"2025-10-25", ZurichOffsetSummer},
		{"2025-10-26", ZurichOffsetWinter},
		{"2027-03-27", ZurichOffsetWinter},
		{"2027-03-28", ZurichOffsetSummer},
		{"2027-10-30", ZurichOffsetSummer},
		{"2027-10-31", ZurichOffsetWinter},
	}
	for _, tc := range tests {
		t.Run(tc.date, func(t *testing.T) {
			got, err := ZurichOffsetMinutes(tc.date)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ZurichOffsetMinutes(%s) = %d, want %d", tc.date, got, tc.want)
			}
		})
	}
	if _, err := ZurichOffsetMinutes("nope"); !errors.Is(err, ErrDateFormat) {
		t.Fatalf("ZurichOffsetMinutes error = %v, want ErrDateFormat", err)
	}
}

func TestLastSundayOfMonth(t *testing.T) {
	tests := []struct {
		year, month int
		want        string
	}{
		{2024, 3, "2024-03-31"},
		{2024, 10, "2024-10-27"},
		{2025, 3, "2025-03-30"},
		{2025, 10, "2025-10-26"},
		{2026, 3, "2026-03-29"},
		{2026, 10, "2026-10-25"},
		{2027, 3, "2027-03-28"},
		{2027, 10, "2027-10-31"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := lastSundayOfMonth(tc.year, tc.month).String(); got != tc.want {
				t.Fatalf("lastSundayOfMonth(%d,%d) = %s, want %s", tc.year, tc.month, got, tc.want)
			}
		})
	}
}

func TestZurichDateOf(t *testing.T) {
	tests := []struct {
		name    string
		instant string
		want    string
	}{
		{"midday utc winter", "2026-01-15T12:00:00Z", "2026-01-15"},
		{"late utc winter rolls over", "2026-01-15T23:30:00Z", "2026-01-16"},
		{"midnight utc winter", "2026-01-15T00:00:00Z", "2026-01-15"},
		// The DST boundary: the switch is at 01:00 UTC on 2026-03-29.
		{"just before the switch", "2026-03-29T00:59:59Z", "2026-03-29"},
		{"exactly at the switch", "2026-03-29T01:00:00Z", "2026-03-29"},
		{"the evening before the switch", "2026-03-28T23:30:00Z", "2026-03-29"},
		{"late on the switch day", "2026-03-29T22:30:00Z", "2026-03-30"},
		{"late in summer rolls over earlier", "2026-07-01T22:00:00Z", "2026-07-02"},
		{"summer just before rollover", "2026-07-01T21:59:59Z", "2026-07-01"},
		// The October boundary: the switch is at 01:00 UTC on 2026-10-25.
		{"before the october switch", "2026-10-25T00:30:00Z", "2026-10-25"},
		{"after the october switch", "2026-10-25T01:30:00Z", "2026-10-25"},
		{"october evening", "2026-10-25T23:30:00Z", "2026-10-26"},
		// Offsets other than Z.
		{"offset already local", "2026-03-29T03:30:00+02:00", "2026-03-29"},
		{"negative offset", "2026-01-01T20:00:00-05:00", "2026-01-02"},
		{"fractional seconds truncated", "2026-01-15T12:00:00.123456Z", "2026-01-15"},
		{"lowercase t and z", "2026-01-15t12:00:00z", "2026-01-15"},
		{"year boundary", "2025-12-31T23:30:00Z", "2026-01-01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ZurichDateOf(tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ZurichDateOf(%s) = %s, want %s", tc.instant, got, tc.want)
			}
		})
	}
}

func TestZurichOffsetMinutesAt(t *testing.T) {
	tests := []struct {
		instant string
		want    int
	}{
		{"2026-03-29T00:59:59Z", ZurichOffsetWinter},
		{"2026-03-29T01:00:00Z", ZurichOffsetSummer},
		{"2026-10-25T00:59:59Z", ZurichOffsetSummer},
		{"2026-10-25T01:00:00Z", ZurichOffsetWinter},
		{"2026-01-01T00:00:00Z", ZurichOffsetWinter},
	}
	for _, tc := range tests {
		t.Run(tc.instant, func(t *testing.T) {
			got, err := ZurichOffsetMinutesAt(tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("ZurichOffsetMinutesAt(%s) = %d, want %d", tc.instant, got, tc.want)
			}
		})
	}
}

func TestParseInstantUTC(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
		ok   bool
	}{
		{"epoch", "1970-01-01T00:00:00Z", 0, true},
		{"one second", "1970-01-01T00:00:01Z", 1, true},
		{"positive offset", "1970-01-01T01:00:00+01:00", 0, true},
		{"negative offset", "1969-12-31T23:00:00-01:00", 0, true},
		{"fraction ignored", "1970-01-01T00:00:01.999Z", 1, true},
		{"missing zone", "1970-01-01T00:00:00", 0, false},
		{"missing seconds", "1970-01-01T00:00Z", 0, false},
		{"bad separator", "1970-01-01 00:00:00Z", 0, false},
		{"hour out of range", "1970-01-01T24:00:00Z", 0, false},
		{"minute out of range", "1970-01-01T00:60:00Z", 0, false},
		{"leap second rejected", "1970-01-01T23:59:60Z", 0, false},
		{"empty fraction", "1970-01-01T00:00:00.Z", 0, false},
		{"bad offset", "1970-01-01T00:00:00+0100", 0, false},
		{"bad date", "1970-13-01T00:00:00Z", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseInstantUTC(tc.in)
			if tc.ok != (err == nil) {
				t.Fatalf("ParseInstantUTC(%q) error = %v, want ok=%v", tc.in, err, tc.ok)
			}
			if !tc.ok {
				if !errors.Is(err, ErrTimestampFormat) {
					t.Fatalf("error = %v, want ErrTimestampFormat", err)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("ParseInstantUTC(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestMustDatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustDate did not panic")
		}
	}()
	MustDate("2026-02-30")
}

func TestDateZeroAndValidity(t *testing.T) {
	var d Date
	if !d.IsZero() {
		t.Fatal("zero Date is not reported as zero")
	}
	if IsValidDate("2026-02-30") || !IsValidDate("2026-02-28") {
		t.Fatal("IsValidDate disagrees with ParseDate")
	}
	a, b := MustDate("2026-01-01"), MustDate("2026-01-02")
	if !a.Before(b) || !b.After(a) || a.Before(a) || a.After(a) {
		t.Fatal("Before/After are wrong")
	}
}
