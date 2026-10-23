package grader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReportHTMLRenders is the regression test for a template that never
// rendered. Its upper helper took a string, and both Status and Level are named
// string types, which text/template refuses to convert. Nothing rendered the
// report in a test, so the failure surfaced at the end of a fifteen-minute
// scoring run: every report file written, and then
// "wrong type for value; expected string; got grader.Status".
func TestReportHTMLRenders(t *testing.T) {
	nine := 9
	run := &RunOutcome{
		Scenario: "S1", Visibility: VisibilityPublic, Title: "cold load",
		Seed: 20260416, ConnectorRunID: "run_s1_test",
		Results: []Result{
			{Assertion: Assertion{ID: "S1.digest", Group: GroupPublic, Points: 6,
				Level: LevelScored, Kind: KindStateDigest, Title: "the digest"},
				Status: StatusPass, Earned: 6, Possible: 6, Scenario: "S1",
				Summary: "state digest matches"},
			{Assertion: Assertion{ID: "S1.closure", Group: GroupRobustness, Points: 2,
				Level: LevelScored, Kind: KindReceiptClosure, Title: "closure"},
				Status: StatusFail, Earned: 0, Possible: 2, Scenario: "S1",
				Summary: "one batch never closed",
				Findings: []Finding{{Subject: "batch md-1", Expected: "a DONE sentinel",
					Actual: "no DONE", Hint: "DONE is written last, by rename"}}},
			{Assertion: Assertion{ID: "S1.reports", Group: GroupIntegrity, Points: 0,
				Level: LevelInfo, Kind: KindReportSchema, Title: "reports"},
				Status: StatusSkip, Scenario: "S1", Summary: "no reports to read"},
		},
		HarnessErrors: []string{"a service needed a second teardown pass"},
	}
	sc := Compute(ScoreInput{
		Submission: "/tmp/candidate", Connector: "/tmp/candidate/connector/run.sh",
		Runs: []*RunOutcome{run}, HiddenGraded: false,
		DecisionLog: &nine, DecisionLogBy: "a reviewer",
	})

	path := filepath.Join(t.TempDir(), "report.html")
	if err := WriteReportHTML(path, sc); err != nil {
		t.Fatalf("WriteReportHTML: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"<title>", "S1.digest", "S1.closure", "PASS", "FAIL",
		"a DONE sentinel", "a service needed a second teardown pass",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the report does not contain %q", want)
		}
	}
	// A reviewer scorecard that reaches for a CDN is useless on a laptop with no
	// network, and every artifact here has to be readable offline.
	for _, forbidden := range []string{"http://", "https://", "<script src"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the report reaches outside itself: found %q", forbidden)
		}
	}
}

// TestReportHTMLSurvivesAnEmptyScore pins the degenerate case: a scoring run
// that died before any scenario ran still has to produce a readable page,
// because that is exactly when a reviewer needs to see why.
func TestReportHTMLSurvivesAnEmptyScore(t *testing.T) {
	sc := Compute(ScoreInput{Submission: "/tmp/candidate"})
	path := filepath.Join(t.TempDir(), "report.html")
	if err := WriteReportHTML(path, sc); err != nil {
		t.Fatalf("WriteReportHTML on an empty score: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("an empty page is not a report")
	}
}
