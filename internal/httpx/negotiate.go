package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxRequestBodyBytes is the largest request body DecodeRecords will read: the
// 8 MiB per-chunk limit of the versioned ingest surface. A larger body is
// 413 with MALFORMED_BODY reason body_too_large rather than a silent
// truncation, because a truncated batch is exactly the failure the file
// channel's checksum exists to catch.
const MaxRequestBodyBytes = 8 << 20

// A recordField is one field of one record on the wire. Values are strings
// because CSV and XML carry no types; Null distinguishes an explicit null from
// the empty string.
type recordField struct {
	Name  string
	Value string
	Null  bool
}

// contextKey is the unexported key type of this package's context values.
type contextKey int

const (
	ctxCSVDialect contextKey = iota
	ctxDecision
)

// WithCSVDialect returns a context carrying the CSV dialect DecodeRecords and
// WriteNegotiated must use. The mandated signatures of those two functions have
// no dialect parameter, so the caller passes it here: a handler reads the
// dialect from the batch manifest, stamps it onto the request context and the
// negotiation layer honors it. Without it the canonical dialect applies.
func WithCSVDialect(ctx context.Context, d CSVDialect) context.Context {
	return context.WithValue(ctx, ctxCSVDialect, d)
}

// CSVDialectFrom returns the dialect stamped onto ctx by WithCSVDialect, or the
// canonical dialect when there is none.
func CSVDialectFrom(ctx context.Context) CSVDialect {
	if d, ok := ctx.Value(ctxCSVDialect).(CSVDialect); ok {
		return d
	}
	return CSVDialect{}
}

// ReadBody reads the request body once, caps it at MaxRequestBodyBytes and
// leaves a fresh reader over the same bytes in r.Body, so a handler can hash the
// body for idempotency and then still decode it.
func ReadBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodyBytes+1))
	if err != nil {
		return nil, MalformedBody("body_unreadable", "request body could not be read")
	}
	if len(buf) > MaxRequestBodyBytes {
		e := MalformedBody("body_too_large", "request body exceeds the chunk limit")
		e.Status = http.StatusRequestEntityTooLarge
		return nil, e
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}

// DecodeRecords decodes the request body into one canonical JSON object per
// record, in request order. The format comes from Content-Type; dataset is used
// only for diagnostics, so one handler can serve every dataset.
//
// Per format:
//
//   - application/json: a JSON array of objects, a {"records":[...]} envelope,
//     or a single object, which yields one record. Each element is compacted
//     but otherwise verbatim, so key order and number syntax survive.
//   - application/x-ndjson: one JSON object per line. Empty and whitespace-only
//     lines are skipped; a CR before the newline is tolerated.
//   - application/xml: the generic mapping documented in xmlrecords.go. Every
//     value becomes a JSON string.
//   - text/csv: a header row plus one row per record, in the dialect from
//     WithCSVDialect. Every value becomes a JSON string, subject to the
//     dialect's decimal separator, grouping separator, null token, trim and
//     date format.
//
// Records built from XML and CSV carry their fields in sorted key order, which
// is what makes two channels delivering the same data produce the same content
// hash.
//
// Errors are always *Error: 415 UNSUPPORTED_CONTENT_TYPE for a media type
// outside the four, and 400 MALFORMED_BODY otherwise, with details.reason from
// this closed vocabulary: body_unreadable, body_too_large, invalid_utf8,
// json_not_an_object, json_invalid, json_records_not_an_array, ndjson_invalid,
// csv_header_required, csv_header_missing, csv_empty_column_name,
// csv_duplicate_column, csv_field_count_mismatch, csv_dialect_unsupported,
// csv_unterminated_quote, csv_quote_trailing_data, csv_bare_quote,
// xml_not_well_formed, xml_unexpected_root, xml_unexpected_element,
// xml_unexpected_text, xml_nested_field, xml_duplicate_field.
func DecodeRecords(r *http.Request, dataset string) ([]json.RawMessage, error) {
	f, err := RequestFormat(r)
	if err != nil {
		return nil, withDataset(err, dataset)
	}
	body, err := ReadBody(r)
	if err != nil {
		return nil, withDataset(err, dataset)
	}
	if !utf8.Valid(body) {
		return nil, withDataset(MalformedBody("invalid_utf8",
			"request body is not valid UTF-8"), dataset)
	}
	var (
		recs  []json.RawMessage
		derr  *Error
		trim  = bytes.TrimSpace(body)
		empty = len(trim) == 0
	)
	switch f {
	case FormatJSON:
		if empty {
			return nil, nil
		}
		recs, derr = decodeJSONRecords(trim)
	case FormatNDJSON:
		recs, derr = decodeNDJSONRecords(body)
	case FormatXML:
		if empty {
			return nil, nil
		}
		var fields [][]recordField
		if fields, derr = decodeXMLRecords(body); derr == nil {
			recs = recordsFromFields(fields)
		}
	case FormatCSV:
		recs, derr = decodeCSVRecords(body, CSVDialectFrom(r.Context()))
	}
	if derr != nil {
		return nil, withDataset(derr, dataset)
	}
	return recs, nil
}

