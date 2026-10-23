package kredexp

import (
	"errors"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// The field names of the customer's own field tables, in German and in the
// spelling the sender's configuration and every support ticket use. A diagnostic
// names the field an operator will search for, which is why nothing here is
// translated: "Bruttobetrag" appears in the export configuration, "gross amount"
// appears nowhere.
const (
	fieldSatzart              = "Satzart"
	fieldFormatversion        = "Formatversion"
	fieldMandant              = "Mandant"
	fieldErstellungszeitpunkt = "Erstellungszeitpunkt"
	fieldWaehrung             = "Währung"
	fieldAbsender             = "Absender"
	fieldEmpfaenger           = "Empfänger"
	fieldTestkennzeichen      = "Testkennzeichen"

	fieldBelegnummer       = "Belegnummer"
	fieldLieferantennummer = "Lieferantennummer"
	fieldBelegart          = "Belegart"
	fieldBelegdatum        = "Belegdatum"
	fieldEingangsdatum     = "Eingangsdatum"
	fieldBestellnummer     = "Bestellnummer"
	fieldBruttobetrag      = "Bruttobetrag"
	fieldMWSTBetrag        = "MWST-Betrag"
	fieldMWSTCode          = "MWST-Code"
	fieldZahlungsziel      = "Zahlungsziel"
	fieldSkonto            = "Skonto"
	fieldSkontotage        = "Skontotage"
	fieldKostenstelle      = "Kostenstelle"
	fieldText              = "Text"

	fieldPositionsnummer = "Positionsnummer"
	fieldSachkonto       = "Sachkonto"
	fieldMenge           = "Menge"
	fieldEinheit         = "Einheit"
	fieldEinzelpreis     = "Einzelpreis"
	fieldPositionsbetrag = "Positionsbetrag"
	fieldSteuercode      = "Steuercode"

	fieldAnzahlKopfsaetze      = "Anzahl Kopfsätze"
	fieldAnzahlPositionssaetze = "Anzahl Positionssätze"
	fieldSummeBruttobetrag     = "Summe Bruttobetrag"
	fieldSummePositionsbetrag  = "Summe Positionsbetrag"
)

// numericFormat is the numeric dialect of every number in this format: a decimal
// point, apostrophe grouping in groups of three, and the trailing minus of
// section 3. Both apostrophes are accepted, U+0027 and U+2019, because a text
// field typed on Windows and an amount field written by the same exporter share
// an autocorrect.
//
// The manifest's declared CSV dialect is deliberately ignored. The customer's own
// document fixes the separators of this format, and a manifest that redeclared
// them would be describing a different file than the one the sender agreed to
// send.
func numericFormat(maxFraction int) importer.NumericFormat {
	return importer.NumericFormat{
		DecimalSeparator:   ".",
		ThousandsSeparator: importer.GroupApostrophe,
		MaxFractionDigits:  maxFraction,
	}
}

// A reader reads the fields of one record and collects the findings, so that a
// record produces every finding it has rather than only its first: an operator
// fixing an export wants the whole list of what is wrong with a line.
//
// The findings are stamped with the record's position and with the document key
// when one is known, and are handed to the [importer.Builder] by [reader.emit].
type reader struct {
	rec     record
	diags   []importer.Diagnostic
	blocked bool
}

// newReader returns a reader over one record.
func newReader(rec record) *reader { return &reader{rec: rec} }

// reject records a record-level finding and marks the record dropped.
func (r *reader) reject(code, field, value, message string) {
	r.blocked = true
	r.diags = append(r.diags, importer.Diagnostic{
		Code:     code,
		Severity: importer.SeverityReject,
		Line:     r.rec.line,
		Record:   r.rec.ordinal,
		Field:    field,
		Value:    value,
		Message:  message,
	})
}

// warn records a finding a human should see and a machine may proceed past.
func (r *reader) warn(code, field, value, message string) {
	r.diags = append(r.diags, importer.Diagnostic{
		Code:     code,
		Severity: importer.SeverityWarn,
		Line:     r.rec.line,
		Record:   r.rec.ordinal,
		Field:    field,
		Value:    value,
		Message:  message,
	})
}

// fatal records a file-level finding that does not stop the parse but does stop
// the file from being applied. Only the two trailer mismatches use it: they are
// found after every record has been read, so the documents are already known and
// are still worth reporting as evidence, while the file itself must not be
// posted.
func (r *reader) fatal(code, field, value, message string) {
	r.diags = append(r.diags, importer.Diagnostic{
		Code:     code,
		Severity: importer.SeverityFatal,
		Line:     r.rec.line,
		Record:   r.rec.ordinal,
		Field:    field,
		Value:    value,
		Message:  message,
	})
}

// emit hands the collected findings to b, stamped with the document key, and
// reports whether the record was dropped.
//
// A dropped record's warnings are discarded. A warning describes a document that
// will exist, and travels with it onto a proposal and into a receipt; a warning
// about a line that was rejected describes nothing, and would leave an operator
// looking for an ambiguity in a document that was never created.
func (r *reader) emit(b *importer.Builder, document string) bool {
	for _, d := range r.diags {
		if r.blocked && d.Severity == importer.SeverityWarn {
			continue
		}
		d.Document = document
		b.Add(d)
	}
	return !r.blocked
}

// arity checks the record's field count against its Satzart's field table. A
// mismatch is reported once, against the record as a whole rather than against
// one field, and no further field is read: after an arity change every position
// is a guess.
func (r *reader) arity(want int) bool {
	if len(r.rec.fields) == want {
		return true
	}
	r.reject(CodeFieldCountMismatch, "", strconv.Itoa(len(r.rec.fields)),
		"a "+r.rec.satzart()+" record carries exactly "+strconv.Itoa(want)+" fields; no field is read from a record whose count disagrees, because every position after the difference would be a guess")
	return false
}

// text returns an optional field verbatim.
func (r *reader) text(n int) string { return r.rec.field(n) }

// required returns a field the customer's table marks M, reporting
// [CodeMissingRequiredField] when it is empty.
func (r *reader) required(n int, field string) string {
	v := r.rec.field(n)
	if v == "" {
		r.reject(CodeMissingRequiredField, field, "",
			"the field table marks this field mandatory")
	}
	return v
}

// amount reads a Betragsfeld, a Menge or an Einzelpreis at the fraction-digit
// limit of its field.
//
// required says whether an empty value is [CodeMissingRequiredField]; an empty
// optional amount is zero at scale two, which is what section 5 states for
// MWST-Betrag ("Empty means 0.00") and which is the only reading that keeps a
// control total addable. normalize says whether the value is rescaled to exactly
// two fraction digits: an amount field is, because section 3 fixes its scale,
// while a Menge and an Einzelpreis keep the precision they were delivered with,
// so that "45.5000" and "45.50" stay distinguishable in the store.
func (r *reader) amount(n int, field string, maxFraction int, required, normalize bool) model.Decimal {
	raw := r.rec.field(n)
	if raw == "" {
		if required {
			r.reject(CodeMissingRequiredField, field, "",
				"the field table marks this amount mandatory")
			return model.Decimal{}
		}
		return zeroAmount()
	}
	d, currency, err := importer.ParseAmount(raw, numericFormat(maxFraction))
	switch {
	case errors.Is(err, importer.ErrMoneyScale):
		r.reject(CodeInvalidAmount, field, raw,
			"the value carries more than "+strconv.Itoa(maxFraction)+" fraction digits; an input is never rounded, because a rounded input is how a supplier gets paid the wrong amount")
		return model.Decimal{}
	case err != nil:
		r.reject(CodeInvalidAmount, field, raw,
			"the value is not a decimal in the documented spelling: a decimal point, optional apostrophe grouping in groups of three, and a minus sign after the last digit for a negative")
		return model.Decimal{}
	case currency != "":
		r.reject(CodeInvalidAmount, field, raw,
			"an amount field of this format carries digits only; the currency of a document is Kopfsatz field 8 and the currency of a run is Vorlaufsatz field 5")
		return model.Decimal{}
	}
	if !normalize {
		return d
	}
	out, err := d.Rescale(ScaleAmount)
	if err != nil {
		// Unreachable: the parse capped the scale at two, and increasing a
		// scale is always exact. It is reported rather than ignored so that a
		// future change to the cap cannot silently produce an amount at the
		// wrong scale.
		r.reject(CodeInvalidAmount, field, raw,
			"the value cannot be expressed with exactly two fraction digits")
		return model.Decimal{}
	}
	return out
}

// zeroAmount returns 0.00, the value of an empty optional Betragsfeld.
func zeroAmount() model.Decimal {
	d, err := model.NewDecimal(0, ScaleAmount)
	if err != nil {
		// Unreachable: two is inside the supported scale range.
		panic("kredexp: 0.00 is not representable: " + err.Error())
	}
	return d
}

// date reads a TTMMJJ date and returns it as a canonical YYYY-MM-DD calendar
// date, or "" when it is absent or invalid.
func (r *reader) date(n int, field string, required bool) string {
	raw := r.rec.field(n)
	if raw == "" {
		if required {
			r.reject(CodeMissingRequiredField, field, "",
				"the field table marks this date mandatory")
		}
		return ""
	}
	d, err := ParseTTMMJJ(raw)
	if err != nil {
		r.reject(CodeInvalidDate, field, raw,
			"a date is six digits TTMMJJ naming a day the calendar has; a two digit year from "+strconv.Itoa(CenturyPivot)+" to 99 is 19xx and from 00 to "+strconv.Itoa(CenturyPivot-1)+" it is 20xx")
		return ""
	}
	return d
}

// timestamp reads the TTMMJJHHMM Erstellungszeitpunkt of the Vorlaufsatz and
// returns it as a canonical YYYY-MM-DD date plus a zero-padded HHMM, or two
// empty strings when it is absent or invalid. The clock value is the sender's;
// nothing here reads a clock of its own.
func (r *reader) timestamp(n int, field string) (string, string) {
	raw := r.rec.field(n)
	if raw == "" {
		r.reject(CodeMissingRequiredField, field, "",
			"the field table marks the creation timestamp mandatory")
		return "", ""
	}
	date, clock, err := ParseTTMMJJHHMM(raw)
	if err != nil {
		r.reject(CodeInvalidDate, field, raw,
			"the creation timestamp is ten digits TTMMJJHHMM in local time Europe/Zurich")
		return "", ""
	}
	return date, clock
}

// integer reads an optional whole-number field such as Zahlungsziel or
// Skontotage. An absent value is zero, which is how "no payment term supplied"
// reaches the canonical model; only the shape is checked, because whether a term
// of 3650 days is plausible is a matching question and not a parsing one.
func (r *reader) integer(n int, field string) int {
	raw := r.rec.field(n)
	if raw == "" {
		return 0
	}
	v, err := importer.ParseInteger(raw)
	if err != nil {
		r.reject(CodeInvalidAmount, field, raw,
			"the field table types this field N: a whole number of digits, with no grouping and no decimal part")
		return 0
	}
	return v
}

// count reads a mandatory record count of the Nachlaufsatz.
func (r *reader) count(n int, field string) int {
	raw := r.rec.field(n)
	if raw == "" {
		r.reject(CodeMissingRequiredField, field, "",
			"the trailer's record counts are mandatory; section 7 requires the receiver to verify them")
		return 0
	}
	v, err := importer.ParseInteger(raw)
	if err != nil || v < 0 {
		r.reject(CodeInvalidAmount, field, raw,
			"a record count is a non-negative whole number of digits")
		return 0
	}
	return v
}

// ParseTTMMJJ parses the six-digit TTMMJJ date of the customer document and
// returns it as a canonical YYYY-MM-DD calendar date.
//
// The century pivot is section 3's and applies without exception: a two-digit
// year from [CenturyPivot] to 99 is 19xx, from 00 to [CenturyPivot]-1 it is
// 20xx. The result is validated against the real calendar, so 290226 - the 29th
// of February 2026, a year that has 28 - is an error and not a silent 1 March.
func ParseTTMMJJ(s string) (string, error) {
	if len(s) != 6 || !allDigits(s) {
		return "", errBadDate
	}
	day := atoi2(s[0:2])
	month := atoi2(s[2:4])
	year := 2000 + atoi2(s[4:6])
	if atoi2(s[4:6]) >= CenturyPivot {
		year = 1900 + atoi2(s[4:6])
	}
	d, err := model.ParseDate(pad4(year) + "-" + pad2(month) + "-" + pad2(day))
	if err != nil {
		return "", errBadDate
	}
	return d.String(), nil
}

// ParseTTMMJJHHMM parses the ten-digit Erstellungszeitpunkt of the Vorlaufsatz
// and returns the canonical calendar date and the zero-padded HHMM clock reading
// it carries. The clock is local time Europe/Zurich per section 4, and is kept as
// the sender's four digits rather than turned into an instant: this importer
// resolves no time zone and reads no clock.
func ParseTTMMJJHHMM(s string) (date, clock string, err error) {
	if len(s) != 10 || !allDigits(s) {
		return "", "", errBadDate
	}
	date, err = ParseTTMMJJ(s[0:6])
	if err != nil {
		return "", "", err
	}
	hour, minute := atoi2(s[6:8]), atoi2(s[8:10])
	if hour > 23 || minute > 59 {
		return "", "", errBadDate
	}
	return date, s[6:10], nil
}

// errBadDate is the internal marker of an unparseable date. It never leaves the
// package: a caller turns it into [CodeInvalidDate].
var errBadDate = errors.New("kredexp: value is not a date in the documented format")

// allDigits reports whether s consists only of ASCII digits and is not empty.
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

// atoi2 returns the value of a two-digit ASCII number. The caller has already
// checked that the digits are digits.
func atoi2(s string) int { return int(s[0]-'0')*10 + int(s[1]-'0') }

// pad2 renders a number in two digits.
func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

// pad4 renders a year in four digits.
func pad4(v int) string {
	s := strconv.Itoa(v)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
