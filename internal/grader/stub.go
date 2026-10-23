package grader

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// StubMarker is what the connector stub shipped with the exercise reports for
// --version. A candidate replaces the stub, so this string disappears from a
// submission on the first commit that matters.
const StubMarker = "blp-connector-stub"

// stubProbeTimeout bounds the --version probe. It is a courtesy feature, so it
// may never delay a run by more than a moment, and it may never fail one.
const stubProbeTimeout = 5 * time.Second

// NoteIfConnectorIsStub prints an orientation note when the connector is still
// the stub the exercise ships with.
//
// The first command a candidate runs is a scenario, and against an untouched
// checkout it prints a page of failed assertions. That is the truth, and it is
// also the moment somebody decides whether this exercise is broken or fair. The
// contract already requires the connector to answer --version, so the harness
// asks, and if the answer is the stub's it says so and gives the order to work
// in before the page of red.
//
// It is silent on every error, including a connector that does not implement
// --version yet: guessing "you have not started" at somebody who has is worse
// than saying nothing.
func NoteIfConnectorIsStub(connectorCmd, repoRoot string, w io.Writer) {
	if w == nil {
		return
	}
	cmd := strings.TrimSpace(connectorCmd)
	if cmd == "" {
		cmd = filepath.Join(repoRoot, DefaultConnector)
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stubProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, fields[0],
		append(fields[1:], "--version")...).CombinedOutput()
	if err != nil && len(out) == 0 {
		return
	}
	if !strings.Contains(string(out), StubMarker) {
		return
	}
	fmt.Fprint(w, stubNote)
}

// stubNote is the orientation. It names files rather than repeating them,
// because a note that grows into a second brief stops being read.
const stubNote = `
connector/run.sh is still the stub this exercise ships with, so there is nothing
to grade yet and everything below is what an empty run looks like. That is the
starting point, not a broken harness.

The order to work in, each phase with its own acceptance in CHALLENGE.md:

  0. exit codes and the three report files   connector/README.md
  1. master data into the twin, then a delta CHALLENGE.md, phase 1
  2. the legacy delivery, relayed unparsed   CHALLENGE.md, phase 2
  3. the rate table over SOAP                CHALLENGE.md, phase 3
  4. post, acknowledge, reconcile            CHALLENGE.md, phase 4

Replace connector/run.sh with your connector, in any language, and run this
again. Every assertion below prints what it expected and what it got.

`
