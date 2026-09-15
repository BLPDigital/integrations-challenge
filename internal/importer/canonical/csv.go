package canonical

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// FormatCSV is the id of the canonical CSV profile.
const FormatCSV = "blp-csv-v1"

// csvFormat is the canonical CSV importer: a header row naming the canonical
// fields of one dataset, then one record per row, in the manifest-declared
// dialect.
type csvFormat struct{}

// NewCSV returns the canonical CSV format. It is registered by this package's
// init function; a caller that needs it by hand uses
// [importer.Lookup]([FormatCSV]).
func NewCSV() importer.Format { return csvFormat{} }

// ID returns [FormatCSV].
func (csvFormat) ID() string { return FormatCSV }

// Detect reports whether head looks like a canonical CSV file: a first line
// that splits on a plausible delimiter into cells of which at least one names a
// field of some canonical dataset.
//
// The declared delimiter is tried first. When the manifest declares none, comma,
// semicolon and tab are each tried, because a canonical extract written by a
// Swiss sender is as likely to be semicolon-separated as comma-separated and a
// detector that only knows the default would send it to the wrong format.
//
// It is deliberately conservative: a file whose header names no canonical field
// is not detected, even if it parses as CSV. That is what keeps it false for a
// legacy KRED-EXP export, whose first field is the record type VORLAUF and whose
// header row does not exist.
func (csvFormat) Detect(head []byte, opt importer.Options) bool {
	line := firstLine(head)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "<") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "{") {
		return false
	}
	delims := []string{",", ";", "\t"}
	if d := opt.Dialect.Delimiter; d != "" {
		delims = []string{d}
	}
	for _, d := range delims {
		cells := strings.Split(line, d)
		if len(cells) < 2 {
			continue
		}
		for _, c := range cells {
			if canonicalFieldNames[strings.TrimSpace(strings.Trim(c, `"`))] {
				return true
			}
		}
	}
	return false
}

// Parse reads raw. It is [csvFormat.ParseStream] over a reader on raw, which is
// the relationship [importer.StreamParser] fixes: the two paths share every line
// of code below the framing, so they cannot disagree.
func (f csvFormat) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	return f.ParseStream(ctx, bytes.NewReader(raw), opt)
}

// ParseStream reads r one record at a time. Memory is bounded by one record plus
// the reader's buffer, so a 50'000-record extract costs no more than a
// 50-record one.
//
// Recovery is per record: a row with the wrong number of fields is rejected and
// the next row is read, because the record separator of a CSV file is
// unambiguous even when a field is not. An unterminated quote is the one fatal
// defect, and it is fatal for a structural reason rather than a policy one: from
// the missing quote onwards, every delimiter and every line break is inside a
// string or outside one depending on a byte that is not there.
func (f csvFormat) ParseStream(ctx context.Context, r io.Reader, opt importer.Options) (*importer.Result, error) {
	if ctx == nil {
		return nil, importer.ErrNilContext
	}
	b := importer.NewBuilder(FormatCSV, opt)
	dial, bad := resolveDialect(opt)
	if bad != nil {
		b.Add(*bad)
		return b.Result(), nil
	}
	ds, ok := datasetOf(b, opt)
	if !ok {
		return b.Result(), nil
	}
	if !dial.header {
		b.Add(*fileDiag(importer.SeverityFatal, importer.CodeFieldInvalid, "header", "false",
			"the canonical CSV profile requires a header row: a positional column contract cannot be validated against the dataset"))
		return b.Result(), nil
	}

	counted := &countingReader{src: r, hash: sha256.New()}
	dec, err := importer.NewDecoder(dial.encoding, counted)
	if err != nil {
		return nil, err
	}
	sc := newCSVScanner(dec, dial)

	var header []string
	lines := 0
	records := 0
	docs := 0
	embedded := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := sc.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if !reportReadError(b, err, row.start, records+1) {
				return nil, err
			}
			break
		}
		fields, startLine := row.fields, row.start
		lines = row.end
		if header == nil {
			header = fields
			if d := checkHeader(header, startLine); d != nil {
				b.Add(*d)
				break
			}
			continue
		}
		if len(fields) == 1 && fields[0] == "" {
			// A blank line between records is whitespace, not a record. A file
			// that ends with a newline produces one, and rejecting it would
			// make every well-formed file report a defect.
			continue
		}
		records++
		if row.trailingQuoteData {
			b.Add(importer.Diagnostic{
				Code: importer.CodeFieldInvalid, Severity: importer.SeverityReject,
				Line: startLine, Record: records,
				Message: "a quoted field is followed by unquoted data, so what the value is meant to be is not decidable",
			})
			continue
		}
		if len(fields) != len(header) {
			b.Add(importer.Diagnostic{
				Code: importer.CodeFieldInvalid, Severity: importer.SeverityReject,
				Line: startLine, Record: records,
				Message: "the row has " + itoa(len(fields)) + " fields and the header has " + itoa(len(header)),
			})
			continue
		}
		rec := newRecord(startLine)
		for i, name := range header {
			rec.set(name, csvCell(fields[i], dial))
		}
		out := emitRecord(b, ds, rec, dial, records)
		if out.accepted {
			docs++
			embedded += out.lines
		}
		if b.HasFatal() {
			// A record-level path reported a fatal finding, which means the
			// file has stopped being interpretable. Reading on would produce
			// noise rather than information.
			break
		}
	}

	b.SetSource(counted.n, hex.EncodeToString(counted.hash.Sum(nil)))
	b.SetLines(lines)
	finishTotals(b, opt, records, embedded)
	return b.Result(), nil
}

