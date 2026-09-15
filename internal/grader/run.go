package grader

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// ScanDriverInterval is how often the background driver calls
// POST /admin/v1/inbox/scan while a connector is running.
//
// The interval is a real duration and it is not a source of nondeterminism. The
// twin's file channel has no timer of its own: a scan is the only way a batch is
// picked up, batches are picked up in ascending directory-name order, and
// publication is a single rename, so a directory is never seen half-written. An
// extra scan therefore changes the value of received_scan on a receipt and
// nothing else, and received_scan is excluded from the state digest and from
// every scored assertion. What the driver buys is that a connector which writes a
// batch and then reads the outbox in the same invocation does not deadlock
// against a scanner nobody called.
const ScanDriverInterval = 50 * time.Millisecond

// triggerPollInterval is how often a kill trigger's admin counter is read, and
// triggerDeadline bounds the wait. Both protect the harness; neither scores. The
// trigger itself is a counter on the server, so the crash lands at the same
// logical point on a fast and on a slow machine.
const (
	triggerPollInterval = 10 * time.Millisecond
	triggerDeadline     = 5 * time.Minute
)

// Options configures one grading run.
type Options struct {
	// RepoRoot is the module root. Everything else is resolved against it.
	RepoRoot string
	// ScenarioPath is the scenario file, or empty to look the id up in
	// [ScenarioDir] and then in [HiddenScenarioDir].
	ScenarioPath string
	// ScenarioID is the scenario to run.
	ScenarioID string
	// Seed overrides the scenario's own seed when non-nil.
	Seed *int64
	// ConnectorCmd is the connector command line. Empty means
	// [DefaultConnector] under RepoRoot. It is split on spaces, so a wrapper
	// with arguments works without a shell.
	ConnectorCmd string
	// NoChaos disables fault injection on both services. It changes nothing
	// else, so the state digest is identical either way.
	NoChaos bool
	// OutDir is the root of the run's output tree. Empty means
	// grading/out under RepoRoot.
	OutDir string
	// HarnessRunID names the output tree. Empty derives it from the scenario and
	// the seed, so a re-run is diffable against its predecessor.
	HarnessRunID string
	// Stdout receives progress lines. Nil discards them.
	Stdout io.Writer
	// KeepGoing continues the scenario after a step fails. Off by default: a
	// scenario whose seeding failed produces meaningless assertions.
	KeepGoing bool
}

// DefaultConnector is the connector entry point of the contract.
const DefaultConnector = "connector/run.sh"

// ScenarioDir and HiddenScenarioDir are where scenario files live, relative to
// the repository root.
const (
	// ScenarioDir holds the public scenarios, which ship.
	ScenarioDir = "grading/scenarios"
	// HiddenScenarioDir holds the hidden and stretch scenarios, which do not.
	HiddenScenarioDir = "grading/hidden/scenarios"
)

// Dirs are the directories of one scenario run.
type Dirs struct {
	// Root is out/<harness_run_id>/<scenario>.
	Root string
	// ERPExport is the ERP's legacy export drop, i.e. the sender's SFTP
	// directory, and the connector's --erp-export-dir.
	ERPExport string
	// TwinData is the twin's data directory: store, inbox and outbox.
	TwinData string
	// TwinDrop is the twin's inbox ROOT. The connector writes batches into its
	// incoming/ subdirectory, which is the only twin-owned path it may write,
	// and reads its receipts from the sibling receipts/. Handing over the root
	// rather than incoming/ is deliberate: a connector that only knew
	// incoming/ would have to guess where its receipts appear,
	// and the connector's --twin-drop-dir.
	TwinDrop string
	// State is the connector's --state-dir.
	State string
	// Reports is the connector's --report-dir.
	Reports string
	// Logs holds the service and connector transcripts.
	Logs string
	// Evidence holds the state dumps every finding points at.
	Evidence string
}

// requiredEmpty are the directories asserted empty before a run. The evidence
// directory is the harness's own and is excluded.
func (d Dirs) requiredEmpty() []string {
	return []string{d.ERPExport, d.TwinData, d.State, d.Reports, d.Logs}
}

// A RunOutcome is everything one scenario run produced.
type RunOutcome struct {
	// Scenario is the scenario that ran.
	Scenario string `json:"scenario"`
	// Visibility is copied out so a report can group without reloading files.
	Visibility Visibility `json:"visibility"`
	// Title is the scenario's human title.
	Title string `json:"title"`
	// Seed is the seed actually used.
	Seed int64 `json:"seed"`
	// ConnectorRunID is the --run-id handed to every invocation. It is the same
	// string for both phases of a crash-and-resume scenario, per BUILD-SPEC 9.
	ConnectorRunID string `json:"connector_run_id"`
	// Dirs are the run's directories.
	Dirs Dirs `json:"dirs"`
	// Connector records every invocation.
	Connector []ConnectorRun `json:"connector_runs"`
	// Results are the evaluated assertions in script order.
	Results []Result `json:"results"`
	// HarnessErrors are the harness's own failures: a service that would not
	// start, a step that could not run. They carry no points and are printed
	// first, because a harness gap read as a candidate's failure is the worst
	// outcome this tool can produce.
	HarnessErrors []string `json:"harness_errors,omitempty"`
	// Chaos reports whether fault injection was on.
	Chaos bool `json:"chaos"`
}

