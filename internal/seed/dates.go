package seed

import (
	"fmt"
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// addDays returns the calendar date n days after date; n may be negative.
func addDays(date string, n int) (string, error) {
	d, err := model.ParseDate(date)
	if err != nil {
		return "", err
	}
	return d.AddDays(n).String(), nil
}

// daysBetween returns the number of days from a to b, negative when b precedes a.
func daysBetween(a, b string) (int, error) {
	da, err := model.ParseDate(a)
	if err != nil {
		return 0, err
	}
	db, err := model.ParseDate(b)
	if err != nil {
		return 0, err
	}
	return int(db.DaysSinceEpoch() - da.DaysSinceEpoch()), nil
}

// offsetSuffix renders a UTC offset in minutes as an RFC 3339 suffix.
func offsetSuffix(minutes int) string {
	sign := '+'
	if minutes < 0 {
		sign = '-'
		minutes = -minutes
	}
	return fmt.Sprintf("%c%02d:%02d", sign, minutes/60, minutes%60)
}

// zurichMidnight returns local midnight of a Europe/Zurich calendar date as an
// RFC 3339 instant carrying the offset actually in force at that instant.
//
// It resolves the offset by trying both candidates and keeping the
// self-consistent one, which is the standard way to turn a local wall time into
// an instant. That matters exactly once and decisively: midnight on the summer
// time switch day 2026-03-29 is still +01:00, because the transition happens at
// 02:00 local, while midnight on 2026-03-30 is +02:00. A generator that used the
// whole-day offset convention would emit +02:00 for the switch day and quietly
// destroy the DST trap.
func zurichMidnight(date string) (string, error) {
	if _, err := model.ParseDate(date); err != nil {
		return "", err
	}
	for _, off := range []int{model.ZurichOffsetWinter, model.ZurichOffsetSummer} {
		cand := date + "T00:00:00" + offsetSuffix(off)
		got, err := model.ZurichOffsetMinutesAt(cand)
		if err != nil {
			return "", err
		}
		if got == off {
			return cand, nil
		}
	}
	// Unreachable in Europe/Zurich, where no transition happens at midnight, so
	// local midnight always exists and is unambiguous.
	return "", fmt.Errorf("seed: local midnight of %s is not a valid Europe/Zurich instant", date)
}

// sortStrings sorts a slice of strings in ascending byte order in place. It is a
// named helper so every explicit ordering in this package reads the same way.
func sortStrings(xs []string) { sort.Strings(xs) }
