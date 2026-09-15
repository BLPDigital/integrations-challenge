package model

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Canonical JSON errors.
var (
	// ErrFloatUnsupported reports a float32 or float64 encountered while
	// canonicalizing. Money and quantities are Decimal; a float in a canonical
	// payload is a bug that would make a content hash depend on binary rounding.
	ErrFloatUnsupported = errors.New("model: float values are not canonicalizable")
	// ErrUnsupportedType reports a value kind JSON cannot represent.
	ErrUnsupportedType = errors.New("model: unsupported type for canonical JSON")
	// ErrDuplicateKey reports two object keys that collide after trimming and
	// NFC normalization.
	ErrDuplicateKey = errors.New("model: duplicate object key in canonical JSON")
	// ErrBadMarshaler reports a json.Marshaler that produced invalid JSON.
	ErrBadMarshaler = errors.New("model: MarshalJSON produced invalid JSON")
)

var (
	jsonNumberType    = reflect.TypeOf(json.Number(""))
	marshalerType     = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// CanonicalJSON encodes v in the one byte form this landscape hashes and
// compares. The rules are:
//
//   - object keys sorted ascending by byte order, duplicates rejected;
//   - no insignificant whitespace;
//   - no HTML escaping: '<', '>' and '&' are written literally;
//   - Decimal as a string, Money always as {amount_minor,currency,scale};
//   - nil slices and maps as [] and {}, never null, so an absent list and an
//     empty list hash identically;
//   - strings NFC-normalized (see NormalizeNFC for the subset) and trimmed of
//     leading and trailing whitespace, object keys included;
//   - invalid UTF-8 replaced by U+FFFD, U+2028 and U+2029 escaped;
//   - float32 and float64 rejected, since a binary float has no canonical
//     decimal form.
//
// Struct encoding follows the encoding/json rules for `json` tags, including
// renaming, "-", omitempty and embedded-struct flattening, with two documented
// simplifications: the ",string" tag option is ignored, and when two fields
// resolve to the same name the first one found wins instead of both being
// dropped. Types implementing json.Marshaler or encoding.TextMarshaler are
// honored, and the JSON they return is canonicalized in turn.
func CanonicalJSON(v any) ([]byte, error) {
	n, err := canonNode(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	n.writeCanonical(&buf)
	return buf.Bytes(), nil
}

// ContentHash returns the lowercase hex sha256 of CanonicalJSON(v). It is the
// identity of a record's content: unchanged detection, proposal hashes and the
// state digest are all built on it.
//
// The signature has no error because every call site treats a hash as a value.
// A value that cannot be canonicalized returns the empty string, which no valid
// hash equals, so a caller comparing hashes can never mistake a failure for a
// match; callers that need the reason call CanonicalJSON directly.
func ContentHash(v any) string {
	b, err := CanonicalJSON(v)
	if err != nil {
		return ""
	}
	return Sha256Hex(b)
}

// Sha256Hex returns the lowercase hex sha256 of b. It is the hash used for
// content-addressed raw source bytes and manifest checksums.
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DecodeJSONTree decodes JSON into a generic value tree whose numbers are
// json.Number rather than float64, so the result can be handed to CanonicalJSON
// without a float ever appearing. Objects become map[string]any, arrays []any,
// and JSON null becomes nil.
//
// Callers that build canonical payloads out of decoded JSON must use this rather
// than json.Unmarshal into an any: the standard library turns every number into
// a float64, which CanonicalJSON rejects.
func DecodeJSONTree(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("model: trailing data after JSON value")
	}
	return v, nil
}

// cnode is a canonicalized JSON value that knows how to write itself.
type cnode interface {
	writeCanonical(*bytes.Buffer)
}

type cnull struct{}

func (cnull) writeCanonical(b *bytes.Buffer) { b.WriteString("null") }

type cbool bool

func (v cbool) writeCanonical(b *bytes.Buffer) {
	if v {
		b.WriteString("true")
		return
	}
	b.WriteString("false")
}

// cnumber is a JSON number literal, already in its canonical textual form.
type cnumber string

func (v cnumber) writeCanonical(b *bytes.Buffer) { b.WriteString(string(v)) }

type cstring string

func (v cstring) writeCanonical(b *bytes.Buffer) { writeJSONString(b, string(v)) }

type carray []cnode

func (a carray) writeCanonical(b *bytes.Buffer) {
	b.WriteByte('[')
	for i, n := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		n.writeCanonical(b)
	}
	b.WriteByte(']')
}

