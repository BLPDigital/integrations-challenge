package httpx

import (
	"strings"
	"unicode/utf8"
)

// A CSVDialect is the CSV variant a caller uses. Its JSON tags are exactly the
// field names of the manifest's csv object, so a manifest's dialect unmarshals
// straight into this type:
//
//	{"delimiter":",","quote":"\"","escape":"double","header":true,
//	 "line_ending":"LF","decimal_separator":".","thousands_separator":"",
//	 "null_token":"","date_format":"RFC3339","trim":"both"}
//
// The zero value is the canonical dialect: comma, double quote, doubled-quote
// escaping, header row present, LF line endings, dot decimal separator, no
// grouping separator, no null token, no trimming, RFC3339 dates. Every field is
// therefore optional and a zero CSVDialect is always valid.
//
// Delimiter and Quote are strings rather than runes so the manifest maps
// directly; each must be exactly one rune.
type CSVDialect struct {
	// Delimiter separates fields. Default ",".
	Delimiter string `json:"delimiter,omitempty"`
	// Quote delimits a field that contains the delimiter, the quote itself or
	// a line break. Default "\"".
	Quote string `json:"quote,omitempty"`
	// Escape is the quote-escaping scheme. Only "double" is supported, which
	// is also the default; anything else is rejected rather than approximated.
	Escape string `json:"escape,omitempty"`
	// Header selects whether a header row is present. Nil means true. False is
	// rejected on both directions: this surface requires a header row, because
	// a positional CSV contract cannot be validated.
	Header *bool `json:"header,omitempty"`
	// LineEnding is "LF" (default) or "CRLF" and affects writing only; the
	// reader accepts both.
	LineEnding string `json:"line_ending,omitempty"`
	// DecimalSeparator is "." (default) or ",". When it is not ".", a cell that
	// is syntactically a number in this dialect is rewritten to canonical form
	// on read and back on write. A cell that is not syntactically a number is
	// never touched.
	DecimalSeparator string `json:"decimal_separator,omitempty"`
	// ThousandsSeparator is stripped from numeric cells on read and never
	// emitted on write. Default "" (no grouping).
	ThousandsSeparator string `json:"thousands_separator,omitempty"`
	// NullToken is the cell value that means JSON null. Default "" (no null
	// token), in which case an empty cell is the empty string, not null.
	NullToken string `json:"null_token,omitempty"`
	// Trim is "none" (default), "both", "left" or "right" and is applied to
	// every cell after parsing, quoted cells included.
	Trim string `json:"trim,omitempty"`
	// DateFormat is the calendar-date format of date cells: "RFC3339"
	// (YYYY-MM-DD, the default), "DD.MM.YYYY", "DD/MM/YYYY", "MM/DD/YYYY" or
	// "YYYYMMDD". A cell that does not match the declared format is passed
	// through verbatim. An unknown format is rejected.
	DateFormat string `json:"date_format,omitempty"`
	// Columns fixes the header and the column order when writing. Empty means
	// the sorted union of the keys of all records, which is deterministic but
	// alphabetical; a caller that needs a fixed header supplies it here.
	Columns []string `json:"columns,omitempty"`
}

// DefaultCSVDialect returns the canonical dialect: the zero CSVDialect, spelled
// out so a caller can copy and change one field.
func DefaultCSVDialect() CSVDialect {
	header := true
	return CSVDialect{
		Delimiter:        ",",
		Quote:            `"`,
		Escape:           "double",
		Header:           &header,
		LineEnding:       "LF",
		DecimalSeparator: ".",
		Trim:             "none",
		DateFormat:       "RFC3339",
	}
}

// csvResolved is the validated, rune-typed form of a CSVDialect.
type csvResolved struct {
	delim   rune
	quote   rune
	crlf    bool
	dec     string
	thou    string
	null    string
	hasNull bool
	trimL   bool
	trimR   bool
	dateFmt string
	columns []string
}