// Failed reports whether this run is one a human has to look at: any assertion
// that failed or could not be evaluated, or a harness error, which means the run
// was not fully assessed.
//
// It deliberately does not read the point value. It used to count only FAILED
// SCORED assertions, and S0's assertions are all info level because the smoke
// gate scores nothing, so `grade run --scenario S0` printed "5 passed, 7 failed"
// and exited 0 against a connector that does not exist yet. That is the first
// command a candidate runs and the one their own CI runs, so a green exit code
// there is the most expensive lie this tool could tell.
//
// Points are decided by the scorer, which does not use this: a failed stretch
// assertion still costs nothing.
func (o *RunOutcome) Failed() bool {
	if len(o.HarnessErrors) > 0 {
		return true
	}
	for _, r := range o.Results {
		if r.Status == StatusFail || r.Status == StatusError {
			return true
		}
	}
	return false
}

// Earned and Possible sum the run's points.
func (o *RunOutcome) Earned() int {
	n := 0
	for _, r := range o.Results {
		n += r.Earned
	}
	return n
}

// Possible sums the points the run could have awarded.
func (o *RunOutcome) Possible() int {
	n := 0
	for _, r := range o.Results {
		n += r.Possible
	}
	return n
}

// Run executes one scenario end to end and returns what it produced.
//
// It returns an error only when the run could not be attempted at all - an
// unloadable scenario, an output tree that is not empty, a service binary that
// will not build. Everything that happens after the services are up is recorded
// in the RunOutcome, including harness failures, because a partially completed
// run still carries evidence worth reading.
func Run(opts Options) (*RunOutcome, error) {
	sc, err := resolveScenario(opts)
	if err != nil {
		return nil, err
	}
	seedValue := sc.Seed
	if opts.Seed != nil {
		seedValue = *opts.Seed
	}
	r := &runner{opts: opts, sc: sc, seed: seedValue, out: opts.Stdout}
	if r.out == nil {
		r.out = io.Discard
	}
	return r.run()
}

// resolveScenario loads the scenario named by the options.
func resolveScenario(opts Options) (*Scenario, error) {
	if opts.ScenarioPath != "" {
		return LoadScenario(opts.ScenarioPath)
	}
	if strings.TrimSpace(opts.ScenarioID) == "" {
		return nil, errors.New("grader: no scenario given")
	}
	for _, dir := range []string{ScenarioDir, HiddenScenarioDir} {
		p := filepath.Join(opts.RepoRoot, dir, opts.ScenarioID+".json")
		if _, err := os.Stat(p); err == nil {
			return LoadScenario(p)
		}
	}
	return nil, fmt.Errorf("grader: no scenario %q in %s or %s",
		opts.ScenarioID, ScenarioDir, HiddenScenarioDir)
}

// runner holds the mutable state of one scenario run.
type runner struct {
	opts Options
	sc   *Scenario
	seed int64
	out  io.Writer

	dirs Dirs
	erp  *Service
	twin *Service
	erpc *AdminClient
	twc  *AdminClient

	creds     ERPCredentials
	twinID    string
	twinSec   string
	adminERP  string
	adminTwin string
	runID     string

	dataset *seed.Dataset

	// plantedBatches are the batch ids this scenario dropped into the twin's
	// inbox itself. See [Observed.NoActivity].
	plantedBatches map[string]bool

	// pending is a connector invocation that was started and not waited for,
	// because the next step is a kill_connector.
	pending *pendingConnector

	// obs is the cached observation of both services' state, dropped by every
	// mutating step. See runner.observe.
	obs *Observed

	// scanStop stops the background scan driver.
	scanStop chan struct{}
	scanDone chan struct{}
	scanOnce sync.Once

	outcome *RunOutcome
}

// pendingConnector is a started, unreaped connector invocation.
type pendingConnector struct {
	cmd   *exec.Cmd
	rec   *ConnectorRun
	done  chan error
	start time.Time
	files []*os.File
}

// logf writes a progress line.
func (r *runner) logf(format string, args ...any) {
	fmt.Fprintf(r.out, format+"\n", args...)
}

// harness records a harness failure.
func (r *runner) harness(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.outcome.HarnessErrors = append(r.outcome.HarnessErrors, msg)
	r.logf("harness: %s", msg)
}

