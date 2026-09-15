package miniblp

import (
	"sort"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// seedRecord writes one master record straight into the store, the way
// POST /admin/v1/seed writes the pre-loaded tables. It is how a matching test
// builds its landscape without spending the connector's request budget on it.
func (h *harness) seedRecord(dataset, key string, payload any) {
	h.t.Helper()
	if _, err := h.srv.Store().Apply(store.Revision{
		Dataset: dataset, Key: key, Payload: payload,
		Provenance: store.Provenance{Channel: store.ChannelInternal, Profile: "pre-seeded",
			SourceSystem: "other-integration", RunID: "run_test"},
	}); err != nil {
		h.t.Fatalf("seed %s/%s: %v", dataset, key, err)
	}
}

// deliverInvoices writes invoices into the store and runs the matching engine
// over them, exactly as a commit or a scan would.
//
// The invoices are written directly rather than delivered through the canonical
// profile, because a legacy invoice carries the sender's own units of measure -
// PAL, TON - which the canonical record shape does not accept. That is what the
// legacy profile exists for, and the matching engine has to see those units or
// the two EXC_UOM_ verdicts could never be reached.
func (h *harness) deliverInvoices(invoices ...model.APInvoice) *batchState {
	h.t.Helper()
	b := newBatchState(manifest{BatchID: "fixture", RunID: "run_test"}, "sha", ChannelREST, 0)
	b.manifest.normalize()
	for _, inv := range invoices {
		key := inv.Key()
		out, err := h.srv.Store().Apply(store.Revision{
			Dataset: model.DatasetInvoice.String(), Key: key, Payload: inv,
			Provenance: store.Provenance{Channel: ChannelREST, Profile: ProfileKredExp,
				RunID: "run_test", BatchID: "fixture"},
		})
		if err != nil {
			h.t.Fatalf("apply invoice %q: %v", key, err)
		}
		if out.Result == store.ResultApplied {
			b.changedInvoices[key] = true
			b.invoiceVersions[key] = out.Version
		}
	}
	h.srv.mu.Lock()
	err := h.srv.matchBatch(b)
	h.srv.mu.Unlock()
	if err != nil {
		h.t.Fatalf("match: %v", err)
	}
	return b
}

// openPairs returns the twin's open exception queue as "subject|code" pairs,
// sorted, which is the shape grading compares as a symmetric difference.
func (h *harness) openPairs() []string {
	h.t.Helper()
	list, err := h.srv.Store().Exceptions(store.ExceptionOpen)
	if err != nil {
		h.t.Fatalf("exceptions: %v", err)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.SubjectKey+"|"+e.Code)
	}
	sort.Strings(out)
	return out
}

// invoiceFixture builds one invoice of the matching fixture.
func invoiceFixture(supplier, number, currency, gross, vat string, opts ...func(*model.APInvoice)) model.APInvoice {
	inv := model.APInvoice{
		SupplierNumber:        supplier,
		SupplierInvoiceNumber: number,
		CompanyCode:           "CH10",
		DocumentType:          model.DocumentTypeInvoice,
		DocumentDate:          "2026-03-16",
		Currency:              currency,
		GrossAmount:           model.MustDecimal(gross),
		VATAmount:             model.MustDecimal(vat),
		CostCenter:            "CC-2200",
		Lines: []model.APInvoiceLine{{
			LineNo:     "00010",
			GLAccount:  "400000",
			Quantity:   model.MustDecimal("1.000"),
			UoM:        "EA",
			UnitPrice:  model.MustDecimal(gross),
			LineAmount: model.MustDecimal(gross),
		}},
	}
	// The line amount plus the VAT has to be the gross by default, so a test
	// that is not about the totals rule does not trip over it.
	if lineTotal, err := inv.GrossAmount.Sub(inv.VATAmount); err == nil {
		inv.Lines[0].LineAmount = lineTotal
		inv.Lines[0].UnitPrice = lineTotal
	}
	for _, opt := range opts {
		opt(&inv)
	}
	return inv
}

