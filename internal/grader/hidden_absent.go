//go:build !hidden

package grader

// HiddenKindsBuilt reports whether the hidden assertion kinds are compiled in.
//
// Without the `hidden` build tag they are not, and nothing here names them: a
// leaked scenario file from grading/hidden/scenarios/ then fails to load with
// "unknown kind", and the list of known kinds it prints does not include the ones
// it was asking for. That is the whole mechanism. It is not secrecy about
// semantics - BUILD-SPEC 0.1.5 requires everything graded to be published, and
// every hidden check re-combines a published rule - it protects which
// combinations we chose to look at, so a hidden scenario stays a test of reading
// the documents rather than of reading our answer key.
//
// Build the grader with `go build -tags hidden ./cmd/grade` to run them. The
// candidate bundle ships neither the tag's file nor the scenarios that need it.
const HiddenKindsBuilt = false