// run is the whole scenario.
func (r *runner) run() (*RunOutcome, error) {
	chaos := r.sc.Chaos && !r.opts.NoChaos
	r.runID = connectorRunID(r.sc.ID, r.seed)
	r.outcome = &RunOutcome{
		Scenario: r.sc.ID, Visibility: r.sc.Visibility, Title: r.sc.Title,
		Seed: r.seed, ConnectorRunID: r.runID, Chaos: chaos,
	}

	if err := r.prepareDirs(); err != nil {
		return nil, err
	}
	r.outcome.Dirs = r.dirs

	ds, err := seed.Generate(r.sc.SeedScenario, r.seed)
	if err != nil {
		return nil, fmt.Errorf("grader: generating the %s dataset at seed %d: %w",
			r.sc.SeedScenario, r.seed, err)
	}
	r.dataset = ds

	bins, err := buildServices(r.opts.RepoRoot, filepath.Join(r.dirs.Root, "bin"))
	if err != nil {
		return nil, err
	}

	r.adminERP = deriveSecret("erp-admin", r.seed)
	r.adminTwin = deriveSecret("twin-admin", r.seed)
	r.twinID = "blp-twin-connector"
	r.twinSec = deriveSecret("twin-client", r.seed)

	if err := r.startServices(bins, chaos); err != nil {
		return nil, err
	}
	defer r.stopServices()

	if err := r.erpc.get(erpPathCredentials, &r.creds); err != nil {
		return nil, fmt.Errorf("grader: reading the ERP credentials: %w", err)
	}

	r.startScanDriver()
	defer r.stopScanDriver()

	for i, st := range r.sc.Steps {
		if err := r.step(i, st); err != nil {
			r.harness("step %d (%s): %v", i, st.Kind, err)
			if !r.opts.KeepGoing {
				break
			}
		}
	}

	// A connector left running because its kill step never fired must not
	// outlive the scenario.
	if r.pending != nil {
		r.harness("a connector invocation was started and never reaped; signalling it")
		r.killPending(syscall.SIGKILL, false)
	}
	r.stopScanDriver()
	return r.outcome, nil
}

// prepareDirs creates the run's directory tree and asserts the directories the
// run is graded out of are empty.
func (r *runner) prepareDirs() error {
	outRoot := r.opts.OutDir
	if outRoot == "" {
		outRoot = filepath.Join(r.opts.RepoRoot, "grading", "out")
	}
	// Absolute, always. Every directory below is handed to the connector on its
	// command line, and the connector runs with its own working directory: a
	// relative --out therefore sent the reports somewhere neither side agreed on,
	// and the scorecard said "run.json is absent" about a connector that had
	// written one. Nothing about that failure pointed at the path.
	if abs, err := filepath.Abs(outRoot); err == nil {
		outRoot = abs
	}
	harnessID := r.opts.HarnessRunID
	if harnessID == "" {
		harnessID = r.runID
	}
	root := filepath.Join(outRoot, harnessID, r.sc.ID)
	d := Dirs{
		Root:      root,
		ERPExport: filepath.Join(root, "erp", "export"),
		TwinData:  filepath.Join(root, "twin"),
		State:     filepath.Join(root, "state"),
		Reports:   filepath.Join(root, "reports"),
		Logs:      filepath.Join(root, "logs"),
		Evidence:  filepath.Join(root, "evidence"),
	}
	d.TwinDrop = filepath.Join(d.TwinData, "inbox")
	for _, dir := range []string{d.ERPExport, d.TwinData, d.TwinDrop, d.State, d.Reports, d.Logs, d.Evidence} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("grader: %w", err)
		}
	}
	// The tree must be empty at the start. A leftover from an earlier run is
	// exactly the state that produces an assertion which passes for the wrong
	// reason, and finding that out from a scorecard is worse than finding it out
	// here.
	//
	// The leftover is almost always OUR OWN previous run of the same scenario, at
	// the same path, because the path is derived from the scenario and the seed.
	// So the harness clears it and says so, instead of refusing to run and asking
	// a human to delete a directory the harness created: "run it twice and it
	// stops working" is not a property a grading tool may have. Only this
	// scenario's own subtree is ever removed, never the --out root.
	for _, dir := range d.requiredEmpty() {
		leftover, err := nonEmptyFiles(dir)
		if err != nil {
			return fmt.Errorf("grader: %w", err)
		}
		if len(leftover) == 0 {
			continue
		}
		r.logf("clearing the previous output tree at %s (%d leftover file%s)",
			root, len(leftover), map[bool]string{false: "", true: "s"}[len(leftover) != 1])
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("grader: clearing %s: %w", root, err)
		}
		for _, dir := range []string{d.ERPExport, d.TwinData, d.TwinDrop, d.State,
			d.Reports, d.Logs, d.Evidence} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("grader: %w", err)
			}
		}
		break
	}
	r.dirs = d
	return nil
}

// plural adds an ellipsis when a leftover list was capped.
func plural(n int) string {
	if n >= 8 {
		return " ..."
	}
	return ""
}

