package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// DetectHeadBytes is how many leading bytes of a file [Format.Detect] is given.
// It is large enough to hold a CSV header row, an XML prolog and root element,
// or the first records of a legacy fixed-layout file, and small enough that
// detection never reads a whole file.
const DetectHeadBytes = 8 << 10

// A Format is one file format the twin can read. Implementations are registered
// once, from an init function, and are then used concurrently by the inbox
// scanner and by the REST ingest path, so they must hold no mutable state: every
// method is a pure function of its arguments.
type Format interface {
	// ID is the stable identifier of the format, as it appears in a manifest's
	// profile field and in a [Result]. Lowercase, hyphenated, versioned, e.g.
	// "blp-csv-v1" or "kredexp-2.1".
	ID() string

	// Detect reports whether head, the first [DetectHeadBytes] of a file, looks
	// like this format. It is a cheap structural sniff, never a parse: it must
	// not allocate proportionally to the file, must not report a diagnostic and
	// must be false for every other registered format's files. A format whose
	// files carry no recognizable signature reports false and relies on the
	// manifest's declared profile instead.
	Detect(head []byte, opt Options) bool

	// Parse reads raw and returns the documents it contains together with every
	// diagnostic it found. A defect in raw is a diagnostic, never an error: the
	// returned *Result is non-nil whenever err is nil, and err reports only a
	// programming or environment fault, such as a nil or cancelled ctx.
	//
	// Parse is deterministic. Identical raw and identical opt always produce a
	// byte-identical [Projection].
	Parse(ctx context.Context, raw []byte, opt Options) (*Result, error)
}

// A StreamParser is a [Format] that reads from an [io.Reader] without holding
// the file in memory. Formats whose grammar has a resync point - a line, a
// record, an array element - implement it, and the twin prefers it: a 50'000
// record file must parse in bounded memory.
//
// The relationship to [Format.Parse] is a contract, not a convention:
//
//	f.Parse(ctx, raw, opt)  and  ParseStream(ctx, f, bytes.NewReader(raw), opt)
//
// return the same [Result], with the same documents, the same diagnostics in
// the same order and the same [FileInfo], for every raw and every opt. The
// canonical formats satisfy it by implementing ParseStream and defining Parse
// as a call to it over a [bytes.Reader]; a format that implements only Parse
// satisfies it because [ParseStream] falls back to reading the stream.
type StreamParser interface {
	Format

	// ParseStream reads r to EOF and returns the same *Result Parse would
	// return for those bytes. It must not buffer more than one record plus its
	// own bounded lookahead.
	ParseStream(ctx context.Context, r io.Reader, opt Options) (*Result, error)
}

// Options carries the per-file parameters a [Format] needs. They come from the
// batch manifest (see BUILD-SPEC 8.1) for the file channel and from the request
// headers for the REST channel.
//
// BUILD-SPEC 10 names only Filename. The remaining fields are the manifest's own
// file entry, minus the parts the scanner rather than the importer verifies
// (path, sha256): an importer that cannot see the declared dialect cannot honor
// it, and honoring it is required by 8.1. Every field is optional and the zero
// Options is valid: UTF-8, the canonical CSV dialect, no declared count.
//
// The JSON tags are the manifest's own file-entry field names, so a manifest
// entry unmarshals straight into Options and a golden fixture can carry its
// options as a sibling file.
type Options struct {
	// Filename is the name of the file being parsed, used in diagnostics and
	// in [FileInfo]. It is never opened: a Format is handed bytes or a reader.
	Filename string `json:"filename,omitempty"`

	// Dataset is the manifest-declared [model.Dataset] the file carries, e.g.
	// "supplier". Formats whose record shape is dataset-specific require it;
	// formats that carry their own record types, such as kredexp-2.1, ignore
	// it. An empty Dataset means the file does not declare one.
	Dataset string `json:"dataset,omitempty"`

	// Encoding is the manifest-declared character set: one of [EncodingUTF8],
	// [EncodingUTF8BOM], [EncodingLatin1] or [EncodingCP1252]. Empty means
	// [EncodingUTF8].
	Encoding string `json:"encoding,omitempty"`

	// Dialect is the manifest-declared CSV dialect: delimiter, quote, escape,
	// header, line ending, decimal and thousands separator, date format, null
	// token and trim. It is [httpx.CSVDialect] rather than a type of this
	// package because its JSON tags are exactly the manifest's csv object, so a
	// manifest unmarshals straight into it and the twin's HTTP surface and its
	// file surface cannot drift apart.
	//
	// The non-CSV formats honor the subset that is not about field framing:
	// DecimalSeparator, ThousandsSeparator, DateFormat and NullToken.
	Dialect httpx.CSVDialect `json:"csv,omitempty"`

	// DeclaredRecords is the manifest's record_count for this file, or 0 when
	// none was declared. A format that finds a different number reports
	// [CodeTrailerCountMismatch] and leaves [Totals.TrailerVerified] false, so
	// a truncated transfer cannot be Accepted. The authoritative whole-file
	// reject stays the scanner's, which also verifies the sha256.
	DeclaredRecords int `json:"record_count,omitempty"`

	// MaxDiagnostics caps the diagnostics a [Result] carries. Zero or negative
	// means [MaxDiagnostics]. It exists for tests; production always uses the
	// default, because the cap is part of the published contract.
	MaxDiagnostics int `json:"max_diagnostics,omitempty"`
}

