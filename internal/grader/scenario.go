package grader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Visibility says whether a scenario ships to candidates.
type Visibility string

// The visibilities. A public scenario ships in the candidate bundle, a hidden one
// is stripped, and a stretch one ships or not but is worth zero points either
// way.
const (
	// VisibilityPublic scenarios ship and are the ones `grade selfcheck` runs.
	VisibilityPublic Visibility = "public"
	// VisibilityHidden scenarios are stripped from the bundle by
	// `make candidate-bundle`.
	VisibilityHidden Visibility = "hidden"
	// VisibilityStretch scenarios are tie-break notes and score nothing.
	VisibilityStretch Visibility = "stretch"
)

// StepKind names one step of a scenario's script.
type StepKind string

// The step kinds. The set is closed: a scenario file naming anything else fails
// validation at load time rather than at minute nine of a grading run.
const (
	// StepReset resets both services: a new run, a zeroed clock and quota, no
	// documents, an empty twin store and a rewritten export drop.
	StepReset StepKind = "reset"
	// StepSeed loads the scenario dataset into the ERP and pre-seeds the twin's
	// cost centers and unit conversions.
	StepSeed StepKind = "seed"
	// StepRunConnector invokes the candidate's connector once.
	StepRunConnector StepKind = "run_connector"
	// StepScan drives POST /admin/v1/inbox/scan, so the file channel never
	// depends on a timer.
	StepScan StepKind = "scan"
	// StepKillConnector sends a signal to a running connector. It is what the
	// crash-and-resume scenario is built out of.
	StepKillConnector StepKind = "kill_connector"
	// StepDropFile writes a file into the ERP export drop or the twin inbox, to
	// plant something the connector must cope with.
	StepDropFile StepKind = "drop_file"
	// StepERPMutate reseeds the ERP, which is the only mutation surface the ERP
	// exposes. See [Step.Scenario] for what that costs.
	StepERPMutate StepKind = "erp_mutate"
	// StepERPAdvance moves the ERP's master data on to a later scenario WITHOUT
	// discarding the documents already posted, the idempotency keys that prove
	// they were posted once, or any counter. It is what makes a two-phase delta
	// scenario honest: a watermark that no second process reads back is not a
	// watermark, and a reseed between the phases would make the second night
	// look like a first night to the ERP.
	StepERPAdvance StepKind = "erp_advance"
	// StepAssert evaluates one assertion.
	StepAssert StepKind = "assert"
)

// A Step is one instruction of a scenario script. It is a tagged union: the
// fields a kind does not use are absent from the file and zero here.
type Step struct {
	// Kind selects the union member.
	Kind StepKind `json:"kind"`
	// Note is a human-readable reason for this step, printed by selfcheck. A
	// scenario file that says why it does something survives its author.
	Note string `json:"note,omitempty"`

	// Phase names a run_connector step, e.g. "a" and "b" of the crash-and-resume
	// scenario. It reaches the connector's environment as BLP_PHASE and names
	// the log files, so two invocations do not overwrite each other's evidence.
	Phase string `json:"phase,omitempty"`
	// TimeoutS is the harness safety timeout of one connector invocation in real
	// seconds. It defaults to [DefaultConnectorTimeoutS]. Exceeding it is a
	// harness event and never a score: see [ConnectorRun.TimedOut].
	TimeoutS int `json:"timeout_s,omitempty"`
	// EnvExtra is added to the connector's environment for this invocation only.
	// It never carries a credential the contract does not already publish.
	EnvExtra map[string]string `json:"env_extra,omitempty"`
	// ExpectExitIn constrains this invocation's exit code. Empty means the
	// contract's default of 0 or 2, which is checked by an exit_code_in
	// assertion rather than here.
	ExpectExitIn []int `json:"expect_exit_in,omitempty"`

	// FreshRunID gives a run_connector step its own connector run id instead of
	// the scenario's.
	//
	// The contract fixes one run id per run and a resume reuses it, because a
	// resume is the same run continuing. What this models is the other case: the
	// night after a run that was interrupted, when the schedule starts a new run
	// with a new id and last night's work is still half finished. A connector
	// whose idempotency key folds the run id in is correct on a resume and posts
	// everything again the next night, which is the subtle version of that defect
	// and the reason it needs its own phase to be visible at all.
	//
	// The harness's own run id, which names the output tree, is unaffected.
	FreshRunID bool `json:"fresh_run_id,omitempty"`

	// Signal is the signal of a kill_connector step: "SIGKILL", "SIGTERM" or
	// "SIGINT".
	Signal string `json:"signal,omitempty"`
	// After is when to send it. "first_erp_post" waits until the ERP has
	// accepted at least one document, "first_twin_batch" until the twin has seen
	// at least one batch, and "immediate" sends it at once. Every trigger is
	// observed through an admin counter, never through a sleep, so the crash
	// lands at the same logical point on a fast and a slow machine.
	After string `json:"after,omitempty"`

	// Target of a drop_file step: "erp_export" or "twin_incoming".
	Target string `json:"target,omitempty"`
	// Path is the file name relative to the target directory, and Content its
	// bytes. ContentFile reads the bytes from a file relative to the scenario
	// file instead.
	Path        string `json:"path,omitempty"`
	Content     string `json:"content,omitempty"`
	ContentFile string `json:"content_file,omitempty"`

	// Scenario and Seed of an erp_mutate step. The ERP has no per-record
	// mutation endpoint, so a mutation is a reseed: it replaces the dataset and
	// resets the run, which also discards documents already posted. A mutate
	// step therefore belongs before the first posting phase, and the loader
	// rejects one that follows a run_connector step without an intervening
	// reset.
	Scenario string `json:"scenario,omitempty"`
	Seed     *int64 `json:"seed,omitempty"`

	// Assert is the assertion of an assert step.
	Assert *Assertion `json:"assert,omitempty"`
}