// csvCell turns one parsed cell into a [cell], applying the declared trim and
// the declared null token. The order matters: the token is compared against the
// trimmed value, so a null token of "NULL" also matches " NULL " under a trim of
// "both" and does not match it under a trim of "none".
func csvCell(v string, d dialect) cell {
	v = d.trim(v)
	if d.hasNull && v == d.nullTok {
		return nullCell()
	}
	return textCell(v)
}

// checkHeader reports the header defects that make a file unparseable: no
// columns, an empty column name, or the same column twice. Each is fatal, because
// there is no per-record recovery from a header that does not name the fields.
func checkHeader(header []string, line int) *importer.Diagnostic {
	if len(header) == 0 || (len(header) == 1 && header[0] == "") {
		return &importer.Diagnostic{
			Code: importer.CodeFieldInvalid, Severity: importer.SeverityFatal, Line: line,
			Message: "the file has no header row",
		}
	}
	seen := make(map[string]bool, len(header))
	for i, name := range header {
		if name == "" {
			return &importer.Diagnostic{
				Code: importer.CodeFieldInvalid, Severity: importer.SeverityFatal, Line: line,
				Field: "column " + itoa(i+1), Message: "a header column has no name",
			}
		}
		if seen[name] {
			return &importer.Diagnostic{
				Code: importer.CodeFieldInvalid, Severity: importer.SeverityFatal, Line: line,
				Field: name, Message: "the header names this column twice, so a value in it is ambiguous",
			}
		}
		seen[name] = true
	}
	return nil
}

// A csvScanner reads records from a decoded stream in the given dialect. It
// tracks the physical line a record starts on and the line it ends on, so a
// record containing a quoted line break is reported at the line an operator
// would search for.
type csvScanner struct {
	br   *bufio.Reader
	d    dialect
	line int
	done bool
}

// newCSVScanner returns a scanner over r.
func newCSVScanner(r io.Reader, d dialect) *csvScanner {
	return &csvScanner{br: bufio.NewReaderSize(r, 64<<10), d: d, line: 0}
}

// A csvRow is one record as the scanner read it.
type csvRow struct {
	// fields are the record's cells, before the dialect's trim and null token.
	fields []string
	// start and end are the physical lines the record began and ended on. They
	// differ only for a record containing a quoted line break.
	start, end int
	// trailingQuoteData reports unquoted data after a closed quoted field, as in
	// `"abc"def`. The framing is unambiguous, so the file recovers; the value is
	// not, so the record is rejected.
	trailingQuoteData bool
}

// next returns the next record. It reports [io.EOF] when the stream is
// exhausted, an [importer.EncodingError] for an undecodable byte, and
// errUnterminatedQuote or errRecordTooLarge for the two fatal framing defects.
func (s *csvScanner) next() (csvRow, error) {
	if s.done {
		return csvRow{start: s.line, end: s.line}, io.EOF
	}
	s.line++
	row := csvRow{start: s.line}
	var (
		field   strings.Builder
		size    int
		inQuote bool
		closed  bool
	)
	appendField := func() {
		row.fields = append(row.fields, field.String())
		field.Reset()
		closed = false
	}
	for {
		r, _, err := s.br.ReadRune()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				row.end = s.line
				return row, err
			}
			s.done = true
			row.end = s.line
			if inQuote {
				return row, errUnterminatedQuote
			}
			if len(row.fields) == 0 && field.Len() == 0 {
				return row, io.EOF
			}
			appendField()
			return row, nil
		}
		size += len(string(r))
		if size > MaxRecordBytes {
			row.end = s.line
			return row, errRecordTooLarge
		}
		switch {
		case inQuote && r == s.d.quote:
			// A doubled quote inside a quoted field is one literal quote.
			nxt, _, err := s.br.ReadRune()
			switch {
			case err != nil && !errors.Is(err, io.EOF):
				row.end = s.line
				return row, err
			case errors.Is(err, io.EOF):
				inQuote, closed = false, true
			case nxt == s.d.quote:
				field.WriteRune(s.d.quote)
			default:
				inQuote, closed = false, true
				if err := s.br.UnreadRune(); err != nil {
					row.end = s.line
					return row, err
				}
			}
		case inQuote:
			if r == '\n' {
				s.line++
			}
			field.WriteRune(r)
		case r == s.d.quote && field.Len() == 0 && !closed:
			inQuote = true
		case r == s.d.delim:
			appendField()
		case r == '\r':
			// Swallowed: a CR before a LF is a line ending, and a lone CR in an
			// unquoted field is a transport artifact with no meaning here.
		case r == '\n':
			appendField()
			row.end = s.line
			return row, nil
		default:
			if closed {
				row.trailingQuoteData = true
			}
			field.WriteRune(r)
		}
	}
}

