package grader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScoredBlocksSumToTheHeadline is a contract and not a unit test. The
// published scoring table in CHALLENGE.md and docs/spec.md is what a candidate
// plans six hours against, and it is only worth trusting if it is checked.
func TestScoredBlocksSumToTheHeadline(t *testing.T) {
	if PointsTotal != PointsHeadline {
		t.Fatalf("the blocks sum to %d under a %d-point headline; one of the two is wrong",
			PointsTotal, PointsHeadline)
	}
	sum := 0
	for _, g := range scoredGroups {
		sum += BudgetOf(g)
	}
	if sum != PointsTotal {
		t.Errorf("the six scored groups budget %d, PointsTotal says %d", sum, PointsTotal)
	}
}

// TestScenarioFilesAllocateExactlyTheBudget reads the shipped scenario files and
// asserts that each block's assertions add up to the block's published budget.
//
// This is the test that would have caught two real defects: a traceability row
// published as ten points against a grader awarding five, and two robustness
// points that went missing when an assertion moved between scenarios and landed
// in the integrity group, which carries flags and never points.
func TestScenarioFilesAllocateExactlyTheBudget(t *testing.T) {
	root := repoRootFromTest(t)
	allocated := map[Group]int{}
	public, err := LoadScenarioDir(filepath.Join(root, ScenarioDir))
	if err != nil {
		t.Fatalf("loading %s: %v", ScenarioDir, err)
	}
	hidden, hiddenPresent := loadHiddenForTest(t, root)
	for _, list := range [][]*Scenario{public, hidden} {
		for _, sc := range list {
			for _, st := range sc.Steps {
				if st.Assert != nil {
					allocated[st.Assert.Group] += st.Assert.Points
				}
			}
		}
	}
	// The scenario files carry these three blocks. The Go task is graded from
	// the fixtures and the decision log is entered by a human, so neither has an
	// allocation here.
	for _, g := range []Group{GroupPublic, GroupRobustness, GroupTraceability} {
		if allocated[g] != BudgetOf(g) {
			t.Errorf("group %s: the scenario files allocate %d, the specification budgets %d",
				g, allocated[g], BudgetOf(g))
		}
	}
	if !hiddenPresent {
		t.Skip("the hidden scenarios are not in this tree, which is the candidate bundle's shape")
	}
	if allocated[GroupHidden] != BudgetOf(GroupHidden) {
		t.Errorf("group %s: the scenario files allocate %d, the specification budgets %d",
			GroupHidden, allocated[GroupHidden], BudgetOf(GroupHidden))
	}
}

// TestScenarioAssertionIDsAreUnique catches a copied assertion whose id was not
// changed, which silently replaces one check with another in the scorecard.
func TestScenarioAssertionIDsAreUnique(t *testing.T) {
	root := repoRootFromTest(t)
	seen := map[string]string{}
	public, err := LoadScenarioDir(filepath.Join(root, ScenarioDir))
	if err != nil {
		t.Fatalf("loading %s: %v", ScenarioDir, err)
	}
	hidden, _ := loadHiddenForTest(t, root)
	for _, list := range [][]*Scenario{public, hidden} {
		for _, sc := range list {
			for _, st := range sc.Steps {
				if st.Assert == nil {
					continue
				}
				if where, dup := seen[st.Assert.ID]; dup {
					t.Errorf("assertion id %q appears in %s and in %s",
						st.Assert.ID, where, sc.ID)
				}
				seen[st.Assert.ID] = sc.ID
			}
		}
	}
}

// TestComputeZeroesBothHalvesOfAnUngradedHiddenBlock pins the fix for a
// scorecard that contradicted itself: the hidden row printed 0 earned of 0
// possible while its twenty points were still in the total, so the blocks did
// not add up to the number at the top of the page.
func TestComputeZeroesBothHalvesOfAnUngradedHiddenBlock(t *testing.T) {
	run := &RunOutcome{Scenario: "H1", Results: []Result{{
		Assertion: Assertion{ID: "H1.digest", Group: GroupHidden, Points: 4, Level: LevelScored},
		Status:    StatusPass, Earned: 4, Possible: 4, Scenario: "H1",
	}}}
	sc := Compute(ScoreInput{Runs: []*RunOutcome{run}, HiddenGraded: false})
	var hidden Block
	total := 0
	for _, b := range sc.Blocks {
		total += b.Earned
		if b.Group == GroupHidden {
			hidden = b
		}
	}
	if hidden.Earned != 0 || hidden.Possible != 0 {
		t.Errorf("ungraded hidden block: earned %d, possible %d; want 0 and 0",
			hidden.Earned, hidden.Possible)
	}
	if total != sc.Total {
		t.Errorf("the blocks sum to %d and the scorecard says %d", total, sc.Total)
	}
	// The note has to say what actually happened. "Not run" would be a lie when
	// the assertions are right there in the table.
	if !strings.Contains(hidden.Note, "-tags hidden") {
		t.Errorf("the note should tell the reviewer how to fix it, got %q", hidden.Note)
	}
}

