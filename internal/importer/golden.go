package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GoldenExpectedSuffix and GoldenOptionsSuffix are the sibling files of a golden
// fixture: "batch-01.txt" is compared against "batch-01.expected.json" and, when
// the fixture needs anything other than the default [Options], is parsed with
// "batch-01.options.json".
const (
	// GoldenExpectedSuffix names the expectation file of a fixture.
	GoldenExpectedSuffix = ".expected.json"
	// GoldenOptionsSuffix names the optional options file of a fixture. Its
	// content is an [Options] object using the manifest's own field names:
	// {"encoding":"windows-1252","dataset":"invoice","record_count":42}.
	GoldenOptionsSuffix = ".options.json"
)

// GoldenDir is the default fixtures directory of the KRED-EXP golden suite,
// relative to the package under test. The four public fixtures live there; the
// hidden ones are the same shape and are never shipped.
const GoldenDir = "testdata/kredexp"

// A GoldenCase is one fixture: the input file, the expectation it is compared
// against, and the [Options] it is parsed with.
type GoldenCase struct {
	// Name is the fixture's base name, e.g. "batch-01". It is the subtest name.
	Name string
	// InputPath is the fixture file.
	InputPath string
	// ExpectedPath is the sibling expectation, which may not exist yet.
	ExpectedPath string
	// Options are the parse options, with Filename already set to the input
	// file's base name so a diagnostic names the fixture and not a temporary
	// path.
	Options Options
}

