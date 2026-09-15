package grader

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// A Level says what an assertion's outcome is allowed to do to a score.
type Level string

// The levels.
const (
	// LevelScored assertions contribute their points to the block they belong
	// to. Everything in BUILD-SPEC 13's table is scored.
	LevelScored Level = "scored"
	// LevelInfo assertions are reported and never scored: the efficiency ratio,
	// the 429 count, the run.json-versus-metrics cross-check of 17.6.6.
	LevelInfo Level = "info"
	// LevelHarness assertions describe the harness rather than the submission: a
	// safety timeout, an unpinned golden value, a service that would not start.
	// They carry zero points by construction and are printed at the top of
	// score.md so nobody reads a harness gap as a candidate's failure.
	LevelHarness Level = "harness"
)

// A Group is the scoring block an assertion belongs to. The groups are exactly
// the rows of BUILD-SPEC 13's scoring table plus the two ungraded ones.
type Group string

// The groups.
const (
	// GroupPublic is the public-scenario block, 30 points.
	GroupPublic Group = "public_scenarios"
	// GroupHidden is the hidden-scenario block, 20 points.
	GroupHidden Group = "hidden_scenarios"
	// GroupGoTask is the kredexp-2.1 block, 20 points.
	GroupGoTask Group = "go_task"
	// GroupRobustness is the robustness-invariant block, 15 points.
	GroupRobustness Group = "robustness"
	// GroupTraceability is the traceability block, 10 points.
	GroupTraceability Group = "traceability"
	// GroupDecisionLog is the decision-log block, 10 points, and the only block
	// a human enters. The tool never guesses it.
	GroupDecisionLog Group = "decision_log"
	// GroupStretch is recorded as a tie-break note and scores nothing.
	GroupStretch Group = "stretch"
	// GroupIntegrity carries the automatic disqualifier checks of BUILD-SPEC 13
	// and 17.5. They are pass/fail flags, not points.
	GroupIntegrity Group = "integrity"
)

// An Assertion is one graded question, as it appears in a scenario file.
type Assertion struct {
	// ID is unique within a scenario and stable across runs, because it is what
	// a debrief, a mutation-testing table and a candidate's second attempt all
	// refer to.
	ID string `json:"id"`
	// Group is the scoring block.
	Group Group `json:"group"`
	// Points is what a pass is worth. Zero for an info or harness assertion.
	Points int `json:"points"`
	// Level decides whether the outcome scores.
	Level Level `json:"level"`
	// Kind selects the check. See [Kinds] for the vocabulary.
	Kind string `json:"kind"`
	// Args are the check's parameters, decoded by the check itself.
	Args json.RawMessage `json:"args,omitempty"`
	// Title is the one-line human description printed by selfcheck. A missing
	// title falls back to the kind, but a written one is worth the two seconds:
	// it is the sentence a candidate reads when the assertion fails.
	Title string `json:"title,omitempty"`
	// Why explains what the assertion protects, for the scorecard. It is the
	// difference between "assertion 12 failed" and a debrief.
	Why string `json:"why,omitempty"`
}

// Validate checks the assertion's shape.
func (a *Assertion) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("assertion id must not be empty")
	}
	if a.Points < 0 {
		return fmt.Errorf("assertion %s: points must not be negative", a.ID)
	}
	switch a.Level {
	case LevelScored:
	case LevelInfo, LevelHarness:
		if a.Points != 0 {
			return fmt.Errorf("assertion %s: a %s assertion must carry 0 points, has %d",
				a.ID, a.Level, a.Points)
		}
	case "":
		return fmt.Errorf("assertion %s: level must be set", a.ID)
	default:
		return fmt.Errorf("assertion %s: unknown level %q", a.ID, a.Level)
	}
	switch a.Group {
	case GroupPublic, GroupHidden, GroupGoTask, GroupRobustness,
		GroupTraceability, GroupDecisionLog, GroupStretch, GroupIntegrity:
	default:
		return fmt.Errorf("assertion %s: unknown group %q", a.ID, a.Group)
	}
	if _, ok := lookupCheck(a.Kind); !ok {
		return fmt.Errorf("assertion %s: unknown kind %q; known kinds are %s",
			a.ID, a.Kind, strings.Join(Kinds(), ", "))
	}
	return nil
}

// A Status is the outcome of one assertion.
type Status string

// The statuses.
const (
	// StatusPass: the assertion held.
	StatusPass Status = "pass"
	// StatusFail: the assertion did not hold, and the findings say how.
	StatusFail Status = "fail"
	// StatusSkip: the assertion could not be evaluated for a documented reason
	// that is not the candidate's - an unpinned golden value, a hidden check
	// without the hidden build tag. It scores zero out of zero, so a skip can
	// never silently inflate a percentage.
	StatusSkip Status = "skip"
	// StatusError: the harness itself broke. Same scoring as a skip, but it is
	// a bug in this package and is reported as one.
	StatusError Status = "error"
)