// nonEmptyFiles returns up to eight paths under dir, relative to it. The twin's
// inbox/incoming is created by the harness and is expected, so an empty
// directory tree counts as empty.
func nonEmptyFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			rel = p
		}
		out = append(out, rel)
		if len(out) >= 8 {
			return filepath.SkipAll
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

// buildServices compiles the two service binaries into binDir and returns their
// paths.
//
// The grader builds rather than assuming a binary is on the path, because
// BUILD-SPEC 16.3 requires the servers to come from the pristine tree with only
// the candidate-writable files overlaid: a candidate who edited the twin's
// matching engine must not be able to affect their own grading run, and the only
// way to be sure of that is to compile the tree the harness was pointed at.
func buildServices(repoRoot, binDir string) (map[string]string, error) {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, fmt.Errorf("grader: %w", err)
	}
	out := map[string]string{}
	for _, name := range []string{"erp", "miniblp"} {
		bin := filepath.Join(binDir, name)
		args := []string{"build", "-o", bin}
		// The twin is built with the grading tag when our own tree has the
		// reference importer, so a scenario can run the pipeline with a working
		// legacy parser regardless of the candidate's. In a candidate bundle
		// that directory and the tagged file are both gone, the tag matches
		// nothing, and the build is identical.
		if name == "miniblp" && referenceImporterPresent(repoRoot) {
			args = append(args, "-tags", "grading")
		}
		args = append(args, "./cmd/"+name)
		cmd := exec.Command("go", args...)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if combined, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("grader: building cmd/%s: %w\n%s", name, err, combined)
		}
		out[name] = bin
	}
	return out, nil
}

// referenceImporterPresent reports whether our reference legacy importer is in
// this tree. It is absent from every candidate bundle, which is the point: the
// same grader code works on both trees without a flag.
func referenceImporterPresent(repoRoot string) bool {
	_, err := os.Stat(filepath.Join(repoRoot, "grading", "reference", "kredexp"))
	return err == nil
}

// startServices starts the ERP and the twin.
func (r *runner) startServices(bins map[string]string, chaos bool) error {
	if err := r.preflightSurvivors(); err != nil {
		return err
	}
	chaosFlag := "--chaos"
	if !chaos {
		chaosFlag = "--no-chaos"
	}
	erpArgs := []string{
		bins["erp"],
		"--listen", "127.0.0.1:0",
		"--scenario", r.sc.SeedScenario,
		"--seed", strconv.FormatInt(r.seed, 10),
		"--quota", strconv.Itoa(r.sc.ERPQuota),
		chaosFlag,
		"--export-dir", r.dirs.ERPExport,
		"--admin-token", r.adminERP,
		// The run id goes on the argv of every child, so a pgrep sweep after a
		// teardown can find a survivor and name the run it belongs to.
		"--soap-truncate-at", strconv.Itoa(r.sc.SOAPTruncateAt),
		"--permanent-fault-at", strconv.Itoa(r.sc.PermanentFaultAt),
		"--permanent-fault-path", r.sc.PermanentFaultPath,
		"--empty-page-at", strconv.Itoa(r.sc.EmptyPageAt),
	}
	erp, err := startService(startOptions{
		name: "erp", argv: erpArgs, dir: r.opts.RepoRoot,
		env:        append(os.Environ(), "BLP_HARNESS_RUN_ID="+r.runID),
		logPath:    filepath.Join(r.dirs.Logs, "erp.log"),
		adminToken: r.adminERP,
	})
	if err != nil {
		return err
	}
	r.erp = erp
	r.erpc = newAdminClient(erp)

	twinEnv := append(os.Environ(),
		"MINIBLP_ADMIN_TOKEN="+r.adminTwin,
		"MINIBLP_RUN_ID="+r.runID,
		"TWIN_CLIENT_ID="+r.twinID,
		"TWIN_CLIENT_SECRET="+r.twinSec,
		"BLP_HARNESS_RUN_ID="+r.runID,
	)
	if r.sc.ReferenceImporter {
		twinEnv = append(twinEnv, "MINIBLP_REFERENCE_IMPORTERS=1")
	}
	twinArgs := []string{
		bins["miniblp"],
		"--listen", "127.0.0.1:0",
		"--data-dir", r.dirs.TwinData,
		"--scenario", r.sc.SeedScenario,
		"--seed", strconv.FormatInt(r.seed, 10),
		"--quota", strconv.Itoa(r.sc.TwinQuota),
		"--permanent-fault-at", strconv.Itoa(r.sc.TwinPermanentFaultAt),
		"--permanent-fault-path", r.sc.TwinPermanentFaultPath,
		chaosFlag,
		"--admin-token", r.adminTwin,
	}
	twin, err := startService(startOptions{
		name: "miniblp", argv: twinArgs, dir: r.opts.RepoRoot, env: twinEnv,
		logPath:    filepath.Join(r.dirs.Logs, "miniblp.log"),
		adminToken: r.adminTwin,
	})
	if err != nil {
		_ = r.erp.Stop()
		return err
	}
	r.twin = twin
	r.twc = newAdminClient(twin)
	r.logf("services up: erp %s, twin %s", r.erp.Addr, r.twin.Addr)
	return nil
}

