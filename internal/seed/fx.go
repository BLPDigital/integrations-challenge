package seed

import (
	"fmt"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// Fixed calendar landmarks of the exchange rate table. They are constants of the
// landscape, not readings of a clock.
const (
	// FxWindowStart and FxWindowEnd bound the base rolling window, half-open.
	FxWindowStart = "2026-02-01"
	FxWindowEnd   = "2026-05-01"
	// DSTSwitchDate is the Europe/Zurich summer time switch. The two EUR rows
	// adjacent across it carry different UTC offsets, so a client that truncates
	// the posting date to UTC picks the row from the day before.
	DSTSwitchDate = "2026-03-29"
	// DSTPrevDate is the day before the switch.
	DSTPrevDate = "2026-03-28"
	// GBPOpenFrom is the valid-from of the open-ended GBP row, whose ValidTo is
	// xsi:nil.
	GBPOpenFrom = "2026-03-01"
	// JPYPinnedDate falls inside the JPY interval that carries the graded rate.
	JPYPinnedDate = "2026-03-16"
	// USDSupersededDate falls inside the USD interval that carries a superseded
	// row placed BEFORE its winner in document order.
	USDSupersededDate = "2026-03-02"
	// USDGapDate and SEKGapDate are the deliberate holes in the daily coverage.
	USDGapDate = "2026-02-16"
	SEKGapDate = "2026-03-10"
)

// Pinned rates. These four values are published and graded: changing one changes
// a published expected amount, so they are literals and never drawn.
var (
	// fxRateEURBeforeSwitch is valid 2026-03-28T00:00:00+01:00 to
	// 2026-03-29T00:00:00+01:00.
	fxRateEURBeforeSwitch = dec(924500, 6)
	// fxRateEUROnSwitch is valid 2026-03-29T00:00:00+01:00 to
	// 2026-03-30T00:00:00+02:00 and converts 1'345.63 EUR to 1252.78 CHF.
	fxRateEUROnSwitch = dec(931000, 6)
	// fxRateEURSuperseded is the losing row for the same interval: a lower
	// Sequence, placed after the winner in document order.
	fxRateEURSuperseded = dec(900000, 6)
	// fxRateGBPOpen converts 100.00 GBP to 108.23 CHF with commercial rounding
	// and to 108.22 with banker's rounding, which is the point.
	fxRateGBPOpen = dec(1082250, 6)
	// fxRateJPY is quoted per 100 units and converts 250000 JPY to 1390.75 CHF.
	fxRateJPY = dec(556300, 6)
	// fxRateUSDSuperseded is the losing USD row, placed BEFORE its winner.
	fxRateUSDSuperseded = dec(810000, 6)
)

// fxPair describes one currency of the rate table.
type fxPair struct {
	base    string
	rateAvg int64 // base rate in millionths, jittered per row
	factor  int64 // RateFactorFrom: JPY is quoted per 100 units
	deleted bool  // every row of this pair is a soft delete
}

// fxPairs is the fixed order the SOAP response groups its rows in. Six pairs
// against CHF, and DKK exists only as DELETED so a DKK invoice has no usable
// rate at all.
var fxPairs = []fxPair{
	{base: "EUR", rateAvg: 930000, factor: 1},
	{base: "USD", rateAvg: 880000, factor: 1},
	{base: "GBP", rateAvg: 1082250, factor: 1},
	{base: "JPY", rateAvg: 556300, factor: 100},
	{base: "SEK", rateAvg: 84000, factor: 1},
	{base: "DKK", rateAvg: 125000, factor: 1, deleted: true},
}

// fxMonths are the months every pair carries a MONTHLY_AVG row for. A monthly
// average must never be used for a daily conversion, and the rows exist so that
// mistake is reachable.
var fxMonths = []struct{ from, to string }{
	{"2026-02-01", "2026-03-01"},
	{"2026-03-01", "2026-04-01"},
	{"2026-04-01", "2026-05-01"},
}

// buildFxRows fills the SOAP exchange rate table to the exact row count of the
// scenario.
//
// The table is built in three parts. First the structural rows over the base
// window [FxWindowStart, FxWindowEnd), grouped by currency in the fixed order of
// [fxPairs] and ascending by valid-from inside each group, with the superseded
// rows placed adjacent to their winners and each group's MONTHLY_AVG rows
// appended. Then filler rows extending the window backwards one interval at a
// time, six rows per round, which is what the tail of a real rolling window
// looks like and puts the oldest rows last in document order. The filler is cut
// mid-round to land on the published row count exactly.
//
// Document order is therefore deliberately not validity order. Nothing about
// resolving a rate may depend on it.
func (b *builder) buildFxRows() error {
	step := b.spec.FxStepDays
	if step <= 0 {
		return fmt.Errorf("seed: scenario %s has a non-positive FX step", b.spec.ID)
	}
	var rows []FxRow
	for _, pair := range fxPairs {
		group, err := b.fxGroup(pair, FxWindowStart, step)
		if err != nil {
			return err
		}
		rows = append(rows, group...)
	}
	if len(rows) > b.spec.FxRows {
		return fmt.Errorf("seed: scenario %s wants %d FX rows but the structural table already has %d",
			b.spec.ID, b.spec.FxRows, len(rows))
	}
	// Filler: extend the window backwards. Every round adds one interval per
	// pair, in the fixed pair order, and the last round is truncated.
	for round := 1; len(rows) < b.spec.FxRows; round++ {
		to, err := addDays(FxWindowStart, -(round-1)*step)
		if err != nil {
			return err
		}
		from, err := addDays(FxWindowStart, -round*step)
		if err != nil {
			return err
		}
		for _, pair := range fxPairs {
			if len(rows) == b.spec.FxRows {
				break
			}
			row, err := b.fxDailyRow(pair, from, to, false)
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
		if round > 4000 {
			return fmt.Errorf("seed: FX filler did not converge for scenario %s", b.spec.ID)
		}
	}
	b.d.FxRows = rows
	return nil
}

// fxGroup builds the structural rows of one currency: its daily or weekly
// intervals over the base window, the planted supersessions, the open-ended GBP
// row, the deliberate coverage gap and the MONTHLY_AVG rows.
func (b *builder) fxGroup(pair fxPair, windowStart string, step int) ([]FxRow, error) {
	forced := []string{DSTPrevDate, DSTSwitchDate, "2026-03-30"}
	if pair.base != "EUR" {
		forced = nil
	}
	// GBP stops carrying daily rows at GBPOpenFrom and is open-ended from there.
	dailyEnd := FxWindowEnd
	if pair.base == "GBP" {
		dailyEnd = GBPOpenFrom
	}
	tiles, err := tileWindow(windowStart, dailyEnd, step, forced)
	if err != nil {
		return nil, err
	}
	gapDate := ""
	switch pair.base {
	case "USD":
		gapDate = USDGapDate
	case "SEK":
		gapDate = SEKGapDate
	}

	var out []FxRow
	for _, t := range tiles {
		if gapDate != "" {
			in, err := model.DateInHalfOpenRange(gapDate, t.from, t.to)
			if err != nil {
				return nil, err
			}
			if in {
				// The tile is dropped, which is the coverage hole. Recorded so
				// invoice construction can hit it on purpose and avoid it
				// otherwise.
				b.d.FxGaps = append(b.d.FxGaps, FxGap{Currency: pair.base, From: t.from, To: t.to})
				continue
			}
		}
		row, err := b.fxDailyRow(pair, t.from, t.to, false)
		if err != nil {
			return nil, err
		}
		switch {
		case pair.base == "EUR" && t.from == DSTPrevDate:
			row.Rate = fxRateEURBeforeSwitch
			row.Sequence = 7
		case pair.base == "EUR" && t.from == DSTSwitchDate:
			row.Rate = fxRateEUROnSwitch
			row.Sequence = 9
			// A nil Comment on the graded winner: a client that treats every
			// xsi:nil as an error rejects the one row it needed most.
			row.CommentNil = true
			row.Comment = ""
		}
		if err := row.setLiteral(); err != nil {
			return nil, err
		}
		out = append(out, row)

		if pair.base == "EUR" && t.from == DSTSwitchDate {
			// The superseded row: same key, LOWER Sequence, placed AFTER the
			// winner in document order, so last-wins picks 0.900000.
			sup := row
			sup.Rate = fxRateEURSuperseded
			sup.Sequence = 4
			sup.CommentNil = false
			sup.Comment = "korrigiert"
			if err := sup.setLiteral(); err != nil {
				return nil, err
			}
			out = append(out, sup)
		}
		if pair.base == "USD" {
			in, err := model.DateInHalfOpenRange(USDSupersededDate, t.from, t.to)
			if err != nil {
				return nil, err
			}
			if in {
				// The mirror image: a superseded USD row placed BEFORE its
				// winner. Together with the EUR pair above, both first-wins and
				// last-wins are wrong somewhere in the table.
				sup := out[len(out)-1]
				sup.Rate = fxRateUSDSuperseded
				sup.Sequence = 1
				sup.Comment = "storniert"
				if err := sup.setLiteral(); err != nil {
					return nil, err
				}
				winner := out[len(out)-1]
				winner.Sequence = 8
				if err := winner.setLiteral(); err != nil {
					return nil, err
				}
				out[len(out)-1] = sup
				out = append(out, winner)
			}
		}
	}

	if pair.base == "GBP" {
		row, err := b.fxDailyRow(pair, GBPOpenFrom, "", false)
		if err != nil {
			return nil, err
		}
		row.Rate = fxRateGBPOpen
		row.Sequence = 3
		if err := row.setLiteral(); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if pair.base == "JPY" {
		// Pin the interval that carries the graded JPY rate, and pretty-print
		// exactly this row: its RateFactorFrom of 100 is already a trap, and a
		// client that does not trim before parsing fails on a correct value.
		for i := range out {
			in, err := model.DateInHalfOpenRange(JPYPinnedDate, out[i].ValidFromDate, out[i].ValidToDate)
			if err != nil {
				return nil, err
			}
			if in {
				out[i].Rate = fxRateJPY
				out[i].Sequence = 2
				out[i].PrettyPrinted = true
				if err := out[i].setLiteral(); err != nil {
					return nil, err
				}
				break
			}
		}
	}

	for _, m := range fxMonths {
		row, err := b.fxDailyRow(pair, m.from, m.to, true)
		if err != nil {
			return nil, err
		}
		if err := row.setLiteral(); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// fxDailyRow builds one row of a pair over a half-open interval. An empty to
// makes the row open-ended, which the SOAP channel spells as xsi:nil.
func (b *builder) fxDailyRow(pair fxPair, from, to string, monthly bool) (FxRow, error) {
	rateType := model.RateTypeDaily
	if monthly {
		rateType = model.RateTypeMonthlyAvg
	}
	jitter := int64(b.p.between(-4000, 4000))
	millionths := pair.rateAvg + jitter
	if millionths < 1 {
		millionths = 1
	}
	row := FxRow{
		Sequence:      b.p.between(1, 7),
		Status:        model.FxStatusActive,
		Base:          pair.base,
		Quote:         QuoteCurrency,
		RateType:      rateType,
		Rate:          dec(millionths, 6),
		RateFactor:    pair.factor,
		ValidFromDate: from,
		ValidToDate:   to,
		Provider:      RateProvider,
	}
	if pair.deleted {
		row.Status = model.FxStatusDeleted
	}
	inst, err := zurichMidnight(from)
	if err != nil {
		return FxRow{}, err
	}
	row.ValidFrom = inst
	if to == "" {
		row.ValidToNil = true
	} else {
		instTo, err := zurichMidnight(to)
		if err != nil {
			return FxRow{}, err
		}
		row.ValidTo = instTo
	}
	if err := row.setLiteral(); err != nil {
		return FxRow{}, err
	}
	return row, nil
}

// setLiteral refreshes the host-locale spelling of the row's rate. It is called
// after every mutation of Rate so the literal and the exact value can never
// drift apart.
func (r *FxRow) setLiteral() error {
	s, err := FormatHostRate(r.Rate)
	if err != nil {
		return err
	}
	r.RateLiteral = s
	return nil
}

// tile is one half-open validity interval of the rate table.
type tile struct{ from, to string }

// tileWindow tiles [start, end) into intervals of step days, and splits the
// tiling so that every date in forced is an interval boundary. That is how the
// two single-day EUR rows adjacent across the summer time switch survive a
// scenario whose other rows are weekly.
func tileWindow(start, end string, step int, forced []string) ([]tile, error) {
	if step <= 0 {
		return nil, fmt.Errorf("seed: tile step must be positive, got %d", step)
	}
	total, err := daysBetween(start, end)
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, fmt.Errorf("seed: empty tile window %s..%s", start, end)
	}
	seen := map[string]bool{}
	var bounds []string
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			bounds = append(bounds, d)
		}
	}
	for off := 0; off < total; off += step {
		d, err := addDays(start, off)
		if err != nil {
			return nil, err
		}
		add(d)
	}
	add(end)
	for _, f := range forced {
		in, err := model.DateInHalfOpenRange(f, start, end)
		if err != nil {
			return nil, err
		}
		if in {
			add(f)
		}
	}
	// bounds is built from a slice walk plus a membership map; the map is only a
	// duplicate filter and the order below comes from an explicit sort.
	sortStrings(bounds)
	out := make([]tile, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		out = append(out, tile{from: bounds[i], to: bounds[i+1]})
	}
	return out, nil
}
