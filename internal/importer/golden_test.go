package importer_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"

	// The one place formats are linked in. Importing it here rather than
	// importing the format packages directly is what makes the skip below
	// meaningful: the golden suite sees exactly the formats the twin sees.
	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/all"
)

// update rewrites the expectation files instead of comparing against them. It is
// never set in CI: a golden file that regenerates itself on failure is not a
// golden file, it is a record of the last bug.
var update = flag.Bool("update", false, "rewrite the golden expectation files from the current output")

// kredExpID is the format id of the Go task.
const kredExpID = "kredexp-2.1"

// goldenDirs are the fixture directories of the KRED-EXP golden suite, tried in
// order. The first is the public suite at the repository root; the second lets a
// package-local suite be dropped in without touching this file.
func goldenDirs() []string {
	return []string{
		filepath.Join("..", "..", importer.GoldenDir),
		importer.GoldenDir,
	}
}

func TestKredExpGolden(t *testing.T) {
	f, ok := importer.Lookup(kredExpID)
	if !ok {
		t.Skipf("the %s importer is not registered, so there is nothing to compare.\n"+
			"This is the expected state before the Go task is done: implement\n"+
			"internal/importer/kredexp and uncomment the one blank-import line in\n"+
			"internal/importer/all/all.go.",
			kredExpID)
	}

	var (
		dir   string
		cases []importer.GoldenCase
	)
	for _, d := range goldenDirs() {
		got, err := importer.LoadGoldenCases(d)
		if err != nil {
			t.Fatalf("loading fixtures from %s: %v", d, err)
		}
		if len(got) > 0 {
			dir, cases = d, got
			break
		}
	}
	if len(cases) == 0 {
		t.Skipf("no golden fixtures found in %v; the %s importer is registered but has nothing to be compared against",
			goldenDirs(), kredExpID)
	}
	t.Logf("comparing %d fixtures in %s against %s", len(cases), dir, f.ID())

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			out, err := importer.RunGoldenCase(context.Background(), f, c)
			if err != nil {
				// An error here is never a wrong expectation. It is a broken
				// contract: an error returned for a data defect, a nil Result,
				// or a streaming path that disagrees with the whole-file one.
				t.Fatal(err)
			}
			if *update {
				if err := importer.WriteGoldenExpectation(out); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s", c.ExpectedPath)
				return
			}
			if out.Expected == nil {
				t.Fatalf("no expectation file at %s. Run with -update to write one from the current output, then read it before you trust it.", c.ExpectedPath)
			}
			if out.Diff != "" {
				t.Fatalf("the parse does not match %s:\n\n%s", filepath.Base(c.ExpectedPath), out.Diff)
			}
			if !out.Detected {
				t.Errorf("Detect returned false for %s. The parse matched, so the format works; it just cannot recognize its own files, and the twin then depends on the manifest naming the profile.", c.Name)
			}
		})
	}
}

func TestDetectIsExclusive(t *testing.T) {
	// A file of one format must not be detected as another. This is what keeps
	// a legacy semicolon export from being read as a canonical semicolon CSV,
	// which would parse a Vorlaufsatz as a header row and reject every record
	// in the file for the wrong reason.
	root := filepath.Join("testdata", "formats")
	owners, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	// The registry also holds the stub formats this package's own tests
	// register, which own no fixtures and must not be asserted about.
	var registered []string
	for _, id := range importer.IDs() {
		if !strings.HasPrefix(id, "test-") {
			registered = append(registered, id)
		}
	}
	if len(registered) == 0 {
		t.Fatal("no formats are registered; the blank import of internal/importer/all is not doing its job")
	}

	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		ownerID := owner.Name()
		cases, err := importer.LoadGoldenCases(filepath.Join(root, ownerID))
		if err != nil {
			t.Fatal(err)
		}
		if len(cases) == 0 {
			t.Errorf("%s has no fixtures, so nothing asserts that other formats reject its files", ownerID)
			continue
		}
		for _, c := range cases {
			raw, err := os.ReadFile(c.InputPath)
			if err != nil {
				t.Fatal(err)
			}
			head := importer.Head(raw)

			t.Run(ownerID+"/"+c.Name, func(t *testing.T) {
				for _, id := range registered {
					f := importer.MustLookup(id)
					got := f.Detect(head, c.Options)
					switch {
					case id == ownerID:
						// The owner should recognize its own file. The stub
						// importer of an unimplemented format does not, and
						// that is a documented state rather than a failure.
						if !got && id != kredExpID {
							t.Errorf("%s does not detect its own fixture %s", id, c.Name)
						}
					case got:
						t.Errorf("%s detected %s, which belongs to %s", id, c.Name, ownerID)
					}
				}
			})
		}
	}
}

