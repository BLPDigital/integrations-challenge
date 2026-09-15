package miniblp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The blocking exception codes of the matching engine, from BUILD-SPEC 8.3,
// 16.1 and 17.5. The set is closed and published: an unpublished code cannot be
// graded, and grading compares the twin's open queue against the expected set as
// a symmetric difference, so inventing a code costs exactly as much as missing
// one.
const (
	// ExcSupplierUnknown reports an invoice whose supplier the twin does not
	// hold.
	ExcSupplierUnknown = "EXC_SUPPLIER_UNKNOWN"
	// ExcSupplierBlocked reports an invoice for a blocked supplier.
	ExcSupplierBlocked = "EXC_SUPPLIER_BLOCKED"
	// ExcPONotFound reports a purchase order reference that resolves to nothing.
	ExcPONotFound = "EXC_PO_NOT_FOUND"
	// ExcPOSupplierMismatch reports a purchase order belonging to another
	// supplier.
	ExcPOSupplierMismatch = "EXC_PO_SUPPLIER_MISMATCH"
	// ExcPOClosed reports a purchase order that is CLOSED or CANCELLED.
	ExcPOClosed = "EXC_PO_CLOSED"
	// ExcTotalsMismatch reports a gross amount that is not the sum of the line
	// amounts plus VAT, exactly.
	ExcTotalsMismatch = "EXC_TOTALS_MISMATCH"
	// ExcCostCenterUnknown reports a cost center that does not exist, is
	// blocked, belongs to another company code or is not valid on the document
	// date.
	ExcCostCenterUnknown = "EXC_COST_CENTER_UNKNOWN"
	// ExcFxRateMissing reports a foreign currency invoice with no DAILY rate
	// covering its posting date.
	ExcFxRateMissing = "EXC_FX_RATE_MISSING"
	// ExcUoMUnmappable reports a unit of measure with no conversion to a base
	// unit. A guess here is a wrong quantity in a ledger.
	ExcUoMUnmappable = "EXC_UOM_UNMAPPABLE"
	// ExcUoMUnconvertible reports a conversion whose base quantity would need
	// more than three decimals. Rounding it is a silent change to what was
	// ordered.
	ExcUoMUnconvertible = "EXC_UOM_UNCONVERTIBLE"
	// ExcDuplicateInvoiceAmended reports a supplier invoice number re-delivered
	// with different content. It is one blocking exception carrying both gross
	// amounts, their delta and the ERP document number already posted, because
	// posting an amended invoice again is a duplicate in a customer's ledger and
	// there is no reversal endpoint.
	ExcDuplicateInvoiceAmended = "EXC_DUPLICATE_INVOICE_AMENDED"
)

// Warning codes the matching engine adds to a proposal. A warning is never an
// exception: it travels on the record, on the proposal and into the receipt, and
// it never enters the open queue and never changes an exit code.
const (
	// WarnAmbiguousFieldSemantics is the importer's warning about a field the
	// source specification leaves undecided. It reaches the proposal so that the
	// ambiguity is visible to whoever approves the posting.
	WarnAmbiguousFieldSemantics = importer.CodeAmbiguousFieldSemantics
	// WarnUoMConverted reports a line whose quantity was converted to its base
	// unit. The conversion is exact; the warning exists because a converted
	// quantity is not the quantity the sender wrote.
	WarnUoMConverted = "W_UOM_CONVERTED"
)

// SubjectTypeInvoice and SubjectTypeProposal are the subject types of the
// twin's exceptions. An exception about a delivery is about the invoice; one
// about a posting is about the proposal.
const (
	SubjectTypeInvoice  = "invoice"
	SubjectTypeProposal = "proposal"
)

// ExceptionCodes returns every blocking exception code the matching engine can
// raise, in ascending byte order. It is the published, closed set: the grader
// compares its own expectation against this list, so a code that is not here
// cannot be scored.
func ExceptionCodes() []string {
	out := []string{
		ExcCostCenterUnknown, ExcDuplicateInvoiceAmended, ExcFxRateMissing,
		ExcPOClosed, ExcPONotFound, ExcPOSupplierMismatch, ExcSupplierBlocked,
		ExcSupplierUnknown, ExcTotalsMismatch, ExcUoMUnconvertible, ExcUoMUnmappable,
		// Raised by the acknowledgment path rather than by the match, and
		// published here because the graded exception set is compared as a
		// closed set: a code the twin can open and this list omits would be
		// scored as spurious for every candidate.
		CodeAckRejected,
	}
	sort.Strings(out)
	return out
}

