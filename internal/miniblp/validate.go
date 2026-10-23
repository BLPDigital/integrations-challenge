package miniblp

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer/canonical"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// IfVersionField is the optional per-record precondition on the REST channel:
// the version the sender believes is current, 0 for "must not exist". It is
// stripped from the record before the record is validated, so it never reaches
// the canonical payload and never changes a content hash.
const IfVersionField = "if_version"

// A Finding is one record-level error or warning, in the vocabulary of
// BUILD-SPEC 8.1 and 16.1: the record-level E_ codes of internal/model, the
// importer's own diagnostic codes, and the twin's ingest codes declared in
// server.go.
type Finding struct {
	// Code is the machine-readable finding.
	Code string `json:"code"`
	// Field is the field at fault, "" for a finding about the whole record.
	Field string `json:"field"`
	// Message says what is wrong and why. It never carries the value the sender
	// was supposed to send: an error body is not a correctness oracle.
	Message string `json:"message"`
	// Pointer locates the field in the delivered bytes: a JSON Pointer, an
	// XPath or line:col, whichever the delivered format has.
	Pointer string `json:"pointer,omitempty"`
}

// A RecordResult is what happened to one delivered record. The slice of them is
// the per-record half of an ingest response and of records.csv, always in
// request order.
type RecordResult struct {
	// Ordinal is the 1-based position of the record in its file or chunk.
	Ordinal int `json:"ordinal"`
	// Dataset is the dataset the record belongs to.
	Dataset string `json:"dataset"`
	// NaturalKey is the record's natural key, "" when the record was too broken
	// to have one.
	NaturalKey string `json:"natural_key"`
	// Outcome is one of the Outcome constants. Exactly one applies.
	Outcome string `json:"outcome"`
	// TwinID is the twin's surrogate id for the record, "" when none was
	// assigned because nothing was applied.
	TwinID string `json:"twin_id,omitempty"`
	// Version is the record's version after the delivery, 0 when nothing was
	// applied.
	Version int `json:"version,omitempty"`
	// ContentHash is the record's content hash after the delivery.
	ContentHash string `json:"content_hash,omitempty"`
	// Errors are the blocking findings; Warnings never block.
	Errors   []Finding `json:"errors"`
	Warnings []Finding `json:"warnings"`
	// Pointer locates the record itself in the delivered bytes.
	Pointer string `json:"pointer,omitempty"`
	// File is the file the record came from on the file channel, "" on REST.
	File string `json:"file,omitempty"`
	// Line is the 1-based line the record starts on, 0 when the format has no
	// line structure.
	Line int `json:"line,omitempty"`
	// ChunkOrdinal is the X-Chunk-Ordinal of the REST chunk that carried the
	// record, 0 on the file channel.
	ChunkOrdinal int64 `json:"chunk_ordinal,omitempty"`
	// AckEffect is the row of the ack table that applied, for a record of the
	// outbox_ack dataset, and "" for every other dataset.
	AckEffect string `json:"ack_effect,omitempty"`
	// RawExcerptSHA256 is the sha256 of the record's canonical payload, which is
	// its content hash, and "" for a record that produced no payload. The
	// importer framework does not retain per-record source bytes; the file's
	// bytes are reachable through the receipt's source_sha256.
	RawExcerptSHA256 string `json:"raw_excerpt_sha256,omitempty"`
}

// parsed is the outcome of decoding one file or one chunk: the documents that
// survived, the findings that stopped the rest, and the mapping from delivered
// record ordinal to both.
//
// Ordinals are reconstructed rather than taken from the importer, and the
// reconstruction is what makes the closure invariant of BUILD-SPEC 8.2 an
// invariant: the records seen are exactly the documents produced plus the
// distinct ordinals a blocking diagnostic named, so every record seen has one
// outcome and no record can be counted twice or not at all. A file-level
// diagnostic names no ordinal and is therefore a finding about the file, not a
// dropped record.
type parsed struct {
	// Seen is how many records the file or chunk delivered.
	Seen int
	// Docs maps a record ordinal to the document it produced.
	Docs map[int]importer.Document
	// Errors and Warnings map a record ordinal to its findings.
	Errors   map[int][]Finding
	Warnings map[int][]Finding
	// FileErrors and FileWarnings are the findings about the file as a whole.
	FileErrors   []Finding
	FileWarnings []Finding
	// Fatal reports a file-level fatal diagnostic: the file stopped making
	// sense and nothing after the finding can be trusted.
	Fatal bool
	// DocWarnings maps a document natural key to its warning codes, sorted and
	// deduplicated. It is how an importer warning travels onto a proposal.
	DocWarnings map[string][]string
	// Result is the importer's own Result, kept for the receipt's file entry.
	Result *importer.Result
}

