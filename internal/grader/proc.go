package grader

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// HealthDeadline and HealthInterval bound the readiness poll of a started
// service: BUILD-SPEC 16.4 fixes them at 10 s and 20 ms.
//
// This is the one place in the package that reads a real clock, and it grades
// nothing: a service that will not start is a harness failure, and the deadline
// only decides how long the harness waits before saying so.
const (
	// HealthDeadline is how long a service gets to answer GET /healthz.
	HealthDeadline = 10 * time.Second
	// HealthInterval is the poll interval.
	HealthInterval = 20 * time.Millisecond
)

// TerminateGrace is how long a process group gets between SIGTERM and SIGKILL.
const TerminateGrace = 3 * time.Second

// PortReleaseDeadline bounds the wait for a torn-down service's port to come
// free. A port still bound after teardown means a child outlived its group, and
// the next scenario would inherit it, so the runner reports it rather than
// letting a stale server answer the next scenario's assertions.
const PortReleaseDeadline = 5 * time.Second

// A Service is one child service process the harness owns.
type Service struct {
	// Name is "erp" or "miniblp", as it appears in the announcement line.
	Name string
	// Addr is the address the child actually bound, read from its announcement.
	Addr string
	// BaseURL is "http://" + Addr.
	BaseURL string
	// AdminToken gates the child's admin surface.
	AdminToken string
	// LogPath is the file the child's stdout and stderr were written to.
	LogPath string

	cmd     *exec.Cmd
	logFile *os.File
	stdout  io.ReadCloser
	mu      sync.Mutex
	waited  bool
	waitErr error
}

// startOptions describes a service to start.
type startOptions struct {
	// name is the service name, used in errors and in the announcement check.
	name string
	// argv is the command and its arguments. The binary is expected to accept
	// --listen 127.0.0.1:0 and to announce the address it bound.
	argv []string
	// env is the child's complete environment.
	env []string
	// dir is the child's working directory.
	dir string
	// logPath is where stdout and stderr go.
	logPath string
	// adminToken is recorded on the Service for the admin client.
	adminToken string
}