// withDataset annotates err with the dataset it happened on, so a rejection
// names the file and the dataset without the handler restating them.
func withDataset(err error, dataset string) error {
	if dataset == "" {
		return err
	}
	return AsError(err).WithDetail("dataset", dataset)
}

// decodeJSONRecords handles the three accepted JSON shapes.
func decodeJSONRecords(body []byte) ([]json.RawMessage, *Error) {
	switch body[0] {
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, MalformedBody("json_invalid", "json array is not valid: "+err.Error())
		}
		return compactAll(arr)
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(body, &obj); err != nil {
			return nil, MalformedBody("json_invalid", "json object is not valid: "+err.Error())
		}
		raw, ok := obj["records"]
		if !ok {
			return compactAll([]json.RawMessage{json.RawMessage(body)})
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, MalformedBody("json_records_not_an_array",
				"the records member must be an array")
		}
		return compactAll(arr)
	}
	return nil, MalformedBody("json_not_an_object",
		"json body must be an array, an object or a records envelope")
}

// decodeNDJSONRecords handles one JSON value per line.
func decodeNDJSONRecords(body []byte) ([]json.RawMessage, *Error) {
	var out []json.RawMessage
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimSuffix(line, []byte("\r")))
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, MalformedBody("ndjson_invalid",
				"ndjson line is not a valid json value").WithDetail("line", len(out)+1)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, line); err != nil {
			return nil, MalformedBody("ndjson_invalid", "ndjson line is not a valid json value")
		}
		out = append(out, json.RawMessage(buf.Bytes()))
	}
	return out, nil
}

// compactAll compacts every element so equal records are byte-equal.
func compactAll(in []json.RawMessage) ([]json.RawMessage, *Error) {
	out := make([]json.RawMessage, 0, len(in))
	for _, raw := range in {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return nil, MalformedBody("json_invalid", "json record is not valid")
		}
		out = append(out, json.RawMessage(buf.Bytes()))
	}
	return out, nil
}

// decodeCSVRecords parses a CSV body with a header row into records.
func decodeCSVRecords(body []byte, dialect CSVDialect) ([]json.RawMessage, *Error) {
	d, derr := dialect.resolve()
	if derr != nil {
		return nil, derr
	}
	rows, derr := parseCSV(string(body), d)
	if derr != nil {
		return nil, derr
	}
	if len(rows) == 0 {
		return nil, MalformedBody("csv_header_missing", "csv body has no header row")
	}
	header := make([]string, len(rows[0]))
	for i, name := range rows[0] {
		name = d.trimCell(name)
		if name == "" {
			return nil, MalformedBody("csv_empty_column_name",
				"csv header has an empty column name").WithDetail("column", i+1)
		}
		for _, prev := range header[:i] {
			if prev == name {
				return nil, MalformedBody("csv_duplicate_column",
					"csv header names "+name+" twice")
			}
		}
		header[i] = name
	}
	out := make([]json.RawMessage, 0, len(rows)-1)
	for i, row := range rows[1:] {
		if len(row) != len(header) {
			return nil, MalformedBody("csv_field_count_mismatch",
				"csv row has a different field count than the header").
				WithDetail("line", i+2).
				WithDetail("expected", len(header)).
				WithDetail("seen", len(row))
		}
		fields := make([]recordField, len(header))
		for j, cell := range row {
			fields[j] = csvCellToField(header[j], cell, d)
		}
		out = append(out, recordFromFields(fields))
	}
	return out, nil
}

// csvCellToField applies the dialect's cell transformations in a fixed order:
// trim, null token, number, date. A cell is at most one of a number and a date,
// so the order between those two is immaterial.
func csvCellToField(name, cell string, d csvResolved) recordField {
	cell = d.trimCell(cell)
	if d.hasNull && cell == d.null {
		return recordField{Name: name, Null: true}
	}
	if v, ok := numberFromDialect(cell, d); ok {
		return recordField{Name: name, Value: v}
	}
	if v, ok := dateFromDialect(cell, d); ok {
		return recordField{Name: name, Value: v}
	}
	return recordField{Name: name, Value: cell}
}