// The two fatal framing defects of a delimited file.
var (
	// errUnterminatedQuote reports a quoted field the file never closed.
	errUnterminatedQuote = errors.New("canonical: unterminated quoted field")
	// errRecordTooLarge reports a record over [MaxRecordBytes].
	errRecordTooLarge = errors.New("canonical: record exceeds the size limit")
)

// reportReadError turns a scanner or decoder error into a fatal diagnostic and
// reports whether it was a data defect. A false result means the error is an
// environment fault the caller must return as an error, because a failing disk
// is not something a receipt should call a bad file.
func reportReadError(b *importer.Builder, err error, line, ordinal int) bool {
	var enc *importer.EncodingError
	switch {
	case errors.As(err, &enc):
		b.Add(importer.Diagnostic{
			Code: importer.CodeEncoding, Severity: importer.SeverityFatal, Line: line, Record: ordinal,
			Value:   "offset " + itoa64(enc.Offset),
			Message: "the declared encoding " + enc.Encoding + " cannot represent this byte; it is reported rather than replaced, because a replacement character in a supplier name is a corruption that looks like data",
		})
		return true
	case errors.Is(err, errUnterminatedQuote):
		b.Add(importer.Diagnostic{
			Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal, Line: line, Record: ordinal,
			Message: "a quoted field is never closed; from here on, every delimiter and every line break is inside a string or outside one depending on a byte that is missing, so there is no safe place to resume",
		})
		return true
	case errors.Is(err, errRecordTooLarge):
		b.Add(importer.Diagnostic{
			Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal, Line: line, Record: ordinal,
			Value:   itoa(MaxRecordBytes),
			Message: "a single record exceeds the record size limit, which a file with an unclosed quote does before it runs out of file",
		})
		return true
	default:
		return false
	}
}

// datasetOf resolves the dataset the file carries, reporting the two ways that
// can fail: nothing declared, and something declared that is not a dataset.
func datasetOf(b *importer.Builder, opt importer.Options) (model.Dataset, bool) {
	if opt.Dataset == "" {
		b.Add(*fileDiag(importer.SeverityFatal, importer.CodeFieldRequired, "dataset", "",
			"the manifest has to declare which dataset this file carries: the canonical profiles carry one dataset per file and never guess it from the columns"))
		return "", false
	}
	ds := model.Dataset(opt.Dataset)
	if !ds.Valid() {
		b.Add(*fileDiag(importer.SeverityFatal, importer.CodeEnumUnknown, "dataset", opt.Dataset,
			"the declared dataset is not one of this landscape's datasets"))
		return "", false
	}
	return ds, true
}

// finishTotals fills in the computed side of the control block and compares it
// against the manifest's declared record count.
//
// A mismatch is a rejection rather than a warning: the declared count is the file
// channel's truncated-transfer guard, and a batch that applies 11'998 of 12'000
// supplier records because the transfer was cut short is exactly the silent
// partial load the guard exists to prevent. The authoritative whole-file reject
// stays the scanner's, which also verifies the sha256; this one makes the
// importer's own Result honest about it.
func finishTotals(b *importer.Builder, opt importer.Options, records, embedded int) {
	t := b.Totals()
	t.ComputedDocuments = b.Documents()
	t.ComputedLines = embedded
	if opt.DeclaredRecords > 0 {
		t.TrailerDeclared = true
		t.DeclaredDocuments = opt.DeclaredRecords
		switch {
		case b.HasBlocking():
			// The computed count is known to be short by the rejected records,
			// so comparing it would report a second finding for the first one.
			b.Add(importer.Diagnostic{
				Code: importer.CodeTrailerNotVerified, Severity: importer.SeverityWarn,
				Field:   "record_count",
				Value:   itoa(opt.DeclaredRecords),
				Message: "the declared record count was not verified, because a record was rejected and the computed count is therefore incomplete",
			})
		case records != opt.DeclaredRecords:
			b.Add(importer.Diagnostic{
				Code: importer.CodeTrailerCountMismatch, Severity: importer.SeverityReject,
				Field:   "record_count",
				Value:   itoa(records),
				Message: "the manifest declares " + itoa(opt.DeclaredRecords) + " records and the file carries a different number, which is what a truncated transfer looks like",
			})
		default:
			t.TrailerVerified = true
		}
	}
	b.SetTotals(t)
}

// A countingReader counts and hashes the source bytes as they are read, so a
// streaming parse still reports the file's length and sha256 without holding it.
type countingReader struct {
	src  io.Reader
	hash interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int
}

// Read implements io.Reader.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	if n > 0 {
		c.n += n
		if _, werr := c.hash.Write(p[:n]); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// firstLine returns the first line of b, without its line ending.
func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSuffix(string(bytes.TrimPrefix(b, importer.UTF8BOM)), "\r")
}

// itoa formats an int.
func itoa(n int) string { return strconv.Itoa(n) }

// itoa64 formats an int64.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
