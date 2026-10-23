package canonical

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// FormatXML is the id of the canonical XML profile.
const FormatXML = "blp-xml-v1"

// The element names of the canonical XML shape.
const (
	// xmlRecordElement is the element name of a record. The dataset's own name
	// is accepted as well, so <supplier> reads as a supplier record and a file
	// stays legible to a human.
	xmlRecordElement = "record"
)

// xmlFormat is the canonical XML importer: a root element holding record
// elements, each holding one element per field. A monetary field may be a nested
// money object, and an invoice may repeat <lines> for its items.
//
// Namespaces are handled by matching local names only. A sender who wraps the
// same document in a namespace, or changes the prefix, changes nothing about the
// data, and a parser that couples a prefix to a name breaks the first time a
// middleware rewrites it.
type xmlFormat struct{}

// NewXML returns the canonical XML format.
func NewXML() importer.Format { return xmlFormat{} }

// ID returns [FormatXML].
func (xmlFormat) ID() string { return FormatXML }

// Detect reports whether head begins, after whitespace and an optional byte order
// mark, with "<".
func (xmlFormat) Detect(head []byte, opt importer.Options) bool {
	b, _ := importer.StripBOM(head)
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '<':
			return true
		default:
			return false
		}
	}
	return false
}

// Parse reads raw. It is [xmlFormat.ParseStream] over a reader on raw.
func (f xmlFormat) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	return f.ParseStream(ctx, bytes.NewReader(raw), opt)
}

// ParseStream reads r one element at a time.
//
// A well-formedness failure is FATAL at the breaking byte, and the diagnostic
// carries both the byte offset and the line. XML has no record separator a
// parser may resume at: an unclosed element makes every following element a
// child of the wrong parent, and a document with a stray "&" stops being XML
// rather than stops being valid. That is the same structural argument as for a
// JSON array, and the opposite of the per-line recovery a CSV file allows.
//
// The XML prolog's encoding declaration wins over the manifest's, and a
// disagreement between the two is [importer.CodeEncodingConflict]: two
// statements about how to read the same bytes, and no way to tell which one the
// sender meant.
func (f xmlFormat) ParseStream(ctx context.Context, r io.Reader, opt importer.Options) (*importer.Result, error) {
	if ctx == nil {
		return nil, importer.ErrNilContext
	}
	b := importer.NewBuilder(FormatXML, opt)
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

	xd := xml.NewDecoder(dec)
	// The stream is already UTF-8 by the time the XML decoder sees it, so a
	// prolog naming any of the four declarable encodings is honored by reading
	// the bytes unchanged. The disagreement check happens on the prolog itself,
	// below, where both statements are still visible.
	xd.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if conflictOf(charset, dial.encoding) {
			return nil, errEncodingConflict
		}
		return input, nil
	}

	var records, embedded int
	depth := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tok, err := xd.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, errEncodingConflict) {
				b.Add(importer.Diagnostic{
					Code: importer.CodeEncodingConflict, Severity: importer.SeverityFatal,
					Line: tracker.lineAt(xd.InputOffset()), Field: "encoding", Value: dial.encoding,
					Message: "the XML prolog and the manifest declare different encodings; the prolog wins, and a disagreement is reported rather than resolved, because the two statements cannot both be true of the same bytes",
				})
				break
			}
			if !reportReadError(b, err, tracker.lineAt(xd.InputOffset()), records+1) {
				b.Add(importer.Diagnostic{
					Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal,
					Line:  tracker.lineAt(xd.InputOffset()),
					Value: "offset " + itoa64(xd.InputOffset()),
					Message: "the document is not well-formed here, so the whole file is rejected: an unclosed element makes every element after it a child of the wrong parent, and there is no record separator to resume at (" +
						err.Error() + ")",
				})
			}
			break
		}
		start, isStart := tok.(xml.StartElement)
		if !isStart {
			continue
		}
		depth++
		if depth == 1 {
			// The root element. Its name is not constrained: <records>,
			// <suppliers> and <ns:Envelope> are all the same document.
			continue
		}
		if depth > 2 {
			// Cannot happen: a record element is consumed whole below, so the
			// only start elements this loop sees are the root's children.
			continue
		}
		if !isRecordElement(start.Name.Local, ds) {
			b.Add(importer.Diagnostic{
				Code: importer.CodeFieldUnknown, Severity: importer.SeverityWarn,
				Line: tracker.lineAt(xd.InputOffset()), Field: start.Name.Local,
				Message: "this element is not a record element (<" + xmlRecordElement + "> or <" + ds.String() + ">) and was skipped",
			})
			if err := xd.Skip(); err != nil {
				b.Add(importer.Diagnostic{
					Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal,
					Line: tracker.lineAt(xd.InputOffset()), Value: "offset " + itoa64(xd.InputOffset()),
					Message: "the document is not well-formed here (" + err.Error() + ")",
				})
				break
			}
			depth--
			continue
		}
		records++
		line := tracker.lineAt(xd.InputOffset())
		rec, rerr := decodeXMLRecord(xd, dial, line)
		if rerr != nil {
			b.Add(importer.Diagnostic{
				Code: importer.CodeMalformedDocument, Severity: importer.SeverityFatal,
				Line: tracker.lineAt(xd.InputOffset()), Record: records,
				Value:   "offset " + itoa64(xd.InputOffset()),
				Message: "the document is not well-formed here, so the whole file is rejected (" + rerr.Error() + ")",
			})
			break
		}
		depth--
		out := emitRecord(b, ds, rec, dial, records)
		embedded += out.lines
	}

	b.SetSource(counted.n, hex.EncodeToString(counted.hash.Sum(nil)))
	b.SetLines(tracker.lines())
	finishTotals(b, opt, records, embedded)
	return b.Result(), nil
}

