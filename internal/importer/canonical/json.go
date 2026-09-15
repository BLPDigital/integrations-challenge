package canonical

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// FormatJSON is the id of the canonical JSON profile. It reads both a JSON array
// and NDJSON, distinguished by content rather than by a manifest field: the two
// shapes cannot be confused, so asking a sender to declare which one they wrote
// would only create a way for the declaration to be wrong.
const FormatJSON = "blp-json-v1"

// jsonFormat is the canonical JSON importer.
type jsonFormat struct{}

// NewJSON returns the canonical JSON and NDJSON format.
func NewJSON() importer.Format { return jsonFormat{} }

// ID returns [FormatJSON].
func (jsonFormat) ID() string { return FormatJSON }

// Detect reports whether head begins, after whitespace, with "[" or "{". Nothing
// else is JSON in this landscape: a bare number, a bare string and a bare
// boolean are all valid JSON documents and none of them is a record.
func (jsonFormat) Detect(head []byte, opt importer.Options) bool {
	b, _ := importer.StripBOM(head)
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '[', '{':
			return true
		default:
			return false
		}
	}
	return false
}

// Parse reads raw. It is [jsonFormat.ParseStream] over a reader on raw.
func (f jsonFormat) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	return f.ParseStream(ctx, bytes.NewReader(raw), opt)
}

// ParseStream reads r one record at a time, in the shape the content selects.
//
// A leading "[" is a JSON array. It streams: elements are decoded one at a time
// and never all held at once. A structural failure anywhere in the array is
// FATAL for the whole file, with the byte offset in the diagnostic, and this is
// the asymmetry with CSV that one scenario asserts. There is no safe resync
// point: a missing brace makes every following object a member of the wrong
// container, and scanning forward to the next "}," is a guess about data, which
// is how a batch silently loses the records it could not place.
//
// A leading "{" is NDJSON: one complete JSON value per line, and a line is where
// recovery happens. A line that does not parse is rejected and the next line is
// read, exactly as for a CSV row, because the line terminator is a record
// separator the format actually has. A single object on one line is a
// one-record file; an object with exactly one member "records" holding an array
// is an envelope, and its elements are the records.
//
// A JSON object pretty-printed across several lines is neither shape and is
// reported as such: it is not an array, and it is not one value per line. The
// remedy the diagnostic names is to wrap the records in an array, which is the
// shape that streams.
func (f jsonFormat) ParseStream(ctx context.Context, r io.Reader, opt importer.Options) (*importer.Result, error) {
	if ctx == nil {
		return nil, importer.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := importer.NewBuilder(FormatJSON, opt)
	dial, bad := resolveDialect(opt)
	if bad != nil {
		b.Add(*bad)
		return b.Result(), nil
	}
	ds, ok := datasetOf(b, opt)
	if !ok {
		return b.Result(), nil
	}

	counted := &countingReader{src: r, hash: sha256.New()}
	tracker := &lineTracker{src: counted}
	dec, err := importer.NewDecoder(dial.encoding, tracker)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(dec, 64<<10)

	shape, serr := jsonShape(br)
	if serr != nil {
		if !reportReadError(b, serr, 1, 0) {
			if !errors.Is(serr, io.EOF) {
				return nil, serr
			}
			b.Add(importer.Diagnostic{
				Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal, Line: 1,
				Message: "the file is empty: a canonical JSON file is an array of records or one record per line",
			})
		}
		b.SetSource(counted.n, hex.EncodeToString(counted.hash.Sum(nil)))
		return b.Result(), nil
	}

	var (
		records, embedded int
		perr              error
	)
	switch shape {
	case '[':
		records, embedded, perr = parseJSONArray(ctx, b, br, tracker, ds, dial)
	default:
		// The NDJSON path reads whole lines, so it counts them itself and the
		// tracker's offset index would be memory spent on nothing.
		tracker.stopTracking()
		records, embedded, perr = parseNDJSON(ctx, b, br, ds, dial)
	}
	if perr != nil {
		return nil, perr
	}

	b.SetSource(counted.n, hex.EncodeToString(counted.hash.Sum(nil)))
	b.SetLines(tracker.lines())
	finishTotals(b, opt, records, embedded)
	return b.Result(), nil
}

// jsonShape peeks at the first non-whitespace byte without consuming it, so the
// decoder that follows sees the document from its beginning.
func jsonShape(br *bufio.Reader) (byte, error) {
	for n := 1; ; n++ {
		buf, err := br.Peek(n)
		if err != nil {
			if len(buf) == 0 {
				return 0, err
			}
			return buf[len(buf)-1], nil
		}
		c := buf[n-1]
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c, nil
		}
	}
}