// withPO sets the purchase order reference.
func withPO(ref string) func(*model.APInvoice) {
	return func(i *model.APInvoice) { i.PONumber = ref }
}

// withCostCenter sets the document cost center.
func withCostCenter(code string) func(*model.APInvoice) {
	return func(i *model.APInvoice) { i.CostCenter = code }
}

// withDate sets the document date.
func withDate(date string) func(*model.APInvoice) {
	return func(i *model.APInvoice) { i.DocumentDate = date }
}

// withLine replaces the single line.
func withLine(lineNo, uom, quantity, amount string) func(*model.APInvoice) {
	return func(i *model.APInvoice) {
		i.Lines = []model.APInvoiceLine{{
			LineNo: lineNo, GLAccount: "400000",
			Quantity: model.MustDecimal(quantity), UoM: uom,
			UnitPrice: model.MustDecimal(amount), LineAmount: model.MustDecimal(amount),
		}}
	}
}

// withLineAmount sets the line amount without touching anything else, for the
// totals rule.
func withLineAmount(amount string) func(*model.APInvoice) {
	return func(i *model.APInvoice) {
		i.Lines[0].LineAmount = model.MustDecimal(amount)
		i.Lines[0].UnitPrice = model.MustDecimal(amount)
	}
}

// withCreditNote makes the invoice a credit note with an explicitly negative
// amount, which is the only correct way to carry one: the sign is the sender's
// and is never derived from the document type.
func withCreditNote() func(*model.APInvoice) {
	return func(i *model.APInvoice) { i.DocumentType = model.DocumentTypeCreditNote }
}