// recordsFromFields converts decoded field lists into canonical JSON objects.
func recordsFromFields(recs [][]recordField) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(recs))
	for _, fields := range recs {
		out = append(out, recordFromFields(fields))
	}
	return out
}

// recordFromFields renders one record as a JSON object with sorted keys. Values
// are JSON strings, or null for an explicit null field.
func recordFromFields(fields []recordField) json.RawMessage {
	sorted := make([]recordField, len(fields))
	copy(sorted, fields)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var buf []byte
	buf = append(buf, '{')
	for i, f := range sorted {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendJSONString(buf, f.Name)
		buf = append(buf, ':')
		if f.Null {
			buf = append(buf, "null"...)
			continue
		}
		buf = appendJSONString(buf, f.Value)
	}
	buf = append(buf, '}')
	return json.RawMessage(buf)
}

// appendJSONString appends s as a JSON string literal. It is hand-written
// rather than delegated to encoding/json so that HTML characters are never
// escaped to <: the canonical JSON of this project has no HTML escaping,
// and a supplier name containing an ampersand must hash the same on every
// channel.
func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			dst = append(dst, '\\', '"')
		case c == '\\':
			dst = append(dst, '\\', '\\')
		case c == '\n':
			dst = append(dst, '\\', 'n')
		case c == '\r':
			dst = append(dst, '\\', 'r')
		case c == '\t':
			dst = append(dst, '\\', 't')
		case c < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// fieldsFromRecord converts a JSON object into a sorted field list. Values are
// stringified: a string loses its quotes, a number keeps its exact literal
// (never parsed into a float), a bool becomes true or false, null becomes an
// explicit null field, and a nested object or array becomes its compact JSON
// text.
func fieldsFromRecord(raw json.RawMessage) ([]recordField, *Error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, Internal("record is not a json object: " + err.Error())
	}
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names)
	fields := make([]recordField, 0, len(names))
	for _, name := range names {
		value, null, err := stringifyJSON(obj[name])
		if err != nil {
			return nil, err
		}
		fields = append(fields, recordField{Name: name, Value: value, Null: null})
	}
	return fields, nil
}

// stringifyJSON renders one JSON value as text for a string-typed wire format.
func stringifyJSON(raw json.RawMessage) (string, bool, *Error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", true, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", false, Internal("record field is not a valid json string")
		}
		return s, false, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "", false, Internal("record field is not valid json")
	}
	return buf.String(), false, nil
}

// WriteNegotiated writes payload in the format the request asked for, with
// status 200. See WriteNegotiatedStatus.
func WriteNegotiated(w http.ResponseWriter, r *http.Request, payload any) error {
	return WriteNegotiatedStatus(w, r, http.StatusOK, payload)
}

