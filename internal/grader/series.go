package grader

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SeriesOptions configures a run of several scenarios.
type SeriesOptions struct {
	// RepoRoot is the module root.
	RepoRoot string
	// ConnectorCmd is the connector command line, empty for the contract's
	// connector/run.sh.
	ConnectorCmd string
	// Seed overrides every scenario's own seed when non-nil.
	Seed *int64
	// NoChaos disables fault injection.
	NoChaos bool
	// OutDir is the root of the output tree.
	OutDir string
	// HarnessRunID names the output tree; empty derives one per scenario.
	HarnessRunID string
	// Stdout receives progress lines.
	Stdout io.Writer
	// Only restricts the series to these scenario ids.
	Only []string
	// IncludeHidden adds the scenarios in grading/hidden/scenarios.
	IncludeHidden bool
	// IncludeStretch adds the stretch scenarios, which score nothing.
	IncludeStretch bool
	// Setup runs connector/setup.sh once before the series when it exists.
	// BUILD-SPEC 17.4: setup runs once with network access, run.sh runs per
	// scenario without it.
	Setup bool
}

// SetupScript is the candidate-owned build step of BUILD-SPEC 17.4.
const SetupScript = "connector/setup.sh"

// RunSeries runs several scenarios sequentially and returns their outcomes.
//
// Sequentially and never in parallel. Two scenarios in parallel would share the
// machine's ports, the go build cache and the reviewer's attention, and the
// virtual clock means there is nothing to gain: a scenario takes as long as it
// takes regardless of what else is running, so parallelism would buy wall time at
// the cost of a class of flake that is very hard to tell from a candidate's bug.
func RunSeries(opts SeriesOptions) ([]*RunOutcome, error) {
	scenarios, err := collectScenarios(opts)
	if err != nil {
		return nil, err
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("grader: no scenarios to run")
	}
	out := opts.Stdout
	if out == nil {
		out = io.Discard
	}
	if opts.Setup {
		if err := runSetup(opts.RepoRoot, out); err != nil {
			return nil, err
		}
	}
	var outcomes []*RunOutcome
	for _, sc := range scenarios {
		fmt.Fprintf(out, "\n=== %s: %s\n", sc.ID, sc.Title)
		res, err := Run(Options{
			RepoRoot:     opts.RepoRoot,
			ScenarioPath: sc.Path,
			Seed:         opts.Seed,
			ConnectorCmd: opts.ConnectorCmd,
			NoChaos:      opts.NoChaos,
			OutDir:       opts.OutDir,
			HarnessRunID: opts.HarnessRunID,
			Stdout:       out,
		})
		if err != nil {
			// A scenario that could not be attempted is recorded as an outcome
			// carrying the reason, so the series continues and the scorecard
			// shows a harness gap rather than silently omitting a block.
			outcomes = append(outcomes, &RunOutcome{
				Scenario: sc.ID, Visibility: sc.Visibility, Title: sc.Title,
				HarnessErrors: []string{err.Error()},
			})
			fmt.Fprintf(out, "harness: %s could not run: %v\n", sc.ID, err)
			continue
		}
		outcomes = append(outcomes, res)
	}
	return outcomes, nil
}

