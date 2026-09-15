package grader

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	// The aggregator, and only the aggregator. internal/importer/kredexp is
	// deliberately NOT imported here: the Go task's one change outside the
	// candidate's own package is a blank-import line in internal/importer/all,
	// and importing the package directly would register the format whether or
	// not that line exists, so the contract would go unchecked. Looking the
	// format up by its published id through the registry checks it exactly.
	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/all"
)

// The two published strings the Go-task grading needs from the candidate's
// package, held here as literals rather than as an import for the reason above.
const (
	// KredExpID is the format id, as it appears in a manifest's profile field.
	// It is fixed by BUILD-SPEC 10 and by the package's own documentation.
	KredExpID = "kredexp-2.1"
	// KredExpNotImplemented is the diagnostic the shipped stub emits. It is not
	// part of the published code set and no fixture expects it; recognizing it
	// is how an unfinished task is told from a wrong one, so that an unfinished
	// task scores zero and says so rather than looking like a broken repository.
	KredExpNotImplemented = "not_implemented"
)

// The fixture directories of the Go task, relative to the repository root.
const (
	// PublicFixtureDir holds the four fixtures that ship.
	PublicFixtureDir = "testdata/kredexp"
	// HiddenFixtureDir holds the three that do not.
	HiddenFixtureDir = "grading/hidden/kredexp"
)

// GoTaskPoints is the point split of BUILD-SPEC 13's Go-task row: 8 for the
// public fixtures, 8 for the hidden ones, 4 for the ambiguity write-up.
//
// The write-up points are not machine-checkable prose. What is machine-checkable
// is whether the two ambiguous fields were *surfaced* rather than guessed, and
// that is what [GoTaskOutcome.AmbiguitySurfaced] measures: it reads the fixtures'
// own diagnostics, not the candidate's DECISIONS.md. A reviewer adjusts the
// prose half by hand through the decision-log score.
const (
	// GoTaskPublicPoints is what the four public fixtures are worth.
	GoTaskPublicPoints = 8
	// GoTaskHiddenPoints is what the three hidden fixtures are worth.
	GoTaskHiddenPoints = 8
	// GoTaskAmbiguityPoints is what surfacing the two ambiguities is worth.
	GoTaskAmbiguityPoints = 4
)

// A FixtureOutcome is one golden fixture's result.
type FixtureOutcome struct {
	// Name is the fixture's base name.
	Name string `json:"name"`
	// Dir is the directory it came from, so a report can tell public from
	// hidden without a second lookup.
	Dir string `json:"dir"`
	// Hidden reports whether the fixture is one of the stripped ones.
	Hidden bool `json:"hidden"`
	// Passed reports whether the projection matched the expectation.
	Passed bool `json:"passed"`
	// Detected reports whether Detect recognized the fixture's head. A format
	// that cannot recognize its own files is a finding in its own right; it is
	// not scored, because the twin falls back on the manifest's declared
	// profile and a batch that names kredexp-2.1 reaches Parse either way.
	Detected bool `json:"detected"`
	// Expectation reports whether the fixture had an expectation file at all.
	Expectation bool `json:"expectation_present"`
	// Diff is the readable projection diff, "" when the fixture passed.
	Diff string `json:"diff,omitempty"`
	// Error is a harness fault: an unreadable fixture, a Parse that returned an
	// error where it must return a Result with diagnostics on it.
	Error string `json:"error,omitempty"`
	// Codes are the diagnostic codes the parse produced, sorted and deduped.
	// They are what the ambiguity check reads.
	Codes []string `json:"codes"`
	// Accepted is the parse's own verdict on whether the file may be applied.
	Accepted bool `json:"accepted"`
}

// A GoTaskOutcome is the whole Go-task score.
type GoTaskOutcome struct {
	// Implemented reports whether the candidate's importer is registered and
	// does more than the stub. An unimplemented task scores zero and says so;
	// it never looks like a broken repository.
	Implemented bool `json:"implemented"`
	// Fixtures are the outcomes in name order, public first.
	Fixtures []FixtureOutcome `json:"fixtures"`
	// PublicPassed, PublicTotal, HiddenPassed and HiddenTotal are the tallies.
	PublicPassed, PublicTotal int `json:"-"`
	HiddenPassed, HiddenTotal int `json:"-"`
	// AmbiguitySurfaced reports whether at least one fixture produced an
	// ambiguous_field_semantics diagnostic, i.e. whether the two undecidable
	// fields were surfaced rather than resolved silently.
	AmbiguitySurfaced bool `json:"ambiguity_surfaced"`
	// Results are the assertion results, so the Go task appears in score.md and
	// report.html in exactly the same shape as every other block.
	Results []Result `json:"results"`
	// HarnessErrors are this block's harness faults.
	HarnessErrors []string `json:"harness_errors,omitempty"`
}

