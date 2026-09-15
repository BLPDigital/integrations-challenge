package grader

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WriteScoreJSON writes the machine-readable scorecard.
func WriteScoreJSON(path string, sc *Score) error {
	body, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("grader: %w", err)
	}
	return writeFile(path, append(body, '\n'))
}

// writeFile writes body to path, creating the directory.
func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("grader: %w", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("grader: %w", err)
	}
	return nil
}

// WriteScoreMarkdown writes score.md.
//
// The order is the whole design: harness events first so nobody reads one as a
// candidate's failure, then disqualifiers, then failures, then the score table,
// then everything that passed. A reviewer has forty-five minutes per submission
// and the first screen has to carry the things that change a decision.
func WriteScoreMarkdown(path string, sc *Score) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Score: %d / %d\n\n", sc.Total, sc.Budget)
	fmt.Fprintf(&b, "**Band:** %s — %s\n\n", sc.Band, sc.BandReason)
	fmt.Fprintf(&b, "**Onsite:** %s — %s\n\n", either(sc.Passed, "yes", "not yet"), sc.PassReason)
	if sc.Submission != "" {
		fmt.Fprintf(&b, "Submission: `%s`  \n", sc.Submission)
	}
	if sc.Connector != "" {
		fmt.Fprintf(&b, "Connector: `%s`\n", sc.Connector)
	}
	b.WriteString("\n")

	if len(sc.HarnessErrors) > 0 {
		b.WriteString("## Harness events (no points, read first)\n\n")
		b.WriteString("These are failures of the grading harness, not of the submission. " +
			"A run with harness events was not fully assessed.\n\n")
		for _, e := range sc.HarnessErrors {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		b.WriteString("\n")
	}

	if dq := sc.Disqualifiers(); len(dq) > 0 {
		b.WriteString("## Automatic disqualifiers\n\n")
		for _, f := range dq {
			fmt.Fprintf(&b, "- **%s** — %s\n", f.ID, f.Message)
			if f.Evidence != "" {
				fmt.Fprintf(&b, "  - evidence: `%s`\n", f.Evidence)
			}
		}
		b.WriteString("\n")
	}

	failures := sc.Failures()
	if len(failures) > 0 {
		b.WriteString("## Failures\n\n")
		for _, r := range failures {
			fmt.Fprintf(&b, "### %s / %s — %d/%d points\n\n", r.Scenario, r.Assertion.ID,
				r.Earned, r.Possible)
			if r.Assertion.Title != "" {
				fmt.Fprintf(&b, "%s\n\n", r.Assertion.Title)
			}
			fmt.Fprintf(&b, "%s\n\n", r.Summary)
			if r.Assertion.Why != "" {
				fmt.Fprintf(&b, "_Why this is graded:_ %s\n\n", r.Assertion.Why)
			}
			for _, f := range r.Findings {
				fmt.Fprintf(&b, "- `%s`\n", emptyDash(f.Subject))
				fmt.Fprintf(&b, "  - expected: %s\n", f.Expected)
				fmt.Fprintf(&b, "  - actual:   %s\n", f.Actual)
				if f.Hint != "" {
					fmt.Fprintf(&b, "  - %s\n", f.Hint)
				}
				if f.EvidencePath != "" {
					fmt.Fprintf(&b, "  - evidence: `%s`\n", f.EvidencePath)
				}
			}
			if r.Truncated > 0 {
				fmt.Fprintf(&b, "- ... and %d more findings; the full list is in the evidence file\n",
					r.Truncated)
			}
			b.WriteString("\n")
		}
	}

	var warnings, notes []Flag
	for _, f := range sc.Flags {
		switch f.Severity {
		case SeverityWarning:
			warnings = append(warnings, f)
		case SeverityNote:
			notes = append(notes, f)
		}
	}
	if len(warnings) > 0 {
		b.WriteString("## Warnings (no points, interview material)\n\n")
		for _, f := range warnings {
			fmt.Fprintf(&b, "- %s\n", f.Message)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Score by block\n\n")
	b.WriteString("| Block | Earned | Assessed | Budget | Note |\n|---|---:|---:|---:|---|\n")
	for _, bl := range sc.Blocks {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s |\n", bl.Label, bl.Earned, bl.Possible,
			bl.Budget, emptyDash(bl.Note))
	}
	fmt.Fprintf(&b, "| **Total** | **%d** | **%d** | **%d** | |\n\n", sc.Total, sc.Possible, sc.Budget)

	if sc.DecisionLog == nil {
		// The checklist itself, not a promise that it exists somewhere: three
		// documents said it was printed here and it was printed nowhere, so the
		// one human-entered number in the model was entered against a feeling.
		b.WriteString("### Decision log\n\nNot scored. It is the one human-entered number in " +
			"the model and the tool never guesses it, so it is scored against the checklist " +
			"below rather than against an impression.\n\n```\n")
		b.WriteString(strings.TrimRight(DecisionLogTemplate, "\n"))
		b.WriteString("\n```\n\n")
	} else {
		fmt.Fprintf(&b, "### Decision log\n\n%d/%d, entered by %s.\n\n", *sc.DecisionLog,
			PointsDecisionLog, emptyDash(sc.DecisionLogBy))
	}

	if sc.GoTask != nil {
		b.WriteString("## Go task fixtures\n\n")
		b.WriteString("| Fixture | Visibility | Result | Detect | Diagnostics |\n|---|---|---|---|---|\n")
		for _, f := range sc.GoTask.Fixtures {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", f.Name,
				either(f.Hidden, "hidden", "public"),
				either(f.Passed, "pass", "FAIL"),
				either(f.Detected, "yes", "no"),
				emptyDash(strings.Join(f.Codes, ", ")))
		}
		b.WriteString("\n")
	}

	b.WriteString("## All assertions\n\n")
	b.WriteString("| Scenario | Assertion | Status | Points | Summary |\n|---|---|---|---:|---|\n")
	for _, r := range sc.Results {
		// An inert run's passes are all worth nothing and all of them read as
		// achievements in a table somebody scans: eighteen greens for a
		// submission whose connector never started. The status carries the
		// qualifier so the row cannot be read that way.
		status := strings.ToUpper(string(r.Status))
		if r.Inert && r.Status == StatusPass {
			status += " (nothing ran)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d/%d | %s |\n", r.Scenario, r.Assertion.ID,
			status, r.Earned, r.Possible, oneLine(r.Summary))
	}
	b.WriteString("\n")

	if len(notes) > 0 {
		b.WriteString("## Notes\n\n")
		for _, f := range notes {
			fmt.Fprintf(&b, "- %s\n", f.Message)
		}
		b.WriteString("\n")
	}
	return writeFile(path, []byte(b.String()))
}