// A Scenario is one declarative grading script. It is data on purpose: a
// scenario is a fixture, and a fixture that is code drifts away from the file
// that documents it.
type Scenario struct {
	// ID is the scenario identifier, e.g. "S1". It names the output directory.
	ID string `json:"id"`
	// Visibility decides whether the scenario ships and whether it scores.
	Visibility Visibility `json:"visibility"`
	// Title and Purpose are the human summary printed by selfcheck and shown in
	// the scorecard.
	Title   string `json:"title"`
	Purpose string `json:"purpose,omitempty"`

	// SeedScenario is the dataset the seed generator builds, which is not always
	// the scenario id: the hidden scenarios re-combine the published mechanisms
	// on top of a published dataset, per BUILD-SPEC 0.1.5, so H1 seeds S1.
	SeedScenario string `json:"seed_scenario"`
	// Seed is the default scenario seed. --seed overrides it.
	Seed int64 `json:"seed"`

	// ERPQuota and TwinQuota are the published hard quotas of BUILD-SPEC 12.
	ERPQuota  int `json:"erp_quota"`
	TwinQuota int `json:"twin_quota"`

	// Chaos enables the content-addressed fault injection of BUILD-SPEC 17.1.
	// --no-chaos turns it off for local debugging; it changes nothing else, so
	// the state digest is identical either way.
	Chaos bool `json:"chaos"`

	// SOAPTruncateAt cuts the SOAP rate table at this many rows and reports
	// Truncated=true. Zero serves the whole table. It is how the FX-edge
	// scenario reaches the refusal case.
	SOAPTruncateAt int `json:"soap_truncate_at,omitempty"`

	// PermanentFaultAt answers the nth non-admin ERP request with a permanent,
	// non-retriable transport error: a 503 whose retriable flag is false. Zero
	// disables it, which is every scenario but the one written for it.
	//
	// It is a count and not a hash because a permanent failure changes what a
	// correct run achieves. Which request it lands on therefore has to be
	// something a scenario states and pins a digest for, not something a seed
	// decides.
	PermanentFaultAt int `json:"permanent_fault_at,omitempty"`

	// PermanentFaultPath narrows it to requests whose path contains this
	// substring, because where a permanent failure lands decides what a correct
	// run can still achieve: on a master page it ends the run, and on a posting
	// it leaves those proposals pending and everything else intact.
	PermanentFaultPath string `json:"permanent_fault_path,omitempty"`

	// TwinPermanentFaultAt and TwinPermanentFaultPath are the same knob on the
	// twin. It is what reaches the one state a crash cannot reach
	// deterministically: the twin still offers a proposal the connector has
	// already posted, because the acknowledgment never landed.
	TwinPermanentFaultAt   int    `json:"twin_permanent_fault_at,omitempty"`
	TwinPermanentFaultPath string `json:"twin_permanent_fault_path,omitempty"`

	// EmptyPageAt makes the ERP answer the nth page of every paginated
	// collection with zero records, has_more still true and the client's own
	// cursor echoed back. Zero disables it.
	EmptyPageAt int `json:"empty_page_at,omitempty"`

	// ReferenceImporter sets MINIBLP_REFERENCE_IMPORTERS=1 on the twin, so a
	// candidate whose kredexp-2.1 parser is unfinished still exercises the
	// pipeline. The Go task is scored from its own fixtures, never from a
	// scenario, which is what decouples the two per BUILD-SPEC 10.
	ReferenceImporter bool `json:"reference_importer,omitempty"`

	// Isolated marks a scenario that must not share a process tree with
	// another. Scenarios run sequentially anyway; the flag is recorded so the
	// runner never batches them if that ever changes.
	Isolated bool `json:"isolated,omitempty"`

	// Golden pins the values that cannot be derived from the seed. See
	// [Golden].
	Golden Golden `json:"golden"`

	// Steps is the script, executed in order.
	Steps []Step `json:"steps"`

	// Path is where this scenario was loaded from. It is not part of the file.
	Path string `json:"-"`
}