// A Finding is one concrete difference, with the file that proves it.
type Finding struct {
	// Subject names what the finding is about: a proposal id, an invoice key, a
	// counter name. It is what a human scans the list by.
	Subject string `json:"subject,omitempty"`
	// Expected and Actual are rendered for humans, not for machines: "CHF
	// 108.23" beats a JSON blob in a terminal, and the machine-readable state is
	// in the evidence file.
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	// Hint is the one clause that turns a diff into a diagnosis, e.g. "rate
	// 1.082250, factor 1, half away from zero".
	Hint string `json:"hint,omitempty"`
	// EvidencePath is a real path in the run's output tree holding the state
	// this finding was read from.
	EvidencePath string `json:"evidence_path"`
}

// A Result is one evaluated assertion.
type Result struct {
	Assertion Assertion `json:"assertion"`
	Status    Status    `json:"status"`
	// Earned and Possible are the points. Possible is zero for a skip, an
	// error, an info and a harness assertion.
	Earned   int `json:"earned"`
	Possible int `json:"possible"`
	// Summary is the single line selfcheck prints. It must be readable on its
	// own: never a bare digest mismatch.
	Summary string `json:"summary"`
	// Findings are the concrete differences, most useful first, capped by
	// [MaxFindings].
	Findings []Finding `json:"findings,omitempty"`
	// Truncated counts findings beyond the cap.
	Truncated int `json:"truncated_findings,omitempty"`
	// Info carries numbers worth reporting whether or not the assertion passed,
	// e.g. the request budget ratio behind a binary in-budget check.
	Info map[string]string `json:"info,omitempty"`
	// Scenario names the scenario this result came from. The runner fills it.
	Scenario string `json:"scenario"`
	// Inert marks every result that came out of a run in which the connector
	// never ran. It is what keeps such a run from being accused: the
	// disqualifier flags in score.go are built from failed results, and half of
	// those flags are accusations - a leaked credential, an unproposed posting -
	// that an inert run fails trivially.
	Inert bool `json:"inert_run,omitempty"`
}

// MaxFindings caps a result's finding list. A failure with four thousand rows is
// not more informative than one with twenty, and it makes score.md unreadable.
const MaxFindings = 20

// A Checker evaluates one assertion kind against the observed state of a run.
//
// It returns a Result and an error. The error is reserved for a harness fault -
// unreadable evidence, a malformed args object - and never for a failed
// assertion, which is what StatusFail is for. That is the same distinction the
// importer framework draws between a Diagnostic and an error, and for the same
// reason: a defect in the thing under test must not be reported through the
// channel that reports defects in the test.
type Checker func(a Assertion, obs *Observed) (Result, error)

// checks is the assertion vocabulary. It is written once at init and read
// afterwards, never mutated during a run.
var checks = map[string]Checker{}

// register adds a check. A duplicate kind is a programming error and panics at
// init, which is the only safe moment for it to happen.
func register(kind string, c Checker) {
	if _, dup := checks[kind]; dup {
		panic("grader: duplicate assertion kind " + kind)
	}
	checks[kind] = c
}

// lookupCheck returns the check for a kind.
func lookupCheck(kind string) (Checker, bool) {
	c, ok := checks[kind]
	return c, ok
}

