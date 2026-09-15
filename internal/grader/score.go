package grader

import (
	"fmt"
	"sort"
	"strings"
)

// The point budget of BUILD-SPEC 13, one constant per row of its scoring table.
//
// # The specification contradicts itself here, and this is the resolution
//
// BUILD-SPEC 13 opens with "Scoring, 100 points" and then gives a table whose
// rows are 30, 20, 20, 15, 10 and 10. Those sum to 105, not 100. Each row's own
// detail column is internally consistent with its row total - the public row's
// 12 + 8 + 6 + 4 is 30, the Go-task row's 8 + 8 + 4 is 20, the robustness row's
// 6 + 4 + 3 + 2 is 15 - so the detailed numbers are the reliable half of the
// contradiction and the headline "100" is the half that is wrong.
//
// This was reported to the owner of the specification and RESOLVED there rather
// than absorbed here: the traceability block drops from 10 points to 5, so the
// rows sum to the 100 the prose promises and the published seniority bands and
// pass threshold keep the meaning they were written with. Traceability was the
// row that gave way because its detail is a single binary property (a sampled
// invoice resolves in both directions, or it does not), while every other block
// carries several checks that fail independently: five points per binary
// property was the outlier.
//
// The "spec.total_mismatch" note flag stays implemented. It costs nothing while
// the rows agree with the prose, and it is the check that caught this.
//
// [Compute] additionally caps each block at its budget and says so in the
// block's note, so a scenario file that awards eleven points in a ten-point block
// is caught by the tool rather than by a reviewer wondering why a submission
// scored 103.
const (
	// PointsPublic is the public-scenario block.
	PointsPublic = 30
	// PointsHidden is the hidden-scenario block.
	PointsHidden = 20
	// PointsGoTask is the kredexp-2.1 block.
	PointsGoTask = 20
	// PointsRobustness is the robustness-invariant block.
	PointsRobustness = 15
	// PointsTraceability is the traceability block. Five, not ten: see the
	// resolution in the block comment above.
	PointsTraceability = 5
	// PointsDecisionLog is the decision-log block, and it is the only block a
	// human enters. The tool never guesses it: a number a machine invented for
	// a judgment question is worse than no number, because it looks like
	// evidence.
	PointsDecisionLog = 10
	// PointsTotal is the whole scale, computed from the rows rather than
	// asserted, so it can never drift away from them.
	PointsTotal = PointsPublic + PointsHidden + PointsGoTask + PointsRobustness +
		PointsTraceability + PointsDecisionLog
	// PointsHeadline is the total BUILD-SPEC 13's prose claims. It differs from
	// PointsTotal, and the difference is reported rather than resolved. See the
	// block comment above.
	PointsHeadline = 100
)

// PassThreshold is the onsite threshold of BUILD-SPEC 13.
const PassThreshold = 50

// A Block is one row of the scoring table.
type Block struct {
	// Group is the block's group.
	Group Group `json:"group"`
	// Label is the human name.
	Label string `json:"label"`
	// Earned is what the submission scored, Possible what the assertions that
	// ran could award, and Budget what the specification allots the block.
	//
	// The three differ on purpose. Possible below Budget means assertions were
	// skipped - an unpinned golden digest, a hidden block not run - and the
	// scorecard says so instead of quietly turning a skip into a loss.
	Earned   int `json:"earned"`
	Possible int `json:"possible"`
	Budget   int `json:"budget"`
	// HumanEntered marks the decision-log block.
	HumanEntered bool `json:"human_entered"`
	// Note explains a gap between Possible and Budget.
	Note string `json:"note,omitempty"`
}

// A Flag is something a reviewer must read before the interview. A flag is never
// a score: it is a fact with a citation.
type Flag struct {
	// ID is stable, so a flag can be referred to in a debrief.
	ID string `json:"id"`
	// Severity is "disqualifier", "warning" or "note".
	Severity string `json:"severity"`
	// Message says what was observed.
	Message string `json:"message"`
	// Evidence is the file that proves it.
	Evidence string `json:"evidence,omitempty"`
}

