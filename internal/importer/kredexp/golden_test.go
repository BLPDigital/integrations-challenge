package kredexp_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer/kredexp"
)

// update rewrites the expectation files from the current output; never set in CI.
var update = flag.Bool("update", false, "rewrite the golden expectation files from the current output")

// publicDir is the fixture suite, relative to this package.
const publicDir = "../../../testdata/kredexp"

// fixtureCount is asserted so a fixture deleted by accident fails the build.
var fixtureCount = map[string]int{publicDir: 4}

// TestGoldenFixtures parses every fixture and compares it against its expected projection.
func TestGoldenFixtures(t *testing.T) {
	f := kredexp.New()
	for _, dir := range []string{publicDir} {
		cases, err := importer.LoadGoldenCases(dir)
		if err != nil {
			t.Fatalf("loading fixtures from %s: %v", dir, err)
		}
		if got, want := len(cases), fixtureCount[dir]; got != want {
			t.Fatalf("%s holds %d fixtures, want %d", dir, got, want)
		}
		for _, c := range cases {
			t.Run(c.Name, func(t *testing.T) {
				out, err := importer.RunGoldenCase(context.Background(), f, c)
				if err != nil {
					// Never a wrong expectation: an error here is a broken
					// contract - an error returned for a data defect, a nil
					// Result, or a streaming path that disagrees with Parse.
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
					t.Fatalf("no expectation file at %s; run with -update and then read what it wrote", c.ExpectedPath)
				}
				if out.Diff != "" {
					t.Fatalf("the parse does not match %s:\n\n%s", filepath.Base(c.ExpectedPath), out.Diff)
				}
				if !out.Detected {
					t.Errorf("Detect returned false for %s, so the twin could only reach this format through a manifest profile", c.Name)
				}
			})
		}
	}
}

// TestFixturesAreGenuineCP1252 asserts the property no expectation file can
// assert: that the fixtures on disk are the bytes the customer's sender writes.
// A fixture rewritten by an editor into UTF-8 with LF terminators would still
// parse, and would stop testing the two things it exists to test.
func TestFixturesAreGenuineCP1252(t *testing.T) {
	for _, dir := range []string{publicDir} {
		cases, err := importer.LoadGoldenCases(dir)
		if err != nil {
			t.Fatalf("loading fixtures from %s: %v", dir, err)
		}
		for _, c := range cases {
			t.Run(c.Name, func(t *testing.T) {
				raw, err := os.ReadFile(c.InputPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, had := importer.StripBOM(raw); had {
					t.Error("the fixture begins with a byte order mark; the format has none")
				}
				if len(raw) < 2 || raw[len(raw)-2] != '\r' || raw[len(raw)-1] != '\n' {
					t.Error("the fixture does not end with CRLF; every record of the format is CRLF terminated")
				}
				for i := 0; i+1 < len(raw); i++ {
					if raw[i] == '\n' && i > 0 && raw[i-1] != '\r' {
						// One fixture carries an embedded lone LF inside a
						// quoted field on purpose; every other LF must be
						// preceded by a CR.
						if c.Name != "03_quoted_fields_ok" {
							t.Fatalf("a bare LF at offset %d: the fixture was rewritten with Unix line endings", i)
						}
					}
				}
				if c.Options.Encoding != importer.EncodingCP1252 {
					t.Errorf("the options file declares encoding %q, want %q", c.Options.Encoding, importer.EncodingCP1252)
				}
			})
		}
	}
}

// TestUTF8FixtureWouldNotPass proves the point of the previous test: the same
// content encoded as UTF-8 does not parse as this format. It is the guard
// against a well-meaning cleanup that "fixes the encoding" of the fixtures.
func TestUTF8FixtureWouldNotPass(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(publicDir, "03_quoted_fields_ok.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Re-encoding the high bytes as UTF-8 produces two bytes where there was
	// one, and the first of them is 0xC2 or 0xE2, which windows-1252 maps to a
	// letter: the amount 1'234.50 becomes 1â€™234.50 and stops being an amount.
	utf8Raw := []byte(string(rune(0)))[:0]
	for _, b := range raw {
		r, ok := importer.CP1252Rune(b)
		if !ok {
			t.Fatalf("the fixture carries an undefined windows-1252 byte 0x%02X", b)
		}
		utf8Raw = append(utf8Raw, []byte(string(r))...)
	}
	res, err := kredexp.New().Parse(context.Background(), utf8Raw, importer.Options{
		Filename: "03_quoted_fields_ok.txt",
		Encoding: importer.EncodingCP1252,
	})
	if err != nil {
		t.Fatalf("Parse returned an error for a data defect: %v", err)
	}
	if res.Accepted {
		t.Error("a UTF-8 re-encoding of the fixture was accepted; the fixtures then no longer test the encoding at all")
	}
}