// stopServices tears both services down and records a teardown fault.
func (r *runner) stopServices() {
	for _, svc := range []*Service{r.twin, r.erp} {
		if svc == nil {
			continue
		}
		if err := svc.Stop(); err != nil {
			r.harness("tearing %s down: %v", svc.Name, err)
		}
	}
	// Anything still carrying this run id was started by this run: preflight
	// refused to start while a stranger carried the id. So it is ours to clean
	// up, and leaving it running would poison every later run of the same
	// scenario - the run id is derived from the scenario, so it repeats.
	survivors := sweepSurvivorPIDs(r.runID)
	if len(survivors) == 0 {
		return
	}
	var killed, stubborn []string
	for _, sv := range survivors {
		if err := syscall.Kill(sv.pid, syscall.SIGKILL); err != nil {
			stubborn = append(stubborn, fmt.Sprintf("pid %d (%v)", sv.pid, err))
			continue
		}
		killed = append(killed, fmt.Sprintf("pid %d: %s", sv.pid, sv.argv))
	}
	// Still a harness fault even though it is now cleaned up: a teardown that
	// needed a second pass is a defect in the teardown, and hiding it would let
	// it rot.
	if len(killed) > 0 {
		r.harness("teardown left %d process(es) carrying run id %s; killed them: %s",
			len(killed), r.runID, strings.Join(killed, ", "))
	}
	if len(stubborn) > 0 {
		r.harness("processes carrying run id %s survived teardown and could not be killed: %s",
			r.runID, strings.Join(stubborn, ", "))
	}
}

// preflightSurvivors refuses to start when a process from an earlier run is still
// carrying this run's id.
//
// The run id is derived from the scenario, so it is the same on every run of that
// scenario. One run killed at the wrong moment - a Ctrl-C, an OOM, an operator's
// pkill - therefore leaves processes that make every later run of that scenario
// report a teardown fault and declare itself not fully assessed, which reads like
// a defect in the submission being graded. Refusing up front, with the pids, gets
// it cleared once instead.
func (r *runner) preflightSurvivors() error {
	survivors := sweepSurvivorPIDs(r.runID)
	if len(survivors) == 0 {
		return nil
	}
	lines := make([]string, 0, len(survivors))
	for _, sv := range survivors {
		lines = append(lines, fmt.Sprintf("  pid %d: %s", sv.pid, sv.argv))
	}
	return fmt.Errorf("processes from an earlier run still carry run id %s:\n%s\n"+
		"they are not this run's, and they would make this run report a teardown fault.\n"+
		"clear them with: kill %s",
		r.runID, strings.Join(lines, "\n"), pidList(survivors))
}

// pidList renders the pids of survivors as a space-separated kill argument.
func pidList(survivors []survivor) string {
	out := make([]string, 0, len(survivors))
	for _, sv := range survivors {
		out = append(out, strconv.Itoa(sv.pid))
	}
	return strings.Join(out, " ")
}

// sweepSurvivors looks for processes still carrying the run id in their argv. It
// is the pgrep sweep of BUILD-SPEC 16.4, done through /proc so it needs no
// external binary.
//
// A survivor is reported and never killed here: a process this harness did not
// start is none of its business, and silently killing by a substring match is how
// a grading harness takes down a reviewer's editor. Whether killing is justified
// is the caller's judgment, and only [runner.stopServices] has the standing to
// make it, because by then a pre-flight sweep has established that every process
// carrying this run id was started by this run.
func sweepSurvivors(runID string) []string {
	found := sweepSurvivorPIDs(runID)
	out := make([]string, 0, len(found))
	for _, p := range found {
		out = append(out, fmt.Sprintf("pid %d: %s", p.pid, p.argv))
	}
	return out
}

// A survivor is one process carrying a run id.
type survivor struct {
	pid  int
	argv string
}

