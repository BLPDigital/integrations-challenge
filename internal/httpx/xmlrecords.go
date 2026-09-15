package httpx

import (
	"encoding/xml"
	"io"
	"strings"
)

// The generic XML element mapping used on both directions of the versioned
// surface. It is deliberately schema-free: one wrapper, one element per record,
// one element per field, text content as the value.
//
//	<records>
//	  <record><supplier_number>0000417</supplier_number><name>Muster AG</name></record>
//	  <record><supplier_number>0000418</supplier_number><name>Beispiel GmbH</name></record>
//	</records>
//
// Documented properties of the mapping:
//
//   - Elements are matched on local name only, never on prefix, and any
//     namespace is accepted. A response is emitted without a namespace.
//   - Field values are strings. XML carries no types, so a JSON number or bool
//     is emitted as its text form and comes back as a string. This is the same
//     lossy step CSV takes and it is why the canonical AP model keeps every key
//     and every amount in a string.
//   - Field text is taken verbatim, including whitespace. The encoder never
//     indents, so a document it produced round-trips exactly; a producer that
//     pretty-prints inside a field element changes the value.
//   - A field element carrying an attribute with local name "nil" and value
//     "true" decodes to JSON null, and JSON null encodes to that attribute.
//     Other attributes are ignored.
//   - A field element with child elements is rejected: nested structures have
//     no place in this mapping. Encode them as their compact JSON text.
//   - Children of <records> other than <record> are ignored, so an envelope may
//     carry metadata elements without breaking a reader.
//   - Duplicate field names within one record are rejected rather than
//     silently collapsed.
const (
	xmlRootElement   = "records"
	xmlRecordElement = "record"
	xmlNilAttr       = "nil"
)

// decodeXMLRecords parses the generic mapping into per-record field lists in
// document order.
func decodeXMLRecords(body []byte) ([][]recordField, *Error) {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	var (
		records [][]recordField
		depth   int
		inRec   bool
		fields  []recordField
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, MalformedBody("xml_not_well_formed", "xml is not well formed: "+err.Error())
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case depth == 1:
				if t.Name.Local != xmlRootElement {
					return nil, MalformedBody("xml_unexpected_root",
						"xml root element must be <"+xmlRootElement+">")
				}
			case depth == 2:
				if t.Name.Local != xmlRecordElement {
					// Envelope metadata: skip the whole subtree.
					if err := dec.Skip(); err != nil {
						return nil, MalformedBody("xml_not_well_formed",
							"xml is not well formed: "+err.Error())
					}
					depth--
					continue
				}
				inRec = true
				fields = nil
			case depth == 3 && inRec:
				f, ferr := decodeXMLField(dec, t)
				if ferr != nil {
					return nil, ferr
				}
				for _, prev := range fields {
					if prev.Name == f.Name {
						return nil, MalformedBody("xml_duplicate_field",
							"xml record carries field "+f.Name+" twice")
					}
				}
				fields = append(fields, f)
				depth--
			default:
				return nil, MalformedBody("xml_unexpected_element",
					"xml element <"+t.Name.Local+"> is not part of the record mapping")
			}
		case xml.EndElement:
			if depth == 2 && inRec {
				records = append(records, fields)
				inRec, fields = false, nil
			}
			depth--
		case xml.CharData:
			if depth <= 2 && strings.TrimSpace(string(t)) != "" {
				return nil, MalformedBody("xml_unexpected_text",
					"xml text content outside a field element")
			}
		}
	}
	return records, nil
}

// decodeXMLField reads one field element whose start tag is start. The decoder
// is positioned after the matching end element on return.
func decodeXMLField(dec *xml.Decoder, start xml.StartElement) (recordField, *Error) {
	f := recordField{Name: start.Name.Local}
	for _, a := range start.Attr {
		if a.Name.Local == xmlNilAttr && a.Value == "true" {
			f.Null = true
		}
	}
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return f, MalformedBody("xml_not_well_formed", "xml is not well formed")
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.StartElement:
			return f, MalformedBody("xml_nested_field",
				"xml field <"+f.Name+"> has child element <"+t.Name.Local+">")
		case xml.EndElement:
			f.Value = sb.String()
			return f, nil
		}
	}
}

// encodeXMLRecords renders records in the generic mapping, without indentation
// and without a namespace, so the bytes are stable and self-inverse.
func encodeXMLRecords(sb *strings.Builder, records [][]recordField) *Error {
	sb.WriteString("<" + xmlRootElement + ">")
	for _, rec := range records {
		sb.WriteString("<" + xmlRecordElement + ">")
		for _, f := range rec {
			if !isXMLName(f.Name) {
				return Internal("field name " + f.Name + " is not a valid XML element name")
			}
			if f.Null {
				sb.WriteString("<" + f.Name + ` ` + xmlNilAttr + `="true"></` + f.Name + ">")
				continue
			}
			sb.WriteString("<" + f.Name + ">")
			writeXMLText(sb, f.Value)
			sb.WriteString("</" + f.Name + ">")
		}
		sb.WriteString("</" + xmlRecordElement + ">")
	}
	sb.WriteString("</" + xmlRootElement + ">")
	return nil
}

// writeXMLText escapes text content. It escapes the five predefined entities
// plus CR, which an XML parser would otherwise normalize away, so a value
// containing CRLF survives a round trip.
func writeXMLText(sb *strings.Builder, s string) {
	for _, r := range s {
		switch r {
		case '&':
			sb.WriteString("&amp;")
		case '<':
			sb.WriteString("&lt;")
		case '>':
			sb.WriteString("&gt;")
		case '"':
			sb.WriteString("&quot;")
		case '\'':
			sb.WriteString("&apos;")
		case '\r':
			sb.WriteString("&#13;")
		default:
			sb.WriteRune(r)
		}
	}
}

// isXMLName reports whether s is a usable unprefixed XML element name. It is
// deliberately narrow: ASCII letters, digits, underscore, hyphen and dot, with
// a letter or underscore first. Every dataset field name in this project is
// snake_case and passes.
func isXMLName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}
