package httpx

import (
	"net/http"
	"strings"
)

// The four media types accepted and emitted on the versioned surface of both
// servers. Anything else is 415 UNSUPPORTED_CONTENT_TYPE.
const (
	// MediaJSON is a JSON array, a {"records":[...]} envelope, or a single
	// JSON object.
	MediaJSON = "application/json"
	// MediaNDJSON is one JSON value per line.
	MediaNDJSON = "application/x-ndjson"
	// MediaXML is the generic <records><record><field>value</field> mapping.
	MediaXML = "application/xml"
	// MediaCSV is a header row plus one row per record, in the caller's
	// dialect.
	MediaCSV = "text/csv"
)

// A Format is one of the four wire formats. It is the negotiated result on both
// the request and the response side; channel and format are independent axes,
// so every format works in both directions.
type Format string

// The wire formats.
const (
	FormatJSON   Format = "json"
	FormatNDJSON Format = "ndjson"
	FormatXML    Format = "xml"
	FormatCSV    Format = "csv"
)

// MediaType returns the canonical media type of f, or "" for a zero Format.
func (f Format) MediaType() string {
	switch f {
	case FormatJSON:
		return MediaJSON
	case FormatNDJSON:
		return MediaNDJSON
	case FormatXML:
		return MediaXML
	case FormatCSV:
		return MediaCSV
	}
	return ""
}

// String returns the short name of f, for logs and error details.
func (f Format) String() string { return string(f) }

// Formats returns the supported formats in a fixed order. The order is the
// package's preference order when a client expresses none.
func Formats() []Format {
	return []Format{FormatJSON, FormatNDJSON, FormatXML, FormatCSV}
}

// FormatForMediaType maps a media type to a Format, case-insensitively and
// ignoring parameters. text/xml is accepted as an alias of application/xml
// because half the tooling in an ERP landscape emits it; application/csv is
// accepted as an alias of text/csv for the same reason. Both aliases are
// documented here and nowhere else in the contract: responses always use the
// canonical type.
func FormatForMediaType(mediaType string) (Format, bool) {
	mt, _ := splitMediaType(mediaType)
	switch mt {
	case MediaJSON, "application/x-json":
		return FormatJSON, true
	case MediaNDJSON, "application/ndjson", "application/jsonl":
		return FormatNDJSON, true
	case MediaXML, "text/xml":
		return FormatXML, true
	case MediaCSV, "application/csv":
		return FormatCSV, true
	}
	return "", false
}

// splitMediaType lowercases and splits a media type header value into the type
// and its parameter string. It does not allocate a map, so no map iteration can
// reach an output.
func splitMediaType(v string) (mediaType, params string) {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ';'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(v[:i])), v[i+1:]
	}
	return strings.ToLower(v), ""
}

// charsetOK reports whether a media type's parameters declare a charset this
// package can read. Absent, utf-8 and utf8 are fine; anything else is 415,
// because transcoding a request body is the file channel's job and its encoding
// contract lives in the manifest, not in a header.
func charsetOK(params string) bool {
	for _, part := range strings.Split(params, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(part[:eq]), "charset") {
			continue
		}
		cs := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
		if !strings.EqualFold(cs, "utf-8") && !strings.EqualFold(cs, "utf8") {
			return false
		}
	}
	return true
}

// RequestFormat returns the format of the request body from Content-Type. A
// request with no body (no Content-Type and no content) is FormatJSON, so a
// bodyless POST is not a media-type error. An unsupported type or a charset
// other than UTF-8 is 415 UNSUPPORTED_CONTENT_TYPE.
func RequestFormat(r *http.Request) (Format, error) {
	ct := r.Header.Get("Content-Type")
	if strings.TrimSpace(ct) == "" {
		return FormatJSON, nil
	}
	mt, params := splitMediaType(ct)
	f, ok := FormatForMediaType(mt)
	if !ok || !charsetOK(params) {
		return "", UnsupportedContentType(ct)
	}
	return f, nil
}

// ResponseFormat returns the format to answer r in. Accept wins; ties are
// broken by the order the client wrote them, which keeps the choice independent
// of map iteration. A wildcard (*/* or application/*) selects the request's own
// Content-Type when that is supported and JSON otherwise, so a symmetric client
// gets its own format back. An absent or empty Accept behaves like */*.
//
// An Accept header that names only unsupported types is
// 415 UNSUPPORTED_CONTENT_TYPE rather than the RFC's 406: the build spec fixes
// one status for both directions of this failure and a single code is easier for
// a connector to handle than two.
func ResponseFormat(r *http.Request) (Format, error) {
	accept := r.Header.Get("Accept")
	if strings.TrimSpace(accept) == "" {
		return mirrorFormat(r), nil
	}

	best := Format("")
	bestQ := int64(-1)
	wildcardQ := int64(-1)
	for _, spec := range strings.Split(accept, ",") {
		mt, params := splitMediaType(spec)
		if mt == "" {
			continue
		}
		q := acceptQuality(params)
		if q == 0 {
			continue
		}
		// Later entries never beat an equal-quality earlier one, so the
		// client's own order is the tie-break.
		if mt == "*/*" || mt == "application/*" || mt == "text/*" {
			if q > wildcardQ {
				wildcardQ = q
			}
			continue
		}
		f, ok := FormatForMediaType(mt)
		if !ok || !charsetOK(params) {
			continue
		}
		if q > bestQ {
			best, bestQ = f, q
		}
	}
	if best != "" && bestQ >= wildcardQ {
		return best, nil
	}
	if wildcardQ >= 0 {
		return mirrorFormat(r), nil
	}
	if best != "" {
		return best, nil
	}
	return "", UnsupportedContentType(accept)
}

// mirrorFormat returns the request's own body format when it is supported, and
// JSON otherwise. It never fails: a client that sent an unsupported body has
// already been answered by RequestFormat.
func mirrorFormat(r *http.Request) Format {
	if f, err := RequestFormat(r); err == nil && f != "" {
		return f
	}
	return FormatJSON
}

// acceptQuality parses the q parameter as thousandths, in integer arithmetic:
// no float64 appears anywhere in the negotiation path. An absent q is 1000, a
// malformed q is 1000 (lenient, because rejecting a request over a header
// typo helps nobody), and more than three fraction digits are truncated.
func acceptQuality(params string) int64 {
	for _, part := range strings.Split(params, ";") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 || !strings.EqualFold(strings.TrimSpace(part[:eq]), "q") {
			continue
		}
		v := strings.TrimSpace(part[eq+1:])
		whole, frac := v, ""
		if dot := strings.IndexByte(v, '.'); dot >= 0 {
			whole, frac = v[:dot], v[dot+1:]
		}
		q := int64(0)
		switch whole {
		case "", "0":
			q = 0
		case "1":
			q = 1000
		default:
			return 1000
		}
		if q == 1000 {
			return 1000
		}
		for i := 0; i < 3; i++ {
			q *= 10
			if i < len(frac) {
				c := frac[i]
				if c < '0' || c > '9' {
					return 1000
				}
				q += int64(c - '0')
			}
		}
		return q
	}
	return 1000
}