// collectScenarios loads the scenarios the options select, in id order.
func collectScenarios(opts SeriesOptions) ([]*Scenario, error) {
	public, err := LoadScenarioDir(filepath.Join(opts.RepoRoot, ScenarioDir))
	if err != nil {
		return nil, err
	}
	all := public
	if opts.IncludeHidden {
		hidden, err := LoadScenarioDir(filepath.Join(opts.RepoRoot, HiddenScenarioDir))
		if err != nil {
			return nil, err
		}
		all = append(all, hidden...)
	}
	only := stringSet(opts.Only)
	var out []*Scenario
	for _, sc := range all {
		if len(only) > 0 {
			if only[sc.ID] {
				out = append(out, sc)
			}
			continue
		}
		if sc.Visibility == VisibilityStretch && !opts.IncludeStretch {
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// runSetup runs connector/setup.sh once, if it exists.
//
// It is the language-fairness step: a candidate whose connector needs a build,
// a virtualenv or a Docker image gets one invocation with network access, and
// every scenario invocation afterwards runs without it. A missing setup script is
// not an error, because a single-file Python connector needs no build.
func runSetup(repoRoot string, out io.Writer) error {
	path := filepath.Join(repoRoot, SetupScript)
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(out, "no %s; nothing to set up\n", SetupScript)
		return nil
	}
	fmt.Fprintf(out, "running %s once, with network access\n", SetupScript)
	svc, err := runOnce(path, repoRoot)
	if err != nil {
		return fmt.Errorf("grader: %s failed: %w\n%s", SetupScript, err, svc)
	}
	return nil
}

// Selfcheck runs every public scenario and prints the per-assertion transcript.
//
// It is the fairness feature of the whole artifact: a candidate runs it, and
// every assertion they will be graded on prints its own verdict with a
// human-readable diff. Nothing here is hidden from them, and nothing they see
// here differs from what the scored run sees, because it is the same code path
// with the same scenario files.
func Selfcheck(opts SeriesOptions, w io.Writer) ([]*RunOutcome, error) {
	runs, err := RunSeries(selfcheckOptions(opts))
	if err != nil {
		return nil, err
	}
	PrintSelfcheck(w, runs)
	return runs, nil
}

// selfcheckOptions is what selfcheck changes about a caller's series options.
//
// Exactly one thing: hidden off, because selfcheck is the candidate's own command
// and the hidden scenarios are not theirs to see. The stretch scenarios stay the
// CALLER's choice: they are published, they score nothing, and they are exactly
// the kind of thing somebody wants to try on purpose. Forcing that off here too
// made `grade selfcheck --stretch` a flag that parsed and did nothing.
func selfcheckOptions(opts SeriesOptions) SeriesOptions {
	opts.IncludeHidden = false
	return opts
}

// ScoreOptions configures a full submission score.
type ScoreOptions struct {
	SeriesOptions
	// Submission is the submission directory, for the report header. Empty uses
	// the repository root.
	Submission string
	// ReportDir is where score.json, score.md, score.junit.xml and report.html
	// go. Empty puts them under the output tree.
	ReportDir string
	// DecisionLog is the human-entered decision-log score, nil when absent.
	DecisionLog *int
	// DecisionLogBy records who entered it.
	DecisionLogBy string
}

// ScoreSubmission runs the public and hidden scenarios plus the Go-task fixtures
// and writes the four report artifacts.
//
// The stretch scenarios are the caller's choice and default to off, because they
// score nothing and X2 alone costs minutes. With them on, each one becomes a line
// in the scorecard's tie-break section, which is what they are for: two
// submissions that score the same, separated by which one survives a SIGKILL and
// three times the data.
func ScoreSubmission(opts ScoreOptions, w io.Writer) (*Score, error) {
	series := opts.SeriesOptions
	series.IncludeHidden = true

	// A submission is graded in a tree WE assemble: our files, with only the
	// candidate-writable paths taken from theirs. Until this existed,
	// --submission was a string in the report header and the run used --repo, so
	// the documented invocation scored the reviewer's own checkout, and a
	// submission that edited the twin graded itself.
	integrity, gradedTree, err := prepareSubmission(&series, opts)
	if err != nil {
		return nil, err
	}
	if gradedTree != "" {
		fmt.Fprintf(w, "grading the submission in %s\n", gradedTree)
		fmt.Fprintf(w, "protected tree: %s\n", integrity.Summary())
	}
	hiddenAvailable, err := hiddenScenariosPresent(series.RepoRoot)
	if err != nil {
		return nil, err
	}
	runs, err := RunSeries(series)
	if err != nil {
		return nil, err
	}
	goTask := GradeGoTask(series.RepoRoot, hiddenFixturesPresent(series.RepoRoot))
	submission := opts.Submission
	if submission == "" {
		submission = series.RepoRoot
	}
	connector := series.ConnectorCmd
	if connector == "" {
		connector = DefaultConnector
	}
	sc := Compute(ScoreInput{
		Submission:    submission,
		Connector:     connector,
		Runs:          runs,
		GoTask:        goTask,
		DecisionLog:   opts.DecisionLog,
		DecisionLogBy: opts.DecisionLogBy,
		HiddenGraded:  hiddenAvailable && HiddenKindsBuilt,
		Integrity:     integrity,
	})

	dir := opts.ReportDir
	if dir == "" {
		dir = filepath.Join(series.RepoRoot, "grading", "out", "score")
	}
	for _, step := range []struct {
		name string
		fn   func(string, *Score) error
	}{
		{"score.json", WriteScoreJSON},
		{"score.md", WriteScoreMarkdown},
		{"score.junit.xml", WriteScoreJUnit},
		{"report.html", WriteReportHTML},
	} {
		if err := step.fn(filepath.Join(dir, step.name), sc); err != nil {
			return sc, err
		}
		fmt.Fprintf(w, "wrote %s\n", filepath.Join(dir, step.name))
	}
	return sc, nil
}

// prepareSubmission points the series at the tree the submission has to be graded
// in, and checks the submission's protected files against our manifest.
//
// It returns the integrity report, the directory the run will use (empty when
// there is nothing to overlay), and an error only when the harness itself could
// not do its job.
//
// A submission that is the repository root is our own tree, which is how the
// reference connector and the mutation gate run: nothing is copied and nothing is
// checked, because there is no second party.
func prepareSubmission(series *SeriesOptions, opts ScoreOptions) (*IntegrityReport, string, error) {
	sub := strings.TrimSpace(opts.Submission)
	if sub == "" {
		return nil, "", nil
	}
	subAbs, err := filepath.Abs(sub)
	if err != nil {
		return nil, "", err
	}
	rootAbs, err := filepath.Abs(series.RepoRoot)
	if err != nil {
		return nil, "", err
	}
	if subAbs == rootAbs {
		return nil, "", nil
	}
	if fi, err := os.Stat(subAbs); err != nil || !fi.IsDir() {
		return nil, "", fmt.Errorf("--submission %s is not a directory", sub)
	}

	report := CheckProtectedTree(rootAbs, subAbs)
	dest := opts.ReportDir
	if dest == "" {
		dest = filepath.Join(rootAbs, "grading", "out", "score")
	}
	// Absolute, always. The graded tree becomes the run's RepoRoot, the connector
	// command is derived from it, and the harness spawns that command with its own
	// working directory: a relative --report-dir therefore produced a relative
	// connector path and every scenario failed to start with "no such file or
	// directory" against a file that was plainly there.
	dest, err = filepath.Abs(dest)
	if err != nil {
		return nil, "", err
	}
	graded := filepath.Join(dest, "graded-tree")
	if err := os.RemoveAll(graded); err != nil {
		return nil, "", err
	}
	if err := BuildGradedTree(rootAbs, subAbs, graded); err != nil {
		return nil, "", err
	}
	series.RepoRoot = graded
	return &report, graded, nil
}

// hiddenScenariosPresent reports whether the hidden scenario directory holds any
// scenario. A stripped bundle has none, and that is a valid bundle: the hidden
// block is then reported as unscored rather than as lost.
func hiddenScenariosPresent(repoRoot string) (bool, error) {
	scs, err := LoadScenarioDir(filepath.Join(repoRoot, HiddenScenarioDir))
	if err != nil {
		return false, err
	}
	return len(scs) > 0, nil
}

// hiddenFixturesPresent reports whether the hidden Go-task fixtures are there.
func hiddenFixturesPresent(repoRoot string) bool {
	entries, err := os.ReadDir(filepath.Join(repoRoot, HiddenFixtureDir))
	return err == nil && len(entries) > 0
}
