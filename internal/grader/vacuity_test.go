package grader

import (
	"os"
	"strings"
	"testing"
)

// An inert run is what the untouched checkout produces: connector/run.sh is
// still the stub, which prints its flags and exits 3 without contacting
// anything. Before the guard such a run collected 29 of 100 points, because most
// of the vocabulary asks whether something bad happened and nothing at all had.
func inertRun() *Observed {
	return &Observed{Scenario: "S1", Evidence: map[string]string{}}
}

func TestAnInertRunIsRecognizedAndActivityLiftsTheGuard(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Observed)
		want bool
	}{
		{"the stub never ran", func(*Observed) {}, true},
		{"one ERP request is activity", func(o *Observed) { o.ERPMetrics.RequestsTotal = 1 }, false},
		{"one twin request is activity", func(o *Observed) { o.TwinMetrics.RequestsTotal = 1 }, false},
		// H2 opens by dropping two batches of its own before the connector is
		// invoked at all, so a batch in the twin is not evidence that anybody's
		// connector ran. Counting it kept that scenario handing out two points
		// to an untouched checkout.
		{"a batch the harness dropped is not activity", func(o *Observed) {
			o.TwinMetrics.Batches = 2
			o.TwinBatches = []TwinBatch{{}, {}}
		}, true},
		{"a created document is activity", func(o *Observed) { o.ERPMetrics.DocumentsCreated = 1 }, false},
		{"a document the ERP recorded is activity", func(o *Observed) { o.ERPDocuments = []ERPDocument{{}} }, false},
		// Reports are deliberately not activity: a run.json written by a
		// connector that never contacted a server describes work not done, and
		// must not be what lifts the guard.
		{"reports alone are not activity", func(o *Observed) {
			o.Reports.RunJSONPresent = true
			o.Reports.PostingsPresent = true
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := inertRun()
			tc.mut(obs)
			if got := obs.NoActivity(); got != tc.want {
				t.Fatalf("NoActivity() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The whole point: an invariant that holds only because nothing happened must
// not be worth its points, and the points must stay in the denominator so the
// scorecard reads 0 of 30 rather than 0 of 0.
func TestAVacuousPassIsNotWorthItsPoints(t *testing.T) {
	a := Assertion{ID: "S1.no_lockout", Group: GroupRobustness, Points: 3,
		Level: LevelScored, Kind: KindERPLockoutsEqZero,
		Title: "no client quarantine was triggered"}

	inert := Evaluate(a, inertRun())
	if inert.Status != StatusFail {
		t.Errorf("on an inert run: status = %q, want %q", inert.Status, StatusFail)
	}
	if inert.Earned != 0 || inert.Possible != 3 {
		t.Errorf("on an inert run: %d/%d points, want 0/3: the points must stay in the denominator",
			inert.Earned, inert.Possible)
	}
	if len(inert.Findings) == 0 {
		t.Error("on an inert run: no finding says why the pass was withdrawn")
	}

	// The same assertion on a run that actually happened is untouched. Zero
	// lockouts is the correct answer for a well-behaved connector.
	obs := inertRun()
	obs.ERPMetrics.RequestsTotal = 412
	if live := Evaluate(a, obs); live.Status != StatusPass || live.Earned != 3 {
		t.Errorf("on a live run: %q %d/%d, want pass 3/3", live.Status, live.Earned, live.Possible)
	}
}

// A failure keeps its own reason. It failed for something more informative than
// "nothing ran", and overwriting that would cost the reviewer the diagnosis.
func TestAnAssertionThatFailedOnItsOwnKeepsItsSummary(t *testing.T) {
	a := Assertion{ID: "S1.no_lockout", Group: GroupRobustness, Points: 1,
		Level: LevelScored, Kind: KindERPLockoutsEqZero}
	obs := inertRun()
	obs.ERPRequests = []ERPRequest{{Sequence: 1, Status: 403}}
	res := Evaluate(a, obs)
	if res.Status != StatusFail {
		t.Fatalf("status = %q, want %q", res.Status, StatusFail)
	}
	if got := res.Summary; got == "" || got == "unearned: the connector produced no activity, so this held vacuously" {
		t.Errorf("summary = %q, want the check's own diagnosis", got)
	}
}

// A disqualifier is an accusation. The legacy-profile check is failed by a run
// that delivered nothing, and rendering that as "the graded profile was
// bypassed" would accuse a candidate who submitted nothing of gaming the
// exercise. It is skipped instead, which also keeps the flag out of score.md,
// because the disqualifier flags are built from failed results.
func TestADisqualifierDoesNotFireOnARunThatNeverHappened(t *testing.T) {
	a := Assertion{ID: "H4.legacy_profile", Group: GroupIntegrity, Points: 0,
		Level: LevelInfo, Kind: KindLegacyProfileUsed}
	res := Evaluate(a, inertRun())
	if res.Status != StatusSkip {
		t.Fatalf("status = %q, want %q", res.Status, StatusSkip)
	}
	if res.Possible != 0 || res.Earned != 0 {
		t.Errorf("%d/%d points, want 0/0", res.Earned, res.Possible)
	}
	sc := Compute(ScoreInput{Runs: []*RunOutcome{{Scenario: "H4", Results: []Result{res}}}})
	for _, f := range sc.Flags {
		if f.Severity == SeverityDisqualifier {
			t.Errorf("disqualifier %q fired on a run that never happened", f.ID)
		}
	}
}

// The guard must never manufacture an accusation. Four of the disqualifier flags
// are built from failed robustness assertions - a leaked credential, a posted
// IBAN, a posting nobody proposed, a bypassed legacy profile - and an inert run
// fails all four trivially. Telling a candidate who submitted nothing that they
// leaked a credential is worse than the points the guard takes away.
func TestTheGuardNeverManufacturesAnAccusation(t *testing.T) {
	accusing := []struct {
		id   string
		kind string
	}{
		{"S1.no_secrets", KindNoSecretInOutput},
		{"S1.no_payment_data", KindNoPaymentDataInOutput},
		{"S1.no_unproposed_postings", KindNoUnproposedPostings},
		{"H4.legacy_profile", KindLegacyProfileUsed},
	}
	var results []Result
	for _, c := range accusing {
		a := Assertion{ID: c.id, Group: GroupRobustness, Points: 1,
			Level: LevelScored, Kind: c.kind}
		res := Evaluate(a, inertRun())
		if !res.Inert {
			t.Errorf("%s: the result is not marked as coming from an inert run", c.id)
		}
		results = append(results, res)
	}
	sc := Compute(ScoreInput{Runs: []*RunOutcome{{Scenario: "S1", Results: results}}})
	for _, f := range sc.Flags {
		if f.Severity == SeverityDisqualifier {
			t.Errorf("disqualifier %q fired on a run that never happened: %s", f.ID, f.Message)
		}
	}
}

// Phase two is the one phase doable with no HTTP at all: read the KRED files out
// of --erp-export-dir, write a batch into --twin-drop-dir, and let the harness's
// own scan pick it up. A candidate who built the file channel and nothing else
// made real deliveries with every counter at zero, and was being told they
// produced nothing. H2, meanwhile, plants three batches of its own before the
// connector is invoked, so the count alone decides nothing.
func TestABatchCountsAsActivityOnlyWhenTheHarnessDidNotPlantIt(t *testing.T) {
	cases := []struct {
		name    string
		planted map[string]bool
		batches []TwinBatch
		want    bool
	}{
		{"H2 plants every batch it shows", map[string]bool{
			"planted-bad-count-0001": true, "planted-conflict-0001": true},
			[]TwinBatch{{ID: "planted-bad-count-0001"}, {ID: "planted-conflict-0001"}}, true},
		{"a batch nobody planted is the connector's", map[string]bool{
			"planted-conflict-0001": true},
			[]TwinBatch{{ID: "planted-conflict-0001"}, {ID: "blp-run-0001"}}, false},
		{"the file channel alone lifts the guard", nil,
			[]TwinBatch{{ID: "blp-run-0001"}}, false},
		{"a batch with no id is attributed to nobody", nil,
			[]TwinBatch{{ID: ""}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := inertRun()
			obs.PlantedBatches = tc.planted
			obs.TwinBatches = tc.batches
			if got := obs.NoActivity(); got != tc.want {
				t.Fatalf("NoActivity() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A skip scores zero out of zero, so three scored assertions skipping on an
// inert run took seven points out of the denominator and an empty submission
// read 0 of 93. Seven missing points read as our gap rather than the
// candidate's.
func TestAScoredSkipOnAnInertRunKeepsItsPointsInTheDenominator(t *testing.T) {
	a := Assertion{ID: "S2.watermark", Group: GroupRobustness, Points: 1,
		Level: LevelScored, Kind: KindNoFullReloadOnSecondRun}
	res := Evaluate(a, inertRun())
	if res.Status != StatusFail {
		t.Errorf("status = %q, want %q", res.Status, StatusFail)
	}
	if res.Earned != 0 || res.Possible != 1 {
		t.Errorf("%d/%d points, want 0/1", res.Earned, res.Possible)
	}
	// The checker's own sentence is the diagnosis and is kept.
	if res.Summary == "unearned: the connector produced no activity, so this held vacuously" {
		t.Errorf("summary = %q, want the check's own reason", res.Summary)
	}
	if len(res.Findings) == 0 {
		t.Error("no finding says the run never happened")
	}
}

// The same skip on a run that actually happened is a harness gap and still
// scores nothing out of nothing, which is what a skip is for.
func TestASkipOnALiveRunStillScoresNothingOfNothing(t *testing.T) {
	a := Assertion{ID: "S2.watermark", Group: GroupRobustness, Points: 1,
		Level: LevelScored, Kind: KindNoFullReloadOnSecondRun}
	obs := inertRun()
	obs.ERPMetrics.RequestsTotal = 412
	res := Evaluate(a, obs)
	if res.Status != StatusSkip || res.Possible != 0 {
		t.Fatalf("%q %d/%d, want skip 0/0", res.Status, res.Earned, res.Possible)
	}
}

// A page of red with no explanation at the top is the reviewer's problem, not
// the candidate's.
func TestTheScorecardSaysWhyEverythingIsZero(t *testing.T) {
	a := Assertion{ID: "S1.no_lockout", Group: GroupRobustness, Points: 3,
		Level: LevelScored, Kind: KindERPLockoutsEqZero}
	res := Evaluate(a, inertRun())
	sc := Compute(ScoreInput{Runs: []*RunOutcome{{Scenario: "S1", Results: []Result{res}}}})
	for _, f := range sc.Flags {
		if f.ID == "note.inert_run" {
			return
		}
	}
	t.Error("no note.inert_run flag explains the zero")
}

// Eighteen green rows for a submission whose connector never started is how a
// scanned table lies. They are worth nothing, and the status says so.
func TestTheAssertionTableDoesNotShowAnInertRunAsEighteenPasses(t *testing.T) {
	inert := Evaluate(Assertion{ID: "S0.no_crash", Group: GroupPublic, Points: 0,
		Level: LevelInfo, Kind: KindNoStderrPanic}, inertRun())
	if inert.Status != StatusPass || !inert.Inert {
		t.Fatalf("setup: %q inert=%v, want a passing inert info assertion", inert.Status, inert.Inert)
	}
	sc := Compute(ScoreInput{Runs: []*RunOutcome{{Scenario: "S0", Results: []Result{inert}}}})
	dir := t.TempDir()
	path := dir + "/score.md"
	if err := WriteScoreMarkdown(path, sc); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "PASS (nothing ran)") {
		t.Error("the table shows a bare PASS for an assertion nothing was asked of")
	}
}

// The legacy-profile disqualifier says the graded profile was bypassed by
// pre-transforming the delivery outside the twin. That accusation only makes
// sense when a delivery arrived. The first real submission this ran against
// stopped after phase 1, said so in its decision log, and was handed the
// disqualifier for a profile it never reached.
func TestAPhaseNotBuiltIsNotAProfileBypassed(t *testing.T) {
	a := Assertion{ID: "H4.legacy_profile", Group: GroupIntegrity, Points: 0,
		Level: LevelInfo, Kind: KindLegacyProfileUsed}

	cases := []struct {
		name    string
		batches []TwinBatch
		wantDQ  bool
	}{
		{"nothing was delivered, so nothing was bypassed", nil, false},
		// A phase-1 submission delivers master data over the file channel, so
		// the twin has batches. None of them is an invoice, and counting
		// batches accused exactly this candidate.
		{"master data arrived and no invoice did", []TwinBatch{
			{ID: "b-1", Files: []TwinFileReceipt{{Dataset: "supplier", Profile: "blp-canonical-v1"}}},
		}, false},
		{"invoices arrived in a shape the connector made itself", []TwinBatch{
			{ID: "b-1", Files: []TwinFileReceipt{{Dataset: "invoice", Profile: "blp-canonical-v1"}}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := inertRun()
			// The run itself happened: the guard for an inert run is a different
			// mechanism and must not be what decides this.
			obs.ERPMetrics.RequestsTotal = 412
			obs.TwinBatches = tc.batches
			res := Evaluate(a, obs)
			if res.Status != StatusFail {
				t.Fatalf("status = %q, want %q", res.Status, StatusFail)
			}
			sc := Compute(ScoreInput{Runs: []*RunOutcome{{Scenario: "H4", Results: []Result{res}}}})
			var gotDQ bool
			for _, f := range sc.Flags {
				if f.ID == "dq.legacy_profile_bypassed" {
					gotDQ = true
				}
			}
			if gotDQ != tc.wantDQ {
				t.Errorf("disqualifier fired = %v, want %v; flags = %v", gotDQ, tc.wantDQ, sc.Flags)
			}
		})
	}
}