// The flag severities.
const (
	// SeverityDisqualifier is an automatic disqualifier of BUILD-SPEC 13 or 17.5.
	SeverityDisqualifier = "disqualifier"
	// SeverityWarning is a finding worth an interview question.
	SeverityWarning = "warning"
	// SeverityNote is a fact worth knowing, e.g. a stretch scenario's outcome.
	SeverityNote = "note"
)

// A Score is the whole scorecard of one submission.
type Score struct {
	// Submission is the directory that was graded.
	Submission string `json:"submission"`
	// Connector is the connector command that was invoked.
	Connector string `json:"connector"`
	// Blocks are the scoring blocks in the specification's order.
	Blocks []Block `json:"blocks"`
	// Total, Possible and Budget are the sums.
	Total    int `json:"total"`
	Possible int `json:"possible"`
	Budget   int `json:"budget"`
	// DecisionLog is the human-entered decision-log score, nil when nobody
	// entered one. A nil is reported as "not scored yet" and never as a zero:
	// zero is a judgment, and the tool has none.
	DecisionLog *int `json:"decision_log_score"`
	// DecisionLogBy records who entered it.
	DecisionLogBy string `json:"decision_log_by,omitempty"`
	// Flags are the disqualifiers, warnings and notes, disqualifiers first.
	Flags []Flag `json:"flags"`
	// Band is the seniority band of BUILD-SPEC 13.
	Band string `json:"band"`
	// BandReason explains the band in one sentence.
	BandReason string `json:"band_reason"`
	// Passed reports whether the onsite threshold was met with no disqualifier.
	Passed bool `json:"passed"`
	// PassReason explains the verdict.
	PassReason string `json:"pass_reason"`
	// Runs are the scenario outcomes.
	Runs []*RunOutcome `json:"runs"`
	// GoTask is the Go-task outcome.
	GoTask *GoTaskOutcome `json:"go_task"`
	// Results are every result across every block, for the per-assertion table.
	Results []Result `json:"results"`
	// HarnessErrors are the harness's own failures across the whole submission.
	HarnessErrors []string `json:"harness_errors,omitempty"`
	// Stretch records the stretch scenarios as a tie-break note.
	Stretch []string `json:"stretch_notes,omitempty"`
	// Integrity is the protected-tree check of the submission, nil when the
	// submission was our own tree and there was nothing to check.
	Integrity *IntegrityReport `json:"integrity,omitempty"`
}

// BudgetOf returns the specification's point budget for a group. It is the
// exported face of [blockBudget], so a tool can print the published table and a
// reader can check it against the scenario files rather than trusting it.
func BudgetOf(g Group) int { return blockBudget(g) }

// blockBudget returns the specification's budget for a group.
func blockBudget(g Group) int {
	switch g {
	case GroupPublic:
		return PointsPublic
	case GroupHidden:
		return PointsHidden
	case GroupGoTask:
		return PointsGoTask
	case GroupRobustness:
		return PointsRobustness
	case GroupTraceability:
		return PointsTraceability
	case GroupDecisionLog:
		return PointsDecisionLog
	}
	return 0
}

// blockLabel returns the human name of a group.
func blockLabel(g Group) string {
	switch g {
	case GroupPublic:
		return "Public scenarios"
	case GroupHidden:
		return "Hidden scenarios"
	case GroupGoTask:
		return "Go task (kredexp-2.1)"
	case GroupRobustness:
		return "Robustness invariants"
	case GroupTraceability:
		return "Traceability"
	case GroupDecisionLog:
		return "Decision log (human)"
	case GroupStretch:
		return "Stretch (tie-break only)"
	case GroupIntegrity:
		return "Integrity checks (flags, not points)"
	}
	return string(g)
}

