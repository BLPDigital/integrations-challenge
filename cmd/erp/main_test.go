package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// TestParseFlags asserts the command line, including the two chaos flags and the
// documented defaults.
func TestParseFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		check func(*testing.T, options)
	}{
		{
			name: "defaults",
			args: nil,
			check: func(t *testing.T, o options) {
				if o.listen != erp.DefaultListenAddr {
					t.Fatalf("listen %q, want %q", o.listen, erp.DefaultListenAddr)
				}
				if o.cfg.Scenario != seed.ScenarioS0 || o.cfg.Seed != 0 {
					t.Fatalf("scenario %q seed %d", o.cfg.Scenario, o.cfg.Seed)
				}
				if !o.cfg.Chaos {
					t.Fatal("chaos defaults to on, because that is what grading uses")
				}
				if o.cfg.ExportDir != erp.DefaultExportDir {
					t.Fatalf("export dir %q", o.cfg.ExportDir)
				}
				if o.cfg.Quota != 0 {
					t.Fatalf("quota %d, want 0 for unlimited", o.cfg.Quota)
				}
			},
		},
		{
			name: "full command line",
			args: []string{
				"--listen", "127.0.0.1:0", "--scenario", "S1", "--seed", "42",
				"--quota", "1200", "--export-dir", "/tmp/drop", "--admin-token", "tok",
				"--soap-truncate-at", "7",
			},
			check: func(t *testing.T, o options) {
				if o.listen != "127.0.0.1:0" || o.cfg.Scenario != "S1" || o.cfg.Seed != 42 ||
					o.cfg.Quota != 1200 || o.cfg.ExportDir != "/tmp/drop" ||
					o.cfg.Creds.AdminToken != "tok" || o.cfg.SOAPTruncateAt != 7 {
					t.Fatalf("options %+v", o)
				}
			},
		},
		{
			name: "no-chaos wins over chaos",
			args: []string{"--chaos", "--no-chaos"},
			check: func(t *testing.T, o options) {
				if o.cfg.Chaos {
					t.Fatal("--no-chaos must win, whatever the argument order")
				}
			},
		},
		{
			name: "chaos off alone",
			args: []string{"--no-chaos"},
			check: func(t *testing.T, o options) {
				if o.cfg.Chaos {
					t.Fatal("--no-chaos did not turn injection off")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseFlags(tc.args, io.Discard)
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			tc.check(t, opts)
		})
	}
}

// TestParseFlagsRejectsGarbage asserts an unusable command line is an error rather
// than a silently substituted default.
func TestParseFlagsRejectsGarbage(t *testing.T) {
	for _, args := range [][]string{
		{"--seed", "not-a-number"},
		{"--unknown-flag"},
		{"stray-argument"},
		{"--listen", ""},
	} {
		if _, err := parseFlags(args, io.Discard); err == nil {
			t.Fatalf("parseFlags(%v) accepted an unusable command line", args)
		}
	}
}

// TestRunAnnouncesItsAddressAndServes asserts the harness contract of section
// 16.4: --listen 127.0.0.1:0 binds a free port and prints the chosen address as
// ONE JSON line on stdout, before serving, so no port is ever hard-coded.
func TestRunAnnouncesItsAddressAndServes(t *testing.T) {
	dir := t.TempDir()
	stdout := &syncBuffer{}
	stop := make(chan struct{})
	ready := make(chan string, 1)
	done := make(chan error, 1)

	go func() {
		done <- run([]string{
			"--listen", "127.0.0.1:0", "--scenario", "S0", "--seed", "20260416",
			"--export-dir", dir, "--no-chaos",
		}, stdout, func(addr string) { ready <- addr }, stop)
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("run returned before it was ready: %v", err)
	}

	// Exactly one line, and it is the announcement.
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout carries %d lines before the first request:\n%s", len(lines), stdout.String())
	}
	var announced addrLine
	if err := json.Unmarshal([]byte(lines[0]), &announced); err != nil {
		t.Fatalf("the announcement is not one JSON object: %q", lines[0])
	}
	if announced.Svc != "erp" {
		t.Fatalf("svc %q, want erp", announced.Svc)
	}
	if announced.Addr != addr {
		t.Fatalf("announced %q, bound %q", announced.Addr, addr)
	}
	host, port, err := net.SplitHostPort(announced.Addr)
	if err != nil {
		t.Fatalf("the announced address is not host:port: %q", announced.Addr)
	}
	if host != "127.0.0.1" || port == "0" {
		t.Fatalf("announced %q: the port must be the one actually bound", announced.Addr)
	}

	resp, err := http.Get("http://" + addr + erp.RouteHealthz)
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: status %d", resp.StatusCode)
	}
	var health struct {
		Status   string `json:"status"`
		Scenario string `json:"scenario"`
		Seed     int64  `json:"seed"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if health.Status != "ok" || health.Scenario != "S0" || health.Seed != 20260416 {
		t.Fatalf("healthz says %+v", health)
	}

	// The export drop is written on startup, sentinels included.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read export dir: %v", err)
	}
	dataFiles, sentinels := 0, 0
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case erp.ExportDataSuffix:
			dataFiles++
		case erp.ExportOKSuffix:
			sentinels++
		}
	}
	if dataFiles == 0 || dataFiles != sentinels {
		t.Fatalf("the export drop holds %d data files and %d sentinels", dataFiles, sentinels)
	}

	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := http.Get("http://" + addr + erp.RouteHealthz); err == nil {
		t.Fatal("the listener is still accepting after shutdown")
	}
}

// TestRunRejectsAnUnknownScenario asserts a bad scenario fails at startup rather
// than serving an empty landscape.
func TestRunRejectsAnUnknownScenario(t *testing.T) {
	err := run([]string{"--listen", "127.0.0.1:0", "--scenario", "NOPE", "--export-dir", ""},
		io.Discard, nil, nil)
	if err == nil {
		t.Fatal("an unknown scenario started a server")
	}
	if !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("the error does not name the scenario: %v", err)
	}
}

// syncBuffer is a bytes.Buffer that is safe to read while the server writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what has been written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