// resolve validates d and fills in the defaults.
func (d CSVDialect) resolve() (csvResolved, *Error) {
	var out csvResolved
	var err *Error
	if out.delim, err = oneRune(d.Delimiter, ',', "delimiter"); err != nil {
		return out, err
	}
	if out.quote, err = oneRune(d.Quote, '"', "quote"); err != nil {
		return out, err
	}
	if out.delim == out.quote {
		return out, MalformedBody("csv_dialect_unsupported", "csv delimiter and quote must differ")
	}
	switch d.Escape {
	case "", "double":
	default:
		return out, MalformedBody("csv_dialect_unsupported", "csv escape "+d.Escape+" is not supported")
	}
	if d.Header != nil && !*d.Header {
		return out, MalformedBody("csv_header_required", "csv without a header row is not supported")
	}
	switch d.LineEnding {
	case "", "LF":
	case "CRLF":
		out.crlf = true
	default:
		return out, MalformedBody("csv_dialect_unsupported", "csv line_ending "+d.LineEnding+" is not supported")
	}
	switch d.DecimalSeparator {
	case "", ".":
		out.dec = "."
	case ",":
		out.dec = ","
	default:
		return out, MalformedBody("csv_dialect_unsupported", "csv decimal_separator "+d.DecimalSeparator+" is not supported")
	}
	out.thou = d.ThousandsSeparator
	if out.thou == out.dec && out.thou != "" {
		return out, MalformedBody("csv_dialect_unsupported", "csv thousands_separator equals decimal_separator")
	}
	out.null, out.hasNull = d.NullToken, d.NullToken != ""
	switch d.Trim {
	case "", "none":
	case "both":
		out.trimL, out.trimR = true, true
	case "left", "start":
		out.trimL = true
	case "right", "end":
		out.trimR = true
	default:
		return out, MalformedBody("csv_dialect_unsupported", "csv trim "+d.Trim+" is not supported")
	}
	switch d.DateFormat {
	case "", "RFC3339", "YYYY-MM-DD":
		out.dateFmt = "RFC3339"
	case "DD.MM.YYYY", "DD/MM/YYYY", "MM/DD/YYYY", "YYYYMMDD":
		out.dateFmt = d.DateFormat
	default:
		return out, MalformedBody("csv_dialect_unsupported", "csv date_format "+d.DateFormat+" is not supported")
	}
	out.columns = d.Columns
	return out, nil
}

// oneRune decodes a single-rune dialect field, defaulting an empty value.
func oneRune(v string, def rune, field string) (rune, *Error) {
	if v == "" {
		return def, nil
	}
	r, size := utf8.DecodeRuneInString(v)
	if r == utf8.RuneError || size != len(v) {
		return 0, MalformedBody("csv_dialect_unsupported", "csv "+field+" must be exactly one character")
	}
	return r, nil
}

// trimCell applies the dialect's trim setting. Only ASCII space, tab, CR and LF
// are considered whitespace: locale-dependent Unicode space classes would make
// the result depend on a table we do not control.
func (d csvResolved) trimCell(s string) string {
	const cutset = " \t\r\n"
	if d.trimL {
		s = strings.TrimLeft(s, cutset)
	}
	if d.trimR {
		s = strings.TrimRight(s, cutset)
	}
	return s
}

// parseCSV splits s into records. It is a hand-written parser rather than
// encoding/csv because the dialect allows a quote character other than '"',
// which encoding/csv hardcodes. Behavior: fields are separated by the
// delimiter, records by LF or CRLF, a quoted field may contain the delimiter,
// line breaks and a doubled quote, and a final line break does not create a
// trailing empty record.
func parseCSV(s string, d csvResolved) ([][]string, *Error) {
	var (
		recs [][]string
		rec  []string
		sb   strings.Builder
		i    int
		line = 1
	)
	for i < len(s) {
		sb.Reset()
		// One field.
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == d.quote {
			i += size
			closed := false
			for i < len(s) {
				r, size = utf8.DecodeRuneInString(s[i:])
				if r == d.quote {
					i += size
					if i < len(s) {
						r2, size2 := utf8.DecodeRuneInString(s[i:])
						if r2 == d.quote {
							sb.WriteRune(d.quote)
							i += size2
							continue
						}
					}
					closed = true
					break
				}
				if r == '\n' {
					line++
				}
				sb.WriteRune(r)
				i += size
			}
			if !closed {
				return nil, MalformedBody("csv_unterminated_quote",
					"csv quoted field is not terminated").WithDetail("line", line)
			}
			// After a closing quote only a delimiter, a line break or the end
			// of the input is allowed.
			if i < len(s) {
				r, _ = utf8.DecodeRuneInString(s[i:])
				if r != d.delim && r != '\r' && r != '\n' {
					return nil, MalformedBody("csv_quote_trailing_data",
						"csv quoted field is followed by unquoted data").WithDetail("line", line)
				}
			}
		} else {
			for i < len(s) {
				r, size = utf8.DecodeRuneInString(s[i:])
				if r == d.delim || r == '\r' || r == '\n' {
					break
				}
				if r == d.quote {
					return nil, MalformedBody("csv_bare_quote",
						"csv unquoted field contains a quote character").WithDetail("line", line)
				}
				sb.WriteRune(r)
				i += size
			}
		}
		rec = append(rec, sb.String())

		if i >= len(s) {
			break
		}
		r, size = utf8.DecodeRuneInString(s[i:])
		switch {
		case r == d.delim:
			i += size
			if i >= len(s) {
				// Trailing delimiter: one more empty field, then end.
				rec = append(rec, "")
				recs = append(recs, rec)
				rec = nil
			}
		case r == '\r':
			i++
			if i < len(s) && s[i] == '\n' {
				i++
			}
			line++
			recs = append(recs, rec)
			rec = nil
		case r == '\n':
			i++
			line++
			recs = append(recs, rec)
			rec = nil
		}
	}
	if rec != nil {
		recs = append(recs, rec)
	}
	return recs, nil
}