// UoMConversion is one row of the unit-of-measure conversion table the twin
// holds: the base quantity of an alternative quantity is alt * Numerator /
// Denominator. An empty Material is the generic rule for the alternative unit
// and a row with a material overrides it for that material.
//
// The table is customizing rather than delivered master data: it is loaded by
// POST /admin/v1/seed, exactly as the cost centers are, because at a real
// customer it was configured long before this interface existed. When no table
// is loaded the twin cannot judge a unit and does not invent an exception, which
// keeps a twin with no customizing from rejecting every invoice it sees.
type UoMConversion struct {
	Material    string `json:"material"`
	AltUoM      string `json:"alt_uom"`
	Numerator   int64  `json:"numerator"`
	Denominator int64  `json:"denominator"`
	BaseUoM     string `json:"base_uom"`
}

// Key returns the natural key of a conversion row: material and alternative
// unit, joined with the unit separator.
func (c UoMConversion) Key() string {
	return c.Material + model.KeySeparator + c.AltUoM
}

// DatasetUoMConversion is the store's auxiliary dataset holding the conversion
// table. It is not one of model.Datasets, so it never reaches the state digest:
// customizing is not delivered content, and a twin seeded with a conversion
// table has to hash identically to one that received the same invoices without
// it.
const DatasetUoMConversion = "uom_conversion"

// matchBatch runs the matching engine over the invoices one batch changed and
// records the proposals and exceptions on the batch.
//
// The caller holds the server lock. Invoices are processed in ascending natural
// key order, and the rules are applied in the published order with the first
// failing rule winning, so exactly one exception is raised per invoice and the
// outcome does not depend on delivery order.
func (s *Server) matchBatch(b *batchState) error {
	keys := b.changed()
	if len(keys) == 0 {
		return nil
	}
	m, err := s.newMatcher()
	if err != nil {
		return err
	}
	prov := store.Provenance{
		BatchID:      b.ID,
		Channel:      store.ChannelInternal,
		Profile:      "match",
		SourceSystem: b.manifest.SourceSystem,
		RunID:        b.manifest.RunID,
	}
	if b.Channel == ChannelFile && b.Scan > 0 {
		scan := b.Scan
		prov.ReceivedScan = &scan
	}
	for _, key := range keys {
		if err := s.matchInvoice(b, m, key, prov); err != nil {
			return err
		}
	}
	if len(b.Proposals) > 0 {
		runID := b.manifest.RunID
		if runID == "" {
			runID = s.cfg.RunID
		}
		if err := s.emitOutbox(runID); err != nil {
			return err
		}
	}
	return nil
}