// seedMatchingLandscape loads the master data of the matching fixture.
func seedMatchingLandscape(h *harness) {
	h.seedRecord("supplier", model.SupplierKey("0000000417"), model.Supplier{
		SupplierNumber: "0000000417", Name: "Steinbach Industrie AG", Country: "CH",
		Currency: "CHF", PaymentTermsDays: 30,
	})
	h.seedRecord("supplier", model.SupplierKey("0000000418"), model.Supplier{
		SupplierNumber: "0000000418", Name: "Meier Transport", Country: "CH", Currency: "CHF",
	})
	h.seedRecord("supplier", model.SupplierKey("0000000499"), model.Supplier{
		SupplierNumber: "0000000499", Name: "Blocked AG", Country: "CH", Currency: "CHF",
		Blocked: true,
	})
	h.seedRecord("cost_center", model.CostCenterKey("CC-2200"), model.CostCenter{
		Code: "CC-2200", Name: "Werk Wil", CompanyCode: "CH10", ValidFrom: "2026-01-01",
	})
	h.seedRecord("cost_center", model.CostCenterKey("CC-EXPIRED"), model.CostCenter{
		Code: "CC-EXPIRED", Name: "Alt", CompanyCode: "CH10",
		ValidFrom: "2026-01-01", ValidTo: "2026-02-01",
	})

	// Purchase orders in the three delivered spellings of BUILD-SPEC 16.1.
	h.seedRecord("purchase_order", model.POKey("PO-4500001234"), model.PurchaseOrder{
		PONumber: "PO-4500001234", SupplierNumber: "0000000417", CompanyCode: "CH10",
		Currency: "CHF", Status: model.POStatusOpen, OrderDate: "2026-02-01", CostCenter: "CC-2200",
	})
	h.seedRecord("purchase_order", model.POKey("4500009999"), model.PurchaseOrder{
		PONumber: "4500009999", SupplierNumber: "0000000418", CompanyCode: "CH10",
		Currency: "CHF", Status: model.POStatusOpen, OrderDate: "2026-02-01", CostCenter: "CC-2200",
	})
	h.seedRecord("purchase_order", model.POKey("4500000001"), model.PurchaseOrder{
		PONumber: "4500000001", SupplierNumber: "0000000417", CompanyCode: "CH10",
		Currency: "CHF", Status: model.POStatusClosed, OrderDate: "2026-02-01", CostCenter: "CC-2200",
	})
	h.seedRecord("purchase_order_line", model.POLineKey("PO-4500001234", "00010"),
		model.PurchaseOrderLine{PONumber: "PO-4500001234", LineNo: "00010", Material: "MAT-STD",
			Quantity: model.MustDecimal("10.000"), UoM: "EA", UnitPrice: model.MustDecimal("10.00"),
			Currency: "CHF"})
	h.seedRecord("purchase_order_line", model.POLineKey("PO-4500001234", "00020"),
		model.PurchaseOrderLine{PONumber: "PO-4500001234", LineNo: "00020", Material: "MAT-1000-3",
			Quantity: model.MustDecimal("2.000"), UoM: "CTN", UnitPrice: model.MustDecimal("18.5000"),
			Currency: "CHF"})

	// The unit-of-measure conversion table, as POST /admin/v1/seed loads it.
	for _, c := range []UoMConversion{
		{AltUoM: "EA", Numerator: 1, Denominator: 1, BaseUoM: "EA"},
		{AltUoM: "CTN", Numerator: 12, Denominator: 1, BaseUoM: "EA"},
		{AltUoM: "TON", Numerator: 1000, Denominator: 1, BaseUoM: "KG"},
		{AltUoM: "KG", Numerator: 1, Denominator: 1, BaseUoM: "KG"},
		{Material: "MAT-1000-3", AltUoM: "CTN", Numerator: 1000, Denominator: 3, BaseUoM: "EA"},
	} {
		h.seedRecord(DatasetUoMConversion, c.Key(), c)
	}

	// Exchange rates. Every trap of BUILD-SPEC 16.1 and 7.3 is here: the
	// daylight saving boundary, the JPY per-100 factor, a superseded row, a
	// DELETED-only currency and a monthly average that must never be used.
	fx := []model.FxRate{
		{Base: "EUR", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-28",
			ValidTo: "2026-03-29", Rate: model.MustDecimal("0.930000"), RateFactor: 1,
			Sequence: 1, Status: model.FxStatusActive},
		{Base: "EUR", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-29",
			ValidTo: "2026-03-30", Rate: model.MustDecimal("0.931000"), RateFactor: 1,
			Sequence: 1, Status: model.FxStatusActive},
		{Base: "GBP", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-01",
			ValidTo: "", Rate: model.MustDecimal("1.082250"), RateFactor: 1,
			Sequence: 1, Status: model.FxStatusActive},
		{Base: "JPY", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-16",
			ValidTo: "2026-03-17", Rate: model.MustDecimal("0.556300"), RateFactor: 100,
			Sequence: 1, Status: model.FxStatusActive},
		{Base: "USD", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-16",
			ValidTo: "2026-03-17", Rate: model.MustDecimal("9.000000"), RateFactor: 1,
			Sequence: 1, Status: model.FxStatusActive},
		{Base: "USD", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-16",
			ValidTo: "2026-03-17", Rate: model.MustDecimal("2.000000"), RateFactor: 1,
			Sequence: 4, Status: model.FxStatusActive},
		{Base: "DKK", Quote: "CHF", RateType: model.RateTypeDaily, ValidFrom: "2026-03-16",
			ValidTo: "2026-03-17", Rate: model.MustDecimal("0.140000"), RateFactor: 1,
			Sequence: 1, Status: model.FxStatusDeleted},
		{Base: "SEK", Quote: "CHF", RateType: model.RateTypeMonthlyAvg, ValidFrom: "2026-03-01",
			ValidTo: "2026-04-01", Rate: model.MustDecimal("0.095000"), RateFactor: 1,
			Sequence: 1, Status: model.FxStatusActive},
	}
	for _, r := range fx {
		h.seedRecord("fx_rate", r.Key(), r)
	}
}