// startService starts one service as a child in a new process group, reads the
// address it announced on stdout and waits for GET /healthz.
//
// A new process group per child, and teardown by signalling the group, is what
// makes cleanup total: a service that spawned a helper cannot leave it behind
// holding a port, and the next scenario's "assert the output tree is empty"
// check would not catch that.
func startService(opts startOptions) (*Service, error) {
	if len(opts.argv) == 0 {
		return nil, fmt.Errorf("grader: %s: no command", opts.name)
	}
	logFile, err := os.Create(opts.logPath)
	if err != nil {
		return nil, fmt.Errorf("grader: %s: %w", opts.name, err)
	}
	cmd := exec.Command(opts.argv[0], opts.argv[1:]...)
	cmd.Env = opts.env
	cmd.Dir = opts.dir
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("grader: %s: %w", opts.name, err)
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("grader: %s: starting %s: %w", opts.name, opts.argv[0], err)
	}
	svc := &Service{
		Name: opts.name, AdminToken: opts.adminToken, LogPath: opts.logPath,
		cmd: cmd, logFile: logFile, stdout: stdout,
	}

	// The announcement is the first line on stdout and nothing else is written
	// before it, so a single buffered read is enough and no timer is involved:
	// if the child dies instead of announcing, the pipe closes and the read
	// returns io.EOF.
	br := bufio.NewReader(stdout)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		svc.teardown()
		return nil, fmt.Errorf("grader: %s: no address announcement on stdout (%v); see %s",
			opts.name, err, opts.logPath)
	}
	var ann struct {
		Svc  string `json:"svc"`
		Addr string `json:"addr"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ann); err != nil {
		svc.teardown()
		return nil, fmt.Errorf("grader: %s: first stdout line is not the address announcement: %q",
			opts.name, strings.TrimSpace(line))
	}
	if ann.Addr == "" {
		svc.teardown()
		return nil, fmt.Errorf("grader: %s: address announcement carried no addr: %q", opts.name, line)
	}
	svc.Addr = ann.Addr
	svc.BaseURL = "http://" + ann.Addr

	// Everything the child says afterwards is its structured request log, which
	// belongs in the evidence tree. Copying it in a goroutine keeps the pipe
	// drained: a service blocked writing its log would look like a hung service.
	go func() {
		_, _ = io.Copy(logFile, br)
	}()

	if err := waitHealthy(svc.BaseURL + "/healthz"); err != nil {
		svc.teardown()
		return nil, fmt.Errorf("grader: %s at %s: %w; see %s", opts.name, ann.Addr, err, opts.logPath)
	}
	return svc, nil
}

// waitHealthy polls url until it answers 200 or the deadline passes.
func waitHealthy(url string) error {
	client := &http.Client{Timeout: HealthInterval * 10}
	deadline := time.Now().Add(HealthDeadline)
	var last error
	for {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("healthz answered %d", resp.StatusCode)
		} else {
			last = err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("not healthy within %s: %w", HealthDeadline, last)
		}
		time.Sleep(HealthInterval)
	}
}

// Stop tears the service down: SIGTERM to the process group, a grace period,
// SIGKILL, Wait, and a check that the port came free.
//
// It is safe to call more than once; the second call is a no-op. The runner
// defers it, so a panic in the middle of a scenario still releases the children.
func (s *Service) Stop() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.waited {
		s.mu.Unlock()
		return s.waitErr
	}
	s.mu.Unlock()
	addr := s.Addr
	err := s.teardown()
	if addr != "" {
		if perr := waitPortReleased(addr); perr != nil {
			if err == nil {
				err = perr
			} else {
				err = fmt.Errorf("%w; %v", err, perr)
			}
		}
	}
	return err
}

// teardown signals the process group and reaps the child.
func (s *Service) teardown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waited {
		return s.waitErr
	}
	s.waited = true
	if s.cmd.Process != nil {
		pgid := -s.cmd.Process.Pid
		_ = syscall.Kill(pgid, syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- s.cmd.Wait() }()
		select {
		case err := <-done:
			s.waitErr = classifyExit(err)
		case <-time.After(TerminateGrace):
			// The grace period expired. SIGKILL the whole group, including any
			// helper the child started, and reap.
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			s.waitErr = classifyExit(<-done)
		}
	}
	if s.logFile != nil {
		_ = s.logFile.Sync()
		_ = s.logFile.Close()
	}
	return s.waitErr
}

// classifyExit turns a Wait error into an error worth reporting. A service that
// exited because we signalled it exited correctly, and so did one that returned
// zero; anything else is worth a line in the report.
func classifyExit(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			switch ws.Signal() {
			case syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT:
				return nil
			}
		}
		if ee.ExitCode() == 0 {
			return nil
		}
	}
	return err
}

// waitPortReleased waits for addr to accept a fresh bind, i.e. for the previous
// listener to be really gone.
func waitPortReleased(addr string) error {
	deadline := time.Now().Add(PortReleaseDeadline)
	var last error
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return nil
		}
		last = err
		if !time.Now().Before(deadline) {
			return fmt.Errorf("port %s still bound %s after teardown: %w", addr, PortReleaseDeadline, last)
		}
		time.Sleep(HealthInterval)
	}
}

// parseSignal maps a scenario file's signal name to a signal.
func parseSignal(name string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SIGKILL", "KILL":
		return syscall.SIGKILL, nil
	case "SIGTERM", "TERM":
		return syscall.SIGTERM, nil
	case "SIGINT", "INT":
		return syscall.SIGINT, nil
	case "":
		return 0, fmt.Errorf("signal must be set")
	}
	return 0, fmt.Errorf("signal %q: want SIGKILL, SIGTERM or SIGINT", name)
}

// A ConnectorRun records one invocation of the candidate's connector.
type ConnectorRun struct {
	// Phase names the invocation within the scenario, e.g. "a" and "b".
	Phase string `json:"phase"`
	// Argv is the command line, verbatim, so a debrief can reproduce it.
	Argv []string `json:"argv"`
	// ExitCode is the process exit code, -1 when the process was signalled.
	ExitCode int `json:"exit_code"`
	// Signal names the signal that ended the process, "" when it exited.
	Signal string `json:"signal,omitempty"`
	// Killed reports that the harness signalled it on purpose, i.e. the
	// crash-and-resume step. A deliberate crash is not a failure.
	Killed bool `json:"killed_by_harness"`
	// TimedOut reports that the safety timeout fired. BUILD-SPEC 17.4: this is
	// a harness event and never a score.
	TimedOut bool `json:"timed_out"`
	// StdoutPath and StderrPath are the captured streams. They are scanned by
	// the data-minimization assertions, which is the only thing they are read
	// for: nothing grades what a connector chooses to print.
	StdoutPath string `json:"stdout_path"`
	StderrPath string `json:"stderr_path"`
	// WallMs is the real elapsed time. Reported, never scored.
	WallMs int64 `json:"wall_ms"`
	// StderrPanic reports a Go panic, a Python traceback or a Java stack trace
	// in stderr. It is a language-neutral marker of a crash the connector did
	// not handle, which is a different thing from a non-zero exit code.
	StderrPanic bool `json:"stderr_panic"`
	// MaxRSSKB is the peak resident set size of the invocation in kibibytes, as
	// the kernel reported it. It is scored nowhere in core, per BUILD-SPEC 17.4:
	// a JVM or .NET baseline must never cost a candidate a point, so this exists
	// for the stretch scale scenario and for information.
	MaxRSSKB int64 `json:"max_rss_kb"`
}

// runOnce runs a command to completion and returns its combined output. It is
// the one-shot counterpart of startService, used for the candidate's setup step.
func runOnce(path, dir string) (string, error) {
	cmd := exec.Command(path)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
