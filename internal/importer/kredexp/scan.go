package kredexp

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
)

// The lexical constants of the format (section 3): a semicolon separates fields,
// a double quote delimits a field that contains a separator, a quote or a line
// break, and a quote inside a quoted field is doubled. The record terminator is
// CRLF; a lone LF is accepted as one too, because a file that lost its carriage
// returns in transit is a transport defect the receiver can still read
// unambiguously, and refusing it would turn a recoverable delivery into a
// support ticket.
const (
	fieldSeparator = ';'
	textDelimiter  = '"'
	cr             = '\r'
	lf             = '\n'
)

// A fault is a file-level fatal finding: one of the three that end a parse
// before any record is interpreted. It is a value rather than an
// [importer.Diagnostic] so the scanner needs no knowledge of severities.
type fault struct {
	// code is the published diagnostic code.
	code string
	// line is the 1-based physical line the finding is on, 0 for a finding
	// about the file as a whole.
	line int
	// field is the field at fault in the customer's own vocabulary, empty when
	// the finding is not about one field.
	field string
	// value is the offending value, verbatim: the byte, the quoted fragment,
	// the version.
	value string
	// message names the rule that was violated.
	message string
}

// diagnostic renders the fault as the fatal diagnostic the Result carries.
func (f *fault) diagnostic() importer.Diagnostic {
	return importer.Diagnostic{
		Code:     f.code,
		Severity: importer.SeverityFatal,
		Line:     f.line,
		Field:    f.field,
		Value:    f.value,
		Message:  f.message,
	}
}

// A record is one lexed record: its fields, already decoded from windows-1252,
// and where it was found.
type record struct {
	// line is the 1-based physical line the record starts on. A record whose
	// quoted text contains a line break spans several lines and is reported on
	// the first, because that is the line an operator searches for.
	line int
	// ordinal is the 1-based position of the record in the file, blank lines
	// excluded.
	ordinal int
	// fields are the field values, verbatim: undoubled quotes, embedded line
	// breaks kept, nothing trimmed and nothing padded, because section 3 says
	// fields are not padded and every leading zero in this format is
	// significant.
	fields []string
}

// satzart returns field 1, the record type, or "" for a record with no fields.
func (r record) satzart() string { return r.field(1) }

// field returns the 1-based field n, or "" when the record is shorter than n.
// Absence and emptiness are deliberately the same value here: both mean "no
// value delivered", and which one a sender produced is a question about the
// field count, which [reader.arity] has already answered.
func (r record) field(n int) string {
	if n < 1 || n > len(r.fields) {
		return ""
	}
	return r.fields[n-1]
}

// countTerminators returns the number of record terminators in raw. A CRLF pair
// is one terminator and not two, which is the only reason this is a function
// rather than a byte count.
func countTerminators(raw []byte) int {
	n := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case cr:
			n++
			if i+1 < len(raw) && raw[i+1] == lf {
				i++
			}
		case lf:
			n++
		}
	}
	return n
}

// countLines returns the number of physical lines in raw: one per terminator,
// plus a final unterminated line when the file does not end with one. It is
// reported in [importer.FileInfo] and is computed independently of the lexer, so
// a file that fails the encoding check still reports its size honestly.
func countLines(raw []byte) int {
	lines := countTerminators(raw)
	if len(raw) > 0 && raw[len(raw)-1] != cr && raw[len(raw)-1] != lf {
		lines++
	}
	return lines
}

// lineOf returns the 1-based physical line the byte at offset sits on: one more
// than the number of terminators before it.
func lineOf(raw []byte, offset int) int {
	if offset > len(raw) {
		offset = len(raw)
	}
	return countTerminators(raw[:offset]) + 1
}

