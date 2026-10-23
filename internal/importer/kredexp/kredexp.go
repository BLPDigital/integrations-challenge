// Package kredexp is the twin's importer for the legacy Kreditoren-Sammelexport
// of Steinbach Industrie AG, format version 2.1.
//
// BLP's own capture uses it to load the subsidiary's invoices into the twin.
//
// The format is specified by the customer's own interface document,
// docs/internal/KRED-EXP-2.1.md, and the diagnostic vocabulary by BUILD-SPEC 10.
// The document contradicts itself in three places, and BUILD-SPEC 5 fixes what a
// correct implementation does about each. All three are decided here the same
// way, which is to say not decided at all but surfaced:
//
//   - Skonto (Kopfsatz field 13) is an amount in the field table and a
//     percentage rate in the glossary. It is carried verbatim in
//     [model.APInvoice.DiscountRaw], never converted, and reported once per
//     document as [CodeAmbiguousFieldSemantics]. Converting it would change what
//     a supplier gets paid.
//   - An empty Kostenstelle on a Positionssatz may or may not inherit the
//     Kopfsatz's. It is left empty and reported once per Positionssatz. An empty
//     Steuercode, by contrast, is documented as inheriting the Kopfsatz's
//     MWST-Code, so it does inherit and is not reported: the difference between
//     the two fields is the whole point.
//   - The sign of a Gutschrift is taken from the explicit trailing minus and
//     never derived from Belegart GU. The trailer sums are the available
//     evidence and a derived flip contradicts them.
//
// Nothing here reads a clock, an environment variable, a random source or the
// filesystem, no map is ranged over into an output, no goroutine is started, no
// package-level variable is mutable, and no float64 appears on any path: an
// amount is a [model.Decimal] from the byte it is read to the byte it is
// written.
package kredexp

import (
	"context"
	"fmt"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
)

// ID is the format id this importer registers under and a manifest names as its profile.
const ID = "kredexp-2.1"

// FormatVersion is the only Formatversion (Vorlaufsatz field 2) this importer
// accepts. A file declaring anything else is [CodeFormatVersionUnsupported]:
// section 9.6 of the customer document puts v2.0 exports out of scope, and a
// v2.0 Positionssatz carries ten fields rather than eleven, so reading one as
// 2.1 would shift every field after the sixth.
const FormatVersion = "2.1"

// The record types (Satzarten) of the format, always capitalized. A file carries
// exactly one Vorlaufsatz as its first record and exactly one Nachlaufsatz as
// its last; between them each Kopfsatz is followed by its own Positionssätze,
// which are never interleaved with another Beleg's.
const (
	// RecordVorlauf is the file header record.
	RecordVorlauf = "VORLAUF"
	// RecordKopf is the document header record.
	RecordKopf = "KOPF"
	// RecordPos is the document position record.
	RecordPos = "POS"
	// RecordNachlauf is the file trailer record.
	RecordNachlauf = "NACHLAUF"
)

// Field counts per record type, from the customer's own field tables. A record
// with a different number of fields is [CodeFieldCountMismatch] and is not read
// further: after an arity change every position is a guess, and a guessed
// position is how an Einzelpreis becomes a Positionsbetrag.
const (
	// FieldsVorlauf is the field count of a Vorlaufsatz (section 4).
	FieldsVorlauf = 8
	// FieldsKopf is the field count of a Kopfsatz (section 5).
	FieldsKopf = 16
	// FieldsPos is the field count of a Positionssatz (section 6).
	FieldsPos = 11
	// FieldsNachlauf is the field count of a Nachlaufsatz (section 7).
	FieldsNachlauf = 5
)

// Fraction-digit limits per field, from section 3 of the customer document:
// "Amount fields always carry exactly two decimals, including 0.00. Einzelpreis
// carries two to four decimals. Menge carries up to three decimals."
//
// They are maxima here, and the asymmetry is deliberate. A value with MORE
// digits than its field allows is [CodeInvalidAmount] and is never rounded,
// because rounding an input is how a supplier gets paid the wrong amount. A
// value with FEWER loses nothing: an amount is normalized up to exactly two
// decimals, and a quantity or a unit price keeps the precision it was delivered
// with, so "45.5000" and "45.50" stay distinguishable in the store.
const (
	// ScaleAmount is the fraction-digit limit of a Betragsfeld.
	ScaleAmount = 2
	// ScaleUnitPrice is the fraction-digit limit of Einzelpreis.
	ScaleUnitPrice = 4
	// ScaleQuantity is the fraction-digit limit of Menge.
	ScaleQuantity = 3
)

