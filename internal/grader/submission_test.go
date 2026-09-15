package grader

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The writable set is written down twice, in Go for the grader and in POSIX sh
// for the candidate's own `make check`. Two copies of one rule in two languages
// is a rule that drifts, so this reads the script and compares.
func TestTheWritableSetAgreesWithTheScript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "tools", "verify-pristine.sh"))
	if err != nil {
		t.Fatalf("reading the script: %v", err)
	}
	script := string(raw)
	for _, p := range WritablePaths {
		pattern := strings.TrimSuffix(p, "/")
		if strings.HasSuffix(p, "/") {
			pattern += "/*)"
		} else {
			pattern += ")"
		}
		if !strings.Contains(script, pattern) {
			t.Errorf("tools/verify-pristine.sh has no case for %q; the two copies of the "+
				"writable set have drifted", pattern)
		}
	}
	// And the other direction: every case in the script is in the Go list, apart
	// from the manifest, which every tree carries its own copy of.
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, "return 0 ;;") || !strings.Contains(line, ")") {
			continue
		}
		pat := strings.TrimSpace(strings.SplitN(line, ")", 2)[0])
		rel := strings.TrimSuffix(pat, "/*")
		if rel == manifestExempt {
			continue
		}
		if !IsWritablePath(rel) && !IsWritablePath(rel+"/x") {
			t.Errorf("the script allows %q and the Go writable set does not", pat)
		}
	}
}

func TestIsWritablePath(t *testing.T) {
	cases := map[string]bool{
		"connector/run.sh":                     true,
		"connector/src/deep/file.py":           true,
		"internal/importer/kredexp/kredexp.go": true,
		"internal/importer/all/all.go":         true,
		"DECISIONS.md":                         true,
		"internal/importer/all/other.go":       false,
		"internal/miniblp/match.go":            false,
		"internal/importer/canonical/csv.go":   false,
		"docs/spec.md":                         false,
		"grading/scenarios/S1.json":            false,
		"connectorX/run.sh":                    false,
		"":                                     false,
	}
	for path, want := range cases {
		if got := IsWritablePath(path); got != want {
			t.Errorf("IsWritablePath(%q) = %v, want %v", path, got, want)
		}
	}
}

// The point of the whole exercise: a submission that edits our services must not
// be able to change what runs. This builds a graded tree from a submission that
// tampers with the twin's matching engine and deletes one of its own files, and
// asserts the tamper is gone, the deletion stuck, and the candidate's own work is
// what got copied.
func TestAGradedTreeTakesOnlyTheCandidatesOwnPaths(t *testing.T) {
	pristine := repoRoot(t)
	sub := t.TempDir()
	dest := filepath.Join(t.TempDir(), "graded-tree")

	write := func(rel, content string) {
		p := filepath.Join(sub, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("connector/run.sh", "#!/bin/sh\necho mine\n")
	write("connector/mine.py", "print('mine')\n")
	write("DECISIONS.md", "# mine\n")
	// The tamper: a protected file of ours, changed.
	write("internal/miniblp/match.go", "package miniblp // TAMPERED\n")

	if err := BuildGradedTree(pristine, sub, dest); err != nil {
		t.Fatalf("BuildGradedTree: %v", err)
	}

	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s from the graded tree: %v", rel, err)
		}
		return string(b)
	}

	if got := read("internal/miniblp/match.go"); strings.Contains(got, "TAMPERED") {
		t.Error("the tampered matching engine reached the graded tree")
	}
	if got := read("connector/run.sh"); !strings.Contains(got, "echo mine") {
		t.Error("the candidate's own connector did not reach the graded tree")
	}
	if got := read("connector/mine.py"); !strings.Contains(got, "mine") {
		t.Error("the candidate's own source file did not reach the graded tree")
	}
	if got := read("DECISIONS.md"); !strings.Contains(got, "# mine") {
		t.Error("the candidate's decision log did not reach the graded tree")
	}
	// connector/ came wholly from the submission, so our stub's setup.sh is gone
	// rather than left behind next to their run.sh.
	if _, err := os.Stat(filepath.Join(dest, "connector", "setup.sh")); err == nil {
		t.Error("our connector/setup.sh survived under a connector/ the candidate replaced")
	}
	// The importer the submission never touched has to be ours, or the tree does
	// not compile.
	if _, err := os.Stat(filepath.Join(dest, "internal", "importer", "kredexp")); err != nil {
		t.Errorf("the untouched importer package is missing from the graded tree: %v", err)
	}
	// And the run's own inputs are there: the scenarios and both services.
	for _, rel := range []string{
		"grading/scenarios/S1.json", "cmd/erp/main.go", "cmd/miniblp/main.go", "go.mod",
	} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s is missing from the graded tree: %v", rel, err)
		}
	}
}