// scan turns raw into records, or returns the first fatal finding that stops it.
// The three checks run in the precedence BUILD-SPEC 10 fixes: byte order mark,
// then undecodable byte, then quoting. Each is a property of the whole file, so
// each is decided over the whole file before the next is considered.
func scan(raw []byte) ([]record, *fault) {
	if _, had := importer.StripBOM(raw); had {
		return nil, &fault{
			code:    CodeEncodingBOMPresent,
			line:    1,
			value:   "EF BB BF",
			message: "the file begins with a byte order mark; the agreed character set is windows-1252, which has none",
		}
	}
	if off, bad := firstInvalidByte(raw); bad {
		return nil, &fault{
			code:    CodeEncodingInvalidByte,
			line:    lineOf(raw, off),
			value:   fmt.Sprintf("0x%02X", raw[off]),
			message: "the byte at offset " + strconv.Itoa(off) + " is not a printable windows-1252 character; it is neither replaced nor dropped, because a replacement character in a supplier name is a corruption that looks like data",
		}
	}
	lx := &lexer{raw: raw, line: 1}
	return lx.records()
}

// firstInvalidByte returns the offset of the first byte that must not appear in
// a KRED-EXP file, and whether there is one.
//
// Two classes are rejected. The five windows-1252 positions 0x81, 0x8D, 0x8F,
// 0x90 and 0x9D are undefined, so a file carrying one was not written by a
// windows-1252 encoder and the rest of its high bytes cannot be trusted either.
// Control bytes other than TAB, CR and LF - the C0 range and DEL - are rejected
// too: the format's only structural bytes are the separator and the terminator,
// and a NUL or a form feed inside a Belegnummer is a transport defect, not a
// value.
func firstInvalidByte(raw []byte) (int, bool) {
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		switch b {
		case '\t', cr, lf:
			continue
		}
		if b < 0x20 || b == 0x7F {
			return i, true
		}
		if _, ok := importer.CP1252Rune(b); !ok {
			return i, true
		}
	}
	return 0, false
}

// decodeCP1252 decodes a run of already-validated windows-1252 bytes. Every byte
// it is given is defined, so the mapping cannot fail; [firstInvalidByte] is what
// guarantees that, and it runs over the whole file before the lexer starts.
func decodeCP1252(bs []byte) string {
	ascii := true
	for _, b := range bs {
		if b >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(bs)
	}
	var sb strings.Builder
	sb.Grow(len(bs) + 8)
	var scratch [utf8.UTFMax]byte
	for _, b := range bs {
		r, _ := importer.CP1252Rune(b)
		n := utf8.EncodeRune(scratch[:], r)
		sb.Write(scratch[:n])
	}
	return sb.String()
}

// A lexer splits the file into records and fields. It is the only place that
// knows about quoting, so every later stage sees plain field values.
type lexer struct {
	raw  []byte
	i    int
	line int
}

// records lexes the whole file. A line with no content at all is skipped rather
// than reported: it carries no Satzart, so there is nothing to reject, and a
// stray blank line at the end of a transfer is not a data defect.
func (lx *lexer) records() ([]record, *fault) {
	var out []record
	ordinal := 0
	for lx.i < len(lx.raw) {
		start := lx.line
		fields, f := lx.record()
		if f != nil {
			return nil, f
		}
		if len(fields) == 1 && fields[0] == "" {
			continue
		}
		ordinal++
		out = append(out, record{line: start, ordinal: ordinal, fields: fields})
	}
	return out, nil
}

// record lexes one record up to and including its terminator.
func (lx *lexer) record() ([]string, *fault) {
	fields := make([]string, 0, FieldsKopf)
	for {
		value, f := lx.field()
		if f != nil {
			return nil, f
		}
		fields = append(fields, value)
		if lx.i < len(lx.raw) && lx.raw[lx.i] == fieldSeparator {
			lx.i++
			continue
		}
		break
	}
	lx.terminator()
	return fields, nil
}

// field lexes one field, quoted or plain.
func (lx *lexer) field() (string, *fault) {
	if lx.i < len(lx.raw) && lx.raw[lx.i] == textDelimiter {
		return lx.quoted()
	}
	return lx.plain()
}

