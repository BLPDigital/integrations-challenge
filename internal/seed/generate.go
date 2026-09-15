package seed

import (
	"fmt"
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// changeSeqBase is the first change sequence handed out. It is deliberately not
// 1, so a client that treats a zero or a small watermark as "no watermark yet"
// is caught by the data rather than by luck.
const changeSeqBase = 100001

// builder carries the generation state of one scenario. Nothing in it is shared
// between calls to [Generate], and its lookup maps are only ever probed by key.
type builder struct {
	spec Spec
	seed int64
	p    *prng
	d    *Dataset

	supplierByNumber  map[string]int
	costCenterByCode  map[string]int
	poByCanonical     map[string]int
	poByDelivered     map[string]int
	poLineIndex       map[string][]int
	uomByMaterialUnit map[string]int

	safeCostCenters         map[string][]string
	safeSuppliers           []string
	blockedSuppliers        []string
	safePOs                 []string
	closedPOs               []string
	closedPOsUsableSupplier []string

	resolvedByCurrency map[string][]model.FxRate
	lineTexts          map[string]string
}

// Generate builds the dataset of a scenario.
//
// The same (scenario, seed) pair always produces byte-identical output,
// including the legacy delivery bytes. That holds because every date is a
// constant of the scenario, every slice is explicitly ordered, and all
// pseudo-randomness comes from one [math/rand.Rand] seeded from seed and drawn
// in this fixed phase order:
//
//	1  cost centers        (no draws: a fixed table plus a deterministic tail)
//	2  suppliers
//	3  unit-of-measure conversions (no draws: a fixed table)
//	4  purchase orders and their lines
//	5  the SOAP exchange rate table
//	6  the base invoices
//	7  change sequences for the base master data
//	8  the delta, on a delta scenario only
//	9  the legacy deliveries      (no draws)
//	10 the golden expected-exception set (no draws)
//
// Because the delta phases run last, the base phases of a delta scenario consume
// exactly the draws its base scenario consumes, and the base delivery of S2 is
// byte-identical to the delivery of S1 for the same seed.
//
// An unknown scenario is an error. So is any internal inconsistency: a scenario
// whose sizes cannot be met, an amount that would have to be rounded to fit its
// field, or a planted record that went missing. The generator would rather fail
// than emit a dataset that does not say what it claims.
func Generate(scenario string, seed int64) (*Dataset, error) {
	spec, err := SpecOf(scenario)
	if err != nil {
		return nil, err
	}
	b := &builder{
		spec: spec,
		seed: seed,
		p:    newPRNG(seed),
		d: &Dataset{
			Scenario:           scenario,
			Seed:               seed,
			ClosedPeriodBefore: ClosedPeriodBefore,
		},
	}
	if err := b.buildCostCenters(); err != nil {
		return nil, err
	}
	if err := b.buildSuppliers(); err != nil {
		return nil, err
	}
	b.buildUoMConversions()
	if err := b.buildPurchaseOrders(); err != nil {
		return nil, err
	}
	if err := b.buildFxRows(); err != nil {
		return nil, err
	}
	b.resolveRates()

	baseInvoices, baseClasses, err := b.buildInvoices(spec.Invoices, spec.InvoiceLines, 0, true)
	if err != nil {
		return nil, err
	}
	if err := b.assignBaseChangeSeqs(); err != nil {
		return nil, err
	}

	var deltaInvoices []model.APInvoice
	if spec.DeltaOf != "" {
		deltaInvoices, err = b.applyDelta(baseInvoices, baseClasses)
		if err != nil {
			return nil, err
		}
	}

	if err := b.buildDeliveries(baseInvoices, deltaInvoices); err != nil {
		return nil, err
	}
	b.collectInvoices()

	exc, err := b.d.DeriveExceptions()
	if err != nil {
		return nil, err
	}
	b.d.ExpectedExceptions = exc
	return b.d, nil
}

// resolveRates indexes the resolved DAILY rates by currency, so invoice
// construction can keep clean invoices on dates their currency is covered on.
func (b *builder) resolveRates() {
	b.resolvedByCurrency = map[string][]model.FxRate{}
	for _, r := range b.d.ResolvedDailyRates() {
		b.resolvedByCurrency[r.Base] = append(b.resolvedByCurrency[r.Base], r)
	}
}

// buildDeliveries renders the legacy file drops. The base set is run 001, the
// delta set run 002, and each run is split into one delivery per company code
// because the Mandant of the Vorlaufsatz is where the company code lives.
func (b *builder) buildDeliveries(base, delta []model.APInvoice) error {
	runs := []struct {
		number   int
		invoices []model.APInvoice
	}{{1, base}}
	if len(delta) > 0 {
		runs = append(runs, struct {
			number   int
			invoices []model.APInvoice
		}{2, delta})
	}
	for _, run := range runs {
		ch10, ch20 := splitByCompanyCode(run.invoices)
		for _, part := range []struct {
			companyCode string
			invoices    []model.APInvoice
		}{
			{model.CompanyCodeCH10, ch10},
			{model.CompanyCodeCH20, ch20},
		} {
			if len(part.invoices) == 0 {
				continue
			}
			del, err := b.buildDelivery(part.companyCode, run.number, part.invoices)
			if err != nil {
				return err
			}
			b.d.Deliveries = append(b.d.Deliveries, del)
		}
	}
	if len(b.d.Deliveries) == 0 {
		return fmt.Errorf("seed: scenario %s produced no legacy delivery", b.spec.ID)
	}
	primary := b.d.Deliveries[0]
	if primary.Mandant != MandantCH10 {
		return fmt.Errorf("seed: the primary delivery must be Mandant %s, got %s", MandantCH10, primary.Mandant)
	}
	b.d.LegacyFile = primary.Bytes
	b.d.LegacyFileName = primary.Name
	b.d.OKFileName = primary.OKName
	return nil
}

// collectInvoices fills the dataset's distinct invoice set: every invoice as
// FIRST delivered, sorted by natural key, plus the amended re-deliveries.
func (b *builder) collectInvoices() {
	seen := map[string]bool{}
	first := map[string]model.APInvoice{}
	amended := map[string]model.APInvoice{}
	var keys, amendedKeys []string
	for i := range b.d.Deliveries {
		for _, inv := range b.d.Deliveries[i].Invoices {
			k := inv.Key()
			if !seen[k] {
				seen[k] = true
				first[k] = inv
				keys = append(keys, k)
				continue
			}
			if model.ContentHash(inv) != model.ContentHash(first[k]) {
				if _, dup := amended[k]; !dup {
					amendedKeys = append(amendedKeys, k)
				}
				amended[k] = inv
			}
		}
	}
	sortStrings(keys)
	sortStrings(amendedKeys)
	b.d.Invoices = make([]model.APInvoice, 0, len(keys))
	for _, k := range keys {
		b.d.Invoices = append(b.d.Invoices, first[k])
	}
	b.d.AmendedInvoices = make([]model.APInvoice, 0, len(amendedKeys))
	for _, k := range amendedKeys {
		b.d.AmendedInvoices = append(b.d.AmendedInvoices, amended[k])
	}
}

// assignBaseChangeSeqs hands out the ERP change sequences of the master data the
// ERP exposes with ?changed_since.
//
// The sequences are strictly increasing and unique across the whole dataset, so
// a watermark is a single comparable number. They are handed out in natural-key
// order per dataset, except for a small deterministic subset that is moved to the
// end: those records carry the highest sequences while sitting early in key
// order, which is what a real ERP looks like after a late correction and what
// breaks a connector that persists "the last key I saw" instead of the highest
// change sequence.
func (b *builder) assignBaseChangeSeqs() error {
	entries := make([]ChangeSeqEntry, 0,
		len(b.d.Suppliers)+len(b.d.PurchaseOrders)+len(b.d.PurchaseOrderLines))
	for i := range b.d.Suppliers {
		entries = append(entries, ChangeSeqEntry{
			Dataset: model.DatasetSupplier.String(), Key: b.d.Suppliers[i].Key()})
	}
	for i := range b.d.PurchaseOrders {
		entries = append(entries, ChangeSeqEntry{
			Dataset: model.DatasetPurchaseOrder.String(), Key: b.d.PurchaseOrders[i].Key()})
	}
	for i := range b.d.PurchaseOrderLines {
		entries = append(entries, ChangeSeqEntry{
			Dataset: model.DatasetPurchaseOrderLine.String(), Key: b.d.PurchaseOrderLines[i].Key()})
	}
	// Out-of-order updates: at least three records, more on a large dataset, and
	// AT LEAST ONE PER DATASET.
	//
	// The per-dataset guarantee is not decoration. A connector that compares its
	// watermark with a strict greater-than, or that assumes the list arrives in
	// key order, only breaks where an out-of-order update actually exists, so a
	// draw that happened to put all three into purchase order lines left the
	// supplier pull untested. It also made the property depend on the random
	// stream, which means an unrelated change elsewhere in the generator could
	// silently remove a planted trap. That happened once; hence this.
	outOfOrder := len(entries) / 500
	if outOfOrder < 3 {
		outOfOrder = 3
	}
	if outOfOrder > len(entries) {
		outOfOrder = len(entries)
	}
	firstOf := map[string]int{}
	for i := range entries {
		if _, seen := firstOf[entries[i].Dataset]; !seen {
			firstOf[entries[i].Dataset] = i
		}
	}
	pickedSet := map[int]bool{}
	// One from each dataset, taken from the middle of its block so the move is
	// visible in both directions.
	for _, ds := range []string{
		model.DatasetSupplier.String(),
		model.DatasetPurchaseOrder.String(),
		model.DatasetPurchaseOrderLine.String(),
	} {
		start, ok := firstOf[ds]
		if !ok {
			continue
		}
		end := start
		for end+1 < len(entries) && entries[end+1].Dataset == ds {
			end++
		}
		if end > start {
			pickedSet[start+(end-start)/2] = true
		}
	}
	for _, i := range b.p.perm(len(entries)) {
		if len(pickedSet) >= outOfOrder {
			break
		}
		pickedSet[i] = true
	}
	picked := make([]int, 0, len(pickedSet))
	for i := range pickedSet {
		picked = append(picked, i)
	}
	sort.Ints(picked)
	moved := make(map[int]bool, len(picked))
	for _, i := range picked {
		moved[i] = true
	}
	ordered := make([]ChangeSeqEntry, 0, len(entries))
	for i := range entries {
		if !moved[i] {
			ordered = append(ordered, entries[i])
		}
	}
	for _, i := range picked {
		ordered = append(ordered, entries[i])
	}
	for i := range ordered {
		ordered[i].ChangeSeq = changeSeqBase + int64(i)
	}
	b.d.ChangeSeqs = ordered
	b.d.MaxChangeSeq = changeSeqBase + int64(len(ordered)) - 1
	return b.applyChangeSeqs()
}

// applyChangeSeqs writes the assigned sequences onto the records themselves.
func (b *builder) applyChangeSeqs() error {
	byKey := make(map[string]int64, len(b.d.ChangeSeqs))
	for _, e := range b.d.ChangeSeqs {
		byKey[e.Dataset+"\x1f"+e.Key] = e.ChangeSeq
	}
	for i := range b.d.Suppliers {
		seq, ok := byKey[model.DatasetSupplier.String()+"\x1f"+b.d.Suppliers[i].Key()]
		if !ok {
			return fmt.Errorf("seed: no change sequence for supplier %q", b.d.Suppliers[i].SupplierNumber)
		}
		b.d.Suppliers[i].ChangeSeq = seq
	}
	for i := range b.d.PurchaseOrders {
		seq, ok := byKey[model.DatasetPurchaseOrder.String()+"\x1f"+b.d.PurchaseOrders[i].Key()]
		if !ok {
			return fmt.Errorf("seed: no change sequence for purchase order %q", b.d.PurchaseOrders[i].PONumber)
		}
		b.d.PurchaseOrders[i].ChangeSeq = seq
	}
	for i := range b.d.PurchaseOrderLines {
		seq, ok := byKey[model.DatasetPurchaseOrderLine.String()+"\x1f"+b.d.PurchaseOrderLines[i].Key()]
		if !ok {
			return fmt.Errorf("seed: no change sequence for purchase order line %q/%q",
				b.d.PurchaseOrderLines[i].PONumber, b.d.PurchaseOrderLines[i].LineNo)
		}
		b.d.PurchaseOrderLines[i].ChangeSeq = seq
	}
	sort.Slice(b.d.ChangeSeqs, func(i, j int) bool {
		return b.d.ChangeSeqs[i].ChangeSeq < b.d.ChangeSeqs[j].ChangeSeq
	})
	return nil
}