// TestMatchingExceptionSet is the matching engine's contract test: one hand-built
// landscape, one invoice per published verdict, and the open exception queue
// compared as an exact set. A missing pair and a spurious pair both fail, which
// is how grading reads the queue, so over-rejecting costs as much as
// under-rejecting.
func TestMatchingExceptionSet(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)

	invoices := []model.APInvoice{
		// Matches: no exception, one proposal each.
		invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35"),
		invoiceFixture("0000000417", "0004712", "GBP", "100.00", "0.00"),
		invoiceFixture("0000000417", "0004713", "JPY", "250000", "0"),
		invoiceFixture("0000000417", "0004714", "EUR", "1345.63", "0.00", withDate("2026-03-29")),
		invoiceFixture("0000000417", "0004715", "USD", "10.00", "0.00"),
		invoiceFixture("0000000417", "0004716", "GBP", "-100.00", "0.00", withCreditNote()),
		invoiceFixture("0000000417", "0004717", "CHF", "120.00", "0.00",
			withPO("PO-4500001234/00010"), withLine("00010", "CTN", "10.000", "120.00")),
		// One invoice per blocking verdict.
		invoiceFixture("0000000900", "0004801", "CHF", "10.00", "0.00"),
		invoiceFixture("0000000499", "0004802", "CHF", "10.00", "0.00"),
		invoiceFixture("0000000417", "0004803", "CHF", "10.00", "0.00", withPO("4500007777")),
		invoiceFixture("0000000417", "0004804", "CHF", "10.00", "0.00", withPO("4500009999")),
		invoiceFixture("0000000417", "0004805", "CHF", "10.00", "0.00", withPO("4500000001")),
		invoiceFixture("0000000417", "0004806", "CHF", "10.00", "0.00", withLineAmount("9.00")),
		invoiceFixture("0000000417", "0004807", "CHF", "10.00", "0.00", withCostCenter("CC-9999")),
		invoiceFixture("0000000417", "0004808", "CHF", "10.00", "0.00", withCostCenter("CC-EXPIRED")),
		invoiceFixture("0000000417", "0004809", "SEK", "10.00", "0.00"),
		invoiceFixture("0000000417", "0004810", "DKK", "10.00", "0.00"),
		invoiceFixture("0000000417", "0004811", "CHF", "10.00", "0.00",
			withLine("00010", "PAL", "1.000", "10.00")),
		invoiceFixture("0000000417", "0004812", "CHF", "10.00", "0.00",
			withPO("PO-4500001234/00020"), withLine("00020", "CTN", "1.000", "10.00")),
	}
	b := h.deliverInvoices(invoices...)

	want := []string{
		key("0000000417", "0004803") + "|" + ExcPONotFound,
		key("0000000417", "0004804") + "|" + ExcPOSupplierMismatch,
		key("0000000417", "0004805") + "|" + ExcPOClosed,
		key("0000000417", "0004806") + "|" + ExcTotalsMismatch,
		key("0000000417", "0004807") + "|" + ExcCostCenterUnknown,
		key("0000000417", "0004808") + "|" + ExcCostCenterUnknown,
		key("0000000417", "0004809") + "|" + ExcFxRateMissing,
		key("0000000417", "0004810") + "|" + ExcFxRateMissing,
		key("0000000417", "0004811") + "|" + ExcUoMUnmappable,
		key("0000000417", "0004812") + "|" + ExcUoMUnconvertible,
		key("0000000499", "0004802") + "|" + ExcSupplierBlocked,
		key("0000000900", "0004801") + "|" + ExcSupplierUnknown,
	}
	sort.Strings(want)
	got := h.openPairs()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("exception set mismatch:\n got:\n%s\n want:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(b.Proposals) != 7 {
		t.Fatalf("proposals = %d (%v), want 7", len(b.Proposals), b.Proposals)
	}
}