// errEncodingConflict reports a prolog that contradicts the manifest.
var errEncodingConflict = errors.New("canonical: the XML prolog and the manifest declare different encodings")

// conflictOf reports whether a prolog charset contradicts the declared encoding.
// The names are compared after normalization, so "utf-8" and "UTF-8" agree, and
// so do "windows-1252" and "cp1252": a sender writing either of those two means
// the same code page.
func conflictOf(prolog, declared string) bool {
	p, d := normalizeCharset(prolog), normalizeCharset(declared)
	return p != "" && d != "" && p != d
}

// normalizeCharset maps the spellings of the four declarable encodings onto one
// name each, and returns "" for anything else, which is treated as no statement
// rather than as a conflict: an encoding this landscape does not accept is
// already reported by the dialect check.
func normalizeCharset(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "utf-8", "utf8", "utf-8-bom":
		return importer.EncodingUTF8
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		return importer.EncodingLatin1
	case "windows-1252", "cp1252", "cp-1252", "windows1252":
		return importer.EncodingCP1252
	default:
		return ""
	}
}

// isRecordElement reports whether an element name names a record.
func isRecordElement(name string, ds model.Dataset) bool {
	return name == xmlRecordElement || name == ds.String()
}

// decodeXMLRecord reads one record element to its end and returns its fields.
//
// A field element with no child elements is a scalar; one with child elements is
// a nested object, which in a canonical record is a money object. An element
// repeated within a record accumulates: <lines> twice is a list of two, and a
// scalar repeated twice is the last one, with the duplicate reported by the
// field reader as an unknown-field-shaped defect rather than silently merged.
func decodeXMLRecord(xd *xml.Decoder, dial dialect, line int) (record, error) {
	rec := newRecord(line)
	lists := make(map[string][]record)
	for {
		tok, err := xd.Token()
		if err != nil {
			return rec, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			for name, rows := range lists {
				rec.set(name, arrayCell(rows))
			}
			return rec, nil
		case xml.StartElement:
			name := t.Name.Local
			sub, text, hasChildren, err := decodeXMLField(xd, dial, line)
			if err != nil {
				return rec, err
			}
			switch {
			case hasChildren && isListField(name):
				lists[name] = append(lists[name], sub)
			case hasChildren:
				rec.set(name, objectCell(sub.fields))
			default:
				rec.set(name, xmlScalar(text, dial))
			}
		}
	}
}