// sweepSurvivorPIDs returns at most eight processes whose argv carries runID, in
// ascending pid order so two sweeps agree.
func sweepSurvivorPIDs(runID string) []survivor {
	if runID == "" {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	var out []survivor
	for _, pid := range pids {
		cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		if !strings.Contains(argv, runID) {
			continue
		}
		out = append(out, survivor{pid: pid, argv: argv})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// startScanDriver starts the background inbox scan driver.
func (r *runner) startScanDriver() {
	r.scanStop = make(chan struct{})
	r.scanDone = make(chan struct{})
	go func() {
		defer close(r.scanDone)
		ticker := time.NewTicker(ScanDriverInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.scanStop:
				return
			case <-ticker.C:
				_ = r.twc.post(twinPathScan, nil, nil)
			}
		}
	}()
}

// stopScanDriver stops the driver and waits for it, so no scan races the final
// state read.
func (r *runner) stopScanDriver() {
	r.scanOnce.Do(func() {
		if r.scanStop == nil {
			return
		}
		close(r.scanStop)
		<-r.scanDone
	})
}

// step executes one scenario step.
func (r *runner) step(i int, st Step) error {
	switch st.Kind {
	case StepAssert:
		// Assertions read; they never mutate. Everything else invalidates the
		// cached observation before it runs.
	default:
		r.invalidateObservation()
	}
	switch st.Kind {
	case StepReset:
		if err := r.erpc.post(erpPathReset, nil, nil); err != nil {
			return err
		}
		return r.twc.post(twinPathReset, nil, nil)
	case StepSeed:
		body := map[string]any{"scenario": r.sc.SeedScenario, "seed": r.seed}
		if err := r.erpc.post(erpPathSeed, body, nil); err != nil {
			return err
		}
		return r.twc.post(twinPathSeed, body, nil)
	case StepScan:
		return r.twc.post(twinPathScan, nil, nil)
	case StepERPAdvance:
		sc := st.Scenario
		sv := r.seed
		if st.Seed != nil {
			sv = *st.Seed
		}
		ds, err := seed.Generate(sc, sv)
		if err != nil {
			return err
		}
		if err := r.erpc.post(erpPathAdvance, map[string]any{"scenario": sc, "seed": sv}, nil); err != nil {
			return err
		}
		// The expected exception set and the dataset counts follow the ERP's
		// master data, so the golden dataset advances with it. Nothing the twin
		// or the ERP already recorded is touched.
		r.dataset = ds
		return nil
	case StepERPMutate:
		sc := st.Scenario
		if sc == "" {
			sc = r.sc.SeedScenario
		}
		sv := r.seed
		if st.Seed != nil {
			sv = *st.Seed
		}
		ds, err := seed.Generate(sc, sv)
		if err != nil {
			return err
		}
		if err := r.erpc.post(erpPathSeed, map[string]any{"scenario": sc, "seed": sv}, nil); err != nil {
			return err
		}
		// The expected exception set and the dataset counts follow the ERP, so
		// the mutation replaces the golden dataset too.
		r.dataset = ds
		return nil
	case StepDropFile:
		return r.dropFile(st)
	case StepRunConnector:
		killNext := i+1 < len(r.sc.Steps) && r.sc.Steps[i+1].Kind == StepKillConnector
		return r.runConnector(st, killNext)
	case StepKillConnector:
		return r.killConnectorStep(st)
	case StepAssert:
		obs, err := r.observe()
		if err != nil {
			return err
		}
		res := Evaluate(*st.Assert, obs)
		r.outcome.Results = append(r.outcome.Results, res)
		return nil
	}
	return fmt.Errorf("unknown step kind %q", st.Kind)
}

// dropFile writes a planted file into the ERP export drop or the twin inbox.
func (r *runner) dropFile(st Step) error {
	var base string
	switch st.Target {
	case DropTargetERPExport:
		base = r.dirs.ERPExport
	case DropTargetTwinIncoming:
		base = r.dirs.TwinDrop
	default:
		return fmt.Errorf("unknown drop target %q", st.Target)
	}
	content := []byte(st.Content)
	if st.ContentFile != "" {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(r.sc.Path), st.ContentFile))
		if err != nil {
			return err
		}
		content = raw
	}
	dest := filepath.Join(base, st.Path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return err
	}
	if st.Target == DropTargetTwinIncoming {
		r.notePlantedBatch(st.Path, content)
	}
	return nil
}

// notePlantedBatch records the batch id of a delivery the HARNESS dropped into
// the twin's inbox, so an assertion can tell it from one the connector made.
//
// The id is read from the manifest, because the id is what the twin keys the
// batch by. A drop whose manifest does not parse - a data file, a deliberately
// malformed plant - still registers its directory, which is the id by
// convention here: the fallback exists so that a plant we failed to read is
// attributed to the harness rather than credited to the candidate.
func (r *runner) notePlantedBatch(path string, content []byte) {
	if r.plantedBatches == nil {
		r.plantedBatches = map[string]bool{}
	}
	var manifest struct {
		BatchID string `json:"batch_id"`
	}
	if err := json.Unmarshal(content, &manifest); err == nil && manifest.BatchID != "" {
		r.plantedBatches[manifest.BatchID] = true
	}
	if dir := filepath.Base(filepath.Dir(path)); dir != "" && dir != "." && dir != string(filepath.Separator) {
		r.plantedBatches[dir] = true
	}
}

// connectorRunIDFor returns the run id one invocation is given.
//
// It is the scenario's for every phase but the ones marked fresh_run_id, which
// get a derived id. Derived and not drawn: two runs of the scenario have to hand
// the connector the same string, or nothing about the run is reproducible.
func (r *runner) connectorRunIDFor(st Step, phase string) string {
	if !st.FreshRunID {
		return r.runID
	}
	return r.runID + "_n" + phase
}

// connectorArgv builds the fixed CLI of BUILD-SPEC 9.
func (r *runner) connectorArgv(runID string) []string {
	cmdline := r.opts.ConnectorCmd
	if strings.TrimSpace(cmdline) == "" {
		cmdline = filepath.Join(r.opts.RepoRoot, DefaultConnector)
	}
	argv := strings.Fields(cmdline)
	return append(argv,
		"run",
		"--run-id", runID,
		"--erp-base-url", r.erp.BaseURL,
		"--twin-base-url", r.twin.BaseURL,
		"--erp-export-dir", r.dirs.ERPExport,
		"--twin-drop-dir", r.dirs.TwinDrop,
		"--state-dir", r.dirs.State,
		"--report-dir", r.dirs.Reports,
	)
}