// cobject holds members in ascending key order.
type cobject struct {
	keys []string
	vals []cnode
}

func (o *cobject) writeCanonical(b *bytes.Buffer) {
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(b, k)
		b.WriteByte(':')
		o.vals[i].writeCanonical(b)
	}
	b.WriteByte('}')
}

// newObject sorts the members by key and rejects duplicates.
func newObject(keys []string, vals []cnode) (*cobject, error) {
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	o := &cobject{keys: make([]string, 0, len(keys)), vals: make([]cnode, 0, len(vals))}
	for _, i := range idx {
		if len(o.keys) > 0 && o.keys[len(o.keys)-1] == keys[i] {
			return nil, ErrDuplicateKey
		}
		o.keys = append(o.keys, keys[i])
		o.vals = append(o.vals, vals[i])
	}
	return o, nil
}

// canonNode converts a reflected value into its canonical node.
func canonNode(v reflect.Value) (cnode, error) {
	if !v.IsValid() {
		return cnull{}, nil
	}
	k := v.Kind()
	if (k == reflect.Pointer || k == reflect.Interface) && v.IsNil() {
		return cnull{}, nil
	}
	t := v.Type()
	if t == jsonNumberType {
		s := v.String()
		if s == "" {
			return nil, ErrBadMarshaler
		}
		return cnumber(s), nil
	}
	switch {
	case t.Implements(marshalerType):
		return fromMarshaler(v.Interface().(json.Marshaler))
	case v.CanAddr() && reflect.PointerTo(t).Implements(marshalerType):
		return fromMarshaler(v.Addr().Interface().(json.Marshaler))
	case t.Implements(textMarshalerType):
		return fromTextMarshaler(v.Interface().(encoding.TextMarshaler))
	case v.CanAddr() && reflect.PointerTo(t).Implements(textMarshalerType):
		return fromTextMarshaler(v.Addr().Interface().(encoding.TextMarshaler))
	}
	switch k {
	case reflect.Pointer, reflect.Interface:
		return canonNode(v.Elem())
	case reflect.Bool:
		return cbool(v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return cnumber(strconv.FormatInt(v.Int(), 10)), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return cnumber(strconv.FormatUint(v.Uint(), 10)), nil
	case reflect.Float32, reflect.Float64:
		return nil, ErrFloatUnsupported
	case reflect.String:
		return cstring(canonString(v.String())), nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
			// []byte, encoded base64 like encoding/json.
			return cstring(base64.StdEncoding.EncodeToString(v.Bytes())), nil
		}
		return canonList(v)
	case reflect.Array:
		return canonList(v)
	case reflect.Map:
		return canonMap(v)
	case reflect.Struct:
		return canonStruct(v)
	}
	return nil, ErrUnsupportedType
}