// scoredGroups are the six blocks that sum to 100, in the specification's order.
var scoredGroups = []Group{
	GroupPublic, GroupHidden, GroupGoTask, GroupRobustness,
	GroupTraceability, GroupDecisionLog,
}

// ScoreInput is everything the scorer needs.
type ScoreInput struct {
	// Submission is the directory that was graded, for the report header.
	Submission string
	// Connector is the connector command, for the report header.
	Connector string
	// Runs are the scenario outcomes in run order.
	Runs []*RunOutcome
	// GoTask is the Go-task outcome, nil when it was not graded.
	GoTask *GoTaskOutcome
	// DecisionLog is the human-entered decision-log score, nil when absent.
	DecisionLog *int
	// DecisionLogBy records who entered it.
	DecisionLogBy string
	// Integrity is the protected-tree check of the submission, nil when there was
	// nothing to check because the submission IS our tree.
	Integrity *IntegrityReport
	// HiddenGraded reports whether the hidden block actually ran. When it did
	// not, its budget is reported as unscored rather than as lost.
	HiddenGraded bool
}

// Compute builds the scorecard.
//
// The arithmetic is deliberately simple and the interesting decisions are all
// about what NOT to do: a skipped assertion adds to neither Earned nor Possible,
// so a harness gap cannot cost a candidate a point; a scenario's block is capped
// at its budget, so a scenario file that over-awards is caught rather than
// rewarded; and the decision-log block stays nil until a human fills it in.
func Compute(in ScoreInput) *Score {
	sc := &Score{
		Submission:    in.Submission,
		Connector:     in.Connector,
		Runs:          in.Runs,
		GoTask:        in.GoTask,
		DecisionLog:   in.DecisionLog,
		DecisionLogBy: in.DecisionLogBy,
		Budget:        PointsTotal,
	}
	for _, run := range in.Runs {
		sc.Results = append(sc.Results, run.Results...)
		sc.HarnessErrors = append(sc.HarnessErrors, prefixEach(run.Scenario, run.HarnessErrors)...)
		if run.Visibility == VisibilityStretch {
			sc.Stretch = append(sc.Stretch, stretchNote(run))
		}
	}
	sc.Integrity = in.Integrity
	if in.GoTask != nil {
		sc.Results = append(sc.Results, in.GoTask.Results...)
		sc.HarnessErrors = append(sc.HarnessErrors, prefixEach("gotask", in.GoTask.HarnessErrors)...)
	}

	earned := map[Group]int{}
	possible := map[Group]int{}
	for _, r := range sc.Results {
		earned[r.Assertion.Group] += r.Earned
		possible[r.Assertion.Group] += r.Possible
	}
	for _, g := range scoredGroups {
		b := Block{Group: g, Label: blockLabel(g), Budget: blockBudget(g),
			Earned: earned[g], Possible: possible[g]}
		if g == GroupDecisionLog {
			b.HumanEntered = true
			b.Possible = PointsDecisionLog
			if in.DecisionLog != nil {
				b.Earned = clamp(*in.DecisionLog, 0, PointsDecisionLog)
			} else {
				b.Earned = 0
				b.Note = "not scored: a human enters this with --decision-log-score"
			}
		}
		if b.Earned > b.Budget {
			b.Note = strings.TrimSpace(b.Note + fmt.Sprintf(
				" the assertions awarded %d, capped at the %d-point budget;"+
					" the scenario files over-award and should be fixed", b.Earned, b.Budget))
			b.Earned = b.Budget
		}
		if g == GroupHidden && !in.HiddenGraded {
			// Zero BOTH halves. Leaving Earned standing while Possible went to
			// zero printed a row reading "0 earned of 0 possible" whose points
			// were nevertheless in the total, so the blocks did not sum to the
			// total and the scorecard contradicted itself.
			b.Earned = 0
			b.Possible = 0
			b.Note = "not run: the hidden scenarios and the hidden build tag were not available"
			if len(results(sc.Results, GroupHidden)) > 0 {
				// The scenarios ran but the binary lacks the hidden kinds, so
				// what ran was the public vocabulary only. Say that, rather than
				// "not run", which a reviewer looking at a table of passing
				// hidden assertions would rightly disbelieve.
				b.Note = "not scored: the hidden scenarios ran, but this grader was built " +
					"without -tags hidden, so their own assertion kinds were unavailable. " +
					"Rebuild with: go build -tags hidden ./cmd/grade"
			}
		}
		if b.Possible < b.Budget && b.Note == "" && g != GroupDecisionLog {
			b.Note = fmt.Sprintf("%d of %d points were not assessed; see the skipped assertions",
				b.Budget-b.Possible, b.Budget)
		}
		sc.Blocks = append(sc.Blocks, b)
		sc.Total += b.Earned
		sc.Possible += b.Possible
	}

	sc.Flags = collectFlags(sc, in)
	sc.Band, sc.BandReason = band(sc)
	sc.Passed, sc.PassReason = verdict(sc)
	sortResults(sc.Results)
	return sc
}

