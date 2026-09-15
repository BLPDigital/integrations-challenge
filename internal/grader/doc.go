// Package grader is the grading harness of the integrations challenge: the
// scenario model, the assertion vocabulary, the process supervisor that runs the
// two services and the candidate's connector, the scoring model of BUILD-SPEC 13
// and the reviewer scorecard.
//
// Three properties are load-bearing and every file in this package is written to
// keep them.
//
// # It never reads the candidate's source
//
// No assertion opens a file the candidate wrote as code, greps for a library
// name, or asks which language or channel was used. Everything graded is read
// back from the two services' admin surfaces and from the three report files the
// connector contract requires. That is what makes a Python submission and a Go
// submission comparable, and it is why the state digest - a hash over logical
// content only - is the primary assertion.
//
// # It never reads the wall clock for a score
//
// The services run on the virtual clock of internal/simclock, the file scanner is
// driven by POST /admin/v1/inbox/scan and never by a timer, and the only real
// duration this package measures is the safety timeout of a connector invocation.
// A timeout is reported as a harness event with zero points, never as a failed
// assertion: a slow machine must not change a score. Health polling has a real
// deadline for the same reason - it protects the harness, it grades nothing.
//
// # Every failure points at a file
//
// A [Finding] carries expected, actual and an evidence path into the run's own
// output tree, and the runner writes the state it asserted over to that tree
// before asserting. A debrief is then a matter of opening a file rather than of
// re-running the harness and hoping.
//
// The entry points are [Run] for one scenario, [Selfcheck] for the public series
// with per-assertion output, [Score] for a full submission, and [GoldenDiff] for
// the Go task's projection.
package grader