// oneLine collapses a summary to one table cell.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

// junitSuites is the root element of score.junit.xml.
type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Errors   int          `xml:"errors,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

// junitSuite is one scenario.
type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Errors   int         `xml:"errors,attr"`
	Cases    []junitCase `xml:"testcase"`
}

// junitCase is one assertion.
type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
	Error     *junitMessage `xml:"error,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

// junitMessage is a failure, skip or error body.
type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// WriteScoreJUnit writes score.junit.xml.
//
// A JUnit file exists so CI can show the per-assertion result without a custom
// plugin, and so an internal job can assert which assertion ids a run failed.
// The assertion id is the test name for that reason: it is the stable identifier
// everything downstream keys on.
func WriteScoreJUnit(path string, sc *Score) error {
	bySuite := map[string][]Result{}
	var order []string
	for _, r := range sc.Results {
		name := r.Scenario
		if name == "" {
			name = "grader"
		}
		if _, ok := bySuite[name]; !ok {
			order = append(order, name)
		}
		bySuite[name] = append(bySuite[name], r)
	}
	root := junitSuites{Name: "blp-integrations-challenge"}
	for _, name := range order {
		suite := junitSuite{Name: name}
		for _, r := range bySuite[name] {
			c := junitCase{Name: r.Assertion.ID, ClassName: name + "." + r.Assertion.Kind}
			c.SystemOut = r.Summary
			switch r.Status {
			case StatusFail:
				c.Failure = &junitMessage{Message: oneLine(r.Summary), Body: renderFindings(r)}
				suite.Failures++
			case StatusSkip:
				c.Skipped = &junitMessage{Message: oneLine(r.Summary)}
				suite.Skipped++
			case StatusError:
				c.Error = &junitMessage{Message: oneLine(r.Summary)}
				suite.Errors++
			}
			suite.Tests++
			suite.Cases = append(suite.Cases, c)
		}
		root.Tests += suite.Tests
		root.Failures += suite.Failures
		root.Skipped += suite.Skipped
		root.Errors += suite.Errors
		root.Suites = append(root.Suites, suite)
	}
	body, err := xml.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("grader: %w", err)
	}
	return writeFile(path, append([]byte(xml.Header), append(body, '\n')...))
}

// renderFindings renders a result's findings as plain text.
func renderFindings(r Result) string {
	var b strings.Builder
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%s\n  expected: %s\n  actual:   %s\n", emptyDash(f.Subject), f.Expected, f.Actual)
		if f.Hint != "" {
			fmt.Fprintf(&b, "  %s\n", f.Hint)
		}
		if f.EvidencePath != "" {
			fmt.Fprintf(&b, "  evidence: %s\n", f.EvidencePath)
		}
	}
	if r.Truncated > 0 {
		fmt.Fprintf(&b, "... and %d more findings\n", r.Truncated)
	}
	return b.String()
}