// TestBlocksAlwaysSumToTheTotal is the same invariant over a graded run.
func TestBlocksAlwaysSumToTheTotal(t *testing.T) {
	seven := 7
	run := &RunOutcome{Scenario: "S1", Results: []Result{
		{Assertion: Assertion{ID: "S1.digest", Group: GroupPublic, Points: 6, Level: LevelScored},
			Status: StatusPass, Earned: 6, Possible: 6, Scenario: "S1"},
		{Assertion: Assertion{ID: "S1.closure", Group: GroupRobustness, Points: 2, Level: LevelScored},
			Status: StatusFail, Earned: 0, Possible: 2, Scenario: "S1"},
	}}
	sc := Compute(ScoreInput{Runs: []*RunOutcome{run}, HiddenGraded: true, DecisionLog: &seven})
	total := 0
	for _, b := range sc.Blocks {
		total += b.Earned
	}
	if total != sc.Total {
		t.Fatalf("the blocks sum to %d and the scorecard says %d", total, sc.Total)
	}
	if sc.Total != 6+seven {
		t.Errorf("total %d, want %d", sc.Total, 6+seven)
	}
}

// loadHiddenForTest loads the hidden scenarios when this build can read them.
//
// Without the `hidden` build tag the assertion kinds they name are not compiled
// in, so LoadScenarioDir refuses the files with "unknown kind". That is the
// protection mechanism working, not a defect: the test reports the hidden block
// as unavailable and checks the rest. Under -tags hidden it loads and is
// checked like any other.
func loadHiddenForTest(t *testing.T, root string) ([]*Scenario, bool) {
	t.Helper()
	list, err := LoadScenarioDir(filepath.Join(root, HiddenScenarioDir))
	if err != nil {
		if strings.Contains(err.Error(), "unknown kind") {
			t.Logf("hidden scenarios present and not readable by this build: %v", err)
			return nil, false
		}
		t.Fatalf("loading %s: %v", HiddenScenarioDir, err)
	}
	return list, len(list) > 0
}

// repoRootFromTest walks up to the module root, so the test does not depend on
// where go test was invoked from.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("no go.mod above the working directory")
	return ""
}

// Every stretch scenario has to reach the scorecard, and the deduplication in
// collectFlags is why this test exists: both notes were added under one flag id,
// so the second one was dropped silently. With a single stretch scenario in the
// tree, nothing would have shown it.
func TestEveryStretchScenarioProducesItsOwnNote(t *testing.T) {
	stretchRun := func(id, title string, pass, fail int) *RunOutcome {
		run := &RunOutcome{Scenario: id, Visibility: VisibilityStretch, Title: title}
		for i := 0; i < pass; i++ {
			run.Results = append(run.Results, Result{
				Assertion: Assertion{ID: id + ".ok", Group: GroupStretch, Level: LevelInfo},
				Status:    StatusPass})
		}
		for i := 0; i < fail; i++ {
			run.Results = append(run.Results, Result{
				Assertion: Assertion{ID: id + ".bad", Group: GroupStretch, Level: LevelInfo},
				Status:    StatusFail})
		}
		return run
	}
	in := ScoreInput{
		Submission: "t",
		Connector:  "t",
		Runs: []*RunOutcome{
			stretchRun("X1", "crash and resume", 8, 0),
			stretchRun("X2", "scale", 9, 1),
		},
	}
	sc := Compute(in)

	if len(sc.Stretch) != 2 {
		t.Fatalf("stretch notes = %d, want 2: %v", len(sc.Stretch), sc.Stretch)
	}
	for _, want := range []string{"X1", "X2"} {
		found := false
		for _, f := range sc.Flags {
			if f.ID == "note.stretch."+want {
				found = true
				if f.Severity != SeverityNote {
					t.Errorf("%s note has severity %q, want a note: a stretch scenario can never deduct",
						want, f.Severity)
				}
			}
		}
		if !found {
			ids := make([]string, 0, len(sc.Flags))
			for _, f := range sc.Flags {
				ids = append(ids, f.ID)
			}
			t.Errorf("no scorecard note for stretch scenario %s; flags were %v", want, ids)
		}
	}
	// A failed check in a stretch scenario is reported and never scored. The
	// comparison is against the same input without the stretch runs, so the claim
	// is "they change nothing" rather than "the total happens to be zero".
	bare := Compute(ScoreInput{Submission: in.Submission, Connector: in.Connector})
	if sc.Total != bare.Total || sc.Possible != bare.Possible {
		t.Errorf("stretch runs moved the score from %d/%d to %d/%d",
			bare.Total, bare.Possible, sc.Total, sc.Possible)
	}
	if strings.Contains(sc.BandReason, "X2") {
		t.Error("a stretch scenario reached the band reason")
	}
}
