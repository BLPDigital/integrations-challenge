// Command grade is the grading harness.
//
// It starts both services on ephemeral ports, seeds a scenario, invokes the
// candidate's connector through the published CLI contract, drives the twin's
// scanner deterministically, and asserts outcome state only: the state digest,
// the exception set, the ERP document set, the servers' own counters and the
// three report files. It never reads the candidate's source, never greps for a
// library, and never cares which language, channel or wire format they chose.
//
// Four subcommands:
//
//	grade run       --scenario S1 [--seed N] [--connector CMD] [--no-chaos] [--out DIR]
//	grade selfcheck [--connector CMD]
//	grade score     --submission DIR [--decision-log N]
//	grade golden-diff --fixture testdata/kredexp/03_quoted_fields_ok.txt
//
// run and selfcheck are what a candidate uses. score and golden-diff are ours,
// and score additionally runs the hidden scenarios and the hidden fixtures when
// they are present, which they are not in a candidate bundle.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/grader"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "grade: %v\n", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `usage:
  grade run         --scenario ID [--seed N] [--connector CMD] [--no-chaos] [--out DIR]
  grade selfcheck   [--connector CMD] [--seed N] [--no-chaos] [--out DIR]
  grade score       [--submission DIR] [--connector CMD] [--decision-log N] [--report-dir DIR]
                    [--stretch]
  grade golden-diff --fixture PATH
  grade scenarios   list the scenarios that are present
`)
	for _, line := range internalUsage() {
		fmt.Fprintln(w, "  "+line)
	}
	fmt.Fprint(w, `