func TestGoldenRunnerAgreesWithItself(t *testing.T) {
	// The runner is graded infrastructure, so it is tested against a format
	// whose behavior this file controls: a fixture that matches must pass, a
	// fixture with a wrong expectation must produce a readable diff, and a
	// format that breaks the error contract must be reported as broken rather
	// than as failing.
	f := lineFormat{}
	cases, err := importer.LoadGoldenCases(filepath.Join("testdata", "runner"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("loaded %d runner fixtures, want 2", len(cases))
	}
	if cases[0].Name != "one-bad" || cases[1].Name != "three" {
		t.Fatalf("fixtures are not sorted by name: %s, %s", cases[0].Name, cases[1].Name)
	}

	for _, c := range cases {
		out, err := importer.RunGoldenCase(context.Background(), f, c)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if out.Expected == nil {
			if err := importer.WriteGoldenExpectation(out); err != nil {
				t.Fatal(err)
			}
			t.Fatalf("%s had no expectation; one was written to %s, re-run the test", c.Name, c.ExpectedPath)
		}
		if out.Diff != "" {
			t.Errorf("%s does not match its expectation:\n%s", c.Name, out.Diff)
		}
		if !out.Passed() {
			t.Errorf("%s did not pass", c.Name)
		}
	}
}

func TestGoldenRunnerReportsAWrongExpectation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.lines"), []byte("0000417\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := `{"accepted":true,"diagnostics":[],"documents":[{"dataset":"supplier","key":"9999999","payload":{"key":"9999999"}}],"totals":{}}`
	if err := os.WriteFile(filepath.Join(dir, "case.expected.json"), []byte(wrong), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := importer.RunGoldenCase(context.Background(), lineFormat{}, cases[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"missing document supplier/9999999", "unexpected document supplier/0000417"} {
		if !strings.Contains(out.Diff, must) {
			t.Errorf("the diff does not contain %q:\n%s", must, out.Diff)
		}
	}
}

func TestGoldenRunnerRejectsAFormatThatErrorsOnBadData(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.lines"), []byte("BOOM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = importer.RunGoldenCase(context.Background(), erroringFormat{}, cases[0])
	if err == nil {
		t.Fatal("the runner accepted a format that returns an error for a data defect")
	}
	if !strings.Contains(err.Error(), "reserved for programming and environment faults") {
		t.Errorf("the error does not explain the contract: %v", err)
	}
}

func TestGoldenRunnerRejectsADisagreeingStreamParser(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.lines"), []byte("0000417\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = importer.RunGoldenCase(context.Background(), driftingFormat{}, cases[0])
	if err == nil || !strings.Contains(err.Error(), "ParseStream and Parse disagree") {
		t.Fatalf("error = %v, want a report that the two paths disagree", err)
	}
}

func TestGoldenCaseOptionsFileIsHonoured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := `{"encoding":"windows-1252","dataset":"invoice","record_count":7,"csv":{"delimiter":";"}}`
	if err := os.WriteFile(filepath.Join(dir, "case.options.json"), []byte(opts), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := cases[0].Options
	if got.Encoding != importer.EncodingCP1252 || got.Dataset != "invoice" || got.DeclaredRecords != 7 {
		t.Errorf("Options = %+v, want the declared encoding, dataset and record count", got)
	}
	if got.Dialect.Delimiter != ";" {
		t.Errorf("the declared CSV dialect was not read: %+v", got.Dialect)
	}
	if got.Filename != "case.txt" {
		t.Errorf("Filename = %q, want the fixture's own name", got.Filename)
	}
}

func TestLoadGoldenCasesOnAMissingDirectory(t *testing.T) {
	cases, err := importer.LoadGoldenCases(filepath.Join("testdata", "there-is-no-such-directory"))
	if err != nil {
		t.Fatalf("a missing fixture directory is not an error, so that an unimplemented task skips: %v", err)
	}
	if len(cases) != 0 {
		t.Errorf("loaded %d cases from a missing directory", len(cases))
	}
}

func TestWriteGoldenExpectation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.lines"), []byte("0000417\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := importer.RunGoldenCase(context.Background(), lineFormat{}, cases[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.Expected != nil {
		t.Fatal("an expectation exists that was never written")
	}
	if err := importer.WriteGoldenExpectation(out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(cases[0].ExpectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(body), "\n") {
		t.Error("the expectation file does not end in a newline")
	}
	if !strings.Contains(string(body), "0000417") {
		t.Errorf("the expectation does not carry the parsed document: %s", body)
	}

	// Written once, it now matches.
	cases, err = importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err = importer.RunGoldenCase(context.Background(), lineFormat{}, cases[0])
	if err != nil {
		t.Fatal(err)
	}
	if !out.Passed() {
		t.Fatalf("the fixture does not match the expectation just written:\n%s", out.Diff)
	}

	// And an outcome with nothing to write is an error rather than an empty file.
	if err := importer.WriteGoldenExpectation(importer.GoldenOutcome{}); err == nil {
		t.Error("WriteGoldenExpectation wrote an empty projection")
	}
}

func TestLoadGoldenCasesIgnoresSiblingsAndDotfiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"case.lines":         "0000417\n",
		"case.expected.json": "{}",
		"case.options.json":  "{}",
		".DS_Store":          "junk",
		"README.md":          "notes",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases, err := importer.LoadGoldenCases(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].Name != "case" {
		t.Fatalf("cases = %+v, want only the fixture itself", cases)
	}
}

func TestLoadGoldenCasesReportsAMalformedOptionsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case.lines"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "case.options.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.LoadGoldenCases(dir); err == nil {
		t.Fatal("a fixture's options file that is not JSON is a broken fixture, not a silent default")
	}
}