// WriteNegotiatedStatus writes payload with the given status in the format
// ResponseFormat selects, and returns the *Error it wrote on failure so the
// caller can log it. An unsupported Accept is answered with
// 415 UNSUPPORTED_CONTENT_TYPE by this function; the caller must not write
// again.
//
// JSON carries the payload as it stands. The three record-shaped formats carry
// only the records: payload is marshalled, and then
//
//   - a JSON array is the record list,
//   - an object with a "records" array is an envelope, whose remaining scalar
//     members are emitted as X-Envelope-<Member> response headers so that
//     next_cursor and max_change_seq survive a CSV or XML response,
//   - any other object is a single record.
//
// CSV writes the header from the dialect's Columns, or the sorted union of all
// record keys when Columns is empty; a payload with no records and no Columns
// writes an empty body, because a header cannot be invented.
func WriteNegotiatedStatus(w http.ResponseWriter, r *http.Request, status int, payload any) error {
	f, err := ResponseFormat(r)
	if err != nil {
		Fail(w, err)
		return err
	}
	body, err := renderPayload(w.Header(), f, payload, CSVDialectFrom(r.Context()))
	if err != nil {
		Fail(w, err)
		return err
	}
	h := w.Header()
	h.Set("Content-Type", f.MediaType())
	h.Set("Vary", "Accept")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

// renderPayload renders payload in f, setting envelope headers on h for the
// record-shaped formats.
func renderPayload(h http.Header, f Format, payload any, dialect CSVDialect) ([]byte, error) {
	raw, err := marshalCanonical(payload)
	if err != nil {
		return nil, err
	}
	if f == FormatJSON {
		return append(raw, '\n'), nil
	}
	records, err := splitEnvelope(h, raw)
	if err != nil {
		return nil, err
	}
	switch f {
	case FormatNDJSON:
		var buf bytes.Buffer
		for _, rec := range records {
			buf.Write(rec)
			buf.WriteByte('\n')
		}
		return buf.Bytes(), nil
	case FormatXML:
		fields := make([][]recordField, 0, len(records))
		for _, rec := range records {
			ff, ferr := fieldsFromRecord(rec)
			if ferr != nil {
				return nil, ferr
			}
			fields = append(fields, ff)
		}
		var sb strings.Builder
		if ferr := encodeXMLRecords(&sb, fields); ferr != nil {
			return nil, ferr
		}
		return []byte(sb.String()), nil
	case FormatCSV:
		return encodeCSVRecords(records, dialect)
	}
	return nil, Internal("unreachable response format " + string(f))
}

// marshalCanonical marshals v without HTML escaping and without a trailing
// newline. Struct field order is the declaration order, which is deterministic;
// map keys are sorted by encoding/json, so no map iteration order reaches the
// wire.
func marshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, Internal("payload is not serializable: " + err.Error())
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// splitEnvelope extracts the record list from a marshalled payload and emits
// the envelope's scalar members as X-Envelope-<Member> headers. Member names
// are lowercased with underscores turned into hyphens, so next_cursor becomes
// X-Envelope-Next-Cursor. Values that are not scalar, or that contain a
// character no header may carry, are omitted rather than mangled.
func splitEnvelope(h http.Header, raw []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	switch trimmed[0] {
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, Internal("payload array is not valid json")
		}
		return arr, nil
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, Internal("payload object is not valid json")
		}
		recordsRaw, ok := obj["records"]
		if !ok {
			return []json.RawMessage{json.RawMessage(trimmed)}, nil
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(recordsRaw, &arr); err != nil {
			return nil, Internal("payload records member is not an array")
		}
		names := make([]string, 0, len(obj))
		for name := range obj {
			if name != "records" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if !isScalarJSON(obj[name]) {
				continue
			}
			value, null, err := stringifyJSON(obj[name])
			if err != nil || null {
				continue
			}
			if !headerSafe(value) || !isXMLName(strings.ReplaceAll(name, "_", "-")) {
				continue
			}
			h.Set("X-Envelope-"+strings.ReplaceAll(name, "_", "-"), value)
		}
		return arr, nil
	}
	return nil, Internal("payload must be an array or an object for this format")
}

// isScalarJSON reports whether raw is a JSON string, number or bool. A nested
// object or array has no header representation and is omitted rather than
// flattened into one.
func isScalarJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '{', '[':
		return false
	}
	return true
}

// headerSafe reports whether v may be carried in a response header: printable
// ASCII only, no CR, no LF, nothing above 0x7e.
func headerSafe(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

// encodeCSVRecords renders records as a header row plus one row per record.
func encodeCSVRecords(records []json.RawMessage, dialect CSVDialect) ([]byte, error) {
	d, derr := dialect.resolve()
	if derr != nil {
		return nil, derr
	}
	all := make([][]recordField, 0, len(records))
	for _, rec := range records {
		fields, ferr := fieldsFromRecord(rec)
		if ferr != nil {
			return nil, ferr
		}
		all = append(all, fields)
	}
	header := d.columns
	if len(header) == 0 {
		header = unionColumns(all)
	}
	if len(header) == 0 {
		return nil, nil
	}
	rows := make([][]string, 0, len(all))
	for _, fields := range all {
		row := make([]string, len(header))
		for i, col := range header {
			row[i] = csvCellFromFields(fields, col, d)
		}
		rows = append(rows, row)
	}
	var sb strings.Builder
	writeCSV(&sb, d, header, rows)
	return []byte(sb.String()), nil
}

// unionColumns returns the sorted union of the field names of every record.
func unionColumns(recs [][]recordField) []string {
	seen := make(map[string]bool)
	var out []string
	for _, fields := range recs {
		for _, f := range fields {
			if !seen[f.Name] {
				seen[f.Name] = true
				out = append(out, f.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// csvCellFromFields renders one cell, applying the dialect's date and decimal
// conventions. A field that is absent and a field that is the empty string are
// both the empty cell: CSV cannot tell them apart, and pretending otherwise
// would invent a distinction the format does not carry.
func csvCellFromFields(fields []recordField, name string, d csvResolved) string {
	for _, f := range fields {
		if f.Name != name {
			continue
		}
		if f.Null {
			return d.null
		}
		if v, ok := dateToDialect(f.Value, d); ok {
			return v
		}
		if v, ok := numberToDialect(f.Value, d); ok {
			return v
		}
		return f.Value
	}
	return ""
}
