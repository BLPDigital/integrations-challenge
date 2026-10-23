package seed

import (
	"fmt"
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// deltaFxStart is the first day the delta scenario's new exchange rate rows
// cover. It sits after the base rolling window, so the new rows extend the table
// forward instead of competing with an existing interval.
const deltaFxStart = FxWindowEnd

// deltaFxDaysPerPair is how many new daily rows each affected currency gains.
const deltaFxDaysPerPair = 12

// deltaFxPairs are the currencies the delta issues new rows for. GBP is absent
// on purpose: its open-ended row already covers every later date, and adding a
// second row over the same days would make two rows valid at once, which is an
// ambiguity the landscape does not have.
var deltaFxPairs = []string{"EUR", "USD", "JPY", "SEK", "DKK"}

// applyDelta turns a cold-load dataset into a delta dataset and returns the
// invoices of the second delivery, in file order.
//
// It runs after the whole base build, so the base phases consumed exactly the
// draws the base scenario consumes and the base delivery stays byte-identical.
// The master data mutations are chosen to be observable but harmless: a name, a
// VAT number, an order date and a line description. None of them is an input to
// the matching engine, so the delta exercises change sequences, watermarks and
// content-hash change detection without moving a single expected exception.
func (b *builder) applyDelta(base []model.APInvoice, classes []exceptionClass) ([]model.APInvoice, error) {
	if len(classes) != len(base) {
		return nil, fmt.Errorf("seed: %d invoices but %d exception classes", len(base), len(classes))
	}
	b.d.DeltaFromChangeSeq = b.d.MaxChangeSeq

	changed := make([]ChangeSeqEntry, 0,
		b.spec.ChangedSuppliers+b.spec.ChangedPurchaseOrders+b.spec.ChangedPurchaseOrderLines)

	supIdx, err := pickIndexes(b.p, len(b.d.Suppliers), b.spec.ChangedSuppliers)
	if err != nil {
		return nil, fmt.Errorf("seed: changed suppliers: %w", err)
	}
	for _, i := range supIdx {
		b.d.Suppliers[i].Name = b.d.Suppliers[i].Name + " – Zweigniederlassung"
		b.d.Suppliers[i].VATNumber = fmt.Sprintf("CHE-%03d.%03d.%03d MWST",
			b.p.between(100, 999), b.p.between(100, 999), b.p.between(100, 999))
		changed = append(changed, ChangeSeqEntry{
			Dataset: model.DatasetSupplier.String(), Key: b.d.Suppliers[i].Key()})
	}

	poIdx, err := pickIndexes(b.p, len(b.d.PurchaseOrders), b.spec.ChangedPurchaseOrders)
	if err != nil {
		return nil, fmt.Errorf("seed: changed purchase orders: %w", err)
	}
	for _, i := range poIdx {
		next, err := addDays(b.d.PurchaseOrders[i].OrderDate, 1)
		if err != nil {
			return nil, err
		}
		b.d.PurchaseOrders[i].OrderDate = next
		changed = append(changed, ChangeSeqEntry{
			Dataset: model.DatasetPurchaseOrder.String(), Key: b.d.PurchaseOrders[i].Key()})
	}

	lineIdx, err := pickIndexes(b.p, len(b.d.PurchaseOrderLines), b.spec.ChangedPurchaseOrderLines)
	if err != nil {
		return nil, fmt.Errorf("seed: changed purchase order lines: %w", err)
	}
	for _, i := range lineIdx {
		b.d.PurchaseOrderLines[i].Description = b.d.PurchaseOrderLines[i].Description + " (rev. 2)"
		changed = append(changed, ChangeSeqEntry{
			Dataset: model.DatasetPurchaseOrderLine.String(), Key: b.d.PurchaseOrderLines[i].Key()})
	}

	if err := b.reassignDeltaChangeSeqs(changed); err != nil {
		return nil, err
	}
	if err := b.appendDeltaFxRows(); err != nil {
		return nil, err
	}

	// The new invoices of the second delivery. Their document numbers start
	// above every number the base delivery used.
	newInvoices, _, err := b.buildInvoices(b.spec.DeltaInvoices, b.spec.DeltaInvoiceLines, b.spec.Invoices, false)
	if err != nil {
		return nil, err
	}

	// Re-deliveries are taken from the clean CH10 invoices of the base file: an
	// exception invoice re-sent would confuse the two mechanisms under test.
	var pool []int
	for i := range base {
		if classes[i] == clsClean && base[i].CompanyCode == model.CompanyCodeCH10 &&
			base[i].DocumentType == model.DocumentTypeInvoice {
			pool = append(pool, i)
		}
	}
	need := b.spec.ResentInvoices + b.spec.AmendedInvoices
	if len(pool) < need {
		return nil, fmt.Errorf("seed: only %d clean CH10 invoices available, need %d for the re-delivery block",
			len(pool), need)
	}
	// Evenly strided so the re-delivered invoices are spread over the base file.
	chosen := make([]int, 0, need)
	for k := 0; k < need; k++ {
		chosen = append(chosen, pool[k*len(pool)/need])
	}
	out := make([]model.APInvoice, 0, len(newInvoices)+need)
	out = append(out, newInvoices...)
	for _, i := range chosen[:b.spec.ResentInvoices] {
		// Byte-identical re-delivery: it must dedupe by content and must not
		// produce a second posting or a second outbox emission.
		out = append(out, base[i])
	}
	for _, i := range chosen[b.spec.ResentInvoices:] {
		amended, err := b.amendInvoice(base[i])
		if err != nil {
			return nil, err
		}
		out = append(out, amended)
	}
	return out, nil
}

// amendInvoice returns a genuinely amended copy of an invoice: one more unit on
// the first line, with the line amount, the VAT and the gross total recomputed,
// so the header total rule still holds and the only difference is the amount.
//
// Re-delivering it is not a second posting. It must raise exactly one blocking
// EXC_DUPLICATE_INVOICE_AMENDED, because auto-posting an amended invoice a second
// time is a duplicate in a customer's ledger and there is no reversal endpoint.
func (b *builder) amendInvoice(inv model.APInvoice) (model.APInvoice, error) {
	if len(inv.Lines) == 0 {
		return model.APInvoice{}, fmt.Errorf("seed: cannot amend invoice %s/%s: no lines",
			inv.SupplierNumber, inv.SupplierInvoiceNumber)
	}
	out := inv
	out.Lines = make([]model.APInvoiceLine, len(inv.Lines))
	copy(out.Lines, inv.Lines)
	qty, err := out.Lines[0].Quantity.Add(dec(1000, 3))
	if err != nil {
		return model.APInvoice{}, err
	}
	amount, err := qty.MulRate(out.Lines[0].UnitPrice, 1, 2)
	if err != nil {
		return model.APInvoice{}, err
	}
	out.Lines[0].Quantity = qty
	out.Lines[0].LineAmount = amount
	if err := b.retotalInvoice(&out, clsClean, false); err != nil {
		return model.APInvoice{}, err
	}
	if model.ContentHash(out) == model.ContentHash(inv) {
		return model.APInvoice{}, fmt.Errorf("seed: amendment of %s/%s changed nothing",
			inv.SupplierNumber, inv.SupplierInvoiceNumber)
	}
	return out, nil
}

// appendDeltaFxRows extends the rate table forward by new daily rows. They are
// appended after the existing rows, so document order stays what it was and the
// new rows are simply the freshest tail.
func (b *builder) appendDeltaFxRows() error {
	want := b.spec.NewFxRows
	if want == 0 {
		return nil
	}
	if want != len(deltaFxPairs)*deltaFxDaysPerPair {
		return fmt.Errorf("seed: scenario %s wants %d new FX rows, the delta plan yields %d",
			b.spec.ID, want, len(deltaFxPairs)*deltaFxDaysPerPair)
	}
	for _, base := range deltaFxPairs {
		var pair fxPair
		found := false
		for _, p := range fxPairs {
			if p.base == base {
				pair, found = p, true
				break
			}
		}
		if !found {
			return fmt.Errorf("seed: delta names unknown currency %q", base)
		}
		for day := 0; day < deltaFxDaysPerPair; day++ {
			from, err := addDays(deltaFxStart, day)
			if err != nil {
				return err
			}
			to, err := addDays(deltaFxStart, day+1)
			if err != nil {
				return err
			}
			row, err := b.fxDailyRow(pair, from, to, false)
			if err != nil {
				return err
			}
			b.d.FxRows = append(b.d.FxRows, row)
		}
	}
	return nil
}

// reassignDeltaChangeSeqs moves the delta records above the base watermark, so
// ?changed_since with the base watermark returns exactly the delta and nothing
// else. The sequences stay strictly increasing and unique.
func (b *builder) reassignDeltaChangeSeqs(changed []ChangeSeqEntry) error {
	if len(changed) == 0 {
		return nil
	}
	keys := make(map[string]bool, len(changed))
	for _, e := range changed {
		keys[e.Dataset+"\x1f"+e.Key] = true
	}
	kept := make([]ChangeSeqEntry, 0, len(b.d.ChangeSeqs))
	for _, e := range b.d.ChangeSeqs {
		if !keys[e.Dataset+"\x1f"+e.Key] {
			kept = append(kept, e)
		}
	}
	if len(kept)+len(changed) != len(b.d.ChangeSeqs) {
		return fmt.Errorf("seed: delta names %d records but %d were removed from the base assignment",
			len(changed), len(b.d.ChangeSeqs)-len(kept))
	}
	// The base records keep the sequences they had, so a watermark carried over
	// from the base scenario still means the same thing.
	next := b.d.DeltaFromChangeSeq + 1

	// The dataset whose own base maximum IS the global base maximum goes first,
	// so its first changed record sits at exactly (its watermark) + 1.
	//
	// That one record is what makes the watermark rule testable. The ERP filters
	// change_seq > changed_since, so sending the persisted watermark is right and
	// sending watermark + 1 loses exactly the record at watermark + 1. Under
	// alphabetical ordering that record belonged to whichever dataset sorts first
	// rather than to the one that owns the boundary, so it was always a sequence
	// no dataset's watermark was adjacent to, an off-by-one cost nothing, and the
	// mutation gate reported the trap as unobservable. TestExclusiveWatermark
	// LosesExactlyOneRecord pins it.
	first := b.datasetHoldingBaseMax()
	sort.Slice(changed, func(i, j int) bool {
		if changed[i].Dataset != changed[j].Dataset {
			if changed[i].Dataset == first {
				return true
			}
			if changed[j].Dataset == first {
				return false
			}
			return changed[i].Dataset < changed[j].Dataset
		}
		return changed[i].Key < changed[j].Key
	})
	for i := range changed {
		changed[i].ChangeSeq = next + int64(i)
	}
	b.d.ChangeSeqs = append(kept, changed...)
	b.d.MaxChangeSeq = next + int64(len(changed)) - 1
	return b.applyChangeSeqs()
}

// datasetHoldingBaseMax returns the dataset whose highest base change sequence is
// the highest of all of them, i.e. the one that owns the delta boundary. Ties go
// to the alphabetically first, so the answer never depends on map iteration.
func (b *builder) datasetHoldingBaseMax() string {
	best, bestSeq := "", int64(-1)
	for _, e := range b.d.ChangeSeqs {
		if e.ChangeSeq > bestSeq || (e.ChangeSeq == bestSeq && e.Dataset < best) {
			best, bestSeq = e.Dataset, e.ChangeSeq
		}
	}
	return best
}

// pickIndexes returns count distinct indexes below n, drawn from the generator// pickIndexes returns count distinct indexes below n, drawn from the generator
// and returned in ascending order.
func pickIndexes(p *prng, n, count int) ([]int, error) {
	if count == 0 {
		return nil, nil
	}
	if count > n {
		return nil, fmt.Errorf("cannot pick %d of %d", count, n)
	}
	idx := p.perm(n)[:count]
	sort.Ints(idx)
	return idx, nil
}