// blocked reports whether anything in the file blocks acceptance: a fatal
// finding, or at least one rejected record.
func (p *parsed) blocked() bool { return p.Fatal || len(p.Errors) > 0 }

// datasetFor resolves a dataset name, reporting the twin's own code rather than
// a model validation error, because an unknown dataset is a manifest or query
// defect and not a record defect.
func datasetFor(name string) (model.Dataset, error) {
	if !model.IsKnownDataset(name) {
		e := &httpx.Error{Code: CodeDatasetUnknown, Message: "unknown dataset",
			Retriable: false, Status: 400}
		return "", e.WithDetail("dataset", name)
	}
	return model.Dataset(name), nil
}

// parseFile parses the bytes of one delivered file with the importer the
// manifest's profile resolves to, and returns them as a parsed. locate maps a
// record ordinal and a field to a pointer in the file's own shape.
func parseFile(ctx context.Context, f importer.Format, raw []byte, opt importer.Options,
	locate func(ordinal int, field string) string) (*parsed, error) {

	res, err := importer.ParseStream(ctx, f, bytes.NewReader(raw), opt)
	if err != nil {
		return nil, err
	}
	return fromResult(res, locate), nil
}

// parseRESTRecords validates the records of one REST chunk.
//
// The chunk's records have already been decoded from their wire format into one
// canonical JSON object each by httpx.DecodeRecords; they are re-emitted here as
// NDJSON and handed to the canonical JSON importer. That is deliberate and it is
// the mechanism behind the channel blindness of the state digest: the REST
// channel and the file channel then run the same field readers, the same model
// validators and the same canonicalization, so a supplier delivered as a CSV
// file in a batch and the same supplier delivered as XML over HTTP produce
// byte-identical stored payloads and therefore identical content hashes.
//
// The dialect is honored for a JSON, NDJSON or XML body. For a CSV body it is
// not: httpx.DecodeRecords has already applied the declared decimal separator,
// grouping separator, null token, trim rule and date format to every cell, and
// applying them a second time would be a second normalization of an already
// canonical value.
func parseRESTRecords(ctx context.Context, ds model.Dataset, format httpx.Format,
	records []json.RawMessage, dial httpx.CSVDialect,
	locate func(ordinal int, field string) string) (*parsed, []*int, error) {

	bodies := make([][]byte, 0, len(records))
	preconds := make([]*int, 0, len(records))
	for _, rec := range records {
		body, ifVersion := stripIfVersion(rec)
		bodies = append(bodies, body)
		preconds = append(preconds, ifVersion)
	}
	ndjson := bytes.Join(bodies, []byte("\n"))

	opt := importer.Options{
		Filename: "chunk." + format.String(),
		Dataset:  ds.String(),
		Encoding: importer.EncodingUTF8,
	}
	if format != httpx.FormatCSV {
		opt.Dialect = dial
	}
	res, err := importer.ParseStream(ctx, canonical.NewJSON(), bytes.NewReader(ndjson), opt)
	if err != nil {
		return nil, nil, err
	}
	p := fromResult(res, locate)
	// The re-emitted body is one record per line, so the importer's line number
	// is the request ordinal. A record that produced no document therefore still
	// has a place in the response, which is what "results in request order"
	// means.
	if p.Seen < len(records) {
		p.Seen = len(records)
	}
	return p, preconds, nil
}