// Golden holds the expected values of a scenario that the seed generator cannot
// derive.
//
// The expected exception set is not here: it is derived from the dataset by
// seed.Dataset.DeriveExceptions, which replays the documented matching order over
// the generated data, so it can never drift away from the data it describes.
//
// The state digest is here, and it is deliberately a pinned value rather than a
// derived one. Deriving it would mean re-implementing the twin's canonical
// projection of every dataset inside the grader, and the one place where two
// correct connectors may legitimately differ - whether the MONTHLY_AVG rate rows
// are delivered to the twin at all, since the published rules say only that they
// must not be *used* - would then be decided by a guess in the grader rather than
// by a documented rule. An unpinned digest is reported as a harness gap worth
// zero points, and the observed value is written to the run's evidence tree so it
// can be reviewed and pinned. See [AssertStateDigest].
type Golden struct {
	// StateDigest is the expected twin state digest, "" when not pinned.
	StateDigest string `json:"state_digest,omitempty"`
	// DigestPinnedBy records who reviewed the pinned digest and against what,
	// so a pinned answer key is attributable.
	DigestPinnedBy string `json:"digest_pinned_by,omitempty"`
	// File names a sibling JSON file holding the same fields, for a golden set
	// too large to read inside a scenario file.
	File string `json:"file,omitempty"`
}

// DefaultConnectorTimeoutS is the harness safety timeout of one connector
// invocation, in real seconds. BUILD-SPEC 17.4: no wall-clock timeout is scored,
// and this one exists purely so a hung submission cannot hang a grading run.
const DefaultConnectorTimeoutS = 900

// LoadScenario reads and validates one scenario file.
func LoadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("grader: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var sc Scenario
	if err := dec.Decode(&sc); err != nil {
		return nil, fmt.Errorf("grader: %s: %w", path, err)
	}
	sc.Path = path
	if sc.Golden.File != "" {
		gp := filepath.Join(filepath.Dir(path), sc.Golden.File)
		graw, err := os.ReadFile(gp)
		if err != nil {
			return nil, fmt.Errorf("grader: %s: golden file: %w", path, err)
		}
		var g Golden
		if err := json.Unmarshal(graw, &g); err != nil {
			return nil, fmt.Errorf("grader: %s: %w", gp, err)
		}
		if sc.Golden.StateDigest == "" {
			sc.Golden.StateDigest = g.StateDigest
		}
		if sc.Golden.DigestPinnedBy == "" {
			sc.Golden.DigestPinnedBy = g.DigestPinnedBy
		}
	}
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("grader: %s: %w", path, err)
	}
	return &sc, nil
}