// canonList converts a slice or array. A nil slice becomes [], never null.
func canonList(v reflect.Value) (cnode, error) {
	out := make(carray, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		n, err := canonNode(v.Index(i))
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// canonMap converts a map. A nil map becomes {}, never null. Keys must be
// strings, integers or encoding.TextMarshalers, as in encoding/json.
func canonMap(v reflect.Value) (cnode, error) {
	keys := make([]string, 0, v.Len())
	vals := make([]cnode, 0, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		k, err := canonMapKey(iter.Key())
		if err != nil {
			return nil, err
		}
		n, err := canonNode(iter.Value())
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
		vals = append(vals, n)
	}
	return newObject(keys, vals)
}

// canonMapKey renders a map key as a canonical object key.
func canonMapKey(k reflect.Value) (string, error) {
	t := k.Type()
	if t.Implements(textMarshalerType) {
		b, err := k.Interface().(encoding.TextMarshaler).MarshalText()
		if err != nil {
			return "", err
		}
		return canonString(string(b)), nil
	}
	switch k.Kind() {
	case reflect.String:
		return canonString(k.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(k.Uint(), 10), nil
	}
	return "", ErrUnsupportedType
}

// canonStruct converts a struct, honoring `json` tags and embedded fields.
func canonStruct(v reflect.Value) (cnode, error) {
	fields := structFields(v.Type())
	keys := make([]string, 0, len(fields))
	vals := make([]cnode, 0, len(fields))
	for _, f := range fields {
		fv, ok := fieldByIndex(v, f.index)
		if !ok {
			continue // nil embedded pointer: its fields are absent, not null
		}
		if f.omitEmpty && isEmptyValue(fv) {
			continue
		}
		n, err := canonNode(fv)
		if err != nil {
			return nil, err
		}
		keys = append(keys, f.name)
		vals = append(vals, n)
	}
	return newObject(keys, vals)
}

// fieldInfo is a resolved struct field: its JSON name and the index path to
// reach it through embedded structs.
type fieldInfo struct {
	name      string
	index     []int
	omitEmpty bool
}

// structFields resolves the JSON fields of t, shallowest first, flattening
// embedded structs without a JSON name.
func structFields(t reflect.Type) []fieldInfo {
	var out []fieldInfo
	seen := map[string]bool{}
	collectFields(t, nil, &out, seen)
	return out
}

// collectFields appends the fields of t reachable through prefix.
func collectFields(t reflect.Type, prefix []int, out *[]fieldInfo, seen map[string]bool) {
	type embedded struct {
		typ   reflect.Type
		index []int
	}
	var deferred []embedded
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" && tag == "-" {
			continue
		}
		index := append(append([]int{}, prefix...), i)
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			// An embedded struct is flattened even when the field itself is
			// unexported: its exported fields are promoted, as in encoding/json.
			if ft.Kind() == reflect.Struct {
				deferred = append(deferred, embedded{typ: ft, index: index})
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		*out = append(*out, fieldInfo{
			name:      name,
			index:     index,
			omitEmpty: hasOption(opts, "omitempty"),
		})
	}
	for _, e := range deferred {
		collectFields(e.typ, e.index, out, seen)
	}
}

// hasOption reports whether the comma-separated tag options contain want.
func hasOption(opts, want string) bool {
	for opts != "" {
		var o string
		o, opts, _ = strings.Cut(opts, ",")
		if o == want {
			return true
		}
	}
	return false
}

// fieldByIndex walks an index path, reporting false when it crosses a nil
// embedded pointer.
func fieldByIndex(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 {
			if v.Kind() == reflect.Pointer {
				if v.IsNil() {
					return reflect.Value{}, false
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v, true
}

// isEmptyValue mirrors the encoding/json notion of emptiness for omitempty.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// fromMarshaler canonicalizes the JSON a json.Marshaler produced.
func fromMarshaler(m json.Marshaler) (cnode, error) {
	b, err := m.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var raw any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, ErrBadMarshaler
	}
	if dec.More() {
		return nil, ErrBadMarshaler
	}
	return canonFromAny(raw)
}

// fromTextMarshaler canonicalizes the text an encoding.TextMarshaler produced.
func fromTextMarshaler(m encoding.TextMarshaler) (cnode, error) {
	b, err := m.MarshalText()
	if err != nil {
		return nil, err
	}
	return cstring(canonString(string(b))), nil
}

// canonFromAny canonicalizes a decoded JSON value tree.
func canonFromAny(x any) (cnode, error) {
	switch t := x.(type) {
	case nil:
		return cnull{}, nil
	case bool:
		return cbool(t), nil
	case string:
		return cstring(canonString(t)), nil
	case json.Number:
		return cnumber(string(t)), nil
	case []any:
		out := make(carray, 0, len(t))
		for _, e := range t {
			n, err := canonFromAny(e)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		vals := make([]cnode, 0, len(t))
		for k, e := range t {
			n, err := canonFromAny(e)
			if err != nil {
				return nil, err
			}
			keys = append(keys, canonString(k))
			vals = append(vals, n)
		}
		return newObject(keys, vals)
	}
	return nil, ErrUnsupportedType
}

// canonString trims and NFC-normalizes a string. Trimming happens after
// normalization so a decomposed non-breaking sequence cannot survive it.
func canonString(s string) string {
	return strings.TrimSpace(NormalizeNFC(s))
}

// hexDigits is used by the string escaper.
const hexDigits = "0123456789abcdef"

// writeJSONString writes s as a JSON string with no HTML escaping. Control
// characters use the short escapes where they exist, U+2028 and U+2029 are
// escaped because they break JavaScript consumers, and invalid UTF-8 becomes
// U+FFFD.
func writeJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"':
				b.WriteString(`\"`)
			case c == '\\':
				b.WriteString(`\\`)
			case c == '\n':
				b.WriteString(`\n`)
			case c == '\r':
				b.WriteString(`\r`)
			case c == '\t':
				b.WriteString(`\t`)
			case c == '\b':
				b.WriteString(`\b`)
			case c == '\f':
				b.WriteString(`\f`)
			case c < 0x20:
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xf])
			default:
				b.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString("\ufffd")
			i++
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			b.WriteString(`\u202`)
			b.WriteByte(hexDigits[r&0xf])
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	b.WriteByte('"')
}