// writeCSV renders header and rows in d's dialect.
func writeCSV(sb *strings.Builder, d csvResolved, header []string, rows [][]string) {
	eol := "\n"
	if d.crlf {
		eol = "\r\n"
	}
	writeCSVRow(sb, d, header, eol)
	for _, row := range rows {
		writeCSVRow(sb, d, row, eol)
	}
}

// writeCSVRow writes one row, quoting only the cells that need it.
func writeCSVRow(sb *strings.Builder, d csvResolved, row []string, eol string) {
	for i, cell := range row {
		if i > 0 {
			sb.WriteRune(d.delim)
		}
		if csvNeedsQuote(cell, d) {
			sb.WriteRune(d.quote)
			for _, r := range cell {
				if r == d.quote {
					sb.WriteRune(d.quote)
				}
				sb.WriteRune(r)
			}
			sb.WriteRune(d.quote)
			continue
		}
		sb.WriteString(cell)
	}
	sb.WriteString(eol)
}

// csvNeedsQuote reports whether cell must be quoted in d.
func csvNeedsQuote(cell string, d csvResolved) bool {
	for _, r := range cell {
		if r == d.delim || r == d.quote || r == '\r' || r == '\n' {
			return true
		}
	}
	return false
}

// numberFromDialect rewrites a cell that is syntactically a number in d into
// canonical form: grouping separators removed, the decimal separator turned
// into '.'. ok is false when the cell is not a number in this dialect, in which
// case the cell is passed through untouched. Leading zeros are preserved, so a
// key like "0000417" survives.
func numberFromDialect(cell string, d csvResolved) (string, bool) {
	if d.thou == "" && d.dec == "." {
		return cell, false
	}
	s := cell
	if d.thou != "" {
		s = strings.ReplaceAll(s, d.thou, "")
	}
	if d.dec != "." {
		if strings.Contains(s, ".") {
			return cell, false
		}
		if n := strings.Count(s, d.dec); n > 1 {
			return cell, false
		}
		s = strings.Replace(s, d.dec, ".", 1)
	}
	if !isPlainDecimal(s) || s == cell {
		return cell, false
	}
	return s, true
}

// numberToDialect rewrites a canonical decimal into d's decimal separator. No
// grouping separator is ever emitted, so a reader that strips grouping and a
// writer that omits it agree.
func numberToDialect(cell string, d csvResolved) (string, bool) {
	if d.dec == "." || !isPlainDecimal(cell) || !strings.Contains(cell, ".") {
		return cell, false
	}
	return strings.Replace(cell, ".", d.dec, 1), true
}

// isPlainDecimal reports whether s is an optionally negative decimal with at
// most one point and at least one digit: the shape ParseDecimal accepts.
func isPlainDecimal(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
	}
	digits, points := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			points++
			if points > 1 || i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return digits > 0
}

// dateFromDialect converts a cell in d's date format to YYYY-MM-DD. ok is false
// when the cell does not match the declared format, in which case it is passed
// through verbatim.
func dateFromDialect(cell string, d csvResolved) (string, bool) {
	if d.dateFmt == "RFC3339" {
		return cell, false
	}
	var y, m, day string
	switch d.dateFmt {
	case "DD.MM.YYYY", "DD/MM/YYYY":
		sep := d.dateFmt[2]
		if len(cell) != 10 || cell[2] != sep || cell[5] != sep {
			return cell, false
		}
		day, m, y = cell[0:2], cell[3:5], cell[6:10]
	case "MM/DD/YYYY":
		if len(cell) != 10 || cell[2] != '/' || cell[5] != '/' {
			return cell, false
		}
		m, day, y = cell[0:2], cell[3:5], cell[6:10]
	case "YYYYMMDD":
		if len(cell) != 8 {
			return cell, false
		}
		y, m, day = cell[0:4], cell[4:6], cell[6:8]
	default:
		return cell, false
	}
	if !allDigits(y) || !allDigits(m) || !allDigits(day) {
		return cell, false
	}
	return y + "-" + m + "-" + day, true
}

// dateToDialect converts a YYYY-MM-DD cell into d's date format. ok is false
// when the cell is not a calendar date in canonical form.
func dateToDialect(cell string, d csvResolved) (string, bool) {
	if d.dateFmt == "RFC3339" {
		return cell, false
	}
	if len(cell) != 10 || cell[4] != '-' || cell[7] != '-' {
		return cell, false
	}
	y, m, day := cell[0:4], cell[5:7], cell[8:10]
	if !allDigits(y) || !allDigits(m) || !allDigits(day) {
		return cell, false
	}
	switch d.dateFmt {
	case "DD.MM.YYYY":
		return day + "." + m + "." + y, true
	case "DD/MM/YYYY":
		return day + "/" + m + "/" + y, true
	case "MM/DD/YYYY":
		return m + "/" + day + "/" + y, true
	case "YYYYMMDD":
		return y + m + day, true
	}
	return cell, false
}

// allDigits reports whether s is non-empty and all ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