// Earned and Possible sum the block's points.
func (g *GoTaskOutcome) Earned() int {
	n := 0
	for _, r := range g.Results {
		n += r.Earned
	}
	return n
}

// Possible sums the points the block could have awarded.
func (g *GoTaskOutcome) Possible() int {
	n := 0
	for _, r := range g.Results {
		n += r.Possible
	}
	return n
}

// GradeGoTask runs every golden fixture against the candidate's registered
// kredexp-2.1 importer and scores the block.
//
// The Go task is scored here and nowhere else, which is the decoupling BUILD-SPEC
// 10 requires: a candidate whose parser is unfinished still scores the pipeline
// (the twin can run our reference importer), and a candidate whose pipeline is
// unfinished still scores the parser. Neither half can take the other down.
//
// Comparison is the documented projection - canonical documents, sorted
// diagnostic tuples, totals, accepted - and not a byte-identical whole-Result
// JSON. A candidate who improves a diagnostic message must not meet fourteen
// golden failures and learn nothing from them.
func GradeGoTask(repoRoot string, includeHidden bool) *GoTaskOutcome {
	out := &GoTaskOutcome{}
	format, ok := importer.Lookup(KredExpID)
	if !ok {
		// Not a harness fault. The blank import is the one documented line the
		// task asks for outside the candidate's own package, so an unregistered
		// format is the task not being done, and it fails every fixture block
		// rather than leaving the hidden half unassessed: 0 of 20 is the honest
		// reading, and "not assessed" would have read as our gap.
		notLinked := Finding{
			Subject:  "internal/importer/all/all.go",
			Expected: "the blank import of internal/importer/kredexp is uncommented",
			Actual:   "the format is not registered, so no fixture can be parsed",
			Hint: "that one line is the only change the task needs outside " +
				"internal/importer/kredexp",
			EvidencePath: filepath.Join(repoRoot, "internal/importer/all/all.go"),
		}
		out.Results = append(out.Results, goTaskResult("gotask.public", GroupGoTask,
			GoTaskPublicPoints, StatusFail,
			fmt.Sprintf("the %s importer is not linked in", KredExpID), notLinked))
		if includeHidden {
			out.Results = append(out.Results, goTaskResult("gotask.hidden", GroupGoTask,
				GoTaskHiddenPoints, StatusFail,
				fmt.Sprintf("the %s importer is not linked in", KredExpID), notLinked))
		}
		// The ambiguity row belongs here too, for the same reason the fixture
		// rows do: 0 of 20 is the honest reading of a task that was not done,
		// and omitting the row took its 4 points out of the denominator, so an
		// untouched checkout read 0 of 96 and the missing four looked like our
		// gap rather than the candidate's.
		out.Results = append(out.Results, ambiguityResult(out))
		return out
	}

	dirs := []struct {
		path   string
		hidden bool
	}{{filepath.Join(repoRoot, PublicFixtureDir), false}}
	if includeHidden {
		dirs = append(dirs, struct {
			path   string
			hidden bool
		}{filepath.Join(repoRoot, HiddenFixtureDir), true})
	}

	for _, d := range dirs {
		cases, err := importer.LoadGoldenCases(d.path)
		if err != nil {
			out.HarnessErrors = append(out.HarnessErrors,
				fmt.Sprintf("loading fixtures from %s: %v", d.path, err))
			continue
		}
		for _, c := range cases {
			fo := FixtureOutcome{Name: c.Name, Dir: d.path, Hidden: d.hidden}
			res, err := importer.RunGoldenCase(context.Background(), format, c)
			if err != nil {
				fo.Error = err.Error()
			} else {
				fo.Detected = res.Detected
				fo.Expectation = res.Expected != nil
				fo.Diff = res.Diff
				fo.Passed = res.Passed()
				if res.Result != nil {
					fo.Accepted = res.Result.Accepted
					fo.Codes = diagnosticCodes(res.Result)
				}
			}
			out.Fixtures = append(out.Fixtures, fo)
			if d.hidden {
				out.HiddenTotal++
				if fo.Passed {
					out.HiddenPassed++
				}
			} else {
				out.PublicTotal++
				if fo.Passed {
					out.PublicPassed++
				}
			}
			for _, code := range fo.Codes {
				if code == importer.CodeAmbiguousFieldSemantics {
					out.AmbiguitySurfaced = true
				}
			}
		}
	}
	sort.SliceStable(out.Fixtures, func(i, j int) bool {
		if out.Fixtures[i].Hidden != out.Fixtures[j].Hidden {
			return !out.Fixtures[i].Hidden
		}
		return out.Fixtures[i].Name < out.Fixtures[j].Name
	})

	out.Implemented = !stubOnly(out.Fixtures)
	out.Results = append(out.Results,
		fixtureBlockResult("gotask.public", GoTaskPublicPoints, out.PublicPassed, out.PublicTotal,
			"public fixtures", out.Fixtures, false),
	)
	if includeHidden {
		out.Results = append(out.Results,
			fixtureBlockResult("gotask.hidden", GoTaskHiddenPoints, out.HiddenPassed, out.HiddenTotal,
				"hidden fixtures", out.Fixtures, true),
		)
	}
	out.Results = append(out.Results, ambiguityResult(out))
	return out
}