// stripIfVersion removes the optional if_version member from a delivered record
// and returns the record without it plus the precondition it declared. A member
// that is not a whole number is ignored rather than rejected: it is not part of
// the record's content, and a malformed precondition must not be able to change
// what is stored.
func stripIfVersion(rec json.RawMessage) ([]byte, *int) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(rec, &obj); err != nil {
		return rec, nil
	}
	raw, ok := obj[IfVersionField]
	if !ok {
		return rec, nil
	}
	delete(obj, IfVersionField)
	var want *int
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		v := n
		want = &v
	}
	// Re-encoded from a map, so the member order is encoding/json's sorted one.
	// That is harmless: the canonical form is what gets hashed, and it sorts
	// anyway.
	buf, err := json.Marshal(obj)
	if err != nil {
		return rec, want
	}
	return buf, want
}

// fromResult turns an importer Result into a parsed, reconstructing the record
// ordinals. locate, when not nil, maps a record ordinal to a pointer into the
// delivered bytes.
func fromResult(res *importer.Result, locate func(ordinal int, field string) string) *parsed {
	p := &parsed{
		Docs:        map[int]importer.Document{},
		Errors:      map[int][]Finding{},
		Warnings:    map[int][]Finding{},
		DocWarnings: map[string][]string{},
		Result:      res,
	}
	if locate == nil {
		locate = func(int, string) string { return "" }
	}

	blockedSet := map[int]bool{}
	for _, d := range res.Diagnostics {
		if d.Severity == importer.SeverityFatal {
			p.Fatal = true
		}
		if d.Record <= 0 {
			f := Finding{Code: d.Code, Field: d.Field, Message: d.Message}
			if d.Severity.Blocking() {
				p.FileErrors = append(p.FileErrors, f)
			} else {
				p.FileWarnings = append(p.FileWarnings, f)
			}
			continue
		}
		if d.Severity.Blocking() {
			blockedSet[d.Record] = true
		}
	}
	blocked := make([]int, 0, len(blockedSet))
	for ord := range blockedSet {
		blocked = append(blocked, ord)
	}
	sort.Ints(blocked)

	// Walk the ordinals in order, handing each one either to the next document
	// or to the rejected record that claimed it.
	p.Seen = len(res.Documents) + len(blocked)
	next := 0
	isBlocked := func(ord int) bool { return blockedSet[ord] }
	for ord := 1; ord <= p.Seen; ord++ {
		if isBlocked(ord) {
			continue
		}
		if next < len(res.Documents) {
			p.Docs[ord] = res.Documents[next]
			next++
		}
	}

	// Attach the record-level findings. A diagnostic that names its document is
	// attached by key, which is what makes an importer whose record numbering
	// differs from the reconstruction still report its findings on the right
	// row; everything else is attached by ordinal.
	keyOrdinal := map[string]int{}
	for ord, doc := range p.Docs {
		if doc.Key != "" {
			if prev, ok := keyOrdinal[doc.Key]; !ok || ord < prev {
				keyOrdinal[doc.Key] = ord
			}
		}
	}
	for _, d := range res.Diagnostics {
		if d.Record <= 0 {
			continue
		}
		ord := d.Record
		if d.Document != "" {
			if o, ok := keyOrdinal[d.Document]; ok {
				ord = o
			}
		}
		f := Finding{Code: d.Code, Field: d.Field, Message: d.Message, Pointer: locate(ord, d.Field)}
		if d.Severity.Blocking() {
			p.Errors[d.Record] = append(p.Errors[d.Record], f)
			continue
		}
		p.Warnings[ord] = append(p.Warnings[ord], f)
		if d.Document != "" {
			p.DocWarnings[d.Document] = appendCode(p.DocWarnings[d.Document], d.Code)
		} else if doc, ok := p.Docs[ord]; ok && doc.Key != "" {
			p.DocWarnings[doc.Key] = appendCode(p.DocWarnings[doc.Key], d.Code)
		}
	}
	return p
}

// appendCode adds a code to a sorted, deduplicated list. Proposal warnings are
// compared by content, so the same set may never produce two orderings.
func appendCode(list []string, code string) []string {
	for _, c := range list {
		if c == code {
			return list
		}
	}
	list = append(list, code)
	sort.Strings(list)
	return list
}

