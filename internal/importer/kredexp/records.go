package kredexp

import (
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// parseRecords interprets the lexed records: the three remaining file-level
// fatal checks in their published precedence, then the body, then the trailer.
//
// The body loop is positional, which is the format's own binding rule: a
// Positionssatz belongs to the Kopfsatz it follows, "Positionssätze of different
// Belege are never interleaved" (section 3), and the only cross-check available
// is that both carry the same Belegnummer.
func parseRecords(b *importer.Builder, recs []record) {
	if len(recs) == 0 {
		b.Add((&fault{
			code:    CodeVorlaufMissing,
			message: "the file carries no records; section 3 requires exactly one Vorlaufsatz as the first record",
		}).diagnostic())
		return
	}
	head := recs[0]
	if head.satzart() != RecordVorlauf {
		b.Add((&fault{
			code:    CodeVorlaufMissing,
			line:    head.line,
			field:   fieldSatzart,
			value:   head.satzart(),
			message: "the first record of the file is not a Vorlaufsatz; without it the run has no Mandant and no default currency",
		}).diagnostic())
		return
	}
	if v := head.field(2); v != FormatVersion {
		b.Add((&fault{
			code:    CodeFormatVersionUnsupported,
			line:    head.line,
			field:   fieldFormatversion,
			value:   v,
			message: "this importer reads Formatversion " + FormatVersion + " only; a v2.0 Positionssatz carries " + strconv.Itoa(FieldsPos-1) + " fields rather than " + strconv.Itoa(FieldsPos) + ", so reading one as 2.1 would shift every field after the sixth",
		}).diagnostic())
		return
	}
	last := recs[len(recs)-1]
	if last.satzart() != RecordNachlauf {
		b.Add((&fault{
			code:    CodeTrailerMissing,
			field:   fieldSatzart,
			value:   last.satzart(),
			message: "the last record of the file is not a Nachlaufsatz; section 7 requires the receiver to verify the control totals and a file that carries none cannot be verified",
		}).diagnostic())
		return
	}

	st := &state{b: b, run: readVorlauf(b, head), gross: zeroAmount(), lineSum: zeroAmount()}
	for _, rec := range recs[1 : len(recs)-1] {
		switch rec.satzart() {
		case RecordKopf:
			st.kopf(rec)
		case RecordPos:
			st.pos(rec)
		default:
			st.misplaced(rec)
		}
	}
	st.flush()
	st.trailer(last)
}

// A runHeader is what the Vorlaufsatz contributes to every document of the run:
// the Mandant, which is the only company code the file carries, and the default
// currency a Kopfsatz that names none falls back on (section 5, field 8).
type runHeader struct {
	mandant  string
	currency string
}

// readVorlauf validates the Vorlaufsatz and returns the run header.
//
// A defect here is a reject like any other and does not stop the file: the
// records after it are still readable, and a run whose Testkennzeichen is
// missing has still delivered its invoices. What the receiver does with a test
// delivery is out of scope per section 9.5, so the indicator is validated and
// not acted on, and the creation timestamp is validated and then dropped: the
// canonical model has nowhere to put a sender's clock reading, and this importer
// reads no clock of its own.
func readVorlauf(b *importer.Builder, rec record) runHeader {
	r := newReader(rec)
	if !r.arity(FieldsVorlauf) {
		// The Mandant and the currency are read anyway, from the positions the
		// field table gives them: they are needed by every document in the file,
		// and a header with the wrong field count still usually carries them.
		r.emit(b, "")
		return runHeader{mandant: rec.field(3), currency: rec.field(5)}
	}
	run := runHeader{
		mandant:  r.required(3, fieldMandant),
		currency: r.required(5, fieldWaehrung),
	}
	r.timestamp(4, fieldErstellungszeitpunkt)
	r.required(6, fieldAbsender)
	r.required(7, fieldEmpfaenger)
	r.required(8, fieldTestkennzeichen)
	r.emit(b, "")
	return run
}

// A state is the body walk: the run header, the Kopfsatz currently open, and the
// counts and control totals computed over the records that were accepted.
//
// Only accepted records contribute. That is what makes the trailer check
// meaningful and also what makes it impossible when a record was rejected: the
// computed sums are then known to be short, and a mismatch derived from them
// would report the trailer as wrong when the trailer is right.
type state struct {
	b   *importer.Builder
	run runHeader

	pending *pending
	docs    int
	lines   int
	gross   model.Decimal
	lineSum model.Decimal
}

// A pending is the document being assembled: a Kopfsatz that was accepted, plus
// the Positionssätze read since. It is emitted when the next Kopfsatz, the
// trailer or the end of the body arrives, because a document is only complete
// once its last line has been seen.
type pending struct {
	line        int
	key         string
	belegnummer string
	vatCode     string
	invoice     model.APInvoice
}

// flush emits the pending document, if any.
func (st *state) flush() {
	if st.pending == nil {
		return
	}
	p := st.pending
	st.pending = nil
	payload, err := model.CanonicalJSON(p.invoice)
	if err != nil {
		// Canonicalization can only fail on a value this package built, so this
		// is a programming fault in the code above and not a data defect. It is
		// reported as fatal rather than panicked on: a twin that panics on one
		// record loses a batch that also carried good files.
		st.b.Add(importer.Diagnostic{
			Code:     CodeInvalidAmount,
			Severity: importer.SeverityFatal,
			Line:     p.line,
			Field:    "",
			Document: p.key,
			Message:  "internal: the document could not be canonicalized: " + err.Error(),
		})
		return
	}
	st.b.AddDocument(importer.Document{
		Dataset: model.DatasetInvoice.String(),
		Key:     p.key,
		Line:    p.line,
		Payload: payload,
	})
	st.docs++
}

// kopf reads a Kopfsatz (section 5) and opens a document.
//
// Field 13, Skonto, is the first of the three documented contradictions. The
// field table calls it "Skontobetrag in der Währung des Belegs" and the glossary
// calls it "Skontosatz in Prozent", and the example value 2.00 with Skontotage 10
// is compatible with both readings. It is carried verbatim into
// [model.APInvoice.DiscountRaw] as a string, never parsed and never converted,
// and reported once per document. A receiver that guesses "amount" pays a 2 franc
// discount on a 1'345 franc invoice; one that guesses "percent" pays 26.91. The
// difference is the supplier's money, so the reading is the sender's to confirm.
//
// The Belegart is carried verbatim and never used to derive a sign: that is the
// third contradiction, and section 3's trailing minus is the only sign this
// format has.
func (st *state) kopf(rec record) {
	st.flush()
	r := newReader(rec)
	if !r.arity(FieldsKopf) {
		r.emit(st.b, "")
		return
	}
	beleg := r.required(2, fieldBelegnummer)
	lieferant := r.required(3, fieldLieferantennummer)
	key := ""
	if beleg != "" && lieferant != "" {
		key = model.InvoiceKey(lieferant, beleg)
	}

	currency := r.text(8)
	if currency == "" {
		// Section 5, field 8: "if empty, the currency of the Vorlaufsatz
		// applies". This inheritance is documented, so it is applied and not
		// reported; the two that are not documented are surfaced instead.
		currency = st.run.currency
	}
	inv := model.APInvoice{
		SupplierNumber:        lieferant,
		SupplierInvoiceNumber: beleg,
		// The Mandant is the only company code this format carries; a Kopfsatz
		// has no such field. It is delivered as the file spelled it, "0100" and
		// not "CH10": mapping a Mandant to a canonical company code is master
		// data the twin holds, and an importer that invented the mapping would
		// be guessing on behalf of a system it cannot see.
		CompanyCode:      st.run.mandant,
		DocumentType:     r.required(4, fieldBelegart),
		DocumentDate:     r.date(5, fieldBelegdatum, true),
		ReceiptDate:      r.date(6, fieldEingangsdatum, false),
		PONumber:         r.text(7),
		Currency:         currency,
		VATCode:          r.text(11),
		PaymentTermsDays: r.integer(12, fieldZahlungsziel),
		DiscountRaw:      r.text(13),
		DiscountDays:     r.integer(14, fieldSkontotage),
		CostCenter:       r.text(15),
		Text:             r.text(16),
		Lines:            []model.APInvoiceLine{},
	}
	inv.GrossAmount = r.amount(9, fieldBruttobetrag, ScaleAmount, true, true)
	inv.VATAmount = r.amount(10, fieldMWSTBetrag, ScaleAmount, false, true)
	if inv.DiscountRaw != "" {
		r.warn(CodeAmbiguousFieldSemantics, fieldSkonto, inv.DiscountRaw,
			"the field table documents Skonto as an amount in the currency of the document and the glossary documents it as a rate in percent; the value is carried verbatim and never converted, because the two readings pay the supplier different sums")
	}
	if r.blocked {
		r.emit(st.b, key)
		return
	}
	next, err := st.gross.Add(inv.GrossAmount)
	if err == nil {
		next, err = next.Rescale(ScaleAmount)
	}
	if err != nil {
		r.reject(CodeInvalidAmount, fieldBruttobetrag, rec.field(9),
			"the amount cannot be added to the run's control total without exceeding the exact decimal range")
		r.emit(st.b, key)
		return
	}
	if !r.emit(st.b, key) {
		return
	}
	st.gross = next
	st.pending = &pending{
		line:        rec.line,
		key:         key,
		belegnummer: beleg,
		vatCode:     inv.VATCode,
		invoice:     inv,
	}
}

// pos reads a Positionssatz (section 6) and appends it to the open document.
//
// Field 5, Kostenstelle, is the second documented contradiction: section 9.3 says
// the document does not define whether an empty position cost center means "use
// the Kopfsatz's" or "no cost center assigned", and that the sending system
// allows both. It is left empty and reported once per Positionssatz. Field 10,
// Steuercode, looks like the same problem and is not: section 6 states that an
// empty Steuercode means the Kopfsatz's MWST-Code applies. That inheritance is
// documented, so it happens silently. Treating the two fields alike - inheriting
// both, or reporting both - is what a reader who pattern-matched the layout
// instead of reading it would do.
func (st *state) pos(rec record) {
	r := newReader(rec)
	if st.pending == nil {
		r.reject(CodeOrphanLine, fieldBelegnummer, rec.field(2),
			"a Positionssatz belongs to the Kopfsatz it follows; none precedes this line, or the one that does was rejected")
		r.emit(st.b, "")
		return
	}
	if !r.arity(FieldsPos) {
		r.emit(st.b, st.pending.key)
		return
	}
	if beleg := rec.field(2); beleg != st.pending.belegnummer {
		r.reject(CodeOrphanLine, fieldBelegnummer, beleg,
			"the Belegnummer of a Positionssatz repeats field 2 of its Kopfsatz; Positionssätze of different Belege are never interleaved")
		r.emit(st.b, st.pending.key)
		return
	}

	taxCode := r.text(10)
	if taxCode == "" {
		taxCode = st.pending.vatCode
	}
	line := model.APInvoiceLine{
		LineNo:     r.required(3, fieldPositionsnummer),
		GLAccount:  r.required(4, fieldSachkonto),
		CostCenter: r.text(5),
		UoM:        r.required(7, fieldEinheit),
		TaxCode:    taxCode,
	}
	line.Quantity = r.amount(6, fieldMenge, ScaleQuantity, true, false)
	line.UnitPrice = r.amount(8, fieldEinzelpreis, ScaleUnitPrice, true, false)
	line.LineAmount = r.amount(9, fieldPositionsbetrag, ScaleAmount, true, true)
	// Field 11, the position text, has nowhere to go: the canonical invoice line
	// carries no free text. It is read and dropped rather than folded into
	// another field, because a position text appended to a document text would
	// change a content hash that has to be reproducible from the source bytes.
	if line.CostCenter == "" {
		r.warn(CodeAmbiguousFieldSemantics, fieldKostenstelle, "",
			"section 9.3 leaves undefined whether an empty position Kostenstelle inherits the one of the Kopfsatz; it is left empty rather than inherited, because booking a cost to a center nobody chose is not recoverable from the ledger")
	}
	if r.blocked {
		r.emit(st.b, st.pending.key)
		return
	}
	next, err := st.lineSum.Add(line.LineAmount)
	if err == nil {
		next, err = next.Rescale(ScaleAmount)
	}
	if err != nil {
		r.reject(CodeInvalidAmount, fieldPositionsbetrag, rec.field(9),
			"the amount cannot be added to the run's control total without exceeding the exact decimal range")
		r.emit(st.b, st.pending.key)
		return
	}
	if !r.emit(st.b, st.pending.key) {
		return
	}
	st.lineSum = next
	st.lines++
	st.pending.invoice.Lines = append(st.pending.invoice.Lines, line)
}

// misplaced reports a record that is neither a Kopfsatz nor a Positionssatz in
// the body of the file: an unknown Satzart, and also a Vorlaufsatz or a
// Nachlaufsatz in a position section 3 does not allow. Both are the same defect
// from the reader's side - a record it cannot interpret here - and the published
// code set has one code for it.
//
// A misplaced record does not close the open document. It is one line the sender
// should not have sent, and the Positionssätze around it still belong to the
// Kopfsatz they follow.
func (st *state) misplaced(rec record) {
	r := newReader(rec)
	r.reject(CodeUnknownRecordType, fieldSatzart, rec.satzart(),
		"the valid Satzarten are "+RecordVorlauf+", "+RecordKopf+", "+RecordPos+" and "+RecordNachlauf+"; the first must be the first record of the file and the last must be its last")
	key := ""
	if st.pending != nil {
		key = st.pending.key
	}
	r.emit(st.b, key)
}

// trailer reads the Nachlaufsatz (section 7) and either verifies the file or
// records why it could not be verified.
//
// The rule of BUILD-SPEC 10 is exact and this is the whole of it: the counts and
// both sums are compared only when nothing was rejected, and otherwise one
// [CodeTrailerNotVerified] warning is reported instead. A control total computed
// over an incomplete set of records is not evidence about the sender, and
// reporting it as a mismatch would blame the trailer for a defect three hundred
// lines above it.
//
// A mismatch that is found is fatal: section 7 says a file whose control totals
// do not match must not be posted. The documents already read stay in the
// [importer.Result] - they are what an operator compares against the sender's
// own report - and [importer.Result.Accepted] is false, so nothing is applied.
func (st *state) trailer(rec record) {
	r := newReader(rec)
	t := importer.Totals{
		TrailerDeclared:   true,
		ComputedDocuments: st.docs,
		ComputedLines:     st.lines,
		ComputedGross:     st.gross.String(),
		ComputedLineSum:   st.lineSum.String(),
	}
	var (
		declaredGross, declaredLineSum model.Decimal
		haveGross, haveLineSum         bool
	)
	if r.arity(FieldsNachlauf) {
		t.DeclaredDocuments = r.count(2, fieldAnzahlKopfsaetze)
		t.DeclaredLines = r.count(3, fieldAnzahlPositionssaetze)
		before := len(r.diags)
		declaredGross = r.amount(4, fieldSummeBruttobetrag, ScaleAmount, true, true)
		haveGross = len(r.diags) == before
		before = len(r.diags)
		declaredLineSum = r.amount(5, fieldSummePositionsbetrag, ScaleAmount, true, true)
		haveLineSum = len(r.diags) == before
	}
	if haveGross {
		t.DeclaredGross = declaredGross.String()
	}
	if haveLineSum {
		t.DeclaredLineSum = declaredLineSum.String()
	}
	r.emit(st.b, "")

	// The warning is added to the builder directly rather than through the
	// reader, because it is the one finding that has to survive a rejected
	// trailer record: it says that the trailer was not checked, which is exactly
	// what a rejected trailer means.
	if st.b.HasBlocking() {
		st.b.Add(importer.Diagnostic{
			Code:     CodeTrailerNotVerified,
			Severity: importer.SeverityWarn,
			Line:     rec.line,
			Record:   rec.ordinal,
			Message:  "a record of this file was rejected, so the computed counts and sums are known to be incomplete and the trailer was not compared against them",
		})
		t.TrailerVerified = false
		st.b.SetTotals(t)
		return
	}

	// Past this point every declared value was read: each way of failing to
	// read one is a reject, and a reject lands in the branch above. The two
	// sums are therefore compared unguarded, and the "have" flags exist only so
	// that a trailer whose sum could not be parsed reports no declared value
	// rather than a fabricated zero.
	verified := true
	if t.DeclaredDocuments != t.ComputedDocuments {
		st.mismatch(rec, CodeTrailerCountMismatch, fieldAnzahlKopfsaetze,
			strconv.Itoa(t.DeclaredDocuments), strconv.Itoa(t.ComputedDocuments),
			"the declared number of Kopfsätze disagrees with the number read")
		verified = false
	}
	if t.DeclaredLines != t.ComputedLines {
		st.mismatch(rec, CodeTrailerCountMismatch, fieldAnzahlPositionssaetze,
			strconv.Itoa(t.DeclaredLines), strconv.Itoa(t.ComputedLines),
			"the declared number of Positionssätze disagrees with the number read")
		verified = false
	}
	if declaredGross.Cmp(st.gross) != 0 {
		st.mismatch(rec, CodeTrailerSumMismatch, fieldSummeBruttobetrag,
			declaredGross.String(), st.gross.String(),
			"the declared sum of the Bruttobeträge disagrees with the sum read; section 7 forbids posting a file whose control totals do not match")
		verified = false
	}
	if declaredLineSum.Cmp(st.lineSum) != 0 {
		st.mismatch(rec, CodeTrailerSumMismatch, fieldSummePositionsbetrag,
			declaredLineSum.String(), st.lineSum.String(),
			"the declared sum of the Positionsbeträge disagrees with the sum read; section 7 forbids posting a file whose control totals do not match")
		verified = false
	}
	t.TrailerVerified = verified
	st.b.SetTotals(t)
}

// mismatch reports one trailer disagreement, naming the declared and the
// computed value: a diagnostic that says "the totals do not match" without
// saying which two numbers disagree costs an operator the round trip of adding
// up the file themselves.
func (st *state) mismatch(rec record, code, field, declared, computed, message string) {
	st.b.Add(importer.Diagnostic{
		Code:     code,
		Severity: importer.SeverityFatal,
		Line:     rec.line,
		Record:   rec.ordinal,
		Field:    field,
		Value:    declared,
		Message:  message + ": declared " + declared + ", read " + computed,
	})
}