// stubOnly reports whether every fixture produced the stub's not_implemented
// diagnostic, which is how an unfinished task is told from a wrong one.
func stubOnly(fixtures []FixtureOutcome) bool {
	if len(fixtures) == 0 {
		return true
	}
	for _, f := range fixtures {
		stub := false
		for _, c := range f.Codes {
			if c == KredExpNotImplemented {
				stub = true
			}
		}
		if !stub {
			return false
		}
	}
	return true
}

// diagnosticCodes returns a result's diagnostic codes, sorted and deduped.
func diagnosticCodes(r *importer.Result) []string {
	if r == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, d := range r.Diagnostics {
		seen[d.Code] = true
	}
	return sortedKeys(seen)
}

// fixtureBlockResult scores one fixture block, awarding points proportionally to
// the fixtures that passed.
//
// Proportional and not all-or-nothing, because the fixtures test different
// things: a candidate who handles quoting, Swiss numbers and the trailer but
// misreads the century pivot has done most of the task, and a block that scored
// zero for that would tell them nothing about which part to fix.
func fixtureBlockResult(id string, points, passed, total int, label string,
	fixtures []FixtureOutcome, hidden bool) Result {
	a := Assertion{
		ID: id, Group: GroupGoTask, Points: points, Level: LevelScored,
		Kind:  "golden_fixtures",
		Title: fmt.Sprintf("kredexp-2.1 %s", label),
		Why:   "the projection compares documents, diagnostic tuples, totals and accepted",
	}
	res := Result{Assertion: a, Scenario: "gotask", Possible: points}
	if total == 0 {
		res.Status = StatusSkip
		res.Possible = 0
		res.Summary = fmt.Sprintf("no %s found", label)
		return res
	}
	res.Earned = points * passed / total
	if passed == total {
		res.Status = StatusPass
		res.Summary = fmt.Sprintf("all %d %s pass", total, label)
		return res
	}
	res.Status = StatusFail
	res.Summary = fmt.Sprintf("%d of %d %s pass (%d/%d points)", passed, total, label, res.Earned, points)
	for _, f := range fixtures {
		if f.Hidden != hidden || f.Passed {
			continue
		}
		actual := "the projection differs"
		switch {
		case f.Error != "":
			actual = f.Error
		case !f.Expectation:
			actual = "the fixture has no expectation file"
		}
		res.Findings = append(res.Findings, Finding{
			Subject:      f.Name,
			Expected:     "the projection matches " + f.Name + importer.GoldenExpectedSuffix,
			Actual:       actual,
			Hint:         firstDiffLine(f.Diff),
			EvidencePath: filepath.Join(f.Dir, f.Name+importer.GoldenExpectedSuffix),
		})
	}
	return res
}

// firstDiffLine returns the first line of a projection diff, which is the line a
// candidate should read first.
func firstDiffLine(diff string) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return ""
	}
	if i := strings.IndexByte(diff, '\n'); i > 0 {
		return diff[:i] + " (see grade golden-diff for the rest)"
	}
	return diff
}