Every subcommand resolves paths against the repository root, which defaults to
the working directory and is overridable with --repo.
`)
}

func run(args []string, stdout, stderr *os.File) error {
	if len(args) == 0 {
		usage(stderr)
		return errors.New("missing subcommand")
	}
	sub, rest := args[0], args[1:]

	// A subcommand that only exists in an internally built grader. The map is
	// empty without -tags hidden, so the shipped binary answers "unknown
	// subcommand" and the shipped source does not name what it would have run.
	if fn, ok := internalSubcommands[sub]; ok {
		return fn(rest, stdout, stderr)
	}
	switch sub {
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "selfcheck":
		return cmdSelfcheck(rest, stdout, stderr)
	case "score":
		return cmdScore(rest, stdout, stderr)
	case "golden-diff":
		return cmdGoldenDiff(rest, stdout, stderr)
	case "scenarios":
		return cmdScenarios(rest, stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}

// commonFlags are the flags every scenario-running subcommand shares.
type commonFlags struct {
	repo      string
	connector string
	seed      int64
	seedSet   bool
	noChaos   bool
	out       string
	setup     bool
}

func (c *commonFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.repo, "repo", ".", "repository root")
	fs.StringVar(&c.connector, "connector", "",
		"connector command line; empty uses "+grader.DefaultConnector)
	fs.Int64Var(&c.seed, "seed", 0, "override every scenario's seed")
	fs.BoolVar(&c.noChaos, "no-chaos", false, "disable fault injection on both services")
	fs.StringVar(&c.out, "out", "", "output tree root; empty uses grading/out")
	fs.BoolVar(&c.setup, "setup", true, "run connector/setup.sh once before the series when it exists")
}

func (c *commonFlags) series(stdout *os.File) (grader.SeriesOptions, error) {
	root, err := filepath.Abs(c.repo)
	if err != nil {
		return grader.SeriesOptions{}, err
	}
	opts := grader.SeriesOptions{
		RepoRoot:     root,
		ConnectorCmd: c.connector,
		NoChaos:      c.noChaos,
		OutDir:       c.out,
		Stdout:       stdout,
		Setup:        c.setup,
	}
	if c.seedSet {
		seed := c.seed
		opts.Seed = &seed
	}
	return opts, nil
}

// seedWasSet records whether --seed appeared, because zero is a legal seed and
// therefore cannot mean "unset".
func markSeedSet(fs *flag.FlagSet, c *commonFlags) {
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "seed" {
			c.seedSet = true
		}
	})
}

func cmdRun(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c commonFlags
	c.bind(fs)
	scenario := fs.String("scenario", "", "scenario id, for example S0, S1, S2")
	scenarioPath := fs.String("scenario-file", "", "explicit scenario file, overriding --scenario")
	keepGoing := fs.Bool("keep-going", false, "continue the scenario after a step fails")
	if err := fs.Parse(args); err != nil {
		return err
	}
	markSeedSet(fs, &c)
	if strings.TrimSpace(*scenario) == "" && strings.TrimSpace(*scenarioPath) == "" {
		return errors.New("--scenario is required")
	}
	series, err := c.series(stdout)
	if err != nil {
		return err
	}
	opts := grader.Options{
		RepoRoot:     series.RepoRoot,
		ScenarioID:   *scenario,
		ScenarioPath: *scenarioPath,
		Seed:         series.Seed,
		ConnectorCmd: series.ConnectorCmd,
		NoChaos:      series.NoChaos,
		OutDir:       series.OutDir,
		Stdout:       stdout,
		KeepGoing:    *keepGoing,
	}
	grader.NoteIfConnectorIsStub(series.ConnectorCmd, series.RepoRoot, stdout)
	outcome, err := grader.Run(opts)
	if err != nil {
		return err
	}
	grader.PrintSelfcheck(stdout, []*grader.RunOutcome{outcome})
	if outcome.Failed() {
		return fmt.Errorf("scenario %s failed", outcome.Scenario)
	}
	return nil
}

func cmdSelfcheck(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade selfcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c commonFlags
	c.bind(fs)
	only := fs.String("only", "", "comma separated scenario ids to restrict the series to")
	stretch := fs.Bool("stretch", false, "include the stretch scenarios, which score nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	markSeedSet(fs, &c)
	series, err := c.series(stdout)
	if err != nil {
		return err
	}
	series.IncludeStretch = *stretch
	if s := strings.TrimSpace(*only); s != "" {
		series.Only = strings.Split(s, ",")
	}
	grader.NoteIfConnectorIsStub(series.ConnectorCmd, series.RepoRoot, stdout)
	runs, err := grader.Selfcheck(series, stdout)
	if err != nil {
		return err
	}
	failed := 0
	for _, r := range runs {
		if r.Failed() {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d scenarios failed", failed, len(runs))
	}
	return nil
}

func cmdScore(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c commonFlags
	c.bind(fs)
	submission := fs.String("submission", "", "submission directory; empty uses the repository root")
	reportDir := fs.String("report-dir", "", "where score.json, score.md, score.junit.xml and report.html go")
	decisionLog := fs.Int("decision-log", -1,
		"the human-entered decision-log score out of 10; omitted leaves it unscored")
	// The scorecard, the HTML report and score.json all instruct
	// --decision-log-score, so both spellings work. A reviewer typing what our own
	// output told them to type must not land in a flag-parse error.
	decisionLogScore := fs.Int("decision-log-score", -1, "alias of --decision-log")
	decisionLogBy := fs.String("decision-log-by", "", "who entered the decision-log score")
	stretch := fs.Bool("stretch", false,
		"also run the stretch scenarios, which score nothing and are recorded as a tie-break note")
	if err := fs.Parse(args); err != nil {
		return err
	}
	markSeedSet(fs, &c)
	series, err := c.series(stdout)
	if err != nil {
		return err
	}
	series.IncludeStretch = *stretch
	opts := grader.ScoreOptions{
		SeriesOptions: series,
		Submission:    *submission,
		ReportDir:     *reportDir,
		DecisionLogBy: *decisionLogBy,
	}
	entered := *decisionLog
	if entered < 0 {
		entered = *decisionLogScore
	}
	if entered >= 0 {
		v := entered
		opts.DecisionLog = &v
	}
	if _, err := grader.ScoreSubmission(opts, stdout); err != nil {
		return err
	}
	return nil
}

func cmdGoldenDiff(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade golden-diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fixture := fs.String("fixture", "", "fixture .txt whose projection to diff against its .expected.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*fixture) == "" {
		return errors.New("--fixture is required")
	}
	rep, err := grader.GoldenDiff(*fixture)
	if err != nil {
		return err
	}
	grader.PrintGoldenDiff(stdout, rep)
	if rep.Diff != "" {
		return fmt.Errorf("%s does not match its expectation", *fixture)
	}
	return nil
}

func cmdScenarios(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("grade scenarios", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "repository root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	allocated := map[grader.Group]int{}
	hiddenAbsent := false
	for _, dir := range []string{grader.ScenarioDir, grader.HiddenScenarioDir} {
		path := filepath.Join(root, dir)
		list, err := grader.LoadScenarioDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(stdout, "%s: absent\n", dir)
				if dir == grader.HiddenScenarioDir {
					hiddenAbsent = true
				}
				continue
			}
			return err
		}
		if len(list) == 0 {
			// A stripped tree is the normal case for the candidate bundle: the
			// loader reports an empty directory rather than an error, and
			// "absent" is the honest word for it.
			fmt.Fprintf(stdout, "%s: absent\n", dir)
			if dir == grader.HiddenScenarioDir {
				hiddenAbsent = true
			}
			continue
		}
		fmt.Fprintf(stdout, "%s:\n", dir)
		for _, sc := range list {
			fmt.Fprintf(stdout, "  %-6s %-10s %s\n", sc.ID, sc.Visibility, sc.Title)
			for _, st := range sc.Steps {
				if st.Assert != nil {
					allocated[st.Assert.Group] += st.Assert.Points
				}
			}
			for _, line := range scoredLines(sc) {
				fmt.Fprintf(stdout, "         %s\n", line)
			}
		}
	}
	// The published scoring table is only trustworthy if it can be checked, so
	// print what the files actually allocate rather than what a document claims.
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "point budget per block, from the scenario files above:")
	for _, g := range []grader.Group{grader.GroupPublic, grader.GroupHidden,
		grader.GroupRobustness, grader.GroupTraceability} {
		if g == grader.GroupHidden && hiddenAbsent {
			// Absent is not zero, and printing 0 here would read like a bug in
			// the harness rather than a directory that was stripped on purpose.
			fmt.Fprintf(stdout, "  %-22s %3s allocated  %3d in the specification (stripped from this tree)\n",
				g, "-", grader.BudgetOf(g))
			continue
		}
		fmt.Fprintf(stdout, "  %-22s %3d allocated  %3d in the specification\n",
			g, allocated[g], grader.BudgetOf(g))
	}
	fmt.Fprintf(stdout, "  %-22s %3s allocated  %3d in the specification (a human enters it)\n",
		grader.GroupDecisionLog, "-", grader.BudgetOf(grader.GroupDecisionLog))
	fmt.Fprintf(stdout, "  %-22s %3s allocated  %3d in the specification (graded from the fixtures)\n",
		grader.GroupGoTask, "-", grader.BudgetOf(grader.GroupGoTask))
	if hiddenAbsent {
		fmt.Fprintln(stdout, "  the hidden block is seven scenarios summing to twenty points. They re-combine")
		fmt.Fprintln(stdout, "  mechanisms these documents publish and introduce none of their own.")
	}
	return nil
}

// scoredLines renders one line per scored assertion of a scenario: the id, the
// points and the assertion kind. A candidate can add these up and arrive at the
// published table, which is the point.
func scoredLines(sc *grader.Scenario) []string {
	var out []string
	for _, st := range sc.Steps {
		if st.Assert == nil || st.Assert.Points == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("%2d  %-34s %s",
			st.Assert.Points, st.Assert.ID, st.Assert.Kind))
	}
	return out
}