// parseJSONArray streams the elements of a JSON array.
func parseJSONArray(ctx context.Context, b *importer.Builder, br *bufio.Reader, tracker *lineTracker, ds model.Dataset, dial dialect) (int, int, error) {
	dec := json.NewDecoder(br)
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		fatalJSON(b, dec.InputOffset(), tracker.lineAt(dec.InputOffset()), err)
		return 0, 0, nil
	}
	records, embedded := 0, 0
	for dec.More() {
		if err := ctx.Err(); err != nil {
			return records, embedded, err
		}
		// The element is decoded into its raw bytes first, so its start offset
		// is exactly the offset after it minus its own length. That is how a
		// diagnostic can name the line the record starts on - the line an
		// operator searches for - rather than the line its closing brace
		// happens to fall on.
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			fatalJSON(b, dec.InputOffset(), tracker.lineAt(dec.InputOffset()), err)
			return records, embedded, nil
		}
		line := tracker.lineAt(dec.InputOffset() - int64(len(raw)))
		v, derr := decodeJSONValue(string(raw))
		if derr != nil {
			fatalJSON(b, dec.InputOffset(), line, derr)
			return records, embedded, nil
		}
		records++
		out := emitJSONValue(b, ds, dial, v, line, records)
		embedded += out.lines
	}
	if _, err := dec.Token(); err != nil {
		fatalJSON(b, dec.InputOffset(), tracker.lineAt(dec.InputOffset()), err)
	}
	return records, embedded, nil
}

// parseNDJSON reads one JSON value per line, recovering at every line.
func parseNDJSON(ctx context.Context, b *importer.Builder, br *bufio.Reader, ds model.Dataset, dial dialect) (int, int, error) {
	records, embedded := 0, 0
	line := 0
	for {
		if err := ctx.Err(); err != nil {
			return records, embedded, err
		}
		raw, err := readLine(br)
		if len(raw) == 0 && err != nil {
			if !errors.Is(err, io.EOF) {
				reportReadError(b, err, line+1, records+1)
			}
			return records, embedded, nil
		}
		line++
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			if err != nil {
				return records, embedded, nil
			}
			continue
		}
		v, derr := decodeJSONValue(trimmed)
		if derr != nil {
			records++
			b.Add(importer.Diagnostic{
				Code: importer.CodeMalformedDocument, Severity: importer.SeverityReject,
				Line: line, Record: records, Message: ndjsonAdvice(trimmed, derr),
			})
			if err != nil {
				return records, embedded, nil
			}
			continue
		}
		if elems, isEnvelope := jsonEnvelope(v); isEnvelope {
			for _, e := range elems {
				records++
				out := emitJSONValue(b, ds, dial, e, line, records)
				embedded += out.lines
			}
		} else {
			records++
			out := emitJSONValue(b, ds, dial, v, line, records)
			embedded += out.lines
		}
		if err != nil {
			return records, embedded, nil
		}
	}
}

// ndjsonAdvice explains a line that did not parse, and names the remedy for the
// one common near-miss: a pretty-printed object, which is valid JSON and is not
// one value per line.
func ndjsonAdvice(line string, err error) string {
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected end of JSON input") {
		return "this line is the beginning of a JSON value but not a whole one: a canonical JSON file is an array of records, or exactly one complete record per line. A record pretty-printed across several lines is neither; wrap the records in an array instead."
	}
	return "this line is not a JSON value: " + err.Error()
}

