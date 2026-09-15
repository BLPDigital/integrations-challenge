package grader

import (
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot finds the module root from this test file's own path, so the test does
// not depend on the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(self)))
}

func ids(scs []*Scenario) []string {
	out := make([]string, 0, len(scs))
	for _, sc := range scs {
		out = append(out, sc.ID)
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The stretch scenarios score nothing and cost minutes, so they are opt-in. What
// must not happen is the flag being accepted and ignored, which is what selfcheck
// did until this test existed.
func TestStretchScenariosAreOptInAndOptInWorks(t *testing.T) {
	root := repoRoot(t)

	off, err := collectScenarios(SeriesOptions{RepoRoot: root})
	if err != nil {
		t.Fatalf("collect without stretch: %v", err)
	}
	for _, id := range []string{"X1", "X2"} {
		if has(ids(off), id) {
			t.Errorf("%s is in the default series; the stretch scenarios are opt-in", id)
		}
	}
	if !has(ids(off), "S1") {
		t.Fatalf("the default series lost S1: %v", ids(off))
	}

	on, err := collectScenarios(SeriesOptions{RepoRoot: root, IncludeStretch: true})
	if err != nil {
		t.Fatalf("collect with stretch: %v", err)
	}
	for _, id := range []string{"X1", "X2"} {
		if !has(ids(on), id) {
			t.Errorf("%s missing with IncludeStretch: %v", id, ids(on))
		}
	}

	// The regression: selfcheck forces hidden off and must leave stretch alone.
	got := selfcheckOptions(SeriesOptions{RepoRoot: root, IncludeStretch: true, IncludeHidden: true})
	if got.IncludeHidden {
		t.Error("selfcheck must never include the hidden scenarios")
	}
	if !got.IncludeStretch {
		t.Error("selfcheck dropped IncludeStretch, so --stretch would parse and do nothing")
	}
}

// Every scenario file has to declare a visibility the loader knows, and the
// stretch ones have to score nothing: a scored assertion in a scenario that does
// not score would be a point nobody can earn.
func TestStretchScenariosScoreNothing(t *testing.T) {
	all, err := collectScenarios(SeriesOptions{RepoRoot: repoRoot(t), IncludeStretch: true})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	seen := 0
	for _, sc := range all {
		if sc.Visibility != VisibilityStretch {
			continue
		}
		seen++
		if sc.Scores() {
			t.Errorf("%s is stretch and reports that it scores", sc.ID)
		}
		for _, st := range sc.Steps {
			if st.Kind != StepAssert || st.Assert == nil {
				continue
			}
			if st.Assert.Level == LevelScored || st.Assert.Points != 0 {
				t.Errorf("%s: assertion %s is worth %d points at level %q; a stretch scenario is a tie-break note",
					sc.ID, st.Assert.ID, st.Assert.Points, st.Assert.Level)
			}
			if st.Assert.Group != GroupStretch {
				t.Errorf("%s: assertion %s is in group %q, want %q",
					sc.ID, st.Assert.ID, st.Assert.Group, GroupStretch)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no stretch scenario was loaded, so this test proves nothing")
	}
}

// A run that printed failures must not exit 0. The regression this pins: Failed()
// counted only failed SCORED assertions, and the smoke gate S0 scores nothing, so
// "grade run --scenario S0" against a connector that does not exist printed seven
// failures and exited successfully.
func TestARunWithFailuresIsFailed(t *testing.T) {
	fail := Result{Assertion: Assertion{ID: "S0.exit", Level: LevelInfo, Points: 0}, Status: StatusFail}
	pass := Result{Assertion: Assertion{ID: "S0.ok", Level: LevelInfo, Points: 0}, Status: StatusPass}
	scoredFail := Result{Assertion: Assertion{ID: "S1.digest", Level: LevelScored, Points: 6}, Status: StatusFail}
	errored := Result{Assertion: Assertion{ID: "S1.odd", Level: LevelScored, Points: 1}, Status: StatusError}

	cases := []struct {
		name string
		run  RunOutcome
		want bool
	}{
		{"an info-level failure is still a failure", RunOutcome{Results: []Result{pass, fail}}, true},
		{"a scored failure is a failure", RunOutcome{Results: []Result{pass, scoredFail}}, true},
		{"an assertion that could not be evaluated is a failure", RunOutcome{Results: []Result{errored}}, true},
		{"a harness error is a failure even with no assertions",
			RunOutcome{HarnessErrors: []string{"the twin would not start"}}, true},
		{"everything passing is not a failure", RunOutcome{Results: []Result{pass, pass}}, false},
		{"a skip is not a failure",
			RunOutcome{Results: []Result{pass, {Assertion: Assertion{ID: "S2.x"}, Status: StatusSkip}}}, false},
	}
	for _, c := range cases {
		if got := c.run.Failed(); got != c.want {
			t.Errorf("%s: Failed() = %v, want %v", c.name, got, c.want)
		}
	}
}
