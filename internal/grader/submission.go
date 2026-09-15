package grader

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WritablePaths are the paths a candidate owns. Everything else in the tree is
// ours: the two services, the grader, the fixtures, the scenarios and the docs.
//
// It is the same set tools/verify-pristine.sh enforces, and a test asserts the
// two lists agree, because two copies of one rule in two languages is a rule
// that drifts.
var WritablePaths = []string{
	"connector/",
	"internal/importer/kredexp/",
	"internal/importer/all/all.go",
	"DECISIONS.md",
}

// manifestExempt is writable in the checksum sense only: it is the manifest
// itself, which every tree carries its own copy of.
const manifestExempt = "PROTECTED.sha256"

// IsWritablePath reports whether a repository-relative path belongs to the
// candidate.
func IsWritablePath(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range WritablePaths {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return true
			}
			continue
		}
		if rel == p {
			return true
		}
	}
	return false
}

// An IntegrityReport is what the protected-tree check found in a submission.
type IntegrityReport struct {
	// Manifest is the manifest that was checked against.
	Manifest string `json:"manifest"`
	// FromSubmission reports that the manifest came out of the submission, which
	// is the normal and correct case. False means the submission carries none,
	// which is itself worth a look: the manifest is a protected file.
	FromSubmission bool `json:"manifest_from_submission"`
	// Stale reports that the submission's manifest differs from ours, i.e. their
	// checkout predates our HEAD. Normal, expected, and evidence of nothing:
	// it is reported so a reviewer knows which tree they are looking at.
	Stale bool `json:"checkout_predates_ours,omitempty"`
	// Checked is how many protected files were hashed.
	Checked int `json:"checked"`
	// Modified are protected files whose content differs from the manifest.
	Modified []string `json:"modified,omitempty"`
	// Missing are protected files the submission does not have, capped for the
	// report: a legitimate submission of the four published deliverables is
	// missing every one of them, so the full list is three hundred lines of
	// nothing. MissingCount is the real number.
	Missing      []string `json:"missing_sample,omitempty"`
	MissingCount int      `json:"missing_count,omitempty"`
	// Err is set when the check itself could not run, which is a harness event
	// and never a finding about the submission.
	Err string `json:"error,omitempty"`
}

// Clean reports whether nothing outside the candidate-writable set was CHANGED.
//
// A protected file the submission does not carry is not a finding. The published
// deliverable is four things and "nothing else in the tree changes", so a zip of
// connector/ plus the importer plus DECISIONS.md is a legitimate submission in
// which every protected file is absent, and the graded tree takes ours anyway. A
// deleted file cannot reach the run either. Only a file they changed is a
// statement about our tree.
func (r IntegrityReport) Clean() bool {
	return r.Err == "" && len(r.Modified) == 0
}

// Summary is the one line a scorecard carries.
func (r IntegrityReport) Summary() string {
	switch {
	case r.Err != "":
		return "the protected-tree check could not run: " + r.Err
	case r.Clean():
		where := "the manifest they were given"
		if !r.FromSubmission {
			where = "ours, because the submission carries no manifest of its own"
		}
		if r.MissingCount > 0 {
			return fmt.Sprintf("%d of %d protected files present and matching %s, "+
				"%d not submitted and taken from ours",
				r.Checked-r.MissingCount, r.Checked, where, r.MissingCount)
		}
		return fmt.Sprintf("all %d protected files match %s", r.Checked, where)
	default:
		return fmt.Sprintf("%d protected file(s) CHANGED outside the candidate-writable set: %s",
			len(r.Modified), strings.Join(firstFew(r.Modified, 5), ", "))
	}
}