// isListField reports whether an element name is a repeated list rather than a
// nested object. Only the invoice's lines are: the field is a list even when the
// invoice has exactly one line, which a shape-sniffing parser would get wrong for
// every single-line invoice in a file.
func isListField(name string) bool { return name == "lines" }

// decodeXMLField reads one field element to its end. It returns the element's
// children as a record, its character data, and whether it had child elements.
func decodeXMLField(xd *xml.Decoder, dial dialect, line int) (record, string, bool, error) {
	sub := newRecord(line)
	var text strings.Builder
	hasChildren := false
	for {
		tok, err := xd.Token()
		if err != nil {
			return sub, text.String(), hasChildren, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return sub, text.String(), hasChildren, nil
		case xml.CharData:
			text.Write(t)
		case xml.StartElement:
			hasChildren = true
			child, childText, childHasChildren, err := decodeXMLField(xd, dial, line)
			if err != nil {
				return sub, text.String(), hasChildren, err
			}
			if childHasChildren {
				sub.set(t.Name.Local, objectCell(child.fields))
			} else {
				sub.set(t.Name.Local, xmlScalar(childText, dial))
			}
		}
	}
}

// xmlScalar turns character data into a cell, applying the declared trim and null
// token. An element that is present and empty is an empty value, not a null,
// unless the declared null token is the empty string, which the dialect forbids.
func xmlScalar(text string, dial dialect) cell {
	v := dial.trim(text)
	if dial.hasNull && v == dial.nullTok {
		return nullCell()
	}
	return textCell(v)
}

// A lineTracker counts the newlines of a stream and answers, for a byte offset
// the XML decoder reports, which line that offset is on.
//
// It exists because [xml.Decoder] reports a byte offset and not a line, and a
// diagnostic that says "offset 41213" costs an operator the work of finding the
// line themselves. Memory is bounded: newline offsets are consumed as the
// decoder's offset passes them, so the tracker holds only the newlines inside the
// decoder's own read-ahead.
type lineTracker struct {
	src io.Reader

	pending  []int64 // newline offsets not yet passed by the decoder, ascending
	offset   int64   // source bytes read so far
	line     int64   // newlines already passed
	untimely bool    // offsets are no longer recorded, only counted
}

// Read implements io.Reader, recording the offset of every newline it sees.
func (t *lineTracker) Read(p []byte) (int, error) {
	n, err := t.src.Read(p)
	for i := 0; i < n; i++ {
		if p[i] != '\n' {
			continue
		}
		if t.untimely {
			t.line++
			continue
		}
		t.pending = append(t.pending, t.offset+int64(i))
	}
	t.offset += int64(n)
	return n, err
}

// stopTracking switches the tracker to counting lines without recording their
// offsets. A reader that never asks for a line by offset - the NDJSON path counts
// its own lines, because it reads them - would otherwise accumulate one offset
// per line of the file, which for a 50'000-record extract is memory spent on an
// answer nobody wants.
func (t *lineTracker) stopTracking() {
	t.line += int64(len(t.pending))
	t.pending = nil
	t.untimely = true
}

// lineAt returns the 1-based line the given source offset falls on.
func (t *lineTracker) lineAt(offset int64) int {
	i := 0
	for i < len(t.pending) && t.pending[i] < offset {
		t.line++
		i++
	}
	t.pending = t.pending[i:]
	return int(t.line) + 1
}

// lines returns how many lines have been seen so far.
func (t *lineTracker) lines() int {
	n := int(t.line) + len(t.pending)
	if n == 0 && t.offset > 0 {
		return 1
	}
	return n
}