// The manifest a submission is checked against is OURS. A submission that ships
// its own regenerated manifest over its own edits must not pass.
//
// Both of these build their own pristine tree rather than using the repository,
// so they hold wherever the package is compiled, including inside the candidate
// bundle while its manifest is being regenerated.
// The check reads the manifest the SUBMISSION carries, because ours is the wrong
// question: it moves, and a candidate's fork does not move with it.
//
// This test also pins the residual honestly. A submission that rewrites a
// protected file AND regenerates its own manifest over the edit is NOT caught,
// and that is accepted: the score cannot depend on it, because BuildGradedTree
// takes only the candidate-writable paths from a submission and everything else
// from our pristine tree, so the edited file never reaches a running service.
// The behavior this replaced did not catch that candidate either. It only ever
// caught our own drift, and reported it as their tampering.
func TestTheProtectedCheckReadsTheManifestTheSubmissionWasGiven(t *testing.T) {
	pristine := newPristine(t, map[string]string{
		"internal/miniblp/match.go": "package miniblp // ours, and since changed\n",
		"docs/spec.md":              "the specification\n",
		"connector/run.sh":          "#!/bin/sh\n# the stub\n",
	})
	sub := t.TempDir()
	// Their tree is the one we published on the day they forked: our match.go
	// has moved on since, and theirs is untouched.
	writeTestFile(t, sub, "internal/miniblp/match.go", "package miniblp // as shipped\n")
	writeTestFile(t, sub, "docs/spec.md", "the specification\n")
	writeTestFile(t, sub, "connector/run.sh", "#!/bin/sh\n# mine, and that is allowed\n")
	shipped, err := hashFile(filepath.Join(sub, "internal", "miniblp", "match.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, sub, manifestExempt, shipped+"  internal/miniblp/match.go\n")

	rep := CheckProtectedTree(pristine, sub)
	if rep.Err != "" {
		t.Fatalf("the check could not run: %s", rep.Err)
	}
	if !rep.Clean() {
		t.Errorf("an untouched fork was flagged: modified = %v", rep.Modified)
	}
	if !rep.FromSubmission {
		t.Error("the manifest was not taken from the submission")
	}
	if !rep.Stale {
		t.Error("their checkout predates ours and that was not reported")
	}
	if rep.MissingCount != 0 {
		t.Errorf("missing = %d, want 0", rep.MissingCount)
	}
}

// A file they really did change, against the manifest they really were given.
func TestAProtectedFileTheCandidateChangedIsReported(t *testing.T) {
	pristine := newPristine(t, map[string]string{
		"internal/miniblp/match.go": "package miniblp // ours\n",
	})
	sub := t.TempDir()
	writeTestFile(t, sub, "internal/miniblp/match.go", "package miniblp // TAMPERED\n")
	// The manifest they were given, which still holds our hash.
	shipped, err := hashFile(filepath.Join(pristine, "internal", "miniblp", "match.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, sub, manifestExempt, shipped+"  internal/miniblp/match.go\n")

	rep := CheckProtectedTree(pristine, sub)
	if rep.Clean() {
		t.Error("a changed protected file passed the check")
	}
	if len(rep.Modified) != 1 || rep.Modified[0] != "internal/miniblp/match.go" {
		t.Errorf("modified = %v, want exactly internal/miniblp/match.go", rep.Modified)
	}
	if strings.Contains(rep.Summary(), "match the manifest") {
		t.Errorf("the summary reads like a pass: %q", rep.Summary())
	}
}

// A submission that carries no manifest is graded against ours, which is
// strictly harsher, and the absence is reported rather than passed over.
func TestASubmissionWithoutAManifestFallsBackToOurs(t *testing.T) {
	pristine := newPristine(t, map[string]string{
		"internal/miniblp/match.go": "package miniblp // ours\n",
	})
	sub := t.TempDir()
	writeTestFile(t, sub, "internal/miniblp/match.go", "package miniblp // TAMPERED\n")

	rep := CheckProtectedTree(pristine, sub)
	if rep.FromSubmission {
		t.Error("a manifest was taken from a submission that has none")
	}
	if rep.Clean() {
		t.Error("a changed protected file passed the check")
	}
	if !strings.Contains(rep.Summary(), "CHANGED") {
		t.Errorf("summary = %q", rep.Summary())
	}
}

// A submission of only the four published deliverables is clean: absent is not
// modified, and the candidate's own paths are never checked at all.
func TestAbsentProtectedFilesAreNotAFinding(t *testing.T) {
	pristine := newPristine(t, map[string]string{
		"internal/miniblp/match.go": "package miniblp // ours\n",
		"docs/spec.md":              "the specification\n",
		"grading/scenarios/S1.json": "{}\n",
		"connector/run.sh":          "#!/bin/sh\n# the stub\n",
	})
	sub := t.TempDir()
	writeTestFile(t, sub, "connector/run.sh", "#!/bin/sh\n# entirely mine\n")
	writeTestFile(t, sub, "DECISIONS.md", "# mine\n")

	rep := CheckProtectedTree(pristine, sub)
	if !rep.Clean() {
		t.Errorf("a submission of connector/ alone is not clean: %s", rep.Summary())
	}
	if rep.Checked != 3 {
		t.Errorf("checked = %d, want the 3 protected files: the candidate's own are not checked",
			rep.Checked)
	}
	if rep.MissingCount != 3 {
		t.Errorf("missing = %d, want 3", rep.MissingCount)
	}
	if !strings.Contains(rep.Summary(), "not submitted and taken from ours") {
		t.Errorf("the summary does not say what happens to the absent files: %q", rep.Summary())
	}
}

// newPristine writes a tree plus the manifest that covers it, which is what our
// own tree is to a submission.
func newPristine(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	var manifest strings.Builder
	names := make([]string, 0, len(files))
	for rel := range files {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		writeTestFile(t, root, rel, files[rel])
		sum, err := hashFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		manifest.WriteString(sum + "  " + rel + "\n")
	}
	writeTestFile(t, root, manifestExempt, manifest.String())
	return root
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The graded tree is normally built INSIDE the repository it is built from: the
// default report directory is grading/out/score. Walking the source therefore
// walks into the destination, and copying our own tree into a directory under it
// recursed until the filesystem refused the path length. This is that case.
func TestAGradedTreeCanBeBuiltInsideTheTreeItCopies(t *testing.T) {
	pristine := t.TempDir()
	writeTestFile(t, pristine, "cmd/erp/main.go", "package main\n")
	writeTestFile(t, pristine, "docs/spec.md", "the specification\n")
	writeTestFile(t, pristine, "connector/run.sh", "#!/bin/sh\n# the stub\n")
	// The output directory of an earlier review, with its own graded tree in it.
	writeTestFile(t, pristine, "grading/out/review/old/score/graded-tree/docs/spec.md", "stale\n")

	sub := t.TempDir()
	writeTestFile(t, sub, "connector/run.sh", "#!/bin/sh\n# mine\n")

	dest := filepath.Join(pristine, "grading", "out", "score", "graded-tree")
	if err := BuildGradedTree(pristine, sub, dest); err != nil {
		t.Fatalf("BuildGradedTree into its own source: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "cmd", "erp", "main.go")); err != nil {
		t.Errorf("the pristine tree did not arrive: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "connector", "run.sh"))
	if err != nil || !strings.Contains(string(b), "# mine") {
		t.Errorf("the submission's connector did not arrive: %v", err)
	}
	// grading/out is skipped wholesale: it is gitignored build output, it holds
	// every earlier review's graded tree, and it is where this one is being
	// written.
	if _, err := os.Stat(filepath.Join(dest, "grading", "out")); err == nil {
		t.Error("grading/out was copied into the graded tree")
	}
	// And nothing nested itself: no graded-tree inside the graded tree.
	if _, err := os.Stat(filepath.Join(dest, "grading", "out", "score", "graded-tree")); err == nil {
		t.Error("the graded tree copied itself into itself")
	}
}

// The graded tree becomes the run's RepoRoot and the connector command is derived
// from it, so it has to be absolute however the reviewer spelled --report-dir. A
// relative one made every scenario fail to start with "no such file or directory"
// against a connector that was plainly there.
func TestTheGradedTreeIsAlwaysAnAbsolutePath(t *testing.T) {
	pristine := t.TempDir()
	writeTestFile(t, pristine, "connector/run.sh", "#!/bin/sh\n")
	writeTestFile(t, pristine, "go.mod", "module x\n")
	writeTestFile(t, pristine, manifestExempt, "")
	sub := t.TempDir()
	writeTestFile(t, sub, "connector/run.sh", "#!/bin/sh\n# mine\n")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, t.TempDir())
	if err != nil {
		t.Skip("cannot express the temp directory relative to the working directory")
	}

	series := SeriesOptions{RepoRoot: pristine}
	_, graded, err := prepareSubmission(&series, ScoreOptions{Submission: sub, ReportDir: rel})
	if err != nil {
		t.Fatalf("prepareSubmission: %v", err)
	}
	if !filepath.IsAbs(graded) {
		t.Errorf("graded tree = %q, want an absolute path", graded)
	}
	if !filepath.IsAbs(series.RepoRoot) {
		t.Errorf("series.RepoRoot = %q, want an absolute path", series.RepoRoot)
	}
	if _, err := os.Stat(filepath.Join(series.RepoRoot, "connector", "run.sh")); err != nil {
		t.Errorf("the graded tree has no connector: %v", err)
	}
}

// Every directory the harness creates is handed to the connector on its command
// line, and the connector runs with its own working directory. A relative --out
// therefore made the connector write its reports somewhere the grader did not
// look, and the scorecard blamed the candidate for a missing run.json.
func TestTheRunDirectoriesAreAlwaysAbsolute(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, t.TempDir())
	if err != nil {
		t.Skip("cannot express the temp directory relative to the working directory")
	}
	r := &runner{
		opts:  Options{OutDir: rel, RepoRoot: t.TempDir()},
		sc:    &Scenario{ID: "T"},
		runID: "run_t_0000",
	}
	if err := r.prepareDirs(); err != nil {
		t.Fatalf("prepareDirs: %v", err)
	}
	for name, dir := range map[string]string{
		"root": r.dirs.Root, "erp export": r.dirs.ERPExport, "twin drop": r.dirs.TwinDrop,
		"state": r.dirs.State, "reports": r.dirs.Reports, "logs": r.dirs.Logs,
		"evidence": r.dirs.Evidence,
	} {
		if !filepath.IsAbs(dir) {
			t.Errorf("%s directory is %q, want an absolute path", name, dir)
		}
	}
}
