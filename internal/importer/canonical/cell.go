package canonical

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// A cellKind is what a parsed field turned out to be. CSV produces only text and
// null; XML produces text, objects and arrays; JSON produces all of them, which
// is why JSON is the only format where a field can be the wrong JSON type.
type cellKind uint8

const (
	// cellAbsent is a field the record did not carry at all.
	cellAbsent cellKind = iota
	// cellNull is an explicit null: JSON null, or a cell equal to the declared
	// null token.
	cellNull
	// cellText is a string.
	cellText
	// cellNumber is a JSON number, kept as its literal so no float64 ever
	// appears. It is the wrong type for every field of every canonical record
	// except the members of a money object.
	cellNumber
	// cellBool is a JSON boolean.
	cellBool
	// cellObject is a nested object, which in a canonical record is always a
	// money object.
	cellObject
	// cellArray is a repeated element: the embedded lines of an invoice.
	cellArray
)

// A cell is one parsed field, in a form that has not yet been interpreted. The
// numeric literal is kept as text so that no parsing path can produce a float64
// and so that the exact scale as written survives.
type cell struct {
	kind cellKind
	text string
	num  string
	b    bool
	obj  map[string]cell
	arr  []record
}

// textCell returns a text cell.
func textCell(s string) cell { return cell{kind: cellText, text: s} }

// nullCell returns an explicit null cell.
func nullCell() cell { return cell{kind: cellNull} }

// numberCell returns a JSON number cell carrying its literal.
func numberCell(lit string) cell { return cell{kind: cellNumber, num: lit} }

// boolCell returns a boolean cell.
func boolCell(v bool) cell { return cell{kind: cellBool, b: v} }

// objectCell returns a nested-object cell.
func objectCell(m map[string]cell) cell { return cell{kind: cellObject, obj: m} }

// arrayCell returns a repeated-element cell.
func arrayCell(rs []record) cell { return cell{kind: cellArray, arr: rs} }

// empty reports whether the cell carries no value: absent, null, or text that is
// empty. It is the question "did the sender supply this field", which the
// canonical model answers with an empty string rather than with a null.
func (c cell) empty() bool {
	switch c.kind {
	case cellAbsent, cellNull:
		return true
	case cellText:
		return c.text == ""
	default:
		return false
	}
}

// any returns the cell as the generic value [model.KeyString] expects: a string,
// a [json.Number] or nil. It is how a natural key gets its type check without
// this package re-deciding what a key is.
func (c cell) any() any {
	switch c.kind {
	case cellText:
		return c.text
	case cellNumber:
		return json.Number(c.num)
	case cellBool:
		return c.b
	case cellObject:
		return map[string]any{}
	case cellArray:
		return []any{}
	default:
		return nil
	}
}

// display returns the cell as a diagnostic value: what the sender actually sent,
// so an operator can find it in the file.
func (c cell) display() string {
	switch c.kind {
	case cellText:
		return c.text
	case cellNumber:
		return c.num
	case cellBool:
		return strconv.FormatBool(c.b)
	case cellNull:
		return "null"
	case cellObject:
		return "{...}"
	case cellArray:
		return "[...]"
	default:
		return ""
	}
}

// A record is one parsed record: its fields, and the line it started on.
type record struct {
	fields map[string]cell
	names  []string
	line   int
}

// newRecord returns an empty record starting on the given line.
func newRecord(line int) record {
	return record{fields: make(map[string]cell), line: line}
}

// set adds a field, keeping the declaration order for deterministic reporting.
// A repeated name replaces the earlier value: the last one wins, and the caller
// reports the duplicate, because whether a duplicate is a defect depends on the
// format.
func (r *record) set(name string, c cell) {
	if _, dup := r.fields[name]; !dup {
		r.names = append(r.names, name)
	}
	r.fields[name] = c
}

// sortedNames returns the field names in ascending byte order, so an
// unknown-field warning does not depend on where in a header the column was.
func (r record) sortedNames() []string {
	out := append([]string(nil), r.names...)
	sort.Strings(out)
	return out
}