// matchInvoice matches one invoice and either emits a proposal or raises one
// blocking exception.
func (s *Server) matchInvoice(b *batchState, m *matcher, key string, prov store.Provenance) error {
	rev, ok := s.st.Get(model.DatasetInvoice.String(), key)
	if !ok {
		return nil
	}
	var inv model.APInvoice
	if err := decodePayload(rev.Payload, &inv); err != nil {
		return fmt.Errorf("miniblp: decoding invoice %q: %w", key, err)
	}
	lines, err := m.linesOf(inv, key)
	if err != nil {
		return err
	}
	inv.Lines = lines

	raise := func(code, field, message string, details map[string]string) error {
		return s.raise(b, store.Exception{
			SubjectKey:          key,
			SubjectType:         SubjectTypeInvoice,
			Stage:               store.StageMatch,
			Code:                code,
			Field:               field,
			Message:             message,
			State:               store.ExceptionOpen,
			SourceBatchID:       b.ID,
			SourceFileOrChunk:   m.locate(b, key),
			SourceLineOrOrdinal: m.lineOf(b, key),
			Details:             details,
		}, prov)
	}

	// 0. A re-delivery with different content is not a second posting.
	if rev.Version >= 2 {
		details, err := s.amendedDetails(key, rev.Version, inv, m)
		if err != nil {
			return err
		}
		return raise(ExcDuplicateInvoiceAmended, "supplier_invoice_number",
			"this supplier invoice number was already delivered with different content", details)
	}

	// 1. Supplier.
	sup, ok, err := m.supplier(inv.SupplierNumber)
	if err != nil {
		return err
	}
	if !ok {
		return raise(ExcSupplierUnknown, "supplier_number", "the invoice's supplier is not known to the twin", nil)
	}
	if sup.Blocked {
		return raise(ExcSupplierBlocked, "supplier_number", "the invoice's supplier is blocked", nil)
	}

	// 2. Purchase order, header and line.
	var po model.PurchaseOrder
	havePO := false
	if strings.TrimSpace(inv.PONumber) != "" {
		number, _ := splitPOReference(inv.PONumber)
		po, havePO, err = m.purchaseOrder(number)
		if err != nil {
			return err
		}
		if !havePO {
			return raise(ExcPONotFound, "po_number", "the referenced purchase order does not exist", nil)
		}
		if po.SupplierNumber != inv.SupplierNumber {
			return raise(ExcPOSupplierMismatch, "po_number",
				"the referenced purchase order belongs to another supplier", nil)
		}
		if po.Status == model.POStatusClosed || po.Status == model.POStatusCancelled {
			return raise(ExcPOClosed, "po_number",
				"the referenced purchase order is closed or cancelled", nil)
		}
	}

	// 3. Header total consistency, exact.
	lineSum, err := inv.LineTotal()
	if err != nil {
		return raise(ExcTotalsMismatch, "gross_amount", "the line amounts do not sum: "+err.Error(), nil)
	}
	want, err := lineSum.Add(inv.VATAmount)
	if err != nil {
		return raise(ExcTotalsMismatch, "gross_amount", "the line amounts and the VAT do not sum: "+err.Error(), nil)
	}
	if !want.Equal(inv.GrossAmount) {
		return raise(ExcTotalsMismatch, "gross_amount",
			"the gross amount is not the sum of the line amounts plus the VAT amount", nil)
	}

	// 4. Cost center: the invoice's, else the purchase order's.
	code := inv.CostCenter
	if code == "" && havePO {
		code = po.CostCenter
	}
	cc, ok, err := m.costCenter(code)
	if err != nil {
		return err
	}
	if !ok || cc.Blocked || cc.CompanyCode != inv.CompanyCode {
		return raise(ExcCostCenterUnknown, "cost_center",
			"the cost center is unknown, blocked, or not configured for the company code", nil)
	}
	postingDate, err := postingDateOf(inv)
	if err != nil {
		return raise(ExcCostCenterUnknown, "document_date", "the document date is not a calendar date", nil)
	}
	inRange, err := model.DateInHalfOpenRange(postingDate, cc.ValidFrom, cc.ValidTo)
	if err != nil || !inRange {
		return raise(ExcCostCenterUnknown, "cost_center",
			"the cost center is not valid on the posting date", nil)
	}

	// 5. Currency: a foreign currency needs a DAILY rate covering the posting
	// date on the Europe/Zurich calendar. A MONTHLY_AVG row is not a daily rate
	// and a DELETED row is not a rate at all.
	var rate model.FxRate
	needsFx := inv.Currency != quoteCurrency
	if needsFx {
		// Both messages carry the currency and the posting date in details. An
		// exception a clerk has to act on has to say WHICH currency and WHICH
		// date it could not price, or the clerk's next step is a database query.
		if !HasFxTable(inv.CompanyCode) {
			return raise(ExcFxRateMissing, "company_code",
				"this company code has no exchange rate table, so a foreign currency invoice of it cannot be converted",
				map[string]string{
					"currency":     inv.Currency,
					"company_code": inv.CompanyCode,
					"posting_date": postingDate,
				})
		}
		rate, ok, err = m.dailyRate(inv.Currency, postingDate)
		if err != nil {
			return err
		}
		if !ok {
			return raise(ExcFxRateMissing, "currency",
				"no DAILY exchange rate covers the posting date for this currency",
				map[string]string{
					"currency":     inv.Currency,
					"rate_type":    model.RateTypeDaily,
					"posting_date": postingDate,
				})
		}
	}

	// 6. Units of measure.
	warnings := append([]string{}, b.docWarnings[key]...)
	if m.hasConversions() {
		for _, line := range inv.Lines {
			material := ""
			if havePO {
				if l, ok, err := m.poLine(po.PONumber, line.LineNo); err != nil {
					return err
				} else if ok {
					material = l.Material
				}
			}
			conv, ok := m.conversion(material, line.UoM)
			if !ok {
				return raise(ExcUoMUnmappable, "uom",
					"unit of measure "+line.UoM+" has no conversion to a base unit", nil)
			}
			base, err := convertQuantity(line.Quantity, conv)
			if err != nil {
				return raise(ExcUoMUnconvertible, "quantity",
					"the base quantity of unit "+line.UoM+" is not exact at three decimals", nil)
			}
			if conv.Numerator != conv.Denominator && !base.Equal(line.Quantity) {
				warnings = appendCode(warnings, WarnUoMConverted)
			}
		}
	}

	// 7. Convert and emit.
	amount := inv.GrossAmount
	factor := int64(0)
	if needsFx {
		factor = rate.RateFactor
		if factor <= 0 {
			factor = 1
		}
		amount, err = inv.GrossAmount.MulRate(rate.Rate, factor, 2)
		if err != nil {
			return raise(ExcFxRateMissing, "gross_amount",
				"the gross amount could not be converted with the covering rate: "+err.Error(),
				map[string]string{
					"currency":     inv.Currency,
					"rate":         rate.Rate.String(),
					"rate_factor":  strconv.FormatInt(factor, 10),
					"posting_date": postingDate,
				})
		}
	}
	return s.emitProposal(b, m, key, rev.Version, inv, po, havePO, cc.Code, rate, factor, amount, warnings, prov)
}

