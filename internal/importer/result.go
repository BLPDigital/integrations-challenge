package importer

import (
	"encoding/json"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// A FileInfo describes the bytes a [Result] came from. It is what makes a
// diagnostic reproducible: the sha256 identifies the exact bytes, so the raw
// store can hand them back and a reviewer can re-run the parse on them.
type FileInfo struct {
	// Name is [Options.Filename], verbatim.
	Name string `json:"name"`
	// Bytes is the length of the source in bytes, before decoding.
	Bytes int `json:"bytes"`
	// SHA256 is the lowercase hex sha256 of the source bytes, the same digest
	// the manifest declares and the raw store is keyed by.
	SHA256 string `json:"sha256"`
	// Encoding is the encoding the file was decoded with: the manifest's
	// declared one, defaulting to [EncodingUTF8]. A file whose own prolog
	// contradicts the manifest is reported as [CodeEncodingConflict] rather
	// than decoded twice, so this field never disagrees with the declaration.
	Encoding string `json:"encoding"`
	// Lines is the number of physical lines read, 0 for a format with no line
	// structure.
	Lines int `json:"lines"`
}

// A Document is one canonical record a [Format] produced, ready for the store.
//
// Payload is canonical JSON rather than a typed entity because a Result carries
// documents of whatever dataset the file holds, and because the twin's two
// channels must produce byte-identical content hashes: the REST path decodes
// into canonical JSON too (see [httpx.DecodeRecords]), so a supplier delivered
// as CSV in a batch and the same supplier delivered as JSON over HTTP hash the
// same.
type Document struct {
	// Dataset is the [model.Dataset] the document belongs to.
	Dataset string `json:"dataset"`
	// Key is the natural key, as [model.SupplierKey] and its siblings build it.
	// Composite keys are joined with [model.KeySeparator].
	Key string `json:"key"`
	// Line is the 1-based line the document starts on, 0 when the format has no
	// line structure.
	Line int `json:"line"`
	// Ordinal is the 1-based position of the document within the file, in
	// delivery order. It is the record_ordinal of a provenance entry.
	Ordinal int `json:"ordinal"`
	// Payload is the canonical JSON of the entity, as [model.CanonicalJSON]
	// produced it.
	Payload json.RawMessage `json:"payload"`
}

// Totals is the declared-versus-computed control block of a file. Declared
// values come from the file's own trailer, or from the manifest when the format
// has no trailer; computed values are what the parse actually saw. The pair is
// the whole point: a number the sender asserts and a number the receiver counts,
// compared before anything is posted.
//
// The decimal fields are exact decimal literals as [model.Decimal.String]
// renders them, so declared and computed compare textually as well as
// numerically. An empty string means the format declares no such total, which is
// distinct from a declared zero.
type Totals struct {
	// DeclaredDocuments is the document count the file or the manifest asserts,
	// 0 when none is asserted.
	DeclaredDocuments int `json:"declared_documents"`
	// ComputedDocuments is the number of documents actually produced.
	ComputedDocuments int `json:"computed_documents"`
	// DeclaredLines and ComputedLines are the same pair for subordinate records
	// - the Positionssätze of a legacy batch, the embedded lines of an invoice.
	DeclaredLines int `json:"declared_lines"`
	// ComputedLines is the number of subordinate records actually produced.
	ComputedLines int `json:"computed_lines"`
	// DeclaredGross and ComputedGross are the control total over document-level
	// amounts, summed across currencies exactly as the legacy trailer does.
	// Summing across currencies is arithmetically meaningless and is still the
	// right thing to compute: it is what the sender computed, so it is what
	// detects a lost record.
	DeclaredGross string `json:"declared_gross"`
	// ComputedGross is the sum actually read.
	ComputedGross string `json:"computed_gross"`
	// DeclaredLineSum and ComputedLineSum are the control total over line
	// amounts.
	DeclaredLineSum string `json:"declared_line_sum"`
	// ComputedLineSum is the line sum actually read.
	ComputedLineSum string `json:"computed_line_sum"`
	// TrailerDeclared reports that this file carries a control block at all: a
	// legacy trailer record, or a manifest record_count. When it is false there
	// is nothing to verify and the trailer condition on [Result.Accepted] is
	// satisfied vacuously.
	TrailerDeclared bool `json:"trailer_declared"`
	// TrailerVerified reports that every declared value was checked and
	// matched. It is false when a value disagreed and also when a record was
	// rejected, because the computed sums are then known to be incomplete and
	// comparing them would produce a second, misleading finding.
	TrailerVerified bool `json:"trailer_verified"`
}

// A Result is everything one parse of one file produced. It is non-nil whenever
// [Format.Parse] returned a nil error, including for a file that is entirely
// unreadable: the diagnostics say what happened and Accepted is false.
type Result struct {
	// Format is the [Format.ID] that produced this Result.
	Format string `json:"format"`
	// File describes the source bytes.
	File FileInfo `json:"file"`
	// Documents are the canonical records, in delivery order. Never nil: a file
	// that produced nothing carries an empty slice, so an absent list and an
	// empty list encode identically and hash identically.
	Documents []Document `json:"documents"`
	// Diagnostics are the findings, sorted by line, record, field then code,
	// capped at [MaxDiagnostics] plus one [CodeDiagnosticsTruncated] warning.
	// Never nil.
	Diagnostics []Diagnostic `json:"diagnostics"`
	// Totals is the declared-versus-computed control block.
	Totals Totals `json:"totals"`
	// Accepted reports that the file may be applied. See [Builder.Result] for
	// the exact rule.
	Accepted bool `json:"accepted"`
}

// Count returns how many diagnostics of the given severity the Result carries.
// It counts the retained diagnostics, so a truncated Result under-reports;
// [Result.Accepted] is computed before truncation and is authoritative.
func (r *Result) Count(s Severity) int {
	n := 0
	for _, d := range r.Diagnostics {
		if d.Severity == s {
			n++
		}
	}
	return n
}

// FirstFatal returns the first fatal diagnostic, in sorted order, and whether
// there is one. Fatal findings are ordered by position, so the first one is the
// place the file stopped making sense.
func (r *Result) FirstFatal() (Diagnostic, bool) {
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityFatal {
			return d, true
		}
	}
	return Diagnostic{}, false
}

