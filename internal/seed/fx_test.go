package seed

import (
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestFxTableShape(t *testing.T) {
	for _, scenario := range Scenarios() {
		t.Run(scenario, func(t *testing.T) {
			d := generated(t, scenario)
			spec, err := SpecOf(scenario)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.FxRows) != spec.FxRows+spec.NewFxRows {
				t.Fatalf("got %d rows, want %d", len(d.FxRows), spec.FxRows+spec.NewFxRows)
			}
			pairs := map[string]int{}
			monthly := map[string]int{}
			for _, r := range d.FxRows {
				if r.Quote != QuoteCurrency {
					t.Fatalf("row %s/%s does not quote against %s", r.Base, r.Quote, QuoteCurrency)
				}
				pairs[r.Base]++
				if r.RateType == model.RateTypeMonthlyAvg {
					monthly[r.Base]++
				}
				if r.Rate.Scale() != 6 {
					t.Fatalf("row %s %s carries scale %d, want six fraction digits",
						r.Base, r.ValidFromDate, r.Rate.Scale())
				}
			}
			if len(pairs) != 6 {
				t.Errorf("want six currency pairs, got %d: %v", len(pairs), pairs)
			}
			for base := range pairs {
				if monthly[base] == 0 {
					t.Errorf("%s has no MONTHLY_AVG row; the monthly-average trap needs one per pair", base)
				}
			}
		})
	}
}

func TestFxDSTPair(t *testing.T) {
	// The two rows adjacent across the Europe/Zurich summer time switch. The
	// offsets differ, so a client that truncates the posting date to UTC lands on
	// the row from the day before and posts 8.75 CHF too little.
	d := generated(t, ScenarioS0)
	var before, on, superseded *FxRow
	for i := range d.FxRows {
		r := &d.FxRows[i]
		if r.Base != "EUR" || r.RateType != model.RateTypeDaily {
			continue
		}
		switch {
		case r.ValidFromDate == DSTPrevDate:
			before = r
		case r.ValidFromDate == DSTSwitchDate && r.Rate.String() == "0.931000":
			on = r
		case r.ValidFromDate == DSTSwitchDate && r.Rate.String() == "0.900000":
			superseded = r
		}
	}
	if before == nil || on == nil || superseded == nil {
		t.Fatalf("the DST row set is incomplete: before=%v on=%v superseded=%v", before, on, superseded)
	}
	if before.Rate.String() != "0.924500" {
		t.Errorf("the row before the switch carries %s, want 0.924500", before.Rate.String())
	}
	if before.ValidFrom != "2026-03-28T00:00:00+01:00" || before.ValidTo != "2026-03-29T00:00:00+01:00" {
		t.Errorf("the row before the switch is %s..%s", before.ValidFrom, before.ValidTo)
	}
	if on.ValidFrom != "2026-03-29T00:00:00+01:00" || on.ValidTo != "2026-03-30T00:00:00+02:00" {
		t.Errorf("the switch row is %s..%s, want +01:00 to +02:00", on.ValidFrom, on.ValidTo)
	}
	if superseded.Sequence >= on.Sequence {
		t.Errorf("the superseded row has sequence %d, the winner %d: it must be lower",
			superseded.Sequence, on.Sequence)
	}
	if !on.CommentNil {
		t.Error("the graded winner should carry an xsi:nil Comment, so blanket nil-is-an-error fails on it")
	}
	// Document order: the superseded row sits AFTER its winner.
	iOn, iSup := -1, -1
	for i := range d.FxRows {
		if &d.FxRows[i] == on {
			iOn = i
		}
		if &d.FxRows[i] == superseded {
			iSup = i
		}
	}
	if iSup < iOn {
		t.Errorf("the superseded EUR row is at index %d, before its winner at %d", iSup, iOn)
	}
	// And the winner resolves.
	rates := map[string][]model.FxRate{}
	for _, r := range d.ResolvedDailyRates() {
		rates[r.Base] = append(rates[r.Base], r)
	}
	got, ok, err := winningRate(rates["EUR"], DSTSwitchDate)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Rate.String() != "0.931000" {
		t.Errorf("resolution on the switch day gave %v (%v)", got.Rate.String(), ok)
	}
}

func TestFxSupersessionBreaksBothDocumentOrderStrategies(t *testing.T) {
	// The EUR pair puts the loser after the winner, so last-wins is wrong. A USD
	// pair puts a loser before its winner, so first-wins is wrong too. Both
	// strategies therefore fail somewhere in the same table, which is the point.
	d := generated(t, ScenarioS0)
	type slot struct {
		index int
		row   FxRow
	}
	byKey := map[string][]slot{}
	for i, r := range d.FxRows {
		if r.RateType != model.RateTypeDaily || r.Status != model.FxStatusActive {
			continue
		}
		byKey[r.Base+"|"+r.ValidFromDate] = append(byKey[r.Base+"|"+r.ValidFromDate], slot{i, r})
	}
	loserAfter, loserBefore := 0, 0
	for _, slots := range byKey {
		if len(slots) < 2 {
			continue
		}
		winner := slots[0]
		for _, s := range slots {
			if s.row.Sequence > winner.row.Sequence {
				winner = s
			}
		}
		for _, s := range slots {
			if s.index == winner.index {
				continue
			}
			if s.index > winner.index {
				loserAfter++
			} else {
				loserBefore++
			}
		}
	}
	if loserAfter == 0 {
		t.Error("no superseded row sits after its winner: last-wins would be right")
	}
	if loserBefore == 0 {
		t.Error("no superseded row sits before its winner: first-wins would be right")
	}
}