// plain lexes an unquoted field, which ends at the next separator, the next
// terminator or end of file. A quote inside it is [CodeMalformedQuoting]: the
// customer document uses the delimiter only around a whole field, so a quote in
// the middle of one means either an unescaped delimiter or a text that should
// have been quoted, and both change where the field ends.
func (lx *lexer) plain() (string, *fault) {
	start := lx.i
	for lx.i < len(lx.raw) {
		switch lx.raw[lx.i] {
		case fieldSeparator, cr, lf:
			return decodeCP1252(lx.raw[start:lx.i]), nil
		case textDelimiter:
			return "", &fault{
				code:    CodeMalformedQuoting,
				line:    lx.line,
				value:   decodeCP1252(lx.raw[start:min(lx.i+1, len(lx.raw))]),
				message: "a text delimiter appears inside an unquoted field; the delimiter encloses a whole field and a delimiter within one is doubled",
			}
		}
		lx.i++
	}
	return decodeCP1252(lx.raw[start:lx.i]), nil
}

// quoted lexes a quoted field: a doubled quote is one literal quote, a line
// break is content and advances the line counter, and the closing quote must be
// followed by a separator, a terminator or end of file.
//
// An unterminated quoted field is fatal and has no resync point. That is not a
// judgment call: the rest of the file is a single unterminated text as far as
// the grammar is concerned, so the next semicolon is either a separator or a
// character in a supplier's free text, and nothing in the file says which.
func (lx *lexer) quoted() (string, *fault) {
	openedAt := lx.line
	lx.i++ // the opening delimiter
	var sb strings.Builder
	for {
		if lx.i >= len(lx.raw) {
			return "", &fault{
				code:    CodeMalformedQuoting,
				line:    openedAt,
				value:   truncateEvidence(sb.String()),
				message: "a quoted field is not terminated before the end of the file; every remaining separator and line break is ambiguous, so no record after it can be framed",
			}
		}
		switch b := lx.raw[lx.i]; b {
		case textDelimiter:
			if lx.i+1 < len(lx.raw) && lx.raw[lx.i+1] == textDelimiter {
				sb.WriteByte(textDelimiter)
				lx.i += 2
				continue
			}
			lx.i++
			if lx.i < len(lx.raw) {
				switch lx.raw[lx.i] {
				case fieldSeparator, cr, lf:
				default:
					return "", &fault{
						code:    CodeMalformedQuoting,
						line:    lx.line,
						value:   truncateEvidence(sb.String()),
						message: "a closing text delimiter is followed by something other than a field separator or a record terminator; a delimiter inside a quoted field is doubled",
					}
				}
			}
			return sb.String(), nil
		case cr:
			// An embedded CRLF is kept as it was delivered, and so is a lone
			// CR: the field's bytes are what the sender typed, and normalizing
			// them would change a content hash that has to be reproducible
			// from the raw bytes.
			sb.WriteByte(cr)
			lx.i++
			if lx.i < len(lx.raw) && lx.raw[lx.i] == lf {
				sb.WriteByte(lf)
				lx.i++
			}
			lx.line++
		case lf:
			sb.WriteByte(lf)
			lx.i++
			lx.line++
		default:
			r, _ := importer.CP1252Rune(b)
			sb.WriteRune(r)
			lx.i++
		}
	}
}

// terminator consumes a record terminator when one is there and advances the
// line counter. End of file terminates the last record too: section 3 says every
// record ends with CRLF, and a transfer that lost the final one has still
// delivered a complete record.
func (lx *lexer) terminator() {
	if lx.i >= len(lx.raw) {
		return
	}
	switch lx.raw[lx.i] {
	case cr:
		lx.i++
		if lx.i < len(lx.raw) && lx.raw[lx.i] == lf {
			lx.i++
		}
		lx.line++
	case lf:
		lx.i++
		lx.line++
	}
}

// evidenceLimit caps how much of an unterminated text a diagnostic quotes. The
// evidence is for a human reading a report, and an unterminated quote can
// swallow a 20 MB file.
const evidenceLimit = 120

// truncateEvidence shortens a quoted fragment to [evidenceLimit] runes, marking
// that it was shortened. It cuts on a rune boundary, so the value stays valid
// UTF-8.
func truncateEvidence(s string) string {
	if utf8.RuneCountInString(s) <= evidenceLimit {
		return s
	}
	n := 0
	for i := range s {
		if n == evidenceLimit {
			return s[:i] + "..."
		}
		n++
	}
	return s
}
