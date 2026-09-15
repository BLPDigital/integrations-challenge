package importer

import (
	"context"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// nextTestID returns a format id no other test has used. The registry is
// process-global and Register panics on a duplicate, so a test that hard-codes an
// id passes once and panics under `go test -count=2`, which CI runs.
var testIDSeq atomic.Int64

func nextTestID(name string) string {
	return "test-" + name + "-" + strconv.FormatInt(testIDSeq.Add(1), 10)
}

// stubFormat is a Format for the framework's own tests. It reports one document
// per line and detects files whose head starts with its own prefix.
type stubFormat struct {
	id     string
	prefix string
}

func (f stubFormat) ID() string { return f.id }

func (f stubFormat) Detect(head []byte, opt Options) bool {
	return f.prefix != "" && strings.HasPrefix(string(head), f.prefix)
}

func (f stubFormat) Parse(ctx context.Context, raw []byte, opt Options) (*Result, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	b := NewBuilder(f.id, opt)
	b.SetSourceBytes(raw)
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		b.AddDocument(Document{Dataset: "supplier", Key: line, Line: i + 1, Payload: []byte(`{"k":"` + line + `"}`)})
	}
	return b.Result(), nil
}

// streamStub implements StreamParser, so ParseStream must prefer it.
type streamStub struct {
	stubFormat
	used *bool
}

func (f streamStub) ParseStream(ctx context.Context, r io.Reader, opt Options) (*Result, error) {
	*f.used = true
	var sb strings.Builder
	if _, err := io.Copy(&sb, r); err != nil {
		return nil, err
	}
	return f.stubFormat.Parse(ctx, []byte(sb.String()), opt)
}

func TestRegisterAndLookup(t *testing.T) {
	id := nextTestID("register")
	f := stubFormat{id: id, prefix: "REG"}
	Register(f)
	got, ok := Lookup(id)
	if !ok || got.ID() != f.ID() {
		t.Fatalf("Lookup = %v, %v; want the registered format", got, ok)
	}
	if _, ok := Lookup("test-never-registered"); ok {
		t.Error("Lookup found a format nothing registered")
	}
	if MustLookup(id).ID() != f.ID() {
		t.Error("MustLookup returned the wrong format")
	}
}

func TestRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "nil format", fn: func() { Register(nil) }},
		{name: "empty id", fn: func() { Register(stubFormat{id: ""}) }},
		{name: "duplicate id", fn: func() {
			id := nextTestID("duplicate")
			Register(stubFormat{id: id})
			Register(stubFormat{id: id})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Register did not panic")
				}
			}()
			tc.fn()
		})
	}
}

func TestMustLookupPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustLookup did not panic for an unregistered id")
		}
	}()
	MustLookup("test-absent-v1")
}

func TestAllIsSortedAndCopied(t *testing.T) {
	Register(stubFormat{id: nextTestID("zzz-order")})
	Register(stubFormat{id: nextTestID("aaa-order")})
	ids := IDs()
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("IDs() = %v, want ascending order", ids)
	}
	first := All()
	second := All()
	if len(first) != len(second) {
		t.Fatal("All() returned different lengths on two calls")
	}
	first[0] = nil
	if All()[0] == nil {
		t.Error("All() returned the registry's own slice; a caller mutated it")
	}
}

func TestDetectFormat(t *testing.T) {
	// The prefixes are unique to this test's run so that a repeated run neither
	// collides in the registry nor sees the previous run's formats.
	run := nextTestID("detect")
	pa, pb := "AAA-"+run+" ", "BBB-"+run+" "
	idB := nextTestID("detect-b")
	Register(stubFormat{id: nextTestID("detect-a"), prefix: pa})
	Register(stubFormat{id: idB, prefix: pb})
	Register(stubFormat{id: nextTestID("detect-amb"), prefix: pa[:4]})

	if f, ok := DetectFormat([]byte(pb+"rest"), Options{}); !ok || f.ID() != idB {
		t.Errorf("DetectFormat for a unique match = %v, %v", f, ok)
	}
	if _, ok := DetectFormat([]byte("CCC-"+run+" rest"), Options{}); ok {
		t.Error("DetectFormat matched a file no format recognizes")
	}
	// Two formats claim the same head. An ambiguous sniff is not a detection:
	// resolving it by registry order would make the answer depend on which
	// formats happen to be linked in.
	if f, ok := DetectFormat([]byte(pa+"rest"), Options{}); ok {
		t.Errorf("DetectFormat resolved an ambiguous head to %s", f.ID())
	}
}

func TestHead(t *testing.T) {
	small := []byte("short")
	if got := Head(small); string(got) != "short" {
		t.Errorf("Head(small) = %q", got)
	}
	big := make([]byte, DetectHeadBytes*2)
	if got := Head(big); len(got) != DetectHeadBytes {
		t.Errorf("Head(big) length = %d, want %d", len(got), DetectHeadBytes)
	}
}

func TestParseStreamPrefersStreamParser(t *testing.T) {
	used := false
	f := streamStub{stubFormat: stubFormat{id: nextTestID("stream")}, used: &used}
	res, err := ParseStream(context.Background(), f, strings.NewReader("a\nb\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("ParseStream did not use the format's own ParseStream")
	}
	if len(res.Documents) != 2 {
		t.Errorf("documents = %d, want 2", len(res.Documents))
	}
}

func TestParseStreamFallsBackToParse(t *testing.T) {
	f := stubFormat{id: nextTestID("nostream")}
	res, err := ParseStream(context.Background(), f, strings.NewReader("a\nb\nc\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Documents) != 3 {
		t.Errorf("documents = %d, want 3", len(res.Documents))
	}
}

func TestParseStreamRejectsNilContextAndFormat(t *testing.T) {
	//lint:ignore SA1012 passing a nil context is exactly what is under test.
	if _, err := ParseStream(nil, stubFormat{id: "x"}, strings.NewReader(""), Options{}); !errors.Is(err, ErrNilContext) {
		t.Error("ParseStream accepted a nil context")
	}
	if _, err := ParseStream(context.Background(), nil, strings.NewReader(""), Options{}); !errors.Is(err, ErrNotRegistered) {
		t.Error("ParseStream accepted a nil format")
	}
}

// failingReader returns an environment fault, which must reach the caller as an
// error and never as a diagnostic.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestParseStreamFallbackReportsReadErrors(t *testing.T) {
	_, err := ParseStream(context.Background(), stubFormat{id: nextTestID("readfail")}, failingReader{}, Options{Filename: "x.csv"})
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("error = %v, want the underlying read error", err)
	}
}
