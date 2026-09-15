package importer

import (
	"sort"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// A Severity classifies what a [Diagnostic] does to the file and to the record
// it was found on. The set is closed and ordered by consequence.
type Severity string

// The three severities.
const (
	// SeverityFatal stops the file. The grammar broke somewhere no resync point
	// exists, or a file-level precondition failed, so nothing after the finding
	// can be trusted. A fatal diagnostic makes [Result.Accepted] false and, for
	// most formats, leaves [Result.Documents] short or empty.
	SeverityFatal Severity = "fatal"
	// SeverityReject drops one record and continues. The rest of the file is
	// still parsed, and every other record is still delivered: one bad line in
	// a twelve-thousand-line master-data extract must not cost the other
	// 11'999. A reject makes [Result.Accepted] false.
	SeverityReject Severity = "reject"
	// SeverityWarn reports something a human should see and a machine may
	// proceed past. Warnings never block: they travel with the record onto the
	// proposal and into the receipt, so a documented ambiguity is surfaced
	// rather than silently resolved. A warn alone leaves Accepted true.
	SeverityWarn Severity = "warn"
)

// Valid reports whether s is one of the three severities.
func (s Severity) Valid() bool {
	switch s {
	case SeverityFatal, SeverityReject, SeverityWarn:
		return true
	}
	return false
}

// Blocking reports whether s prevents acceptance. Fatal and reject block; warn
// does not.
func (s Severity) Blocking() bool {
	return s == SeverityFatal || s == SeverityReject
}

// The diagnostic codes the framework and the canonical formats share. A format
// may add its own - kredexp-2.1 does, and its set is published in
// docs/customer/KRED-EXP-2.1.md and BUILD-SPEC 10 - but it may never invent one
// outside its published list, because a code that is not published cannot be
// graded.
//
// The E_ codes that also exist as record-level errors on the ingest surface are
// re-exported from [model] rather than redeclared, so the file channel and the
// REST channel name the same defect with the same string.
const (
	// CodeEncoding reports a byte the declared encoding cannot represent: an
	// invalid UTF-8 sequence in a file declared UTF-8, or one of the five
	// undefined windows-1252 positions. It is reported at the offending record
	// and never replaced by U+FFFD, because a silent replacement character in a
	// supplier name is a corruption that survives into the ledger.
	CodeEncoding = "E_ENCODING"
	// CodeEncodingConflict reports two contradictory statements about how to
	// read the same bytes: an XML prolog that names one encoding and a manifest
	// that names another. The prolog wins per BUILD-SPEC 8.1, and the
	// disagreement is still reported, because one of the two senders is
	// misconfigured and the next file may be the one where it matters.
	CodeEncodingConflict = "ENCODING_CONFLICT"
	// CodeMalformedDocument reports a structural break with no safe resync
	// point, carrying the byte offset in [Diagnostic.Value]. It is fatal by
	// construction: a JSON array or an XML document that stops making sense
	// mid-way cannot be resumed without guessing where the next record starts.
	CodeMalformedDocument = "E_MALFORMED_DOCUMENT"
	// CodeDecimalFormat reports a numeric field outside the accepted shapes of
	// BUILD-SPEC 16.1. US grouping ("1,234,567.89") is explicitly one of these:
	// these feeds are Swiss and a comma is not a grouping separator.
	CodeDecimalFormat = "E_DECIMAL_FORMAT"
	// CodeMoneyScale reports more fraction digits than the field allows. It is
	// never a rounding: an input is not the place to round, because a rounded
	// input is how a supplier gets paid the wrong amount.
	CodeMoneyScale = "E_MONEY_SCALE"
	// CodeFieldUnknown reports a field the dataset's record shape does not
	// define. It is a warning: an extra column in a master-data extract is
	// usually a sender adding a field, and dropping the record over it would
	// turn a compatible change into an outage.
	CodeFieldUnknown = "E_FIELD_UNKNOWN"

	// CodeMoneyNotIntegerMinor reports a money field delivered as a JSON
	// number, or an amount that cannot be expressed exactly in the minor units
	// of its currency.
	CodeMoneyNotIntegerMinor = model.CodeMoneyNotIntegerMinor
	// CodeKeyNotString reports a natural key delivered as a JSON number. The
	// leading zeros of "0000417" are already gone by the time it parses, so the
	// record is rejected rather than reconstructed.
	CodeKeyNotString = model.CodeKeyNotString
	// CodeKeyMissing reports an empty natural-key component.
	CodeKeyMissing = model.CodeKeyMissing
	// CodeDateFormat reports a date that is neither an RFC 3339 calendar date
	// nor the manifest-declared pattern. An undeclared pattern is this code and
	// never a guess: guessing between 03/04/2026 and 04/03/2026 moves a due
	// date by a month.
	CodeDateFormat = model.CodeDateFormat
	// CodeCurrencyUnknown reports a currency outside the fixed table.
	CodeCurrencyUnknown = model.CodeCurrencyUnknown
	// CodeEnumUnknown reports a value outside a closed enumeration.
	CodeEnumUnknown = model.CodeEnumUnknown
	// CodeFieldRequired reports an empty mandatory field.
	CodeFieldRequired = model.CodeFieldRequired
	// CodeFieldInvalid reports a field that is well-formed alone and wrong in
	// context.
	CodeFieldInvalid = model.CodeFieldInvalid
	// CodeNumberFormat reports a non-monetary numeric field that is not a plain
	// integer, such as a payment term in days.
	CodeNumberFormat = model.CodeNumberFormat
	// CodeUoMUnknown reports a unit of measure outside the known set.
	CodeUoMUnknown = model.CodeUoMUnknown

	// CodeAmbiguousFieldSemantics reports a field whose meaning the source
	// specification leaves genuinely undecided. It is always a warning and
	// always accompanied by the value carried verbatim: the correct behavior
	// for an ambiguity is to surface it, never to resolve it. Converting an
	// ambiguous discount field into money, or inheriting an absent cost center
	// from a header, changes what a supplier gets paid.
	CodeAmbiguousFieldSemantics = "ambiguous_field_semantics"
	// CodeTrailerCountMismatch reports a declared record count that disagrees
	// with the number of records actually read. It is the truncated-transfer
	// guard.
	CodeTrailerCountMismatch = "trailer_count_mismatch"
	// CodeTrailerSumMismatch reports a declared control total that disagrees
	// with the sum actually read.
	CodeTrailerSumMismatch = "trailer_sum_mismatch"
	// CodeTrailerNotVerified reports that the trailer could not be checked
	// because a record was rejected, so the computed sums are known to be
	// incomplete. It is a warning, and it is why a count mismatch and a reject
	// never appear on the same file.
	CodeTrailerNotVerified = "trailer_not_verified"
	// CodeDiagnosticsTruncated is the single warning appended when a file
	// produced more than [MaxDiagnostics] findings. It always sorts last and
	// never affects [Result.Accepted], which is computed over the whole,
	// untruncated set.
	CodeDiagnosticsTruncated = "diagnostics_truncated"
)

// MaxDiagnostics is how many diagnostics a [Result] carries before the rest are
// dropped in favor of one [CodeDiagnosticsTruncated] warning. A file with
// twelve thousand broken lines has already told the operator everything the
// first thousand said, and an unbounded list turns one bad export into an
// out-of-memory report.
const MaxDiagnostics = 1000

// A Diagnostic is one finding about one place in a file. Every field is
// optional except Code, Severity and Message; the locators are filled in as far
// as the format can place the finding.
type Diagnostic struct {
	// Code is the machine-readable finding, from the published set above or
	// from the format's own published list. It is what a report is grouped by
	// and what an assertion names, so it is stable across releases.
	Code string `json:"code"`
	// Severity is what the finding does to the file. See [Severity].
	Severity Severity `json:"severity"`
	// Line is the 1-based line of the file the finding is on, or 0 when the
	// finding is about the file as a whole. For a record spanning several
	// physical lines it is the line the record starts on, because that is the
	// line an operator searches for.
	Line int `json:"line"`
	// Record is the 1-based ordinal of the record within the file, or 0 for a
	// file-level finding. It survives a reformatting that moves every line.
	Record int `json:"record"`
	// Field is the field at fault in the format's own vocabulary: a CSV column
	// name, a canonical JSON field, or the legacy German field name the
	// customer's own configuration and support tickets use. Empty when the
	// finding is about the record as a whole.
	Field string `json:"field"`
	// Value is the offending value, verbatim and untruncated, or the byte
	// offset for [CodeMalformedDocument]. It is diagnostic evidence: a
	// diagnostic that says "invalid amount" without saying which amount costs
	// an operator the round trip of finding the line themselves.
	Value string `json:"value"`
	// Document is the natural key of the document the finding belongs to, or
	// "" when no document could be identified. It is how a warning travels from
	// the importer onto a proposal.
	Document string `json:"document"`
	// Message is a short human explanation. It names the rule that was
	// violated and never the value the sender was supposed to send: a
	// diagnostic says what is wrong and why, never what the right answer would
	// have been.
	Message string `json:"message"`
}

// sortDiagnostics orders ds by line, then record, then field, then code, which
// is the order BUILD-SPEC 10 fixes: an operator reads a file top to bottom, so
// the primary key is where the finding is, not what it is.
//
// The sort is stable, so two findings identical in all four keys keep the order
// the format emitted them in. Emission order is itself deterministic, so the
// result is too.
func sortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		switch {
		case a.Line != b.Line:
			return a.Line < b.Line
		case a.Record != b.Record:
			return a.Record < b.Record
		case a.Field != b.Field:
			return a.Field < b.Field
		default:
			return a.Code < b.Code
		}
	})
}

// truncateDiagnostics caps ds at max entries and appends one
// [CodeDiagnosticsTruncated] warning naming how many findings were dropped. It
// assumes ds is already sorted, so the findings kept are the earliest ones in
// the file.
func truncateDiagnostics(ds []Diagnostic, max int) []Diagnostic {
	if max <= 0 {
		max = MaxDiagnostics
	}
	if len(ds) <= max {
		return ds
	}
	dropped := len(ds) - max
	out := make([]Diagnostic, 0, max+1)
	out = append(out, ds[:max]...)
	out = append(out, Diagnostic{
		Code:     CodeDiagnosticsTruncated,
		Severity: SeverityWarn,
		Message:  strconv.Itoa(dropped) + " further diagnostics were dropped at the " + strconv.Itoa(max) + " entry cap",
	})
	return out
}