// ambiguityResult scores whether the two undecidable fields were surfaced.
//
// It is the machine-checkable half of the write-up points. What it can see is
// whether the parse emitted ambiguous_field_semantics at all; what it cannot see
// is whether the candidate explained the choice, which is why the other half of
// this row lives in the human-entered decision-log score. Silence is what the
// specification punishes, not caution, so the check rewards the diagnostic and
// never asks how many of them there were.
func ambiguityResult(out *GoTaskOutcome) Result {
	a := Assertion{
		ID: "gotask.ambiguity", Group: GroupGoTask, Points: GoTaskAmbiguityPoints,
		Level: LevelScored, Kind: "ambiguity_surfaced",
		Title: "the undecidable fields are surfaced, not guessed",
		Why: "Skonto is an amount in the field table and a percentage in the glossary; " +
			"an empty position cost center has no defined inheritance. Both readings pay " +
			"a supplier a different sum, so the correct answer is a diagnostic",
	}
	res := Result{Assertion: a, Scenario: "gotask", Possible: GoTaskAmbiguityPoints}
	if !out.Implemented {
		// A failure and not a skip: a skip scores 0 of 0 and would take these
		// points out of the denominator, which is the same defect the fixture
		// rows above avoid by failing rather than skipping. The stub surfaces
		// nothing because it parses nothing, and that is the task not being
		// done, not a gap in this harness.
		res.Status = StatusFail
		res.Summary = "the importer is the stub, so no fixture can surface an ambiguity"
		res.Findings = append(res.Findings, Finding{
			Subject:      "ambiguous_field_semantics",
			Expected:     "at least one diagnostic across the fixtures",
			Actual:       "the importer is not implemented, so nothing was parsed",
			Hint:         "this row scores with the rest of the Go task; it is not a harness gap",
			EvidencePath: filepath.Join(PublicFixtureDir),
		})
		return res
	}
	if out.AmbiguitySurfaced {
		res.Status = StatusPass
		res.Earned = GoTaskAmbiguityPoints
		res.Summary = "ambiguous_field_semantics is emitted: the undecidable fields are surfaced"
		return res
	}
	res.Status = StatusFail
	res.Summary = "no fixture produced ambiguous_field_semantics: the undecidable fields were resolved silently"
	res.Findings = append(res.Findings, Finding{
		Subject:  "ambiguous_field_semantics",
		Expected: "at least one diagnostic across the fixtures",
		Actual:   "none",
		Hint: "section 9 of the customer document names two fields it does not decide; " +
			"carrying the value verbatim and saying so is correct, resolving it silently is not",
		EvidencePath: filepath.Join(PublicFixtureDir),
	})
	return res
}

// goTaskResult builds a one-off Go-task result.
func goTaskResult(id string, group Group, points int, status Status, summary string, findings ...Finding) Result {
	a := Assertion{ID: id, Group: group, Points: points, Level: LevelScored, Kind: "golden_fixtures"}
	res := Result{Assertion: a, Status: status, Summary: summary, Findings: findings, Scenario: "gotask"}
	if status == StatusFail {
		res.Possible = points
	} else if status == StatusPass {
		res.Earned, res.Possible = points, points
	}
	return res
}

// GoldenDiffReport is the readable diff of one fixture's projection.
type GoldenDiffReport struct {
	// Fixture is the fixture path.
	Fixture string
	// Detected reports whether Detect recognized the head.
	Detected bool
	// HaveExpectation reports whether an expectation file exists.
	HaveExpectation bool
	// Diff is the projection diff, "" when the fixture matches.
	Diff string
	// Projection is the produced projection, so a reviewer can pin it after
	// reading it. Never written automatically: a golden file that regenerates
	// itself on failure is not a golden file.
	Projection []byte
	// Codes are the diagnostic codes the parse produced.
	Codes []string
	// Accepted is the parse's verdict.
	Accepted bool
	// Totals renders the control totals the parse computed.
	Totals importer.Totals
}

// GoldenDiff runs one fixture through the candidate's importer and returns a
// readable diff of the documented projection.
//
// It exists because the golden test's failure output is a diff of two JSON blobs
// and the projection's own diff is a list of sentences about documents, fields and
// diagnostic tuples. A candidate debugging the century pivot should read "document
// 0000417/0004711 document_date: got 2070-03-29, want 1970-03-29", not a hunk.
func GoldenDiff(fixturePath string) (*GoldenDiffReport, error) {
	format, ok := importer.Lookup(KredExpID)
	if !ok {
		return nil, fmt.Errorf("grader: the %s format is not registered: add the blank import "+
			"of internal/importer/kredexp to internal/importer/all/all.go", KredExpID)
	}
	dir := filepath.Dir(fixturePath)
	base := strings.TrimSuffix(filepath.Base(fixturePath), filepath.Ext(fixturePath))
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		return nil, err
	}
	for _, c := range cases {
		if c.Name != base {
			continue
		}
		res, err := importer.RunGoldenCase(context.Background(), format, c)
		if err != nil {
			return nil, err
		}
		rep := &GoldenDiffReport{
			Fixture:         c.InputPath,
			Detected:        res.Detected,
			HaveExpectation: res.Expected != nil,
			Diff:            res.Diff,
			Projection:      res.Projection,
		}
		if res.Result != nil {
			rep.Codes = diagnosticCodes(res.Result)
			rep.Accepted = res.Result.Accepted
			rep.Totals = res.Result.Totals
		}
		return rep, nil
	}
	return nil, fmt.Errorf("grader: no fixture %q in %s", base, dir)
}
