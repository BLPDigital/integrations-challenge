package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunPrintsTheChosenAddress asserts the harness contract of BUILD-SPEC 16.4:
// --listen 127.0.0.1:0 works and the chosen address is printed as one JSON line
// on stdout, before anything is served, so the grader never needs a fixed port.
func TestRunPrintsTheChosenAddress(t *testing.T) {
	dir := t.TempDir()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- run([]string{
			"--listen", "127.0.0.1:0",
			"--data-dir", filepath.Join(dir, "data"),
			"--scenario", "S0",
			"--seed", "20260416",
			"--no-chaos",
			"--admin-token", "t",
		}, pw)
	}()

	reader := bufio.NewReader(pr)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the address line: %v", err)
	}
	var addr struct {
		Svc  string `json:"svc"`
		Addr string `json:"addr"`
	}
	if err := json.Unmarshal([]byte(line), &addr); err != nil {
		t.Fatalf("the first line of stdout is not one JSON object: %q", line)
	}
	if addr.Svc != "miniblp" {
		t.Fatalf("svc = %q, want miniblp", addr.Svc)
	}
	if !strings.HasPrefix(addr.Addr, "127.0.0.1:") || strings.HasSuffix(addr.Addr, ":0") {
		t.Fatalf("addr = %q, want a concrete port on the loopback interface", addr.Addr)
	}

	// The server is serving on the address it printed.
	resp, err := http.Get("http://" + addr.Addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("healthz body = %s", body)
	}

	// The directory tree of the file channel exists, so a scan has somewhere to
	// look on the very first request.
	for _, sub := range []string{"inbox/incoming", "inbox/processing", "inbox/processed",
		"inbox/rejected", "inbox/receipts", "outbox", "store"} {
		if _, err := os.Stat(filepath.Join(dir, "data", filepath.FromSlash(sub))); err != nil {
			t.Fatalf("directory %s: %v", sub, err)
		}
	}

	// Draining stdout so the pipe never blocks the server's log writes; the
	// process exits with the test binary.
	go func() { _, _ = io.Copy(io.Discard, reader) }()
}

// TestRunRejectsAnUnknownArgument asserts the flag contract fails loudly rather
// than ignoring a typo.
func TestRunRejectsAnUnknownArgument(t *testing.T) {
	err := run([]string{"--data-dir", t.TempDir(), "unexpected"}, io.Discard)
	if err == nil {
		t.Fatal("want an error for a positional argument")
	}
}