// PrintSelfcheck writes the per-assertion selfcheck transcript.
//
// This is the single highest-leverage thing in the artifact for a candidate's time
// budget, so the format is fixed and the rules are strict: one line per
// assertion, numbered n/total; a failure names the subject, the expected value,
// the actual value and the reason in that order; and a bare digest mismatch is
// never printed, because "state_digest differs" costs a candidate an hour and
// teaches them nothing.
func PrintSelfcheck(w io.Writer, runs []*RunOutcome) {
	total := 0
	for _, run := range runs {
		total += len(run.Results)
	}
	n := 0
	width := len(fmt.Sprint(total))
	for _, run := range runs {
		fmt.Fprintf(w, "\nscenario %s — %s (seed %d, chaos %s)\n",
			run.Scenario, run.Title, run.Seed, either(run.Chaos, "on", "off"))
		for _, e := range run.HarnessErrors {
			fmt.Fprintf(w, "  harness  %s\n", e)
		}
		for _, r := range run.Results {
			n++
			prefix := fmt.Sprintf("assertion %*d/%d", width, n, total)
			status := strings.ToUpper(string(r.Status))
			fmt.Fprintf(w, "%s %-5s %s\n", prefix, status, r.Summary)
			indent := strings.Repeat(" ", len(prefix)+7)
			for _, f := range r.Findings {
				fmt.Fprintf(w, "%s%s\n", indent, renderFindingLine(f))
			}
			if r.Truncated > 0 {
				fmt.Fprintf(w, "%s... and %d more; the full list is in %s\n", indent, r.Truncated,
					firstEvidence(r))
			}
			if r.Status == StatusFail && len(r.Findings) == 0 {
				// A failure with no finding would be exactly the bare mismatch
				// this format exists to prevent. It is a bug in the check, and
				// it is reported as one rather than printed as a verdict.
				fmt.Fprintf(w, "%s(this check reported no finding, which is a bug in the "+
					"grader: a failure must always name what differed)\n", indent)
			}
		}
	}
	fmt.Fprintf(w, "\n%s\n", selfcheckTally(runs))
}

// renderFindingLine renders one finding as the single line selfcheck prints.
func renderFindingLine(f Finding) string {
	var b strings.Builder
	if f.Subject != "" {
		b.WriteString(f.Subject)
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "expected %s, got %s", f.Expected, f.Actual)
	if f.Hint != "" {
		fmt.Fprintf(&b, " (%s)", f.Hint)
	}
	return b.String()
}

// firstEvidence returns a result's first evidence path, or a dash.
func firstEvidence(r Result) string {
	if p := evidenceOf(r); p != "" {
		return p
	}
	return "the run's evidence directory"
}

// selfcheckTally renders the closing summary line.
func selfcheckTally(runs []*RunOutcome) string {
	var pass, fail, skipped, errs, earned, possible int
	for _, run := range runs {
		for _, r := range run.Results {
			switch r.Status {
			case StatusPass:
				pass++
			case StatusFail:
				fail++
			case StatusSkip:
				skipped++
			case StatusError:
				errs++
			}
			earned += r.Earned
			possible += r.Possible
		}
	}
	out := fmt.Sprintf("%d passed, %d failed, %d skipped, %d harness errors; %d/%d points on the "+
		"assertions that ran", pass, fail, skipped, errs, earned, possible)
	if skipped > 0 {
		out += "\na skipped assertion scores zero out of zero: it never costs a point, and the " +
			"line above says why it was skipped"
	}
	return out
}

// PrintGoldenDiff writes a readable golden diff.
func PrintGoldenDiff(w io.Writer, rep *GoldenDiffReport) {
	fmt.Fprintf(w, "fixture:     %s\n", rep.Fixture)
	fmt.Fprintf(w, "detected:    %s\n", either(rep.Detected, "yes",
		"no — the twin falls back on the manifest's declared profile, so this is a finding, not a failure"))
	fmt.Fprintf(w, "accepted:    %t\n", rep.Accepted)
	fmt.Fprintf(w, "diagnostics: %s\n", emptyDash(strings.Join(rep.Codes, ", ")))
	fmt.Fprintf(w, "totals:      documents %d declared / %d computed, lines %d / %d\n",
		rep.Totals.DeclaredDocuments, rep.Totals.ComputedDocuments,
		rep.Totals.DeclaredLines, rep.Totals.ComputedLines)
	fmt.Fprintf(w, "             gross %s declared / %s computed\n",
		emptyDash(rep.Totals.DeclaredGross), emptyDash(rep.Totals.ComputedGross))
	if !rep.HaveExpectation {
		fmt.Fprintf(w, "\nno expectation file exists for this fixture.\n"+
			"the projection this parse produced is below; read it before pinning it, because\n"+
			"pinning an unread projection makes a bug into the answer key.\n\n%s\n",
			string(rep.Projection))
		return
	}
	if rep.Diff == "" {
		fmt.Fprintf(w, "\nthe projection matches the expectation.\n")
		return
	}
	fmt.Fprintf(w, "\nprojection differs from the expectation:\n\n%s\n", rep.Diff)
}