// decodeJSONValue decodes one complete JSON value, keeping numbers as literals so
// no float64 ever appears, and rejecting trailing content after the value.
func decodeJSONValue(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("the line carries more than one JSON value")
	}
	return v, nil
}

// jsonEnvelope recognizes {"records":[...]}: an object whose only member is a
// records array. Anything else, including an object that carries records
// alongside other members, is a record: a canonical record has no "records"
// field, so the shape is unambiguous.
func jsonEnvelope(v any) ([]any, bool) {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != 1 {
		return nil, false
	}
	arr, ok := obj["records"].([]any)
	if !ok {
		return nil, false
	}
	return arr, true
}

// emitJSONValue turns one decoded value into a document, or into the finding that
// it is not an object.
func emitJSONValue(b *importer.Builder, ds model.Dataset, dial dialect, v any, line, ordinal int) emitted {
	obj, ok := v.(map[string]any)
	if !ok {
		b.Add(importer.Diagnostic{
			Code: importer.CodeFieldInvalid, Severity: importer.SeverityReject,
			Line: line, Record: ordinal,
			Message: "a record is a JSON object; this element is not",
		})
		return emitted{}
	}
	return emitRecord(b, ds, jsonRecord(obj, dial, line), dial, ordinal)
}

// jsonRecord converts a decoded object into a record, in sorted key order so the
// unknown-field warnings of two deliveries of the same data agree.
func jsonRecord(obj map[string]any, dial dialect, line int) record {
	rec := newRecord(line)
	for _, k := range sortedKeys(obj) {
		rec.set(k, jsonCell(obj[k], dial, line))
	}
	return rec
}

// jsonCell converts a decoded JSON value into a cell. A string equal to the
// declared null token is a null, so a sender who writes "\\\\N" for absence gets
// the same result as one who writes null.
func jsonCell(v any, dial dialect, line int) cell {
	switch t := v.(type) {
	case nil:
		return nullCell()
	case string:
		s := dial.trim(t)
		if dial.hasNull && s == dial.nullTok {
			return nullCell()
		}
		return textCell(s)
	case json.Number:
		return numberCell(t.String())
	case bool:
		return boolCell(t)
	case map[string]any:
		out := make(map[string]cell, len(t))
		for k, mv := range t {
			out[k] = jsonCell(mv, dial, line)
		}
		return objectCell(out)
	case []any:
		rows := make([]record, 0, len(t))
		for _, e := range t {
			sub := newRecord(line)
			if obj, ok := e.(map[string]any); ok {
				for _, k := range sortedKeys(obj) {
					sub.set(k, jsonCell(obj[k], dial, line))
				}
			}
			rows = append(rows, sub)
		}
		return arrayCell(rows)
	default:
		return textCell("")
	}
}

// fatalJSON reports a structural break in a JSON array, with the byte offset the
// decoder had reached. The offset is what an operator needs: a JSON array has no
// line structure a parser can trust once the braces stop balancing.
func fatalJSON(b *importer.Builder, offset int64, line int, err error) {
	b.Add(importer.Diagnostic{
		Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal,
		Line:  line,
		Value: "offset " + itoa64(offset),
		Message: "the JSON array is structurally broken here, so the whole file is rejected: an array has no record separator to resume at, and skipping forward to the next object would be a guess about which container the following records belong to (" +
			err.Error() + ")",
	})
}

// readLine reads one line, without its terminator, capped at [MaxRecordBytes]. It
// returns what it read together with the error that ended it, so a final line
// with no terminator is not lost.
func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		chunk, err := br.ReadString('\n')
		sb.WriteString(chunk)
		if sb.Len() > MaxRecordBytes {
			return sb.String()[:MaxRecordBytes], errRecordTooLarge
		}
		if err != nil {
			return strings.TrimRight(sb.String(), "\r\n"), err
		}
		if strings.HasSuffix(chunk, "\n") {
			return strings.TrimRight(sb.String(), "\r\n"), nil
		}
	}
}

// sortedKeys returns the keys of m in ascending byte order. Every walk of a
// decoded object goes through it: ranging over a map into a record would make
// the order of the unknown-field warnings depend on Go's map seed, and the
// diagnostics would differ between two runs of the same file.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