// inertScenarios names the scenarios in which the connector left no trace at
// all, for the one line a reviewer needs before reading a page of red.
//
// A run counts when it produced results and every one of them is marked Inert,
// which is what [guardVacuous] sets whenever [Observed.NoActivity] holds.
func inertScenarios(runs []*RunOutcome) []string {
	var out []string
	for _, run := range runs {
		if len(run.Results) == 0 {
			continue
		}
		inert := true
		for _, r := range run.Results {
			if !r.Inert {
				inert = false
				break
			}
		}
		if inert {
			out = append(out, run.Scenario)
		}
	}
	return out
}

// results returns the results belonging to one group.
func results(all []Result, g Group) []Result {
	var out []Result
	for _, r := range all {
		if r.Assertion.Group == g {
			out = append(out, r)
		}
	}
	return out
}

// clamp bounds n to [lo, hi].
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// prefixEach prefixes every message with a scope.
func prefixEach(scope string, msgs []string) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, scope+": "+m)
	}
	return out
}

// stretchNote renders a stretch scenario's outcome as a tie-break note.
func stretchNote(run *RunOutcome) string {
	passed, total := 0, 0
	for _, r := range run.Results {
		if r.Status == StatusSkip || r.Status == StatusError {
			continue
		}
		total++
		if r.Status == StatusPass {
			passed++
		}
	}
	return fmt.Sprintf("%s (%s): %d of %d checks held; zero points, tie-break only",
		run.Scenario, run.Title, passed, total)
}

// sortResults orders results for the report: failures first, then by scenario and
// assertion id. score.md puts failures and flags first, which is the whole point
// of a scorecard somebody reads in forty-five minutes.
func sortResults(rs []Result) {
	rank := func(s Status) int {
		switch s {
		case StatusFail:
			return 0
		case StatusError:
			return 1
		case StatusSkip:
			return 2
		}
		return 3
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if ri, rj := rank(rs[i].Status), rank(rs[j].Status); ri != rj {
			return ri < rj
		}
		if rs[i].Scenario != rs[j].Scenario {
			return rs[i].Scenario < rs[j].Scenario
		}
		return rs[i].Assertion.ID < rs[j].Assertion.ID
	})
}