// quoteCurrency is the currency every posting is made in and every seeded rate
// converts to.
const quoteCurrency = "CHF"

// companyCodesWithoutFxTable are the company codes of this landscape that have
// no exchange rate table at all. See HasFxTable.
var companyCodesWithoutFxTable = map[string]bool{model.CompanyCodeCH20: true}

// HasFxTable reports whether a company code has an exchange rate table in this
// landscape.
//
// CH20 has none: BUILD-SPEC 7.3 fixes it as a permanent SOAP fault
// (ERP-FX-014), because that subsidiary was never configured for foreign
// currency. The fact belongs here rather than in the delivered data because the
// canonical fx_rate record has no company code: an exchange rate between two
// currencies on a date is not a property of a subsidiary, and adding one to the
// key would let a connector deliver CH10's rates as CH20's.
//
// The consequence is the one the seeded data is built around: a foreign currency
// invoice of a company code with no rate table can never be converted, so it is
// EXC_FX_RATE_MISSING even when a rate for its currency and date is on file for
// another company code. Converting it with another subsidiary's rate would post
// an amount nobody quoted, and posting it unconverted would post the wrong
// currency; both are worse than an exception a human resolves.
func HasFxTable(companyCode string) bool { return !companyCodesWithoutFxTable[companyCode] }

// emitProposal writes one immutable proposal for a matched invoice, or leaves the
// existing one alone when the same invoice version already produced it.
//
// Re-matching an unchanged invoice is therefore free and cannot create a second
// proposal, which matters because the matching engine runs on every commit and a
// resumed run re-delivers what it already delivered.
func (s *Server) emitProposal(b *batchState, m *matcher, key string, version int,
	inv model.APInvoice, po model.PurchaseOrder, havePO bool, costCenter string,
	rate model.FxRate, factor int64, amount model.Decimal, warnings []string,
	prov store.Provenance) error {

	if prev, ok := m.proposalFor(key); ok && prev.InvoiceVersion == version {
		// The decision has already been made on exactly this content. Resolving
		// the invoice's exceptions is still right: a later delivery may have
		// fixed the master data the first attempt was missing.
		return s.resolveExceptions(b, m, key, prov)
	}

	source, err := model.NewMoney(inv.Currency, inv.GrossAmount)
	if err != nil {
		return fmt.Errorf("miniblp: invoice %q source amount: %w", key, err)
	}
	target, err := model.NewMoney(quoteCurrency, amount)
	if err != nil {
		return fmt.Errorf("miniblp: invoice %q converted amount: %w", key, err)
	}
	if inv.Currency == quoteCurrency {
		target = source
	}

	id := fmt.Sprintf("prp_%07d", s.proposalSeq)
	p := store.Proposal{
		ProposalID:        id,
		InvoiceKey:        key,
		InvoiceVersion:    version,
		CreatedInRun:      prov.RunID,
		CreatedSeq:        s.proposalSeq,
		Status:            store.ProposalPending,
		MatchedCostCenter: costCenter,
		Amount:            target,
		SourceAmount:      source,
		Warnings:          warnings,
	}
	if havePO {
		p.MatchedPO = po.PONumber
	}
	if inv.Currency != quoteCurrency {
		p.FxRateUsed = rate.Rate
		p.FxRateFactor = factor
	}
	if _, err := s.st.PutProposal(p, prov); err != nil {
		return err
	}
	s.proposalSeq++
	b.Proposals = append(b.Proposals, id)
	m.noteProposal(p)

	// A newer decision supersedes the older one rather than editing it, so the
	// history says what was proposed and when it stopped being current.
	if prev, ok := m.previousProposal(key, id); ok {
		if _, err := s.st.SupersedeProposal(prev, id, prov); err != nil {
			return err
		}
	}
	return s.resolveExceptions(b, m, key, prov)
}