// connectorEnv builds the connector's environment. It carries exactly the six
// published credential variables and no admin token.
func (r *runner) connectorEnv(extra map[string]string, phase, runID string) []string {
	env := append(os.Environ(),
		"ERP_CLIENT_ID="+r.creds.ClientID,
		"ERP_CLIENT_SECRET="+r.creds.ClientSecret,
		"TWIN_CLIENT_ID="+r.twinID,
		"TWIN_CLIENT_SECRET="+r.twinSec,
		"SOAP_USERNAME="+r.creds.SOAPUsername,
		"SOAP_PASSWORD="+r.creds.SOAPPassword,
	)
	if phase != "" {
		env = append(env, "BLP_PHASE="+phase)
	}
	// The run id reaches the connector through --run-id and through nothing else.
	// It is a parameter here only so a fresh-run-id phase cannot silently take the
	// scenario's, and the compiler is what enforces that.
	_ = runID
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// runConnector invokes the connector once.
//
// When killNext is set the invocation is started and not waited for, because the
// next step is a kill_connector that owns the reaping. That is how the
// crash-and-resume scenario is expressed without a step kind for "wait": the
// script reads run_connector, kill_connector, run_connector, and the pairing is
// positional and visible in the file.
func (r *runner) runConnector(st Step, killNext bool) error {
	if r.pending != nil {
		return errors.New("a connector invocation is already running")
	}
	phase := st.Phase
	if phase == "" {
		phase = strconv.Itoa(len(r.outcome.Connector) + 1)
	}
	runID := r.connectorRunIDFor(st, phase)
	if runID != r.runID {
		r.logf("connector phase %s runs under a fresh run id %s: this is the night after, "+
			"not a resume", phase, runID)
	}
	argv := r.connectorArgv(runID)
	if _, err := exec.LookPath(argv[0]); err != nil {
		if _, serr := os.Stat(argv[0]); serr != nil {
			return fmt.Errorf("connector %q not found or not executable: %w", argv[0], err)
		}
	}
	stdoutPath := filepath.Join(r.dirs.Logs, "connector-"+phase+".stdout")
	stderrPath := filepath.Join(r.dirs.Logs, "connector-"+phase+".stderr")
	so, err := os.Create(stdoutPath)
	if err != nil {
		return err
	}
	se, err := os.Create(stderrPath)
	if err != nil {
		so.Close()
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = r.opts.RepoRoot
	cmd.Env = r.connectorEnv(st.EnvExtra, phase, runID)
	cmd.Stdout = so
	cmd.Stderr = se
	// A new process group again, so a connector that spawned a worker cannot
	// leave it behind and so a SIGKILL in the crash scenario takes the whole
	// tree down rather than orphaning half of it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rec := ConnectorRun{Phase: phase, Argv: argv, StdoutPath: stdoutPath, StderrPath: stderrPath}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		so.Close()
		se.Close()
		rec.ExitCode = -1
		r.outcome.Connector = append(r.outcome.Connector, rec)
		return fmt.Errorf("starting the connector: %w", err)
	}
	r.logf("connector phase %s started: %s", phase, strings.Join(argv, " "))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	p := &pendingConnector{cmd: cmd, rec: &rec, done: done, start: start, files: []*os.File{so, se}}

	if killNext {
		r.pending = p
		return nil
	}
	timeout := st.TimeoutS
	if timeout <= 0 {
		timeout = DefaultConnectorTimeoutS
	}
	r.finishConnector(p, time.Duration(timeout)*time.Second)
	return nil
}

// finishConnector waits for an invocation, applying the harness safety timeout.
func (r *runner) finishConnector(p *pendingConnector, timeout time.Duration) {
	select {
	case err := <-p.done:
		r.recordExit(p, err, false)
	case <-time.After(timeout):
		// BUILD-SPEC 17.4: the timeout exists so a hung submission cannot hang a
		// grading run. It is a harness event and never a score, which is why it
		// is recorded on the invocation and reported through HarnessErrors
		// rather than through a failed assertion.
		p.rec.TimedOut = true
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		r.recordExit(p, <-p.done, false)
		r.harness("connector phase %s exceeded the %s safety timeout; this is a harness event, not a score",
			p.rec.Phase, timeout)
	}
}

// recordExit finishes an invocation's record.
func (r *runner) recordExit(p *pendingConnector, err error, killed bool) {
	rec := p.rec
	rec.WallMs = time.Since(p.start).Milliseconds()
	rec.Killed = killed
	rec.ExitCode = 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rec.ExitCode = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				rec.Signal = ws.Signal().String()
			}
		} else {
			rec.ExitCode = -1
			r.harness("waiting for connector phase %s: %v", rec.Phase, err)
		}
	}
	if st := p.cmd.ProcessState; st != nil {
		if ru, ok := st.SysUsage().(*syscall.Rusage); ok && ru != nil {
			rec.MaxRSSKB = int64(ru.Maxrss)
		}
	}
	for _, f := range p.files {
		_ = f.Sync()
		_ = f.Close()
	}
	rec.StderrPanic = looksLikeCrash(rec.StderrPath)
	r.outcome.Connector = append(r.outcome.Connector, *rec)
	r.logf("connector phase %s exited %d%s in %d ms", rec.Phase, rec.ExitCode,
		map[bool]string{true: " (signalled by the harness)"}[killed], rec.WallMs)
	if r.pending == p {
		r.pending = nil
	}
}

