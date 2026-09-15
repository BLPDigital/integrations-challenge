package grader

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// TwinRecord is the subset of GET /admin/v1/records/{dataset}/{key} the
// record_field assertion reads.
type TwinRecord struct {
	Dataset     string          `json:"dataset"`
	NaturalKey  string          `json:"natural_key"`
	TwinID      string          `json:"twin_id"`
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	Deleted     bool            `json:"deleted"`
	Revisions   json.RawMessage `json:"revisions"`
	// Payload is the current revision's payload, lifted out of the revision
	// list so an assertion addresses fields rather than array indices.
	Payload map[string]any `json:"-"`
}

// Record returns one twin record, fetching and caching it, and writes it to the
// evidence tree so a finding about a field has a file behind it.
func (o *Observed) Record(dataset, key string) (*TwinRecord, error) {
	if o.records == nil {
		o.records = map[string]*TwinRecord{}
	}
	cacheKey := dataset + "\x00" + key
	if rec, ok := o.records[cacheKey]; ok {
		if rec == nil {
			return nil, fmt.Errorf("no such record")
		}
		return rec, nil
	}
	if o.fetchRecord == nil {
		return nil, fmt.Errorf("no record fetcher available")
	}
	rec, err := o.fetchRecord(dataset, key)
	if err != nil {
		o.records[cacheKey] = nil
		return nil, err
	}
	o.records[cacheKey] = rec
	return rec, nil
}

// lookupPath resolves a dotted path into a decoded JSON tree and renders the
// value the way a diff should show it.
//
// Numbers are rendered from their original text, never through a float64, so
// "0000417" stays a string and 1250.00 does not become 1250. That matters here
// for the same reason it matters everywhere else in this landscape: a leading zero
// and a trailing zero are both information.
func lookupPath(payload map[string]any, path string) (string, bool) {
	var cur any = payload
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return "", false
			}
			cur = node[i]
		default:
			return "", false
		}
	}
	return renderJSONValue(cur), true
}

// renderJSONValue renders a decoded JSON value for a human-readable diff.
func renderJSONValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case float64:
		// Reached only when a caller decoded without UseNumber. Rendered
		// without an exponent so a comparison against a written expectation is
		// not decided by formatting.
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(body)
}

// short trims a hash for a one-line summary. The full value is always in the
// evidence file and in the finding.
func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "..."
}

// sortedKeys returns a map's keys in ascending order. Every rendering path goes
// through it, so no output anywhere depends on map iteration order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stringSet turns a slice into a set.
func stringSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}

// sortPairs orders exception pairs by key then code.
func sortPairs(p []exceptionPair) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Key != p[j].Key {
			return p[i].Key < p[j].Key
		}
		return p[i].Code < p[j].Code
	})
}

// printableKey renders a composite natural key readably: the ASCII unit
// separator that joins its components becomes a slash, so 0000417/0004711 is what
// a reader sees instead of an invisible control character.
func printableKey(key string) string {
	return strings.ReplaceAll(key, model.KeySeparator, "/")
}

// safeName turns a key into something usable as a file name.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// either picks one of two strings on a condition, for a one-line message.
func either(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

// emptyDash renders an empty string as a dash, so a message never has a hole in
// it where a value should be.
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// renderMoney renders minor units as a decimal string at the currency's scale.
// It is integer arithmetic: no float64 touches an amount anywhere in this
// package, including on a path that only prints one.
func renderMoney(m TwinMoney) string {
	neg := m.AmountMinor < 0
	v := m.AmountMinor
	if neg {
		v = -v
	}
	digits := strconv.FormatInt(v, 10)
	if m.Scale == 0 {
		if neg {
			return "-" + digits
		}
		return digits
	}
	for len(digits) <= int(m.Scale) {
		digits = "0" + digits
	}
	cut := len(digits) - int(m.Scale)
	out := digits[:cut] + "." + digits[cut:]
	if neg {
		return "-" + out
	}
	return out
}

// scaleAmount parses a decimal string into minor units at scale. It rejects
// rather than rounds: an expectation written with more fraction digits than the
// currency has is a mistake in a scenario file, and rounding it would hide the
// mistake behind a passing assertion.
func scaleAmount(s string, scale uint8) (int64, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" && fracPart == "" {
		return 0, fmt.Errorf("%q is not a decimal", s)
	}
	if len(fracPart) > int(scale) {
		trimmed := strings.TrimRight(fracPart, "0")
		if len(trimmed) > int(scale) {
			return 0, fmt.Errorf("%q carries more than %d fraction digits", s, scale)
		}
		fracPart = fracPart[:scale]
	}
	for len(fracPart) < int(scale) {
		fracPart += "0"
	}
	n, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a decimal: %w", s, err)
	}
	if neg {
		n = -n
	}
	return n, nil
}

// renderCounts renders a histogram in key order.
func renderCounts[V int | int64](m map[string]V) string {
	if len(m) == 0 {
		return "(none)"
	}
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// twinRecordPath is the admin path of one record, with the composite key's unit
// separators percent-encoded so the router sees one path segment.
func twinRecordPath(dataset, key string) string {
	return "/admin/v1/records/" + urlPathEscape(dataset) + "/" + urlPathEscape(key)
}

// decodeRecord decodes a record body and lifts the current revision's payload out
// of the revision list, so an assertion addresses fields rather than indices.
//
// It decodes with UseNumber, which is the whole reason this is a function and not
// a struct tag: a supplier number of "0000417" that arrived as a JSON string must
// stay a string, and a gross amount of "1250.00" must keep its trailing zero. A
// float64 round trip would quietly destroy both, and destroying them is precisely
// the mistake the identifiers section of the specification is about.
func decodeRecord(body []byte) (*TwinRecord, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var envelope map[string]any
	if err := dec.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("grader: decoding a twin record: %w", err)
	}
	rec := &TwinRecord{}
	if s, ok := envelope["dataset"].(string); ok {
		rec.Dataset = s
	}
	if s, ok := envelope["natural_key"].(string); ok {
		rec.NaturalKey = s
	}
	if s, ok := envelope["twin_id"].(string); ok {
		rec.TwinID = s
	}
	if s, ok := envelope["content_hash"].(string); ok {
		rec.ContentHash = s
	}
	if b, ok := envelope["deleted"].(bool); ok {
		rec.Deleted = b
	}
	if n, ok := envelope["version"].(json.Number); ok {
		if v, err := n.Int64(); err == nil {
			rec.Version = int(v)
		}
	}
	revisions, _ := envelope["revisions"].([]any)
	// The last revision is the current one: History returns them oldest first,
	// which is the order a reviewer reads a record's life in.
	for i := len(revisions) - 1; i >= 0; i-- {
		rev, ok := revisions[i].(map[string]any)
		if !ok {
			continue
		}
		if payload, ok := rev["payload"].(map[string]any); ok {
			rec.Payload = payload
			break
		}
	}
	if rec.Payload == nil {
		rec.Payload = map[string]any{}
	}
	return rec, nil
}

// urlPathEscape percent-encodes one path segment. net/url's PathEscape leaves
// some characters a mux would split on, so the encoding here is explicit: every
// byte outside the unreserved set becomes a triplet.
func urlPathEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

// artifactScanLimit bounds how much of one file a data-minimization scan reads.
// A connector's stdout can be arbitrarily long; a credential that leaks leaks in
// the first megabytes, and reading a gigabyte to be sure would make the harness
// the slow part of the run.
const artifactScanLimit = 8 << 20

// readFileLimited reads at most [artifactScanLimit] bytes of a file.
func readFileLimited(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, artifactScanLimit)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	return string(buf[:n]), nil
}