// CenturyPivot is the two-digit-year pivot of the TTMMJJ date format: a year
// from 70 to 99 is 19xx, a year from 00 to 69 is 20xx (section 3). It is a rule
// of this interface and of no other, which is why the framework's
// [importer.ParseDate] does not know it.
const CenturyPivot = 70

// The diagnostic codes of this format, the closed set of BUILD-SPEC 10. Nothing
// outside it is ever emitted: a code that is not published cannot be graded, and
// the fixtures use only documented codes.
//
// The fatal codes are file level and the first one that fires wins, evaluated in
// the order they are declared here. The order is a precedence and not a position:
// an undecodable byte on line 900 outranks a missing Vorlaufsatz on line 1,
// because a file whose bytes cannot be read has not been read at all. A fatal
// finding therefore yields exactly one diagnostic and no documents.
//
// The reject codes are record level: the record is dropped, its findings are
// reported, and parsing continues with the next record, because one bad line in
// a twelve-thousand-line export must not cost the other 11'999.
const (
	// CodeEncodingBOMPresent reports a leading EF BB BF. Section 3 says
	// "Windows-1252 (CP1252). No byte order mark.", so a BOM means the file was
	// written by something other than the agreed exporter and the first field of
	// the first record is no longer "VORLAUF".
	CodeEncodingBOMPresent = "encoding_bom_present"
	// CodeEncodingInvalidByte reports a byte the declared character set cannot
	// represent: one of the five undefined windows-1252 positions (0x81, 0x8D,
	// 0x8F, 0x90, 0x9D), or a control byte other than TAB, CR and LF. Such a
	// byte is never replaced by U+FFFD: a replacement character in a supplier
	// name is a corruption that looks like data and survives into the ledger.
	CodeEncodingInvalidByte = "encoding_invalid_byte"
	// CodeMalformedQuoting reports quoting the file never resolves: a quoted
	// field that is not terminated before end of file, a quote inside an
	// unquoted field, or content after a closing quote. There is no resync
	// point after it, because whether the next semicolon separates two fields or
	// sits inside a text is exactly what is unknown.
	CodeMalformedQuoting = "malformed_quoting"
	// CodeVorlaufMissing reports a file whose first record is not a Vorlaufsatz,
	// the empty file included. Without it the run has no Mandant and no default
	// currency.
	CodeVorlaufMissing = "vorlauf_missing"
	// CodeFormatVersionUnsupported reports a Formatversion other than
	// [FormatVersion].
	CodeFormatVersionUnsupported = "format_version_unsupported"
	// CodeTrailerMissing reports a file whose last record is not a Nachlaufsatz.
	// Section 7 requires the receiver to verify the control totals, and a file
	// that carries none has not been verified.
	CodeTrailerMissing = "trailer_missing"

	// CodeUnknownRecordType reports a Satzart outside the four, and one of the
	// two file-level Satzarten in a position the grammar does not allow: a
	// second Vorlaufsatz, or a Nachlaufsatz that is not the last record.
	CodeUnknownRecordType = "unknown_record_type"
	// CodeFieldCountMismatch reports a record whose field count is not the one
	// its Satzart's field table fixes.
	CodeFieldCountMismatch = "field_count_mismatch"
	// CodeMissingRequiredField reports an empty field the customer's field table
	// marks M.
	CodeMissingRequiredField = "missing_required_field"
	// CodeInvalidDate reports a date that is not six digits (ten for the
	// Erstellungszeitpunkt), or one that names a day the calendar does not have.
	CodeInvalidDate = "invalid_date"
	// CodeInvalidAmount reports a numeric field outside the accepted shapes, one
	// carrying more fraction digits than its field allows, or one whose
	// magnitude the exact decimal type cannot hold.
	CodeInvalidAmount = "invalid_amount"
	// CodeOrphanLine reports a Positionssatz with no accepted Kopfsatz to belong
	// to: none preceded it, the one that did was rejected, or its Belegnummer
	// disagrees with the Kopfsatz it follows. It is reported once per
	// Positionssatz line, which is what makes a rejected Kopfsatz cost one
	// finding per line rather than one per field.
	CodeOrphanLine = "orphan_line"

	// CodeTrailerCountMismatch reports a declared record count that disagrees
	// with the number of records read.
	CodeTrailerCountMismatch = importer.CodeTrailerCountMismatch
	// CodeTrailerSumMismatch reports a declared control total that disagrees
	// with the sum of the records read.
	CodeTrailerSumMismatch = importer.CodeTrailerSumMismatch
	// CodeTrailerNotVerified reports that the trailer was not checked because a
	// record was rejected. A control total computed over an incomplete set of
	// records is not evidence, and comparing it anyway would produce a second,
	// misleading finding on top of the first real one.
	CodeTrailerNotVerified = importer.CodeTrailerNotVerified

	// CodeAmbiguousFieldSemantics reports a field whose meaning the customer's
	// own document leaves undecided: Skonto, and an empty Kostenstelle on a
	// Positionssatz. It is always a warning and the value always travels with
	// it, because the correct handling of an ambiguity is to surface it.
	CodeAmbiguousFieldSemantics = importer.CodeAmbiguousFieldSemantics
	// CodeDiagnosticsTruncated is appended by [importer.Builder] when a file
	// produces more findings than the published cap. This package never emits
	// it itself.
	CodeDiagnosticsTruncated = importer.CodeDiagnosticsTruncated
)