// A Builder accumulates the parts of a [Result] and assembles them under the
// framework's rules, so that every format - ours and the candidate's - sorts,
// caps and accepts identically. A format that builds a Result by hand will get
// one of those three wrong.
//
// A Builder is not safe for concurrent use; one parse owns one Builder.
type Builder struct {
	formatID string
	file     FileInfo
	docs     []Document
	diags    []Diagnostic
	totals   Totals
	max      int

	fatal  int
	reject int
	warn   int
}

// NewBuilder returns a Builder for a parse of opt.Filename by the format with
// the given id. It records the declared encoding and the manifest's declared
// record count, so a format that reads neither still reports both.
func NewBuilder(formatID string, opt Options) *Builder {
	enc := opt.Encoding
	if enc == "" {
		enc = EncodingUTF8
	}
	b := &Builder{
		formatID: formatID,
		file:     FileInfo{Name: opt.Filename, Encoding: enc},
		max:      opt.MaxDiagnostics,
	}
	if opt.DeclaredRecords > 0 {
		b.totals.TrailerDeclared = true
		b.totals.DeclaredDocuments = opt.DeclaredRecords
	}
	return b
}

// SetSource records the source bytes: their length and their sha256. The bytes
// themselves are not retained, so a streaming format calls it with a hash it
// computed as it read.
func (b *Builder) SetSource(n int, sha256hex string) {
	b.file.Bytes = n
	b.file.SHA256 = sha256hex
}

// SetSourceBytes records the source bytes by hashing them. A streaming format
// uses [Builder.SetSource] instead, so that it never has to hold the file.
func (b *Builder) SetSourceBytes(raw []byte) {
	b.SetSource(len(raw), model.Sha256Hex(raw))
}

// SetLines records how many physical lines were read.
func (b *Builder) SetLines(n int) { b.file.Lines = n }