// resolveExceptions closes the open exceptions of an invoice that now matches.
// An exception is a statement about a delivery, and a delivery that succeeded
// after the master data caught up has to stop appearing in the open queue, or the
// queue stops being the list of things a human has to look at.
func (s *Server) resolveExceptions(b *batchState, m *matcher, key string, prov store.Provenance) error {
	for _, code := range m.openExceptions(key) {
		if _, err := s.st.ResolveException(key, code, prov); err != nil {
			return err
		}
		m.clearException(key, code)
	}
	return nil
}

// raise records one blocking exception and notes it on the batch. b may be nil,
// which is the direct ack path: the exception is raised either way and is simply
// not attributed to a batch.
func (s *Server) raise(b *batchState, e store.Exception, prov store.Provenance) error {
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	if _, err := s.st.RaiseException(e, prov); err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	b.Exceptions = append(b.Exceptions, ExceptionRef{
		SubjectKey: e.SubjectKey, SubjectType: e.SubjectType, Code: e.Code,
	})
	return nil
}

// amendedDetails builds the details of an EXC_DUPLICATE_INVOICE_AMENDED: both
// gross amounts, their delta and the ERP document number the first version was
// already posted under.
//
// The delta is the evidence a human needs to decide whether the amendment is a
// correction or a second invoice, and the document number is what makes the
// decision actionable: without it the only way to find the posting is to search
// the ledger by amount.
func (s *Server) amendedDetails(key string, version int, current model.APInvoice, m *matcher) (map[string]string, error) {
	details := map[string]string{
		"gross_amount_new": current.GrossAmount.String(),
		"currency":         current.Currency,
		"version":          fmt.Sprintf("%d", version),
	}
	history, err := s.st.History(model.DatasetInvoice.String(), key)
	if err != nil {
		return nil, err
	}
	for i := len(history) - 1; i >= 0; i-- {
		rev := history[i]
		if rev.ProvenanceOnly || rev.Version >= version {
			continue
		}
		var prev model.APInvoice
		if err := decodePayload(rev.Payload, &prev); err != nil {
			return nil, err
		}
		details["gross_amount_previous"] = prev.GrossAmount.String()
		if delta, err := current.GrossAmount.Sub(prev.GrossAmount); err == nil {
			details["gross_amount_delta"] = delta.String()
		}
		break
	}
	if p, ok := m.proposalFor(key); ok {
		details["proposal_id"] = p.ProposalID
		if p.Ack != nil && p.Ack.ExternalDocumentNumber != "" {
			details["erp_document_number"] = p.Ack.ExternalDocumentNumber
		}
	}
	return details, nil
}

// postingDateOf returns the posting date of an invoice as a Europe/Zurich
// calendar date.
//
// The posting date is the invoice's document date. A real accounts-payable run
// would post on the run date, and this is a deliberate, documented deviation:
// determinism wins, because the run date is a clock reading and a graded amount
// may not depend on when the grader happened to run. A document date delivered
// as an instant is converted on the Europe/Zurich calendar, which is the whole
// point of the daylight-saving boundary in the seeded data: truncating to UTC
// picks the rate of the day before.
func postingDateOf(inv model.APInvoice) (string, error) {
	if strings.Contains(inv.DocumentDate, "T") {
		return model.ZurichDateOf(inv.DocumentDate)
	}
	if !model.IsValidDate(inv.DocumentDate) {
		return "", model.ErrDateFormat
	}
	return inv.DocumentDate, nil
}

// splitPOReference splits a purchase order reference into its header number and
// an optional line suffix: "4500001234/00010" carries both. The line number
// keeps its leading zeros, because a purchase order line number is a string key.
func splitPOReference(ref string) (number, line string) {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return strings.TrimSpace(ref[:i]), strings.TrimSpace(ref[i+1:])
	}
	return ref, ""
}

// canonicalPONumber normalizes a purchase order number for comparison, exactly
// as BUILD-SPEC 16.1 states it: trim, and if the remainder is all digits compare
// numerically with leading zeros stripped, otherwise compare the trimmed string
// as it stands, case-sensitively. Cost centers are the documented exception to
// this rule and are never normalized: "0815" and "815" are two different cost
// centers.
func canonicalPONumber(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	digits := t
	if rest, ok := strings.CutPrefix(t, "PO-"); ok {
		digits = strings.TrimSpace(rest)
	}
	allDigits := digits != ""
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			allDigits = false
			break
		}
	}
	if !allDigits {
		return t
	}
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return "0"
	}
	return trimmed
}