// pointerLocator returns the function that locates a field of the nth delivered
// record in the bytes the client sent, in the shape the delivered format has:
//
//   - application/json: a JSON Pointer, "/2/vat_amount" into the record array,
//     "/records/2/vat_amount" into a {"records":[...]} envelope, and
//     "/vat_amount" for a body that is one bare object;
//   - application/x-ndjson: the line, then a JSON Pointer into that line's
//     object: "3:/vat_amount";
//   - application/xml: an XPath, "/records/record[3]/vat_amount";
//   - text/csv: line:col, counting the header row as line 1, with column 0 when
//     the field has no column of its own.
func pointerLocator(format httpx.Format, body []byte, dial httpx.CSVDialect) func(int, string) string {
	switch format {
	case httpx.FormatNDJSON:
		return func(ord int, field string) string {
			return strconv.Itoa(ord) + ":" + jsonPointer(field)
		}
	case httpx.FormatXML:
		return func(ord int, field string) string {
			p := "/records/record[" + strconv.Itoa(ord) + "]"
			if field != "" {
				p += "/" + field
			}
			return p
		}
	case httpx.FormatCSV:
		header := csvHeader(body, dial)
		return func(ord int, field string) string {
			col := 0
			for i, name := range header {
				if name == field {
					col = i + 1
					break
				}
			}
			return strconv.Itoa(ord+1) + ":" + strconv.Itoa(col)
		}
	default:
		prefix, indexed := jsonPointerShape(body)
		return func(ord int, field string) string {
			p := prefix
			if indexed {
				p += "/" + strconv.Itoa(ord-1)
			}
			return p + jsonPointer(field)
		}
	}
}

// jsonPointer renders one field name as a JSON Pointer segment, escaping the two
// characters RFC 6901 reserves. An empty field addresses the record itself.
func jsonPointer(field string) string {
	if field == "" {
		return ""
	}
	f := strings.ReplaceAll(field, "~", "~0")
	f = strings.ReplaceAll(f, "/", "~1")
	return "/" + f
}

// jsonPointerShape reports the pointer prefix of a JSON body and whether records
// are addressed by index: a bare array is indexed with no prefix, an envelope is
// indexed under /records, and a single object is neither.
func jsonPointerShape(body []byte) (string, bool) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 {
		return "", true
	}
	if trimmed[0] == '[' {
		return "", true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(trimmed, &obj) == nil {
		if raw, ok := obj["records"]; ok && len(obj) == 1 {
			var arr []json.RawMessage
			if json.Unmarshal(raw, &arr) == nil {
				return "/records", true
			}
		}
		return "", false
	}
	return "", true
}

// csvHeader returns the column names of a CSV body's header row, in order.
//
// It is a header-row scanner and not a CSV reader: it honors the dialect's
// delimiter and quote so a quoted column name containing the delimiter is read
// correctly, and it stops at the first record terminator. The full dialect has
// already been honored by httpx.DecodeRecords; all that is wanted here is the
// column number a finding points at.
func csvHeader(body []byte, dial httpx.CSVDialect) []string {
	delim := byte(',')
	if r := []rune(dial.Delimiter); len(r) == 1 && r[0] < 0x80 {
		delim = byte(r[0])
	}
	quote := byte('"')
	if r := []rune(dial.Quote); len(r) == 1 && r[0] < 0x80 {
		quote = byte(r[0])
	}
	var out []string
	var field bytes.Buffer
	inQuote := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case inQuote && c == quote:
			if i+1 < len(body) && body[i+1] == quote {
				field.WriteByte(quote)
				i++
				continue
			}
			inQuote = false
		case c == quote && field.Len() == 0:
			inQuote = true
		case !inQuote && c == delim:
			out = append(out, strings.TrimSpace(field.String()))
			field.Reset()
		case !inQuote && (c == '\n' || c == '\r'):
			out = append(out, strings.TrimSpace(field.String()))
			return out
		default:
			field.WriteByte(c)
		}
	}
	return append(out, strings.TrimSpace(field.String()))
}