// Add records one diagnostic and counts it against acceptance. The count is
// kept here, before any truncation, so a fatal finding on line 40'000 of a file
// with a thousand earlier rejects still makes the file unacceptable even though
// the cap drops it from the report.
//
// It panics on an empty Code or an unknown [Severity]. Both are programming
// faults of the same class as a duplicate format id: a diagnostic with no code
// cannot be grouped, reported or asserted on, and a severity outside the three
// would silently be treated as non-blocking.
func (b *Builder) Add(d Diagnostic) {
	if d.Code == "" {
		panic("importer: Builder.Add with an empty diagnostic code")
	}
	if !d.Severity.Valid() {
		panic("importer: Builder.Add with severity " + string(d.Severity))
	}
	switch d.Severity {
	case SeverityFatal:
		b.fatal++
	case SeverityReject:
		b.reject++
	case SeverityWarn:
		b.warn++
	}
	b.diags = append(b.diags, d)
}

// AddDocument records one canonical document in delivery order and assigns its
// ordinal. It overwrites [Document.Ordinal]: the ordinal is the builder's to
// assign, so no format can produce a gap or a duplicate.
func (b *Builder) AddDocument(d Document) {
	d.Ordinal = len(b.docs) + 1
	b.docs = append(b.docs, d)
}

// Documents returns how many documents have been added so far.
func (b *Builder) Documents() int { return len(b.docs) }

// SetTotals records the control block. TrailerVerified is recomputed by
// [Builder.Result], which forces it false when any record was rejected, so a
// format cannot claim a verified trailer over an incomplete sum.
func (b *Builder) SetTotals(t Totals) { b.totals = t }

// Totals returns the control block as it stands, so a format can fill in the
// computed side incrementally.
func (b *Builder) Totals() Totals { return b.totals }

// Counts returns how many fatal, reject and warn diagnostics have been added,
// including any that truncation will later drop.
func (b *Builder) Counts() (fatal, reject, warn int) {
	return b.fatal, b.reject, b.warn
}

// HasFatal reports whether a fatal diagnostic has been added. A format uses it
// to stop reading: after a fatal finding there is no resync point, so continuing
// produces noise rather than information.
func (b *Builder) HasFatal() bool { return b.fatal > 0 }

// HasBlocking reports whether a fatal or reject diagnostic has been added. It is
// the condition under which a trailer must not be verified.
func (b *Builder) HasBlocking() bool { return b.fatal > 0 || b.reject > 0 }

// Result assembles the [Result].
//
// It sorts the diagnostics by line, record, field then code; caps them at
// [MaxDiagnostics] and appends one [CodeDiagnosticsTruncated] warning when it
// had to; guarantees Documents and Diagnostics are non-nil; fills in the
// computed document count when the format left it at zero; and computes
// Accepted:
//
//	Accepted == no fatal diagnostic
//	         && no reject diagnostic
//	         && (the trailer was not declared || the trailer verified)
//
// The counts are the untruncated ones. The trailer clause is vacuously true for
// a format that declares no control block - a canonical JSON extract with no
// manifest record count has nothing to verify - and it is forced false whenever
// a record was rejected, because the computed sums are then incomplete and a
// sum mismatch derived from them would be a second, misleading finding. That is
// exactly the rule BUILD-SPEC 10 states as "otherwise the warning
// trailer_not_verified".
//
// Result may be called once. The Builder must not be used afterwards.
func (b *Builder) Result() *Result {
	sortDiagnostics(b.diags)
	diags := truncateDiagnostics(b.diags, b.max)
	if diags == nil {
		diags = []Diagnostic{}
	}
	docs := b.docs
	if docs == nil {
		docs = []Document{}
	}
	totals := b.totals
	if totals.ComputedDocuments == 0 {
		totals.ComputedDocuments = len(docs)
	}
	if b.HasBlocking() {
		totals.TrailerVerified = false
	}
	accepted := b.fatal == 0 && b.reject == 0 && (!totals.TrailerDeclared || totals.TrailerVerified)
	return &Result{
		Format:      b.formatID,
		File:        b.file,
		Documents:   docs,
		Diagnostics: diags,
		Totals:      totals,
		Accepted:    accepted,
	}
}