// format is the KRED-EXP 2.1 importer. It is an empty struct: an
// importer is used concurrently by the inbox scanner and by the REST ingest
// path, so it holds no state at all and every method is a pure function of its
// arguments.
type format struct{}

// New returns the KRED-EXP 2.1 importer.
func New() importer.Format { return format{} }

// init registers the format under [ID].
func init() { importer.Register(New()) }

// ID returns [ID].
func (format) ID() string { return ID }

// Detect reports whether head looks like a KRED-EXP export.
//
// The file has no column header row and every record begins with its Satzart, so
// the first eight bytes are the signature: a Vorlaufsatz followed by the field
// separator. The Formatversion is deliberately not part of the sniff, so that a
// v2.0 file is still recognized and answered with
// [CodeFormatVersionUnsupported] rather than silently belonging to no format at
// all.
//
// A byte order mark is tolerated here and fatal in [format.Parse]: detection
// decides which importer reads the file, and the file whose first three bytes
// are wrong is still this format's file to reject.
func (format) Detect(head []byte, _ importer.Options) bool {
	body, _ := importer.StripBOM(head)
	prefix := RecordVorlauf + string(fieldSeparator)
	if len(body) < len(prefix) {
		return false
	}
	return string(body[:len(prefix)]) == prefix
}

// Parse reads raw and returns the invoices it contains together with every
// finding, per the contract of [importer.Format]: a defect in the data is a
// [importer.Diagnostic] and never an error, and the returned Result is non-nil
// whenever the error is nil.
//
// The order of work is the precedence of BUILD-SPEC 10. The whole file is
// checked for a byte order mark, then for an undecodable byte, then for
// unresolvable quoting; only then is the first record looked at. Each of those
// three fatal findings ends the parse with one diagnostic and no documents,
// which is the honest answer: a file whose bytes or whose field framing cannot
// be trusted has no records, and reporting a hundred field-level findings
// derived from a misframed line would send an operator after a hundred problems
// that do not exist.
//
// [importer.StreamParser] is deliberately not implemented. The framework's
// fallback reads the stream and calls this method, so the two paths cannot
// disagree, and a genuine streaming implementation could not bound its memory
// anyway: an undecodable byte in the last record voids every document in the
// file, so no document may be emitted before end of file is reached.
func (format) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	if ctx == nil {
		return nil, importer.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", ID, err)
	}
	b := importer.NewBuilder(ID, opt)
	b.SetSourceBytes(raw)
	b.SetLines(countLines(raw))

	recs, f := scan(raw)
	if f != nil {
		b.Add(f.diagnostic())
		return b.Result(), nil
	}
	parseRecords(b, recs)
	return b.Result(), nil
}