// A fieldReader interprets one record's cells as canonical values and collects
// the findings. It is the single place a wire value becomes a model value, so a
// defect gets the same code whichever of the three formats delivered it.
//
// Findings are buffered rather than handed straight to the [importer.Builder],
// because the document key is not known until the key fields have been read and
// a diagnostic must be able to name the document it belongs to.
type fieldReader struct {
	rec     record
	dial    dialect
	line    int
	ordinal int

	diags   []importer.Diagnostic
	blocked bool
	used    map[string]bool
}

// newFieldReader returns a reader over rec.
func newFieldReader(rec record, dial dialect, ordinal int) *fieldReader {
	return &fieldReader{rec: rec, dial: dial, line: rec.line, ordinal: ordinal, used: make(map[string]bool)}
}

// reject records a finding that drops the record and continues the file.
func (r *fieldReader) reject(code, field, value, msg string) {
	r.blocked = true
	r.diags = append(r.diags, importer.Diagnostic{
		Code: code, Severity: importer.SeverityReject, Line: r.line,
		Record: r.ordinal, Field: field, Value: value, Message: msg,
	})
}

// warn records a finding that travels with the record.
func (r *fieldReader) warn(code, field, value, msg string) {
	r.diags = append(r.diags, importer.Diagnostic{
		Code: code, Severity: importer.SeverityWarn, Line: r.line,
		Record: r.ordinal, Field: field, Value: value, Message: msg,
	})
}

// get returns a field and marks it as read, so whatever is left over at the end
// is an unknown field.
func (r *fieldReader) get(name string) cell {
	r.used[name] = true
	return r.rec.fields[name]
}

// key reads a natural-key component. The type check is [model.KeyString]'s, so
// the file channel and the ingest API reject a numeric key with the same code
// and for the same reason: the leading zeros of "0000417" are gone by the time
// a JSON number has parsed, and reconstructing them would be a guess about
// which supplier is meant.
func (r *fieldReader) key(name string) string {
	c := r.get(name)
	v, errs := model.KeyString(name, c.any())
	for _, e := range errs {
		if e.Code == model.CodeKeyMissing {
			// Emptiness is the validator's finding, reported once from the
			// entity, so a record with three empty keys does not produce the
			// same finding twice per key.
			continue
		}
		r.reject(e.Code, name, c.display(), e.Message)
	}
	return v
}

// text reads a free-text or enumerated field. The declared trim rule has already
// been applied when the cell was parsed; this trims whitespace unconditionally on
// top of it, because a canonical field with leading whitespace is a transport
// artifact and [model.CanonicalJSON] trims it anyway.
//
// Only a string is accepted. A number would have lost its leading zeros, and a
// boolean coerced to "true" is a serializer bug arriving as data.
func (r *fieldReader) text(name string) string {
	c := r.get(name)
	switch c.kind {
	case cellAbsent, cellNull:
		return ""
	case cellText:
		return strings.TrimSpace(c.text)
	case cellNumber:
		r.reject(importer.CodeFieldInvalid, name, c.display(),
			"this field is a string; a JSON number cannot carry its leading zeros or its exact form")
		return ""
	default:
		// A boolean, an object and an array are all the wrong JSON type here.
		// Coercing a boolean to "true" would be the one that looks harmless,
		// and it is how a serializer bug reaches the store as data.
		r.reject(importer.CodeFieldInvalid, name, c.display(),
			"this field is a string, and neither a boolean nor a structure is one")
		return ""
	}
}

// date reads a calendar date and returns it in canonical YYYY-MM-DD form. An
// empty field is an empty date, which the validator accepts or rejects per
// field: whether a date is mandatory is the entity's rule, not the parser's.
func (r *fieldReader) date(name string) string {
	c := r.get(name)
	if c.empty() {
		return ""
	}
	if c.kind != cellText {
		r.reject(importer.CodeDateFormat, name, c.display(), "a date is a string in the declared format")
		return ""
	}
	s, err := importer.ParseDate(strings.TrimSpace(c.text), r.dial.dateFmt)
	if err != nil {
		r.reject(importer.CodeDateFormat, name, c.text,
			"expected a date as YYYY-MM-DD or in the manifest-declared format "+r.dial.dateFmt)
		return ""
	}
	return s
}