func TestFxOpenEndedAndFactorRows(t *testing.T) {
	d := generated(t, ScenarioS0)
	openEnded, pretty := 0, 0
	var jpy *FxRow
	for i := range d.FxRows {
		r := &d.FxRows[i]
		if r.ValidToNil {
			openEnded++
			if r.ValidTo != "" || r.ValidToDate != "" {
				t.Errorf("an open-ended row still carries a valid-to: %q / %q", r.ValidTo, r.ValidToDate)
			}
			if r.Base == "GBP" && r.Rate.String() != "1.082250" {
				t.Errorf("the open-ended GBP row carries %s, want 1.082250", r.Rate.String())
			}
		}
		if r.PrettyPrinted {
			pretty++
		}
		if r.Base == "JPY" && r.Rate.String() == "0.556300" {
			jpy = r
		}
	}
	if openEnded == 0 {
		t.Error("no row is open-ended, so xsi:nil on ValidTo is never exercised")
	}
	if pretty != 1 {
		t.Errorf("want exactly one pretty-printed row, got %d", pretty)
	}
	if jpy == nil {
		t.Fatal("the pinned JPY row is missing")
	}
	if jpy.RateFactor != 100 {
		t.Errorf("the JPY rate factor is %d, want 100", jpy.RateFactor)
	}
	if !jpy.PrettyPrinted {
		t.Error("the pinned JPY row should be the pretty-printed one")
	}
	for _, r := range d.FxRows {
		if r.Base == "JPY" && r.RateFactor != 100 {
			t.Errorf("JPY row %s carries factor %d: JPY is always quoted per 100 units",
				r.ValidFromDate, r.RateFactor)
		}
		if r.Base != "JPY" && r.RateFactor != 1 {
			t.Errorf("%s row %s carries factor %d, want 1", r.Base, r.ValidFromDate, r.RateFactor)
		}
	}
}

func TestDKKExistsOnlyAsDeleted(t *testing.T) {
	for _, scenario := range Scenarios() {
		d := generated(t, scenario)
		dkk := 0
		for _, r := range d.FxRows {
			if r.Base != "DKK" {
				continue
			}
			dkk++
			if r.Status != model.FxStatusDeleted {
				t.Errorf("%s: a DKK row is %s; DKK must exist only as a soft delete", scenario, r.Status)
			}
		}
		if dkk == 0 {
			t.Errorf("%s: no DKK row at all", scenario)
		}
		for _, r := range d.ResolvedDailyRates() {
			if r.Base == "DKK" {
				t.Errorf("%s: a DKK rate survived resolution", scenario)
			}
		}
	}
}

func TestFxHostLocaleLiterals(t *testing.T) {
	d := generated(t, ScenarioS0)
	for _, r := range d.FxRows {
		if !strings.Contains(r.RateLiteral, ",") {
			t.Fatalf("rate literal %q does not use the host locale decimal comma", r.RateLiteral)
		}
		if strings.Count(r.RateLiteral, ",") != 1 {
			t.Fatalf("rate literal %q has more than one decimal comma", r.RateLiteral)
		}
		frac := r.RateLiteral[strings.IndexByte(r.RateLiteral, ',')+1:]
		if len(frac) != 6 {
			t.Fatalf("rate literal %q does not carry six fraction digits", r.RateLiteral)
		}
		want, err := FormatHostRate(r.Rate)
		if err != nil {
			t.Fatal(err)
		}
		if r.RateLiteral != want {
			t.Fatalf("rate literal %q disagrees with the exact value %s", r.RateLiteral, r.Rate.String())
		}
	}
}

func TestFxCoverageGapsArePlanted(t *testing.T) {
	d := generated(t, ScenarioS1)
	if len(d.FxGaps) == 0 {
		t.Fatal("no coverage gap was planted")
	}
	rates := map[string][]model.FxRate{}
	for _, r := range d.ResolvedDailyRates() {
		rates[r.Base] = append(rates[r.Base], r)
	}
	for _, g := range d.FxGaps {
		if _, ok, err := winningRate(rates[g.Currency], g.From); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Errorf("%s is covered on %s, but that day is meant to be a hole", g.Currency, g.From)
		}
		before, err := addDays(g.From, -1)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := winningRate(rates[g.Currency], before); err != nil {
			t.Fatal(err)
		} else if !ok {
			t.Errorf("%s is not covered on %s either: the hole is not a hole but an edge", g.Currency, before)
		}
	}
}

func TestMonthlyAveragesNeverCoverADailyLookup(t *testing.T) {
	// ResolvedDailyRates is the connector's normalization. A monthly average must
	// never leak into it, whatever its validity interval says.
	d := generated(t, ScenarioS0)
	for _, r := range d.ResolvedDailyRates() {
		if r.RateType != model.RateTypeDaily {
			t.Fatalf("a %s row reached the daily rate set", r.RateType)
		}
	}
}