// LoadScenarioDir reads every *.json scenario in dir, sorted by id. A missing
// directory returns no scenarios and no error: a bundle with the hidden
// scenarios stripped is a valid bundle, and it must not look like a broken one.
func LoadScenarioDir(dir string) ([]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("grader: %w", err)
	}
	var out []*Scenario
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".json" {
			continue
		}
		sc, err := LoadScenario(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Validate checks the scenario's shape. Every message names the field and the
// scenario, because the reader of this error is usually the person who just
// hand-edited the file.
func (sc *Scenario) Validate() error {
	if strings.TrimSpace(sc.ID) == "" {
		return fmt.Errorf("id must not be empty")
	}
	switch sc.Visibility {
	case VisibilityPublic, VisibilityHidden, VisibilityStretch:
	default:
		return fmt.Errorf("visibility %q: want public, hidden or stretch", sc.Visibility)
	}
	if strings.TrimSpace(sc.SeedScenario) == "" {
		return fmt.Errorf("seed_scenario must name a dataset the seed generator knows")
	}
	if len(sc.Steps) == 0 {
		return fmt.Errorf("steps must not be empty")
	}
	if sc.ERPQuota < 0 || sc.TwinQuota < 0 {
		return fmt.Errorf("quotas must not be negative")
	}
	ids := map[string]bool{}
	ranConnector := false
	for i, st := range sc.Steps {
		where := fmt.Sprintf("steps[%d] (%s)", i, st.Kind)
		switch st.Kind {
		case StepReset:
			ranConnector = false
		case StepSeed, StepScan:
		case StepRunConnector:
			if st.TimeoutS < 0 {
				return fmt.Errorf("%s: timeout_s must not be negative", where)
			}
			ranConnector = true
		case StepKillConnector:
			if _, err := parseSignal(st.Signal); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			switch st.After {
			case KillAfterImmediate, KillAfterFirstERPPost, KillAfterFirstTwinBatch:
			default:
				return fmt.Errorf("%s: after %q: want %s, %s or %s", where, st.After,
					KillAfterImmediate, KillAfterFirstERPPost, KillAfterFirstTwinBatch)
			}
		case StepDropFile:
			switch st.Target {
			case DropTargetERPExport, DropTargetTwinIncoming:
			default:
				return fmt.Errorf("%s: target %q: want %s or %s", where, st.Target,
					DropTargetERPExport, DropTargetTwinIncoming)
			}
			if strings.TrimSpace(st.Path) == "" {
				return fmt.Errorf("%s: path must not be empty", where)
			}
			if filepath.IsAbs(st.Path) || strings.Contains(st.Path, "..") {
				return fmt.Errorf("%s: path %q must stay inside the target directory", where, st.Path)
			}
		case StepERPMutate:
			if ranConnector {
				return fmt.Errorf("%s: an erp_mutate reseeds the ERP, which discards the documents "+
					"already posted; use erp_advance between two connector phases, or put the "+
					"mutate before the first run_connector", where)
			}
		case StepERPAdvance:
			if strings.TrimSpace(st.Scenario) == "" {
				return fmt.Errorf("%s: an erp_advance must name the scenario to advance to", where)
			}
		case StepAssert:
			if st.Assert == nil {
				return fmt.Errorf("%s: assert must carry an assertion", where)
			}
			a := st.Assert
			if err := a.Validate(); err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			if ids[a.ID] {
				return fmt.Errorf("%s: duplicate assertion id %q", where, a.ID)
			}
			ids[a.ID] = true
		default:
			return fmt.Errorf("%s: unknown step kind", where)
		}
	}
	return nil
}

// Assertions returns the scenario's assertions in script order.
func (sc *Scenario) Assertions() []Assertion {
	var out []Assertion
	for _, st := range sc.Steps {
		if st.Kind == StepAssert && st.Assert != nil {
			out = append(out, *st.Assert)
		}
	}
	return out
}

// Scores reports whether this scenario contributes points at all. A stretch
// scenario never does, per BUILD-SPEC 12.
func (sc *Scenario) Scores() bool { return sc.Visibility != VisibilityStretch }

// The drop_file targets.
const (
	// DropTargetERPExport is the ERP's legacy export drop, i.e. the sender's
	// SFTP directory.
	DropTargetERPExport = "erp_export"
	// DropTargetTwinIncoming is the twin's inbox/incoming, the only directory a
	// connector may write. A grader drop there simulates a foreign producer.
	DropTargetTwinIncoming = "twin_incoming"
)

// The kill_connector triggers.
const (
	// KillAfterImmediate signals as soon as the process exists.
	KillAfterImmediate = "immediate"
	// KillAfterFirstERPPost signals once the ERP holds at least one document.
	KillAfterFirstERPPost = "first_erp_post"
	// KillAfterFirstTwinBatch signals once the twin has seen at least one batch.
	KillAfterFirstTwinBatch = "first_twin_batch"
)