// convertQuantity returns the base quantity of an alternative quantity: alt *
// numerator / denominator, exact at three decimals. A conversion that would need
// a fourth decimal is model.ErrInexact and never a rounded quantity, because a
// rounded quantity is a different order than the one that was placed.
func convertQuantity(qty model.Decimal, conv UoMConversion) (model.Decimal, error) {
	if conv.Denominator == 0 {
		return model.Decimal{}, fmt.Errorf("miniblp: conversion %s/%s has a zero denominator",
			conv.Material, conv.AltUoM)
	}
	numerator, err := model.NewDecimal(conv.Numerator, 0)
	if err != nil {
		return model.Decimal{}, err
	}
	wide, err := qty.MulRate(numerator, conv.Denominator, model.MaxScale)
	if err != nil {
		return model.Decimal{}, err
	}
	return wide.Rescale(3)
}

// decodePayload decodes a stored payload into a typed entity. The payload is
// canonical JSON, or the generic tree the store replays it as; both re-encode to
// the same bytes, so the decode is exact and no float64 appears on the path.
func decodePayload(payload any, v any) error {
	buf, err := model.CanonicalJSON(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}

// A matcher is the read side of one matching run: the lookups the rules need,
// loaded from the store on first use and cached for the run.
//
// Loading is lazy and per dataset, because the rules are ordered and most
// invoices never reach the later ones: a batch of invoices in CHF never touches
// the exchange rate table, and a batch with no purchase order references never
// builds the canonical purchase order index. Nothing here writes, so a matcher
// can be built once per batch and thrown away.
type matcher struct {
	s *Server

	suppliers   map[string]model.Supplier
	costCenters map[string]model.CostCenter
	poByNumber  map[string]model.PurchaseOrder
	poLines     map[string]model.PurchaseOrderLine

	poIndexed    bool
	fxIndexed    bool
	convIndexed  bool
	linesIndexed bool

	fx    map[string][]model.FxRate
	conv  map[string]UoMConversion
	lines map[string][]model.APInvoiceLine

	// proposals maps an invoice key to its current proposal, and previous keeps
	// the id a superseding proposal has to point at.
	proposals map[string]store.Proposal
	// open maps an invoice key to the codes of its open exceptions.
	open map[string][]string
}

// newMatcher builds the matcher of one matching run, loading only what every run
// needs: the proposals index, so an unchanged invoice cannot produce a second
// proposal, and the open exception queue, so a fixed invoice stops appearing in
// it.
func (s *Server) newMatcher() (*matcher, error) {
	m := &matcher{
		s:           s,
		suppliers:   map[string]model.Supplier{},
		costCenters: map[string]model.CostCenter{},
		poByNumber:  map[string]model.PurchaseOrder{},
		poLines:     map[string]model.PurchaseOrderLine{},
		proposals:   map[string]store.Proposal{},
		open:        map[string][]string{},
	}
	if err := s.st.ScanProposals(func(p store.Proposal) bool {
		if p.Status == store.ProposalSuperseded {
			return true
		}
		if cur, ok := m.proposals[p.InvoiceKey]; !ok || p.CreatedSeq >= cur.CreatedSeq {
			m.proposals[p.InvoiceKey] = p
		}
		return true
	}); err != nil {
		return nil, err
	}
	if err := s.st.ScanExceptions(store.ExceptionOpen, func(e store.Exception) bool {
		m.open[e.SubjectKey] = append(m.open[e.SubjectKey], e.Code)
		return true
	}); err != nil {
		return nil, err
	}
	return m, nil
}

// supplier returns an invoice's supplier.
func (m *matcher) supplier(number string) (model.Supplier, bool, error) {
	if s, ok := m.suppliers[number]; ok {
		return s, true, nil
	}
	rev, ok := m.s.st.Get(model.DatasetSupplier.String(), model.SupplierKey(number))
	if !ok {
		return model.Supplier{}, false, nil
	}
	var sup model.Supplier
	if err := decodePayload(rev.Payload, &sup); err != nil {
		return model.Supplier{}, false, err
	}
	m.suppliers[number] = sup
	return sup, true, nil
}

// costCenter returns a cost center by its code. The code is used verbatim:
// leading zeros are significant, so "0815" and "815" are two cost centers.
func (m *matcher) costCenter(code string) (model.CostCenter, bool, error) {
	if code == "" {
		return model.CostCenter{}, false, nil
	}
	if c, ok := m.costCenters[code]; ok {
		return c, true, nil
	}
	rev, ok := m.s.st.Get(model.DatasetCostCenter.String(), model.CostCenterKey(code))
	if !ok {
		return model.CostCenter{}, false, nil
	}
	var cc model.CostCenter
	if err := decodePayload(rev.Payload, &cc); err != nil {
		return model.CostCenter{}, false, err
	}
	m.costCenters[code] = cc
	return cc, true, nil
}

// purchaseOrder resolves a purchase order number in the three spellings the
// landscape uses. The delivered spelling is tried first, which is a single
// indexed lookup; only a miss builds the canonical index over the dataset.
func (m *matcher) purchaseOrder(number string) (model.PurchaseOrder, bool, error) {
	if rev, ok := m.s.st.Get(model.DatasetPurchaseOrder.String(), model.POKey(number)); ok {
		var po model.PurchaseOrder
		if err := decodePayload(rev.Payload, &po); err != nil {
			return po, false, err
		}
		return po, true, nil
	}
	if !m.poIndexed {
		m.poIndexed = true
		var payloads []any
		if err := m.s.st.Scan(model.DatasetPurchaseOrder.String(), func(rev store.Revision) bool {
			payloads = append(payloads, rev.Payload)
			return true
		}); err != nil {
			return model.PurchaseOrder{}, false, err
		}
		for _, payload := range payloads {
			var po model.PurchaseOrder
			if err := decodePayload(payload, &po); err != nil {
				return model.PurchaseOrder{}, false, err
			}
			m.poByNumber[canonicalPONumber(po.PONumber)] = po
		}
	}
	po, ok := m.poByNumber[canonicalPONumber(number)]
	return po, ok, nil
}

// poLine returns one purchase order line, which is where a line's material -
// and with it its unit conversion - comes from.
func (m *matcher) poLine(poNumber, lineNo string) (model.PurchaseOrderLine, bool, error) {
	if lineNo == "" {
		return model.PurchaseOrderLine{}, false, nil
	}
	key := model.POLineKey(poNumber, lineNo)
	if l, ok := m.poLines[key]; ok {
		return l, true, nil
	}
	rev, ok := m.s.st.Get(model.DatasetPurchaseOrderLine.String(), key)
	if !ok {
		return model.PurchaseOrderLine{}, false, nil
	}
	var line model.PurchaseOrderLine
	if err := decodePayload(rev.Payload, &line); err != nil {
		return line, false, err
	}
	m.poLines[key] = line
	return line, true, nil
}

// dailyRate returns the DAILY rate covering a calendar date for a currency:
// the half-open interval [valid_from, valid_to) contains the date, an open-ended
// valid_to matches everything from valid_from, and the highest sequence wins for
// an otherwise identical key.
func (m *matcher) dailyRate(currency, date string) (model.FxRate, bool, error) {
	if !m.fxIndexed {
		m.fxIndexed = true
		m.fx = map[string][]model.FxRate{}
		var payloads []any
		if err := m.s.st.Scan(model.DatasetFxRate.String(), func(rev store.Revision) bool {
			payloads = append(payloads, rev.Payload)
			return true
		}); err != nil {
			return model.FxRate{}, false, err
		}
		for _, payload := range payloads {
			var r model.FxRate
			if err := decodePayload(payload, &r); err != nil {
				return model.FxRate{}, false, err
			}
			if r.RateType != model.RateTypeDaily || r.Quote != quoteCurrency {
				continue
			}
			if r.Status == model.FxStatusDeleted {
				continue
			}
			m.fx[r.Base] = append(m.fx[r.Base], r)
		}
	}
	var best model.FxRate
	found := false
	for _, r := range m.fx[currency] {
		in, err := model.DateInHalfOpenRange(date, r.ValidFrom, r.ValidTo)
		if err != nil {
			return model.FxRate{}, false, nil
		}
		if !in {
			continue
		}
		if !found || r.Sequence > best.Sequence {
			best, found = r, true
		}
	}
	return best, found, nil
}

// hasConversions reports whether a unit-of-measure conversion table is loaded.
func (m *matcher) hasConversions() bool {
	m.loadConversions()
	return len(m.conv) > 0
}

// conversion returns the conversion row for a material and an alternative unit,
// falling back to the generic rule for the unit.
func (m *matcher) conversion(material, unit string) (UoMConversion, bool) {
	m.loadConversions()
	if c, ok := m.conv[material+model.KeySeparator+unit]; ok {
		return c, true
	}
	c, ok := m.conv[model.KeySeparator+unit]
	return c, ok
}

// loadConversions loads the conversion table once per matching run.
func (m *matcher) loadConversions() {
	if m.convIndexed {
		return
	}
	m.convIndexed = true
	m.conv = map[string]UoMConversion{}
	var payloads []any
	if err := m.s.st.Scan(DatasetUoMConversion, func(rev store.Revision) bool {
		payloads = append(payloads, rev.Payload)
		return true
	}); err != nil {
		return
	}
	for _, payload := range payloads {
		var c UoMConversion
		if err := decodePayload(payload, &c); err != nil {
			continue
		}
		m.conv[c.Key()] = c
	}
}

// linesOf returns an invoice's lines: the ones embedded in its own record, and
// otherwise the invoice_line records delivered separately.
//
// Both shapes exist because a CSV sender cannot put a list in a cell and
// delivers the lines as their own dataset, while a JSON or XML sender embeds
// them. The matching engine has to see the same invoice either way, or the
// totals rule would depend on the wire format.
func (m *matcher) linesOf(inv model.APInvoice, key string) ([]model.APInvoiceLine, error) {
	if len(inv.Lines) > 0 {
		return inv.Lines, nil
	}
	if !m.linesIndexed {
		m.linesIndexed = true
		m.lines = map[string][]model.APInvoiceLine{}
		type entry struct {
			key     string
			payload any
		}
		var entries []entry
		if err := m.s.st.Scan(model.DatasetInvoiceLine.String(), func(rev store.Revision) bool {
			entries = append(entries, entry{rev.Key, rev.Payload})
			return true
		}); err != nil {
			return nil, err
		}
		for _, e := range entries {
			parts := model.SplitKey(e.key)
			if len(parts) != 3 {
				continue
			}
			var line model.APInvoiceLine
			if err := decodePayload(e.payload, &line); err != nil {
				return nil, err
			}
			invKey := model.InvoiceKey(parts[0], parts[1])
			m.lines[invKey] = append(m.lines[invKey], line)
		}
		for k := range m.lines {
			ls := m.lines[k]
			sort.Slice(ls, func(i, j int) bool { return ls[i].LineNo < ls[j].LineNo })
			m.lines[k] = ls
		}
	}
	return m.lines[key], nil
}

// proposalFor returns the current proposal of an invoice.
func (m *matcher) proposalFor(key string) (store.Proposal, bool) {
	p, ok := m.proposals[key]
	return p, ok
}

// previousProposal returns the id of the proposal a new one supersedes.
func (m *matcher) previousProposal(key, newID string) (string, bool) {
	p, ok := m.proposals[key]
	if !ok || p.ProposalID == newID {
		return "", false
	}
	return p.ProposalID, true
}

// noteProposal records a proposal the run just wrote, so a second invoice
// version in the same run supersedes it rather than duplicating it.
func (m *matcher) noteProposal(p store.Proposal) { m.proposals[p.InvoiceKey] = p }

// openExceptions returns the codes of an invoice's open exceptions, sorted.
func (m *matcher) openExceptions(key string) []string {
	codes := append([]string{}, m.open[key]...)
	sort.Strings(codes)
	return codes
}

// clearException forgets a resolved exception.
func (m *matcher) clearException(key, code string) {
	out := m.open[key][:0]
	for _, c := range m.open[key] {
		if c != code {
			out = append(out, c)
		}
	}
	m.open[key] = out
}

// locate returns the file or chunk an invoice was delivered in, for the
// exception's source locator.
func (m *matcher) locate(b *batchState, key string) string {
	for _, r := range b.Records {
		if r.NaturalKey == key && r.Dataset == model.DatasetInvoice.String() {
			return r.File
		}
	}
	return ""
}

// lineOf returns the line or ordinal an invoice was delivered at, as a string,
// for the exception's source locator.
func (m *matcher) lineOf(b *batchState, key string) string {
	for _, r := range b.Records {
		if r.NaturalKey == key && r.Dataset == model.DatasetInvoice.String() {
			if r.Line > 0 {
				return fmt.Sprintf("%d", r.Line)
			}
			return fmt.Sprintf("%d", r.Ordinal)
		}
	}
	return ""
}