// LoadGoldenCases returns the fixtures in dir, sorted by name. A file is a
// fixture unless its name ends in [GoldenExpectedSuffix] or
// [GoldenOptionsSuffix]; subdirectories and dotfiles are ignored, so a fixture
// tree can carry a README and an editor's leftovers.
//
// A missing directory is not an error: it returns no cases. The golden runner
// treats "no fixtures" as a skip with a clear message, because a candidate who
// has not yet been given fixtures should not meet a red test suite.
func LoadGoldenCases(dir string) ([]GoldenCase, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("importer: reading golden fixtures in %s: %w", dir, err)
	}
	var cases []GoldenCase
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasSuffix(name, GoldenExpectedSuffix) || strings.HasSuffix(name, GoldenOptionsSuffix) {
			continue
		}
		if strings.EqualFold(name, "README.md") {
			continue
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		c := GoldenCase{
			Name:         base,
			InputPath:    filepath.Join(dir, name),
			ExpectedPath: filepath.Join(dir, base+GoldenExpectedSuffix),
			Options:      Options{Filename: name},
		}
		optPath := filepath.Join(dir, base+GoldenOptionsSuffix)
		if raw, err := os.ReadFile(optPath); err == nil {
			opt := c.Options
			if err := json.Unmarshal(raw, &opt); err != nil {
				return nil, fmt.Errorf("importer: %s: %w", optPath, err)
			}
			if opt.Filename == "" {
				opt.Filename = name
			}
			c.Options = opt
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("importer: %s: %w", optPath, err)
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases, nil
}

// A GoldenOutcome is what running one fixture produced.
type GoldenOutcome struct {
	// Case is the fixture that was run.
	Case GoldenCase
	// Detected is what [Format.Detect] said about the fixture's head. A
	// fixture the format cannot detect is a finding in its own right: the twin
	// falls back on the manifest's declared profile, but a format that cannot
	// recognize its own files cannot be used without one.
	Detected bool
	// Result is the parse result.
	Result *Result
	// Projection is [Projection] of Result.
	Projection []byte
	// Expected is the expectation file's bytes, nil when it does not exist.
	Expected []byte
	// Diff is [GoldenDiff] of Projection against Expected, "" when they agree.
	// It is "" for a missing expectation too; check Expected for that.
	Diff string
}

// Passed reports whether the fixture matched an existing expectation.
func (o GoldenOutcome) Passed() bool { return o.Expected != nil && o.Diff == "" }

// RunGoldenCase parses one fixture with f and compares it against the fixture's
// expectation. It returns an error only for an environment or programming fault
// - an unreadable fixture, a nil context, a format that returned neither a
// Result nor an error - never for a mismatch, which is what [GoldenOutcome.Diff]
// is for.
//
// It also asserts the framework's determinism contract by parsing the fixture
// twice, once through [Format.Parse] and once through [ParseStream], and
// reporting a difference between the two as an error: a format whose streaming
// and whole-file paths disagree is broken in a way no expectation file would
// catch, because a fixture is only ever run one way.
func RunGoldenCase(ctx context.Context, f Format, c GoldenCase) (GoldenOutcome, error) {
	out := GoldenOutcome{Case: c}
	if ctx == nil {
		return out, ErrNilContext
	}
	raw, err := os.ReadFile(c.InputPath)
	if err != nil {
		return out, fmt.Errorf("importer: %w", err)
	}
	out.Detected = f.Detect(Head(raw), c.Options)

	res, err := f.Parse(ctx, raw, c.Options)
	if err != nil {
		return out, fmt.Errorf("importer: %s: Parse returned an error, which is reserved for programming and environment faults: %w", c.Name, err)
	}
	if res == nil {
		return out, fmt.Errorf("importer: %s: Parse returned a nil Result and a nil error", c.Name)
	}
	out.Result = res
	proj, err := Projection(res)
	if err != nil {
		return out, fmt.Errorf("importer: %s: projecting the result: %w", c.Name, err)
	}
	out.Projection = proj

	if streamed, serr := ParseStream(ctx, f, &bytesReader{rest: raw}, c.Options); serr != nil {
		return out, fmt.Errorf("importer: %s: ParseStream returned an error: %w", c.Name, serr)
	} else if streamProj, perr := Projection(streamed); perr != nil {
		return out, fmt.Errorf("importer: %s: projecting the streamed result: %w", c.Name, perr)
	} else if d := GoldenDiff(streamProj, proj); d != "" {
		return out, fmt.Errorf("importer: %s: ParseStream and Parse disagree:\n%s", c.Name, d)
	}

	expected, err := os.ReadFile(c.ExpectedPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return out, nil
	case err != nil:
		return out, fmt.Errorf("importer: %w", err)
	}
	out.Expected = expected
	out.Diff = GoldenDiff(proj, expected)
	return out, nil
}

// WriteGoldenExpectation writes o's projection to the fixture's expectation
// path, with a trailing newline so the file is a well-behaved text file. It is
// what a `-update` flag calls, and it is never called by a passing test run:
// a golden file that regenerates itself on failure is not a golden file.
func WriteGoldenExpectation(o GoldenOutcome) error {
	if len(o.Projection) == 0 {
		return fmt.Errorf("importer: %s: nothing to write", o.Case.Name)
	}
	body := append(append([]byte(nil), o.Projection...), '\n')
	if err := os.WriteFile(o.Case.ExpectedPath, body, 0o644); err != nil {
		return fmt.Errorf("importer: %w", err)
	}
	return nil
}

// bytesReader is a chunking reader over a byte slice. It exists so the streaming
// comparison reads a fixture in small pieces: a reader that hands over the whole
// file in one Read would not exercise a streaming parser's chunk boundaries at
// all, and the comparison would pass for a parser that only works when it can
// see everything.
type bytesReader struct{ rest []byte }

// Read implements io.Reader, handing over at most 7 bytes at a time. Seven is
// prime and smaller than every record in every fixture, so a record boundary, a
// quoted line break and a multi-byte character each end up split across two
// reads somewhere in any real file.
func (b *bytesReader) Read(p []byte) (int, error) {
	if len(b.rest) == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > 7 {
		n = 7
	}
	if n > len(b.rest) {
		n = len(b.rest)
	}
	copy(p, b.rest[:n])
	b.rest = b.rest[n:]
	return n, nil
}