// Kinds returns every registered assertion kind in ascending order. Under the
// hidden build tag it is longer, which is the point: a leaked scenario file
// naming a hidden kind fails to load rather than revealing what it checked.
func Kinds() []string {
	out := make([]string, 0, len(checks))
	for k := range checks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Evaluate runs one assertion against the observed state.
func Evaluate(a Assertion, obs *Observed) Result {
	c, ok := lookupCheck(a.Kind)
	if !ok {
		return Result{Assertion: a, Status: StatusError, Scenario: obs.Scenario,
			Summary: fmt.Sprintf("unknown assertion kind %q", a.Kind)}
	}
	res, err := c(a, obs)
	res.Assertion = a
	res.Scenario = obs.Scenario
	if err != nil {
		res.Status = StatusError
		res.Earned, res.Possible = 0, 0
		res.Summary = "harness error: " + err.Error()
		return res
	}
	if obs.NoActivity() {
		res = guardVacuous(a, obs, res)
	}
	switch res.Status {
	case StatusPass:
		if a.Level == LevelScored {
			res.Earned, res.Possible = a.Points, a.Points
		} else {
			res.Earned, res.Possible = 0, 0
		}
	case StatusFail:
		res.Earned = 0
		if a.Level == LevelScored {
			res.Possible = a.Points
		} else {
			res.Possible = 0
		}
	default:
		res.Earned, res.Possible = 0, 0
	}
	if len(res.Findings) > MaxFindings {
		res.Truncated = len(res.Findings) - MaxFindings
		res.Findings = res.Findings[:MaxFindings]
	}
	if res.Summary == "" {
		res.Summary = a.Title
	}
	if res.Summary == "" {
		res.Summary = a.Kind
	}
	return res
}

// guardVacuous rewrites the outcome of an assertion evaluated against a run in
// which the connector never ran. See [Observed.NoActivity] for why such a run
// otherwise collects most of the invariant block for free.
//
// Two different rewrites, because the two kinds of assertion fail differently:
//
//   - A scored assertion that PASSED becomes a failure. The points stay in the
//     denominator, so the scorecard reads "0 of 30" rather than the "0 of 0" a
//     skip would produce, and an empty submission cannot reach a percentage.
//     A scored assertion that already failed is left alone: it failed for its
//     own reason, which is the more informative one.
//   - A scored assertion that SKIPPED becomes a failure too, keeping its own
//     sentence. A skip scores 0 of 0, and on an inert run the reason for the
//     skip is the candidate's: no batch reached the twin, so there was nothing
//     to replay. Leaving it a skip shrank the denominator to 93.
//   - An integrity assertion is skipped either way, whatever it returned. Those
//     are the automatic disqualifiers, and a disqualifier is an accusation: the
//     legacy-profile check is failed by a run that delivered nothing, and
//     printing that as "the graded profile was bypassed" would accuse a
//     candidate who submitted nothing of gaming the exercise.
//
// Info and harness assertions are untouched. They carry no points, and what they
// report about an inert run is accurate on its own terms.
func guardVacuous(a Assertion, obs *Observed, res Result) Result {
	finding := Finding{
		Subject:      "connector activity",
		Expected:     "at least one request, batch or document from the connector",
		Actual:       "the connector made no request to either server and produced nothing",
		Hint:         "the assertion holds only because the run never happened",
		EvidencePath: obs.Ev("erp-metrics.json"),
	}
	// Every result of such a run is marked, whatever it returned, because what
	// suppresses the accusations downstream is the mark and not the status: an
	// assertion that failed on its own here failed for want of a run, and no
	// disqualifier may be built from it either.
	res.Inert = true
	switch {
	case a.Group == GroupIntegrity:
		if res.Status == StatusPass || res.Status == StatusFail {
			res.Status = StatusSkip
			res.Summary = "not assessed: the connector produced no activity in this scenario"
			res.Findings = nil
		}
	case a.Level == LevelScored && res.Status == StatusPass:
		res.Status = StatusFail
		res.Summary = "unearned: the connector produced no activity, so this held vacuously"
		res.Findings = append([]Finding{finding}, res.Findings...)
	case a.Level == LevelScored && res.Status == StatusSkip:
		// A skip scores zero OF ZERO, so it takes its points out of the
		// denominator: three scored assertions skip on an inert run - no batch
		// reached the twin, no SOAP response was truncated, the twin has no run
		// report - and an empty submission read 0 of 93 rather than 0 of 100.
		// Seven missing points look like our gap rather than the candidate's.
		//
		// On a run that never happened the skip's own reason IS the candidate's
		// failure, so the sentence is kept and only the status changes. That is
		// safe precisely because it is gated on NoActivity: a skip that really
		// is a harness gap - an unpinned golden digest, a kind struck from the
		// vocabulary - still skips on every run that actually happened.
		res.Status = StatusFail
		res.Findings = append([]Finding{finding}, res.Findings...)
	}
	return res
}

// pass builds a passing result with a readable summary.
func pass(format string, args ...any) (Result, error) {
	return Result{Status: StatusPass, Summary: fmt.Sprintf(format, args...)}, nil
}

// fail builds a failing result with a readable summary.
func fail(format string, args ...any) (Result, error) {
	return Result{Status: StatusFail, Summary: fmt.Sprintf(format, args...)}, nil
}

// skip builds a skipped result, which scores zero out of zero.
func skip(format string, args ...any) (Result, error) {
	return Result{Status: StatusSkip, Summary: fmt.Sprintf(format, args...)}, nil
}

// decodeArgs decodes an assertion's args into v. An absent args object leaves v
// at its zero value, so a check whose arguments are all optional needs no args
// object in the scenario file.
func decodeArgs(a Assertion, v any) error {
	if len(a.Args) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(a.Args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("assertion %s: args: %w", a.ID, err)
	}
	return nil
}