// integer reads a count: a payment term in days, a discount period, an attempt
// counter. An empty field is zero, which for every count in this model is the
// documented absence.
func (r *fieldReader) integer(name string) int {
	c := r.get(name)
	if c.empty() {
		return 0
	}
	var lit string
	switch c.kind {
	case cellText:
		lit = strings.TrimSpace(c.text)
	case cellNumber:
		lit = c.num
	default:
		r.reject(importer.CodeNumberFormat, name, c.display(), "expected a whole number")
		return 0
	}
	n, err := importer.ParseInteger(lit)
	if err != nil {
		r.reject(importer.CodeNumberFormat, name, lit, "expected a whole number with no grouping and no fraction")
		return 0
	}
	return n
}

// integer64 reads a wide count: a change sequence, a rate factor.
func (r *fieldReader) integer64(name string) int64 {
	c := r.get(name)
	if c.empty() {
		return 0
	}
	var lit string
	switch c.kind {
	case cellText:
		lit = strings.TrimSpace(c.text)
	case cellNumber:
		lit = c.num
	default:
		r.reject(importer.CodeNumberFormat, name, c.display(), "expected a whole number")
		return 0
	}
	n, err := strconv.ParseInt(lit, 10, 64)
	if err != nil || strings.HasPrefix(lit, "+") {
		r.reject(importer.CodeNumberFormat, name, lit, "expected a whole number with no grouping and no fraction")
		return 0
	}
	return n
}

// boolean reads a flag. Accepted spellings are true, false, 1 and 0, matched
// case-insensitively; an empty field is false. Nothing else is accepted, because
// a "blocked" column that reads "maybe" must not silently become "not blocked":
// posting to a blocked creditor is exactly what the flag exists to prevent.
func (r *fieldReader) boolean(name string) bool {
	c := r.get(name)
	switch c.kind {
	case cellAbsent, cellNull:
		return false
	case cellBool:
		return c.b
	case cellText:
		switch strings.ToLower(strings.TrimSpace(c.text)) {
		case "":
			return false
		case "true", "1":
			return true
		case "false", "0":
			return false
		}
		r.reject(importer.CodeFieldInvalid, name, c.text, "expected true, false, 1 or 0")
		return false
	default:
		r.reject(importer.CodeFieldInvalid, name, c.display(), "expected true, false, 1 or 0")
		return false
	}
}

// decimal reads a non-monetary exact number: a quantity, an exchange rate. A
// JSON number is rejected rather than parsed: it would arrive as a float64 and
// the exact value, including the scale as written, would already be gone.
func (r *fieldReader) decimal(name string, maxFrac int) model.Decimal {
	c := r.get(name)
	if c.empty() {
		return model.Decimal{}
	}
	switch c.kind {
	case cellText:
		d, _, err := importer.ParseAmount(strings.TrimSpace(c.text), r.dial.numeric(maxFrac))
		r.numericError(name, c.text, maxFrac, err)
		return d
	case cellNumber:
		r.reject(importer.CodeDecimalFormat, name, c.num,
			"an exact number travels as a string; a JSON number has already lost the value to binary rounding")
		return model.Decimal{}
	default:
		r.reject(importer.CodeDecimalFormat, name, c.display(), "expected an exact number as a string")
		return model.Decimal{}
	}
}

// money reads a monetary field and returns the amount together with the currency
// the value itself carried, "" when it carried none. The caller cross-checks that
// currency against the record's own, because an amount whose currency contradicts
// its record is not a rounding question but a posting in the wrong currency.
//
// Three wire forms are accepted, and one is not:
//
//	{"amount_minor":134563,"currency":"CHF","scale":2}   the canonical money object
//	"1'345.63"                                          a decimal string
//	"CHF 1'345.63"                                      a decimal string with its currency
//	134563                                              rejected: E_MONEY_NOT_INTEGER_MINOR
//
// A bare JSON number is rejected whether or not it has a fraction part. With a
// fraction it is a float and the value is already wrong; without one it is minor
// units with no currency, so its scale is unknown and 250000 is either JPY
// 250'000 or CHF 2'500.00.
func (r *fieldReader) money(name string, maxFrac int) (model.Decimal, string) {
	c := r.get(name)
	if c.empty() {
		return model.Decimal{}, ""
	}
	switch c.kind {
	case cellText:
		d, cur, err := importer.ParseAmount(strings.TrimSpace(c.text), r.dial.numeric(maxFrac))
		r.numericError(name, c.text, maxFrac, err)
		return d, cur
	case cellNumber:
		r.reject(importer.CodeMoneyNotIntegerMinor, name, c.num,
			"an amount is a money object {amount_minor,currency,scale} or a decimal string, never a bare JSON number")
		return model.Decimal{}, ""
	case cellObject:
		return r.moneyObject(name, c, maxFrac)
	default:
		r.reject(importer.CodeMoneyNotIntegerMinor, name, c.display(),
			"an amount is a money object {amount_minor,currency,scale} or a decimal string")
		return model.Decimal{}, ""
	}
}