// crashMarkers are the language-neutral signs of an unhandled crash. The list is
// short on purpose: it must never match a connector that merely logged the word
// "error", because a false positive here reads as an accusation.
var crashMarkers = []string{
	"panic: ",
	"goroutine 1 [running]",
	"Traceback (most recent call last)",
	"Exception in thread \"main\"",
	"Unhandled exception",
	"fatal error: ",
}

// looksLikeCrash reports whether a stderr transcript shows an unhandled crash.
func looksLikeCrash(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(raw)
	for _, m := range crashMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// killConnectorStep waits for the step's trigger and signals the running
// connector's process group.
func (r *runner) killConnectorStep(st Step) error {
	p := r.pending
	if p == nil {
		return errors.New("no connector invocation is running to signal")
	}
	sig, err := parseSignal(st.Signal)
	if err != nil {
		return err
	}
	if err := r.waitTrigger(st.After, p); err != nil {
		return err
	}
	r.logf("connector phase %s: sending %s after %s", p.rec.Phase, st.Signal, st.After)
	r.killPending(sig, true)
	return nil
}

// killPending signals the pending invocation's group and reaps it.
func (r *runner) killPending(sig syscall.Signal, deliberate bool) {
	p := r.pending
	if p == nil {
		return
	}
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, sig)
	}
	select {
	case err := <-p.done:
		r.recordExit(p, err, deliberate)
	case <-time.After(TerminateGrace):
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		r.recordExit(p, <-p.done, deliberate)
	}
}

// waitTrigger blocks until the step's trigger condition holds, the connector
// exits on its own, or the harness deadline passes.
//
// Every condition is read from an admin counter, so the crash lands at the same
// logical point regardless of machine speed: "after the ERP has accepted a
// document" is a fact about the server, not about a duration.
func (r *runner) waitTrigger(after string, p *pendingConnector) error {
	if after == KillAfterImmediate {
		return nil
	}
	deadline := time.Now().Add(triggerDeadline)
	for {
		select {
		case err := <-p.done:
			// The connector finished before the trigger fired. Reaping it here
			// keeps the record honest: no crash happened, and the assertions
			// will see a complete run.
			r.recordExit(p, err, false)
			return fmt.Errorf("connector phase %s exited before the %s trigger fired", p.rec.Phase, after)
		default:
		}
		ok, err := r.triggerHolds(after)
		if err == nil && ok {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the %s trigger did not fire within %s", after, triggerDeadline)
		}
		time.Sleep(triggerPollInterval)
	}
}

// triggerHolds reads the counter behind a trigger.
func (r *runner) triggerHolds(after string) (bool, error) {
	switch after {
	case KillAfterFirstERPPost:
		var m ERPMetrics
		if err := r.erpc.get(erpPathMetrics, &m); err != nil {
			return false, err
		}
		return m.DocumentsCreated > 0, nil
	case KillAfterFirstTwinBatch:
		var m TwinMetrics
		if err := r.twc.get(twinPathMetrics, &m); err != nil {
			return false, err
		}
		return m.Batches > 0, nil
	}
	return false, fmt.Errorf("unknown trigger %q", after)
}

// connectorRunID derives the --run-id of a scenario. It is a pure function of
// the scenario and the seed, so both invocations of a crash-and-resume scenario
// receive the same id, which is what BUILD-SPEC 9 requires and what makes
// "resume, not restart" observable.
func connectorRunID(scenario string, seedValue int64) string {
	sum := sha256.Sum256([]byte("blp-grader-run|" + scenario + "|" + strconv.FormatInt(seedValue, 10)))
	return "run_" + strings.ToLower(scenario) + "_" + hex.EncodeToString(sum[:4])
}

// deriveSecret derives a per-run secret from a purpose and the seed. Nothing here
// reads a clock or an unseeded random source, so a run is reproducible, and the
// secrets still differ between two seeds so a submission cannot hard-code one.
func deriveSecret(purpose string, seedValue int64) string {
	sum := sha256.Sum256([]byte("blp-grader-secret|" + purpose + "|" + strconv.FormatInt(seedValue, 10)))
	return hex.EncodeToString(sum[:16])
}

// writeEvidence writes v as indented JSON into the evidence directory and
// returns the path. Every finding points at a file this wrote.
func (r *runner) writeEvidence(name string, v any) string {
	path := filepath.Join(r.dirs.Evidence, name)
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return path
	}
	_ = os.WriteFile(path, append(body, '\n'), 0o644)
	return path
}