// collectFlags gathers the disqualifiers, warnings and notes.
//
// Every disqualifier here is mechanical and each one names the rule it comes
// from. The two disqualifiers of BUILD-SPEC 13 that are NOT here are the ones
// that cannot be decided by a machine without reading prose: "a silent guess on
// an ambiguous field" is decided from the Go task's diagnostics where it is
// observable and from the decision log where it is not, and "a write-up that
// contradicts the server counters" is a warning here rather than a disqualifier
// because BUILD-SPEC 17.6.6 downgraded it explicitly and section 17 overrides
// section 13.
func collectFlags(sc *Score, in ScoreInput) []Flag {
	var flags []Flag
	add := func(f Flag) { flags = append(flags, f) }

	for _, r := range sc.Results {
		if r.Status != StatusFail {
			continue
		}
		// A failure from a run in which the connector never ran is not evidence
		// of anything. Half of these flags are accusations - a leaked
		// credential, a posted invoice nobody proposed - and an inert run fails
		// them all trivially. Telling a candidate who submitted nothing that
		// they leaked a credential is worse than the 29 points the guard took
		// away.
		if r.Inert {
			continue
		}
		switch r.Assertion.Kind {
		case KindLegacyProfileUsed:
			// Nothing reached the twin at all, so there was no delivery to
			// pre-transform. That is phase 2 not being built, which candidates
			// are told they may do, and it must not be rendered as having gamed
			// the graded profile: the first real submission this ran against
			// stopped after phase 1, said so in its decision log, and was handed
			// an automatic disqualifier for bypassing a profile it never reached.
			if r.Info["invoice_files"] == "0" {
				add(Flag{ID: "note.legacy_not_delivered", Severity: SeverityNote,
					Message: "no invoice reached the twin in this scenario in any shape, so the " +
						"kredexp-2.1 profile had nothing to parse. That is the relay not being " +
						"built, not the profile being bypassed",
					Evidence: evidenceOf(r)})
				break
			}
			add(Flag{ID: "dq.legacy_profile_bypassed", Severity: SeverityDisqualifier,
				Message: "the legacy delivery was not parsed by the kredexp-2.1 profile inside the twin; " +
					"pre-transforming it outside the twin means the graded profile never ran (BUILD-SPEC 13)",
				Evidence: evidenceOf(r)})
		case KindNoUnproposedPostings:
			add(Flag{ID: "dq.posted_unmatched", Severity: SeverityDisqualifier,
				Message: "documents were posted for invoices the data says cannot be matched; " +
					"the SLA outranks the customer's request to post everything (BUILD-SPEC 16.2)",
				Evidence: evidenceOf(r)})
		case KindNoSecretInOutput:
			add(Flag{ID: "dq.secret_in_output", Severity: SeverityDisqualifier,
				Message:  "a credential appears verbatim in a candidate artifact (SLA rule 5)",
				Evidence: evidenceOf(r)})
		case KindNoPaymentDataInOutput:
			add(Flag{ID: "dq.payment_data_in_output", Severity: SeverityDisqualifier,
				Message:  "a full supplier IBAN appears verbatim in a candidate artifact (SLA rule 5)",
				Evidence: evidenceOf(r)})
		case KindCounterCrosscheck:
			add(Flag{ID: "warn.counters_disagree", Severity: SeverityWarning,
				Message: "run.json disagrees with the servers' own counters: " + r.Summary +
					" (reported, not scored, per BUILD-SPEC 17.6.6)",
				Evidence: evidenceOf(r)})
		case KindNoRepostOnResume, KindExternalReferenceUnique:
			add(Flag{ID: "warn.duplicate_posting", Severity: SeverityWarning,
				Message:  "a source document reached the ERP more than once: " + r.Summary,
				Evidence: evidenceOf(r)})
		}
		if r.Assertion.Kind == KindCounter && strings.Contains(r.Summary, "duplicate_") {
			add(Flag{ID: "warn.duplicate_counter", Severity: SeverityWarning,
				Message:  "a duplicate counter is non-zero: " + r.Summary,
				Evidence: evidenceOf(r)})
		}
	}

	if names := inertScenarios(sc.Runs); len(names) > 0 {
		add(Flag{ID: "note.inert_run", Severity: SeverityNote,
			Message: fmt.Sprintf("the connector produced no activity in %s: no request reached "+
				"either server, no document was posted and no batch was delivered, so every "+
				"invariant that holds only because nothing happened was withdrawn rather than "+
				"awarded - the points stay in the denominator and this reads 0 of %d, not 0 of 0",
				strings.Join(names, ", "), sc.Budget)})
	}

	if in.GoTask != nil {
		if !in.GoTask.Implemented {
			add(Flag{ID: "note.gotask_stub", Severity: SeverityNote,
				Message: "the kredexp-2.1 importer is the shipped stub; the Go task scored zero " +
					"and the pipeline was graded with the reference importer where a scenario enabled it"})
		} else if !in.GoTask.AmbiguitySurfaced {
			add(Flag{ID: "dq.silent_ambiguity", Severity: SeverityDisqualifier,
				Message: "the importer resolved the undecidable fields without emitting " +
					"ambiguous_field_semantics: a silent guess on an ambiguous field is an " +
					"automatic disqualifier because it changes what a supplier gets paid (BUILD-SPEC 13)"})
		}
	}

	for _, run := range sc.Runs {
		for _, cr := range run.Connector {
			if cr.TimedOut {
				add(Flag{ID: "note.timeout." + run.Scenario, Severity: SeverityNote,
					Message: fmt.Sprintf("%s phase %s hit the harness safety timeout; "+
						"this is a harness event and costs no points (BUILD-SPEC 17.4)",
						run.Scenario, cr.Phase),
					Evidence: cr.StderrPath})
			}
		}
	}
	if PointsTotal != PointsHeadline {
		add(Flag{ID: "spec.total_mismatch", Severity: SeverityNote,
			Message: fmt.Sprintf("BUILD-SPEC 13 says \"Scoring, 100 points\" but its table's rows "+
				"(%d + %d + %d + %d + %d + %d) sum to %d. Each row's own detail is consistent with "+
				"its row total, so this tool implements the rows and reports out of %d. The "+
				"published seniority bands and the 50-point threshold were written for a "+
				"100-point scale and are applied unchanged, which makes them very slightly "+
				"easier to reach. Deciding which number is wrong is an amendment to the "+
				"specification, not a change this tool should make on its own.",
				PointsPublic, PointsHidden, PointsGoTask, PointsRobustness,
				PointsTraceability, PointsDecisionLog, PointsTotal, PointsTotal),
			Evidence: "docs/internal/BUILD-SPEC.md section 13"})
	}
	if in.Integrity != nil && !in.Integrity.Clean() {
		// A warning, deliberately, not a disqualifier. The overlay already made
		// the edit harmless: the graded tree took only the candidate-writable
		// paths, so nothing they changed in our services reached the run and the
		// score stands on its own. What is left is a question for a human, and
		// the two answers are far apart: a debugging change somebody forgot to
		// revert, or an attempt to grade themselves. A script cannot tell those
		// apart and must not decide between them.
		sev := SeverityWarning
		if in.Integrity.Err != "" {
			sev = SeverityNote
		}
		add(Flag{ID: "integrity.protected_tree", Severity: sev,
			Message: in.Integrity.Summary() + ". The graded tree took only the candidate-writable " +
				"paths from the submission, so the edit did not reach the services and the score " +
				"is unaffected. Ask them what it was for.",
			Evidence: in.Integrity.Manifest})
	}
	if len(sc.HarnessErrors) > 0 {
		add(Flag{ID: "note.harness", Severity: SeverityNote,
			Message: fmt.Sprintf("%d harness events were recorded; they carry no points and "+
				"are listed at the top of score.md", len(sc.HarnessErrors))})
	}
	// One flag per stretch scenario, and each needs its OWN id: the deduplication
	// below keeps the first flag per id, so a shared id silently dropped every
	// note but the first. With one stretch scenario nobody could tell.
	for _, run := range in.Runs {
		if run.Visibility != VisibilityStretch {
			continue
		}
		add(Flag{ID: "note.stretch." + run.Scenario, Severity: SeverityNote,
			Message: stretchNote(run)})
	}

	// Deduplicate by id, keeping the first, and order disqualifiers first.
	seen := map[string]bool{}
	var out []Flag
	for _, f := range flags {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		out = append(out, f)
	}
	rank := map[string]int{SeverityDisqualifier: 0, SeverityWarning: 1, SeverityNote: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out
}

// evidenceOf returns a result's first evidence path.
func evidenceOf(r Result) string {
	if len(r.Findings) > 0 {
		return r.Findings[0].EvidencePath
	}
	return ""
}

// Disqualified reports whether any disqualifier fired.
func (s *Score) Disqualified() bool {
	for _, f := range s.Flags {
		if f.Severity == SeverityDisqualifier {
			return true
		}
	}
	return false
}

// Disqualifiers returns the disqualifier flags.
func (s *Score) Disqualifiers() []Flag {
	var out []Flag
	for _, f := range s.Flags {
		if f.Severity == SeverityDisqualifier {
			out = append(out, f)
		}
	}
	return out
}

// Failures returns the failed scored results, most severe first.
func (s *Score) Failures() []Result {
	var out []Result
	for _, r := range s.Results {
		if r.Status == StatusFail {
			out = append(out, r)
		}
	}
	return out
}

// goTaskGreen reports whether the Go task passed every fixture that ran.
func (s *Score) goTaskGreen() bool {
	if s.GoTask == nil || !s.GoTask.Implemented {
		return false
	}
	if s.GoTask.PublicTotal == 0 {
		return false
	}
	if s.GoTask.PublicPassed != s.GoTask.PublicTotal {
		return false
	}
	return s.GoTask.HiddenTotal == 0 || s.GoTask.HiddenPassed == s.GoTask.HiddenTotal
}

// duplicateCountersClean reports whether both duplicate counters ended at zero
// in every scenario. It is a pass-threshold condition of BUILD-SPEC 13.
func (s *Score) duplicateCountersClean() bool {
	for _, r := range s.Results {
		if r.Status != StatusFail || r.Inert {
			continue
		}
		if r.Assertion.Kind == KindNoRepostOnResume ||
			r.Assertion.Kind == KindExternalReferenceUnique {
			return false
		}
		if r.Assertion.Kind == KindCounter && strings.Contains(r.Summary, "duplicate_") {
			return false
		}
	}
	return true
}

// band returns the seniority band of BUILD-SPEC 13 and the sentence behind it.
//
// The bands are read literally, including the parts that are not about the
// number: the top band requires a green Go task AND a decision log naming at
// least two ambiguities, and the bottom-but-interview band requires an honest
// stop line and no silent guesses. Neither of those is a number the tool has, so
// the tool says which condition it could not verify rather than assuming it.
func band(s *Score) (string, string) {
	if s.Disqualified() {
		return "no", fmt.Sprintf("%d/%d, but %d automatic disqualifier(s) fired",
			s.Total, s.Budget, len(s.Disqualifiers()))
	}
	dl := "the decision log has not been scored"
	if s.DecisionLog != nil {
		dl = fmt.Sprintf("the decision log scored %d/%d", *s.DecisionLog, PointsDecisionLog)
	}
	switch {
	case s.Total >= 82:
		if s.goTaskGreen() && s.DecisionLog != nil && *s.DecisionLog >= 6 {
			return "staff or strong senior",
				fmt.Sprintf("%d/%d with a green Go task and %s", s.Total, s.Budget, dl)
		}
		return "solid senior (top band withheld)",
			fmt.Sprintf("%d/%d, but the top band also needs a green Go task and a decision log "+
				"naming at least two ambiguities: %s, Go task %s",
				s.Total, s.Budget, dl, either(s.goTaskGreen(), "green", "not green"))
	case s.Total >= 65:
		return "solid senior", fmt.Sprintf("%d/%d, %s", s.Total, s.Budget, dl)
	case s.Total >= 50:
		return "mid", fmt.Sprintf("%d/%d, %s", s.Total, s.Budget, dl)
	case s.Total >= 38:
		return "interview anyway, subject to the stop line",
			fmt.Sprintf("%d/%d with no disqualifier; this band requires an honest stop line in "+
				"the decision log, which a reviewer confirms", s.Total, s.Budget)
	}
	return "no", fmt.Sprintf("%d/%d is below the 38-point floor", s.Total, s.Budget)
}

// verdict returns the onsite decision and the sentence behind it.
//
// The threshold has four conditions and all four are stated, because a verdict
// that hides which condition failed is a verdict nobody can argue with, and
// arguing with it is exactly what a debrief is for.
func verdict(s *Score) (bool, string) {
	var missing []string
	if s.Total < PassThreshold {
		missing = append(missing, fmt.Sprintf("the total is %d, the threshold is %d",
			s.Total, PassThreshold))
	}
	if s.Disqualified() {
		missing = append(missing, fmt.Sprintf("%d automatic disqualifier(s) fired",
			len(s.Disqualifiers())))
	}
	if !s.duplicateCountersClean() {
		missing = append(missing, "a duplicate counter is non-zero and needs an explicit "+
			"written acknowledgment from the candidate")
	}
	if s.DecisionLog == nil {
		missing = append(missing, "the decision log has not been scored, and the threshold "+
			"requires at least six dimensions engaged")
	} else if *s.DecisionLog < 6 {
		missing = append(missing, fmt.Sprintf("the decision log scored %d, and the threshold "+
			"requires at least six dimensions engaged", *s.DecisionLog))
	}
	if len(missing) == 0 {
		return true, fmt.Sprintf("%d/%d, no disqualifier, both duplicate counters clean, "+
			"decision log %d/%d", s.Total, s.Budget, *s.DecisionLog, PointsDecisionLog)
	}
	return false, "not yet: " + strings.Join(missing, "; ")
}

// DecisionLogTemplate is the checklist a reviewer scores the decision log
// against, printed by `grade score` when --decision-log-score is absent.
//
// It exists so that the one human-entered number in the model is entered against
// a written rubric rather than against a feeling, and so that two reviewers who
// disagree by more than one point have something to disagree about. The dimensions
// are the seven of BUILD-SPEC 16.7; the points are the split of 13's decision-log
// row: six for dimensions engaged, two for ambiguities named with evidence, two
// for an honest stop line.
const DecisionLogTemplate = `Decision log score (0-10). Enter with:

    grade score --submission DIR --decision-log-score N --decision-log-by "your name"

Dimensions engaged, 1 point each, maximum 6. A dimension counts as engaged when
the log says what was decided, what the alternative was, and why:

  [ ] 1  idempotency and delivery semantics: how the key is derived and why the
         run id is not in it
  [ ] 2  failure taxonomy: which failures are exceptions, which are retries, and
         what the exit code means
  [ ] 3  efficiency and API citizenship: the request budget, paging, and what was
         batched
  [ ] 4  money, encoding, identifiers and units: rounding mode, the rate factor,
         leading zeros, the CP1252 bytes
  [ ] 5  state, incrementality and recovery: the watermark, what resume means,
         what a crash leaves behind
  [ ] 6  kredexp-2.1 fidelity and Go craft: the three contradictions and what was
         done about them
  [ ] 7  judgment, communication and ownership: the SLA-versus-customer-email
         conflict, named and resolved

Ambiguities and evidence, 2 points. At least two of the three documented
ambiguities named, with the evidence that decided them:

  [ ] Skonto: amount in the field table, percentage in the glossary
  [ ] empty position Kostenstelle: no defined inheritance
  [ ] GU sign: the trailer sums are the available evidence

Honest stop line, 2 points. The log says what was NOT done and why, without
inventing a reason:

  [ ] a stop line exists and is specific

Total: ____ / 10        Reviewer: ______________
`