// moneyObject converts a canonical money object into an exact decimal.
func (r *fieldReader) moneyObject(name string, c cell, maxFrac int) (model.Decimal, string) {
	minorCell, hasMinor := c.obj["amount_minor"]
	curCell, hasCur := c.obj["currency"]
	scaleCell, hasScale := c.obj["scale"]
	if !hasMinor || !hasCur || !hasScale {
		r.reject(importer.CodeMoneyNotIntegerMinor, name, c.display(),
			"a money object carries amount_minor, currency and scale; all three are mandatory")
		return model.Decimal{}, ""
	}
	minorLit := minorCell.num
	if minorCell.kind == cellText {
		minorLit = strings.TrimSpace(minorCell.text)
	}
	minor, err := strconv.ParseInt(minorLit, 10, 64)
	if err != nil {
		r.reject(importer.CodeMoneyNotIntegerMinor, name+".amount_minor", minorCell.display(),
			"amount_minor is a whole number of minor units")
		return model.Decimal{}, ""
	}
	scaleLit := scaleCell.num
	if scaleCell.kind == cellText {
		scaleLit = strings.TrimSpace(scaleCell.text)
	}
	scale, err := strconv.ParseUint(scaleLit, 10, 8)
	if err != nil || scale > uint64(model.MaxScale) {
		r.reject(importer.CodeMoneyNotIntegerMinor, name+".scale", scaleCell.display(),
			"scale is the minor-unit scale of the currency")
		return model.Decimal{}, ""
	}
	currency := strings.TrimSpace(curCell.text)
	m := model.Money{AmountMinor: minor, Currency: currency, Scale: uint8(scale)}
	if errs := m.Validate(); len(errs) > 0 {
		e := errs[0]
		r.reject(e.Code, name+"."+e.Field, m.String(), e.Message)
		return model.Decimal{}, currency
	}
	if int(scale) > maxFrac {
		r.reject(importer.CodeMoneyScale, name+".scale", scaleLit,
			"this field allows at most "+strconv.Itoa(maxFrac)+" fraction digits, and an input is never rounded")
		return model.Decimal{}, currency
	}
	return m.Decimal(), currency
}

// numericError turns a numeric parse failure into the right finding: a shape
// outside the accepted list is one code, too many fraction digits is another, and
// the difference matters because only the second one is a value a sender would
// call correct.
func (r *fieldReader) numericError(name, value string, maxFrac int, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, importer.ErrMoneyScale):
		r.reject(importer.CodeMoneyScale, name, value,
			"this field allows at most "+strconv.Itoa(maxFrac)+" fraction digits, and an input is never rounded")
	case errors.Is(err, model.ErrOverflow):
		r.reject(importer.CodeDecimalFormat, name, value, "the value is too large to represent exactly")
	default:
		r.reject(importer.CodeDecimalFormat, name, value,
			"expected a decimal with an optional apostrophe grouping, an optional currency prefix, and the sign as a leading minus, a trailing minus or parentheses")
	}
}

// unknownFields warns about every field the record carried that the dataset does
// not define. It is a warning and not a rejection: an extra column is almost
// always a sender adding a field, and dropping twelve thousand supplier records
// over it turns a compatible change into an outage.
func (r *fieldReader) unknownFields() {
	for _, name := range r.rec.sortedNames() {
		if !r.used[name] {
			r.warn(importer.CodeFieldUnknown, name, r.rec.fields[name].display(),
				"this dataset does not define a field of this name; the value was not stored")
		}
	}
}

// stamp sets the document key on every finding this reader produced and returns
// them, so a warning can be traced from a report back to the document it is
// about.
func (r *fieldReader) stamp(docKey string) []importer.Diagnostic {
	for i := range r.diags {
		r.diags[i].Document = docKey
	}
	return r.diags
}