// TestMatchingAmounts pins the four graded conversions of BUILD-SPEC 16.1 plus
// the supersession rule: commercial rounding, the per-unit factor, the
// Europe/Zurich calendar date across the daylight saving switch, and the highest
// sequence winning.
func TestMatchingAmounts(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	h.deliverInvoices(
		invoiceFixture("0000000417", "0004712", "GBP", "100.00", "0.00"),
		invoiceFixture("0000000417", "0004713", "JPY", "250000", "0"),
		invoiceFixture("0000000417", "0004714", "EUR", "1345.63", "0.00", withDate("2026-03-29")),
		invoiceFixture("0000000417", "0004716", "GBP", "-100.00", "0.00", withCreditNote()),
		invoiceFixture("0000000417", "0004715", "USD", "10.00", "0.00"),
		invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35"),
	)

	cases := []struct {
		invoice   string
		wantMinor int64
		wantRate  string
		why       string
	}{
		{"0004712", 10823, "1.082250", "half away from zero, not banker's rounding"},
		{"0004713", 139075, "0.556300", "the per-unit factor of 100 is honored"},
		{"0004714", 125278, "0.931000", "the Europe/Zurich calendar date picks the row of the day itself"},
		{"0004716", -10823, "1.082250", "a credit note rounds away from zero as well"},
		{"0004715", 2000, "2.000000", "the highest sequence wins over document order"},
		{"0004711", 125000, "", "a CHF invoice is not converted at all"},
	}
	for _, tc := range cases {
		p := h.proposalForInvoice(key("0000000417", tc.invoice))
		if p.Amount.Currency != "CHF" {
			t.Errorf("invoice %s: currency = %q, want CHF", tc.invoice, p.Amount.Currency)
		}
		if p.Amount.AmountMinor != tc.wantMinor {
			t.Errorf("invoice %s: amount_minor = %d, want %d (%s)",
				tc.invoice, p.Amount.AmountMinor, tc.wantMinor, tc.why)
		}
		if tc.wantRate == "" {
			if p.FxRateFactor != 0 {
				t.Errorf("invoice %s: fx factor = %d, want 0", tc.invoice, p.FxRateFactor)
			}
			continue
		}
		if got := p.FxRateUsed.String(); got != tc.wantRate {
			t.Errorf("invoice %s: rate = %q, want %q", tc.invoice, got, tc.wantRate)
		}
	}
}

