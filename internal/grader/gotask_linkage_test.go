package grader

import "testing"

// An unregistered kredexp-2.1 importer is the Go task not being done, not a
// fault of ours. It must fail both fixture blocks and raise no harness error,
// because score.md prints harness errors as "not a failure of the submission"
// and a reviewer would read the whole block as our gap.
func TestAnUnlinkedImporterIsTheCandidatesOmissionAndNotAHarnessFault(t *testing.T) {
	out := GradeGoTask(t.TempDir(), true)
	if len(out.HarnessErrors) != 0 {
		t.Fatalf("harness errors = %q, want none", out.HarnessErrors)
	}
	ids := map[string]Status{}
	for _, r := range out.Results {
		ids[r.Assertion.ID] = r.Status
	}
	for _, id := range []string{"gotask.public", "gotask.hidden", "gotask.ambiguity"} {
		if ids[id] != StatusFail {
			t.Errorf("%s = %q, want %q", id, ids[id], StatusFail)
		}
	}
	// All THREE rows, not just the two fixture blocks: the ambiguity row was
	// omitted from this branch entirely, which took its 4 points out of the
	// denominator and made an untouched checkout read 0 of 96.
	if got, want := out.Possible(),
		GoTaskPublicPoints+GoTaskHiddenPoints+GoTaskAmbiguityPoints; got != want {
		t.Errorf("possible = %d, want %d: the points must stay in the denominator", got, want)
	}
	if out.Earned() != 0 {
		t.Errorf("earned = %d, want 0", out.Earned())
	}
}
