package canonical

import (
	"fmt"
	"unicode/utf8"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
)

// MaxRecordBytes caps one record. It is the 8 MiB per-chunk limit of the ingest
// surface, reused here so the file channel and the REST channel refuse the same
// oversized record.
//
// The cap exists because two of the three grammars allow one record to be
// arbitrarily long - a quoted CSV field may contain line breaks, an NDJSON line
// is a whole JSON value - so a file with a single unterminated quote would
// otherwise be read into memory in full while the parser waits for a closing
// quote that never comes. Exceeding it is fatal: a record longer than eight
// megabytes is a corrupted file, not a large invoice.
const MaxRecordBytes = 8 << 20

// The trim rules a dialect may declare.
const (
	trimNone  = "none"
	trimBoth  = "both"
	trimLeft  = "left"
	trimRight = "right"
)

// A dialect is the validated, rune-typed form of the manifest's declared
// dialect, together with the derived numeric and date formats. It is built once
// per file, so no cell parse repeats the validation.
type dialect struct {
	delim    rune
	quote    rune
	header   bool
	nullTok  string
	hasNull  bool
	trimL    bool
	trimR    bool
	dateFmt  string
	decSep   string
	thouSep  string
	encoding string
}

// resolveDialect validates opt and fills in the defaults. A dialect defect is a
// manifest defect rather than a record defect, so it is reported once, at file
// level, and the file is not parsed: every record would carry the same finding.
func resolveDialect(opt importer.Options) (dialect, *importer.Diagnostic) {
	d := opt.Dialect
	out := dialect{header: true, dateFmt: importer.DatePatternRFC3339, decSep: ".", encoding: opt.Encoding}
	if out.encoding == "" {
		out.encoding = importer.EncodingUTF8
	}
	if !importer.KnownEncoding(out.encoding) {
		return out, fileDiag(importer.SeverityFatal, importer.CodeEncoding, "encoding", out.encoding,
			"the declared encoding is not one of UTF-8, UTF-8-BOM, ISO-8859-1 or windows-1252")
	}

	var err error
	if out.delim, err = oneRune(d.Delimiter, ','); err != nil {
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "delimiter", d.Delimiter,
			"the declared delimiter must be exactly one character")
	}
	if out.quote, err = oneRune(d.Quote, '"'); err != nil {
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "quote", d.Quote,
			"the declared quote must be exactly one character")
	}
	if out.delim == out.quote {
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "quote", d.Quote,
			"the declared delimiter and quote must differ")
	}
	switch d.Escape {
	case "", "double":
	default:
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "escape", d.Escape,
			"only the doubled-quote escape scheme is supported; a backslash scheme is rejected rather than approximated")
	}
	if d.Header != nil {
		out.header = *d.Header
	}
	switch d.LineEnding {
	case "", "LF", "CRLF":
		// Both are accepted on read whatever is declared: a file written on
		// Windows and read on Linux is not a data defect, and rejecting one line
		// ending would make the declaration a trap rather than a hint.
	default:
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "line_ending", d.LineEnding,
			"the declared line ending must be LF or CRLF")
	}
	switch d.DecimalSeparator {
	case "":
	case ".", ",":
		out.decSep = d.DecimalSeparator
	default:
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "decimal_separator", d.DecimalSeparator,
			"the declared decimal separator must be . or ,")
	}
	out.thouSep = d.ThousandsSeparator
	if out.thouSep != "" && out.thouSep == out.decSep {
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "thousands_separator", out.thouSep,
			"the declared thousands separator and decimal separator must differ")
	}
	if d.NullToken != "" {
		out.nullTok, out.hasNull = d.NullToken, true
	}
	switch d.Trim {
	case "", trimNone:
	case trimBoth:
		out.trimL, out.trimR = true, true
	case trimLeft:
		out.trimL = true
	case trimRight:
		out.trimR = true
	default:
		return out, fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "trim", d.Trim,
			"the declared trim must be none, both, left or right")
	}
	if d.DateFormat != "" {
		out.dateFmt = d.DateFormat
	}
	if _, err := importer.ParseDate("2026-01-15", out.dateFmt); err != nil {
		return out, fileDiag(importer.SeverityFatal, importer.CodeDateFormat, "date_format", out.dateFmt,
			"the declared date format is not one of RFC3339, DD.MM.YYYY, DD/MM/YYYY, MM/DD/YYYY or YYYYMMDD")
	}
	return out, nil
}

// numeric returns the numeric format of a field allowing maxFrac fraction
// digits.
func (d dialect) numeric(maxFrac int) importer.NumericFormat {
	return importer.NumericFormat{
		DecimalSeparator:   d.decSep,
		ThousandsSeparator: d.thouSep,
		MaxFractionDigits:  maxFrac,
	}
}

// trim applies the declared trim rule to a cell.
func (d dialect) trim(s string) string {
	const ws = " \t\r\n"
	if d.trimL {
		s = trimLeftAny(s, ws)
	}
	if d.trimR {
		s = trimRightAny(s, ws)
	}
	return s
}

// trimLeftAny trims any of the cutset bytes from the left. It is byte-wise on
// purpose: the cutset is ASCII whitespace, so no multi-byte character can be
// split.
func trimLeftAny(s, cutset string) string {
	for len(s) > 0 && containsByte(cutset, s[0]) {
		s = s[1:]
	}
	return s
}

// trimRightAny trims any of the cutset bytes from the right.
func trimRightAny(s, cutset string) string {
	for len(s) > 0 && containsByte(cutset, s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

// containsByte reports whether s contains b.
func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

// oneRune decodes v as exactly one rune, defaulting to def when v is empty.
func oneRune(v string, def rune) (rune, error) {
	if v == "" {
		return def, nil
	}
	r, n := utf8.DecodeRuneInString(v)
	if r == utf8.RuneError || n != len(v) {
		return 0, fmt.Errorf("canonical: %q is not exactly one character", v)
	}
	return r, nil
}

// fileDiag builds a file-level diagnostic: no line, no record, no document.
func fileDiag(sev importer.Severity, code, field, value, msg string) *importer.Diagnostic {
	return &importer.Diagnostic{Code: code, Severity: sev, Field: field, Value: value, Message: msg}
}