// errors reported by this package. They are programming and environment faults,
// never data defects.
var (
	// ErrNilContext reports a nil ctx handed to Parse. It is a programming
	// fault: every parse is cancellable.
	ErrNilContext = errors.New("importer: nil context")
	// ErrNotRegistered reports a lookup for a format id nothing registered.
	ErrNotRegistered = errors.New("importer: format not registered")
)

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Format)
)

// Register adds f to the registry under f.ID(). It is called from an init
// function, so the only way to reach it is to blank-import the package that
// holds the format - internal/importer/all is that one place.
//
// It panics on a nil Format, an empty id, or an id that is already registered.
// A duplicate id is a build-time mistake with a silent runtime consequence: two
// formats would answer to one profile name and which one won would depend on
// package initialization order.
func Register(f Format) {
	if f == nil {
		panic("importer: Register(nil)")
	}
	id := f.ID()
	if id == "" {
		panic("importer: Register with an empty format id")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[id]; dup {
		panic(fmt.Sprintf("importer: duplicate format id %q", id))
	}
	registry[id] = f
}

// Lookup returns the registered format with the given id.
func Lookup(id string) (Format, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[id]
	return f, ok
}

// MustLookup returns the registered format with the given id and panics when
// nothing registered it. It is for wiring code that cannot proceed without the
// format; a caller that can degrade uses [Lookup].
func MustLookup(id string) Format {
	f, ok := Lookup(id)
	if !ok {
		panic(fmt.Sprintf("%v: %q", ErrNotRegistered, id))
	}
	return f
}

// All returns every registered format in ascending byte order of its id. The
// order never depends on map iteration, so a caller that walks the registry into
// a report or a UI produces the same bytes on every run.
func All() []Format {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Format, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// IDs returns the ids of every registered format, ascending.
func IDs() []string {
	fs := All()
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.ID()
	}
	return out
}

// Head returns the leading [DetectHeadBytes] of raw, the slice
// [Format.Detect] is given. It never copies: detection must not cost a file's
// worth of allocation.
func Head(raw []byte) []byte {
	if len(raw) > DetectHeadBytes {
		return raw[:DetectHeadBytes]
	}
	return raw
}

// DetectFormat returns the one registered format that recognizes head. It
// reports false when none does and when more than one does: an ambiguous sniff
// is not a detection, and resolving it by registry order would make the answer
// depend on which formats happen to be linked in. A caller with a manifest uses
// the declared profile and [Lookup] instead, and reaches for detection only when
// the profile is absent.
func DetectFormat(head []byte, opt Options) (Format, bool) {
	var found Format
	for _, f := range All() {
		if !f.Detect(head, opt) {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = f
	}
	return found, found != nil
}

// ParseStream parses r with f in bounded memory when f implements
// [StreamParser], and otherwise reads r to EOF and calls [Format.Parse]. It is
// the entry point the twin uses, so that adding a streaming format changes no
// call site.
//
// The fallback is the only place in the framework that materializes a file, and
// it exists so a format can be written []byte-first and made streaming later
// without breaking its callers.
func ParseStream(ctx context.Context, f Format, r io.Reader, opt Options) (*Result, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if f == nil {
		return nil, fmt.Errorf("importer: ParseStream: %w", ErrNotRegistered)
	}
	if sp, ok := f.(StreamParser); ok {
		return sp.ParseStream(ctx, r, opt)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, fmt.Errorf("importer: ParseStream: reading %s: %w", opt.Filename, err)
	}
	return f.Parse(ctx, buf.Bytes(), opt)
}