// CheckProtectedTree hashes every protected file of a submission against the
// manifest the submission itself carries.
//
// It reads the SUBMISSION's manifest, not ours, and the reason is that ours is
// the wrong question. Our tree moves: a grader defect is fixed, a document is
// corrected, a scenario is added. A candidate forked what we published on the
// day they started, so checking their tree against our HEAD measures our own
// drift and reports it as their tampering. On the first real submission this
// ran against it named fourteen changed files, every one of them ours, none of
// them touched by the candidate, and it listed every internal file the bundle
// deliberately strips as one they had deleted. That is a false accusation
// against every honest candidate whose fork is older than our HEAD, which is
// all of them.
//
// The obvious objection is that a submission's manifest is whatever it says it
// is, since tools/regen-protected.sh ships and one command regenerates it over
// an edit. True, and accepted, for two reasons. The score does not depend on
// it: [BuildGradedTree] takes only the candidate-writable paths from the
// submission and everything else from our pristine tree, so an edited protected
// file never reaches a running service. And the old behavior did not actually
// catch the regenerating candidate either, since their tree and their manifest
// agree by construction; it only caught our own drift, loudly, every time.
//
// What is reported instead is honest and useful: what they changed relative to
// what they were given, plus whether their checkout predates ours, so a
// reviewer knows which tree they are reading.
func CheckProtectedTree(pristineRoot, submissionRoot string) IntegrityReport {
	ours := filepath.Join(pristineRoot, manifestExempt)
	theirs := filepath.Join(submissionRoot, manifestExempt)
	rep := IntegrityReport{Manifest: theirs, FromSubmission: true}
	raw, err := os.ReadFile(theirs)
	if err != nil {
		// A submission without the manifest is graded against ours, which is
		// strictly harsher, and the absence is reported: it is a protected file.
		rep.Manifest, rep.FromSubmission = ours, false
		raw, err = os.ReadFile(ours)
		if err != nil {
			rep.Err = err.Error()
			return rep
		}
	}
	if rep.FromSubmission {
		if mine, err := os.ReadFile(ours); err == nil {
			rep.Stale = string(mine) != string(raw)
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		want, rel := fields[0], fields[1]
		if rel == manifestExempt || IsWritablePath(rel) {
			continue
		}
		rep.Checked++
		have, err := hashFile(filepath.Join(submissionRoot, rel))
		if err != nil {
			rep.MissingCount++
			if len(rep.Missing) < 20 {
				rep.Missing = append(rep.Missing, rel)
			}
			continue
		}
		if have != want {
			rep.Modified = append(rep.Modified, rel)
		}
	}
	sort.Strings(rep.Modified)
	sort.Strings(rep.Missing)
	return rep
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gradedTreeSkip are the directories neither tree contributes, by name: build
// output, runtime state, and the version-control metadata.
var gradedTreeSkip = map[string]bool{
	".git": true, "bin": true, "dist": true, "var": true, ".run": true,
}

// gradedTreeSkipRel are paths skipped by their position rather than their name.
// grading/out holds every run's output, including graded trees of earlier
// reviews, and it is gitignored: copying it in would copy gigabytes of evidence
// into the tree being graded.
var gradedTreeSkipRel = map[string]bool{"grading/out": true}

// BuildGradedTree assembles the tree a submission is graded in: our pristine
// tree, with only the candidate-writable paths taken from the submission.
//
// This is what four documents promise and what nothing did. Without it the
// grader compiled and ran whatever tree it was pointed at, so a submission that
// edited the twin's matching engine to raise no exceptions, or the admin surface
// to return the golden digest, graded itself.
//
// The result is left on disk under dest, because a reviewer has to be able to see
// exactly what was graded.
func BuildGradedTree(pristineRoot, submissionRoot, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// Everything of ours first, the candidate-writable paths included, because a
	// submission does not have to carry them: the published deliverable is four
	// things, so a zip of connector/ plus DECISIONS.md is legitimate and the
	// untouched importer stub then has to be OURS or the tree does not compile.
	if err := copyTree(pristineRoot, dest, func(string) bool { return true }); err != nil {
		return fmt.Errorf("copying the pristine tree: %w", err)
	}
	// Then each writable path the submission actually has REPLACES ours, whole.
	// Replacing rather than merging is what makes a file the candidate deleted
	// inside their own package stay deleted.
	for _, p := range WritablePaths {
		rel := strings.TrimSuffix(p, "/")
		src := filepath.Join(submissionRoot, filepath.FromSlash(rel))
		if _, err := os.Stat(src); err != nil {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := copyPath(src, target); err != nil {
			return fmt.Errorf("overlaying %s: %w", rel, err)
		}
	}
	return nil
}

// copyPath copies one file or one directory tree.
func copyPath(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyFile(src, dst, fi.Mode().Perm())
	}
	return copyTree(src, dst, func(string) bool { return true })
}

// copyTree copies the files of src into dst for which keep returns true, keeping
// the executable bit, which run.sh and setup.sh need.
//
// It skips the destination itself when the destination lies inside the source,
// which is the normal case and not an exotic one: the default report directory is
// grading/out/score inside this repository, so copying our own tree into
// grading/out/score/graded-tree walked into what it was writing and recursed
// until the path was too long for the filesystem.
func copyTree(src, dst string, keep func(rel string) bool) error {
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if gradedTreeSkip[d.Name()] || gradedTreeSkipRel[rel] {
				return fs.SkipDir
			}
			if abs, absErr := filepath.Abs(path); absErr == nil && abs == dstAbs {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !keep(rel) {
			return nil
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(from, to string, perm fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// firstFew caps a list for a one-line summary and says so.
func firstFew(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	out := append([]string(nil), in[:n]...)
	return append(out, fmt.Sprintf("and %d more", len(in)-n))
}