// TestAmendedInvoiceRaisesOneBlockingException asserts the rule of BUILD-SPEC
// 17.5: a supplier invoice number re-delivered with different content is one
// blocking exception carrying both gross amounts, the delta and the ERP document
// number already posted - and never a second proposal.
func TestAmendedInvoiceRaisesOneBlockingException(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	invoiceKey := key("0000000417", "0004711")

	first := h.deliverInvoices(invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35"))
	if len(first.Proposals) != 1 {
		t.Fatalf("first delivery emitted %v, want one proposal", first.Proposals)
	}
	proposalID := first.Proposals[0]

	// The posting is acknowledged, so the exception can name the document the
	// customer's ledger already holds.
	if _, err := h.srv.Store().AckProposal(proposalID, store.ProposalAck{
		Status: store.AckPosted, ExternalDocumentNumber: "AP-2026-0004311",
		IdempotencyKey: "blp:acme-ch:" + proposalID, RunID: "run_test",
	}, store.Provenance{Channel: store.ChannelInternal}); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// The same invoice number, a different amount.
	second := h.deliverInvoices(invoiceFixture("0000000417", "0004711", "CHF", "1300.00", "89.35"))
	if len(second.Proposals) != 0 {
		t.Fatalf("the amended invoice emitted %v, want no proposal", second.Proposals)
	}
	if got := h.openPairs(); len(got) != 1 || got[0] != invoiceKey+"|"+ExcDuplicateInvoiceAmended {
		t.Fatalf("open queue = %v, want one %s", got, ExcDuplicateInvoiceAmended)
	}
	e, ok, err := h.srv.Store().GetException(invoiceKey, ExcDuplicateInvoiceAmended)
	if err != nil || !ok {
		t.Fatalf("exception: ok %v err %v", ok, err)
	}
	if e.Details["gross_amount_previous"] != "1250.00" || e.Details["gross_amount_new"] != "1300.00" {
		t.Fatalf("details = %+v, want both gross amounts", e.Details)
	}
	if e.Details["gross_amount_delta"] != "50.00" {
		t.Fatalf("delta = %q, want 50.00", e.Details["gross_amount_delta"])
	}
	if e.Details["erp_document_number"] != "AP-2026-0004311" {
		t.Fatalf("erp document number = %q", e.Details["erp_document_number"])
	}
	if e.Stage != store.StageMatch || e.State != store.ExceptionOpen {
		t.Fatalf("exception = %+v", e)
	}
}

// TestIdenticalRedeliveryIsNotAnAmendment asserts the other half of the same
// rule: a byte-identical re-delivery dedupes silently and raises nothing.
func TestIdenticalRedeliveryIsNotAnAmendment(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	inv := invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35")
	h.deliverInvoices(inv)
	second := h.deliverInvoices(inv)
	if len(second.Proposals) != 0 {
		t.Fatalf("an identical re-delivery emitted %v", second.Proposals)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("open queue = %v, want nothing", got)
	}
}

// TestRematchDoesNotDuplicateProposals asserts that running the engine again
// over an unchanged invoice produces no second proposal, which is what makes a
// resumed run safe.
func TestRematchDoesNotDuplicateProposals(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	inv := invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35")
	h.deliverInvoices(inv)

	b := newBatchState(manifest{BatchID: "rematch", RunID: "run_test"}, "sha", ChannelREST, 0)
	b.manifest.normalize()
	b.changedInvoices[inv.Key()] = true
	h.srv.mu.Lock()
	err := h.srv.matchBatch(b)
	h.srv.mu.Unlock()
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if len(b.Proposals) != 0 {
		t.Fatalf("rematch emitted %v", b.Proposals)
	}
	counts, err := h.srv.Store().CountProposalsByStatus()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[store.ProposalPending] != 1 {
		t.Fatalf("pending proposals = %d, want 1", counts[store.ProposalPending])
	}
}

// TestMasterDataFixResolvesTheException asserts that an invoice which failed for
// a missing cost center stops appearing in the open queue once the cost center
// arrives and the invoice is matched again.
func TestMasterDataFixResolvesTheException(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	inv := invoiceFixture("0000000417", "0004807", "CHF", "10.00", "0.00", withCostCenter("CC-9999"))
	h.deliverInvoices(inv)
	if got := h.openPairs(); len(got) != 1 {
		t.Fatalf("open queue = %v, want one entry", got)
	}
	h.seedRecord("cost_center", model.CostCenterKey("CC-9999"), model.CostCenter{
		Code: "CC-9999", Name: "Neu", CompanyCode: "CH10", ValidFrom: "2026-01-01",
	})

	b := newBatchState(manifest{BatchID: "refix", RunID: "run_test"}, "sha", ChannelREST, 0)
	b.manifest.normalize()
	b.changedInvoices[inv.Key()] = true
	h.srv.mu.Lock()
	err := h.srv.matchBatch(b)
	h.srv.mu.Unlock()
	if err != nil {
		t.Fatalf("rematch: %v", err)
	}
	if len(b.Proposals) != 1 {
		t.Fatalf("proposals = %v, want one", b.Proposals)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("open queue = %v, want the exception resolved", got)
	}
}

// TestWarningsTravelOntoTheProposal asserts that a documented ambiguity reaches
// the proposal and never the open exception queue.
func TestWarningsTravelOntoTheProposal(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	inv := invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35")
	inv.DiscountRaw = "2.000"

	b := newBatchState(manifest{BatchID: "warn", RunID: "run_test"}, "sha", ChannelREST, 0)
	b.manifest.normalize()
	out, err := h.srv.Store().Apply(store.Revision{
		Dataset: model.DatasetInvoice.String(), Key: inv.Key(), Payload: inv,
		Provenance: store.Provenance{Channel: ChannelREST, Profile: ProfileKredExp},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	b.changedInvoices[inv.Key()] = true
	b.invoiceVersions[inv.Key()] = out.Version
	b.docWarnings[inv.Key()] = []string{WarnAmbiguousFieldSemantics}

	h.srv.mu.Lock()
	err = h.srv.matchBatch(b)
	h.srv.mu.Unlock()
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(b.Proposals) != 1 {
		t.Fatalf("proposals = %v", b.Proposals)
	}
	p, ok, err := h.srv.Store().GetProposal(b.Proposals[0])
	if err != nil || !ok {
		t.Fatalf("proposal: ok %v err %v", ok, err)
	}
	if !contains(p.Warnings, WarnAmbiguousFieldSemantics) {
		t.Fatalf("proposal warnings = %v, want the ambiguity", p.Warnings)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("a warning entered the open exception queue: %v", got)
	}
}

// TestExceptionCodesArePublished asserts that the twin's closed set is exactly
// the set the seeded golden expectations are drawn from. A code the generator can
// expect and the twin cannot raise, or the reverse, would make the graded
// symmetric difference undecidable.
func TestExceptionCodesArePublished(t *testing.T) {
	got := ExceptionCodes()
	want := seedExceptionCodes()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("published exception codes differ:\n twin: %v\n seed: %v", got, want)
	}
}

// key is model.InvoiceKey, spelled short for the fixtures.
func key(supplier, number string) string { return model.InvoiceKey(supplier, number) }

// proposalForInvoice returns the current proposal of an invoice.
func (h *harness) proposalForInvoice(invoiceKey string) store.Proposal {
	h.t.Helper()
	var found store.Proposal
	ok := false
	if err := h.srv.Store().ScanProposals(func(p store.Proposal) bool {
		if p.InvoiceKey == invoiceKey && p.Status != store.ProposalSuperseded {
			found, ok = p, true
			return false
		}
		return true
	}); err != nil {
		h.t.Fatalf("scan proposals: %v", err)
	}
	if !ok {
		h.t.Fatalf("no proposal for invoice %q", invoiceKey)
	}
	return found
}

// TestCompanyCodeWithoutAnFxTableCannotConvert asserts the rule behind
// HasFxTable: a foreign currency invoice of a company code that has no exchange
// rate table is EXC_FX_RATE_MISSING even when a rate for its currency and date is
// on file, because that rate belongs to another subsidiary. Converting with it
// would post an amount nobody quoted.
func TestCompanyCodeWithoutAnFxTableCannotConvert(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	h.seedRecord("cost_center", model.CostCenterKey("CC-20"), model.CostCenter{
		Code: "CC-20", Name: "Werk Lugano", CompanyCode: model.CompanyCodeCH20,
		ValidFrom: "2026-01-01",
	})

	ch20 := invoiceFixture("0000000417", "0004720", "GBP", "100.00", "0.00",
		withCostCenter("CC-20"))
	ch20.CompanyCode = model.CompanyCodeCH20
	ch10 := invoiceFixture("0000000417", "0004721", "GBP", "100.00", "0.00")

	h.deliverInvoices(ch20, ch10)

	if got := h.openPairs(); len(got) != 1 ||
		got[0] != key("0000000417", "0004720")+"|"+ExcFxRateMissing {
		t.Fatalf("open queue = %v, want the CH20 invoice unconverted", got)
	}
	// The CH10 invoice with the same currency and date converts with the same
	// rate, so the verdict is about the company code and not about the rate.
	p := h.proposalForInvoice(key("0000000417", "0004721"))
	if p.Amount.AmountMinor != 10823 {
		t.Fatalf("CH10 amount = %d, want 10823", p.Amount.AmountMinor)
	}
	if HasFxTable(model.CompanyCodeCH20) || !HasFxTable(model.CompanyCodeCH10) {
		t.Fatal("HasFxTable disagrees with the landscape it documents")
	}
}

// TestCH20InCHFStillPosts asserts that the same company code is fine in its own
// currency: the rule is about conversion, not about the subsidiary.
func TestCH20InCHFStillPosts(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	h.seedRecord("cost_center", model.CostCenterKey("CC-20"), model.CostCenter{
		Code: "CC-20", Name: "Werk Lugano", CompanyCode: model.CompanyCodeCH20,
		ValidFrom: "2026-01-01",
	})
	inv := invoiceFixture("0000000417", "0004722", "CHF", "50.00", "0.00", withCostCenter("CC-20"))
	inv.CompanyCode = model.CompanyCodeCH20
	b := h.deliverInvoices(inv)
	if len(b.Proposals) != 1 {
		t.Fatalf("proposals = %v, want one", b.Proposals)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("open queue = %v", got)
	}
}
