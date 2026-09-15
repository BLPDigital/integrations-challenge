package model

import "testing"

// TestGradedAmounts pins the exact money values the challenge is scored on. Each
// case is an assertion a grading scenario makes against a posting proposal, and
// each one fails for a different wrong implementation: banker's rounding, a
// float64 money path, an ignored per-unit rate factor, or an FX date truncated
// to UTC across the DST boundary. If one of these changes, a published expected
// value changes with it, so this test is a contract and not a unit test.
func TestGradedAmounts(t *testing.T) {
	tests := []struct {
		name        string
		gross       string
		rate        string
		factor      int64
		targetScale uint8
		want        string
		catches     string
	}{{
		name: "GBP half away from zero", gross: "100.00", rate: "1.082250",
		factor: 1, targetScale: 2, want: "108.23",
		catches: "banker's rounding gives 108.22, float64 gives 108.22 on most runtimes",
	}, {
		name: "JPY quoted per 100 units", gross: "250000", rate: "0.556300",
		factor: 100, targetScale: 2, want: "1390.75",
		catches: "ignoring RateFactorFrom gives 139075.00, a factor of 100 too much",
	}, {
		name: "EUR on the day of the DST switch", gross: "1345.63", rate: "0.931000",
		factor: 1, targetScale: 2, want: "1252.78",
		catches: "the rate valid on 2026-03-29 in Europe/Zurich",
	}, {
		name: "EUR with the previous day's rate", gross: "1345.63", rate: "0.924500",
		factor: 1, targetScale: 2, want: "1244.03",
		catches: "what a UTC-truncated FX date produces: 8.75 CHF too little",
	}, {
		name: "credit note keeps its sign", gross: "-100.00", rate: "1.082250",
		factor: 1, targetScale: 2, want: "-108.23",
		catches: "half away from zero rounds away on negatives too",
	}, {
		name: "exact rate needs no rounding", gross: "1000.00", rate: "0.500000",
		factor: 1, targetScale: 2, want: "500.00",
		catches: "a rescale that is exact must not drift",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MustDecimal(tc.gross).MulRate(MustDecimal(tc.rate), tc.factor, tc.targetScale)
			if err != nil {
				t.Fatalf("MulRate: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("%s * %s / %d = %s, want %s\n  this case catches: %s",
					tc.gross, tc.rate, tc.factor, got, tc.want, tc.catches)
			}
		})
	}
}

// TestDSTBoundaryRateSelection pins the calendar-date semantics the FX interval
// match depends on. An invoice dated 2026-03-29 is a Europe/Zurich calendar
// date, and the rate row valid from that date is serialized with the post-switch
// offset. Truncating the instant to a UTC date moves it back to 2026-03-28 and
// silently selects the previous day's rate.
func TestDSTBoundaryRateSelection(t *testing.T) {
	off, err := ZurichOffsetMinutes("2026-03-28")
	if err != nil || off != 60 {
		t.Errorf("offset on 2026-03-28 = %d (%v), want 60", off, err)
	}
	if off, err = ZurichOffsetMinutes("2026-03-29"); err != nil || off != 120 {
		t.Errorf("offset on 2026-03-29 = %d (%v), want 120", off, err)
	}
	// The instant that starts 2026-03-29 locally is 2026-03-28T23:00Z. A client
	// that takes the UTC date of that instant gets the wrong calendar day.
	got, err2 := ZurichDateOf("2026-03-28T23:00:00Z")
	if err = err2; err != nil {
		t.Fatalf("ZurichDateOf: %v", err)
	}
	if got != "2026-03-29" {
		t.Fatalf("ZurichDateOf(2026-03-28T23:00:00Z) = %s, want 2026-03-29", got)
	}
}

// TestMoneyNeverTravelsAsJSONNumber pins the rule that makes the leading-zero and
// binary-floating-point pain points enforceable rather than hoped for.
func TestMoneyNeverTravelsAsJSONNumber(t *testing.T) {
	var d Decimal
	if err := d.UnmarshalJSON([]byte(`1250.00`)); err == nil {
		t.Fatal("a JSON number was accepted as a decimal; it must be ErrNotString")
	}
	if err := d.UnmarshalJSON([]byte(`"1250.00"`)); err != nil {
		t.Fatalf("a JSON string decimal was rejected: %v", err)
	}
	if b, err := MustDecimal("1250.00").MarshalJSON(); err != nil || string(b) != `"1250.00"` {
		t.Fatalf("MarshalJSON = %s, %v; want \"1250.00\"", b, err)
	}
}
