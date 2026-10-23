package erp

import (
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// Route templates of the ERP. They are the stable names the fault-injection
// signature and the metrics are keyed by, so they never carry an identifier.
const (
	// RouteAuthToken issues an access token from client credentials.
	RouteAuthToken = "/erp/v1/auth/token"
	// RouteSuppliers is the paginated creditor list.
	RouteSuppliers = "/erp/v1/suppliers"
	// RouteSupplier is the single creditor GET, which exists for the
	// correction path and is net token negative on purpose.
	RouteSupplier = "/erp/v1/suppliers/{supplier_number}"
	// RoutePurchaseOrders is the paginated purchase order list.
	RoutePurchaseOrders = "/erp/v1/purchase-orders"
	// RoutePurchaseOrderLines is the paginated purchase order line list.
	RoutePurchaseOrderLines = "/erp/v1/purchase-order-lines"
	// RouteUoMConversions is the unit-of-measure conversion table.
	RouteUoMConversions = "/erp/v1/uom-conversions"
	// RouteDocuments is the single AP document posting.
	RouteDocuments = "/erp/v1/ap/documents"
	// RouteDocumentsBatch is the batched AP document posting.
	RouteDocumentsBatch = "/erp/v1/ap/documents:batch"
	// RouteSOAP is the single SOAP operation and its WSDL.
	RouteSOAP = "/soap/FinancialReferenceDataService"
	// RouteHealthz is the liveness probe. It is free of charge, because the
	// grader polls it before a run and a probe that spent quota would make the
	// budget depend on how fast the process started.
	RouteHealthz = "/healthz"
)

// A view is one ordered, pageable collection. Every list endpoint is served
// through this interface, so the pagination contract (ordering, cursors, the
// clamp, changed_since) exists in exactly one place and cannot drift between
// datasets.
//
// The order is (change_seq, natural key) ascending, always, which is what makes
// a cursor a position rather than an offset and what makes changed_since a
// suffix of the collection.
type view interface {
	// dataset is the cursor domain: a cursor signed for one dataset is not a
	// cursor for another.
	dataset() string
	// route is the route template, for signatures and metrics.
	route() string
	// length is the number of records in the view.
	length() int
	// keyAt is the natural key of record i.
	keyAt(i int) string
	// seqAt is the change sequence of record i.
	seqAt(i int) int64
	// recordAt is the wire record i, ready to marshal.
	recordAt(i int) any
	// maxChangeSeq is the highest change sequence in the whole collection,
	// independent of any filter, which is the watermark a connector persists
	// after a complete pull.
	maxChangeSeq() int64
}

// indexedView is a view over a sorted slice of records of one type. The sort is
// performed once, when the dataset is loaded, never per request.
type indexedView[T any] struct {
	ds, rt string
	items  []T
	key    func(T) string
	seq    func(T) int64
	max    int64
}

// newIndexedView sorts items by (change sequence, natural key) and returns the
// view over them. The input slice is copied, so the caller's dataset stays in
// its own documented order.
func newIndexedView[T any](ds, rt string, items []T, key func(T) string, seq func(T) int64) *indexedView[T] {
	cp := make([]T, len(items))
	copy(cp, items)
	sort.SliceStable(cp, func(i, j int) bool {
		if seq(cp[i]) != seq(cp[j]) {
			return seq(cp[i]) < seq(cp[j])
		}
		return key(cp[i]) < key(cp[j])
	})
	v := &indexedView[T]{ds: ds, rt: rt, items: cp, key: key, seq: seq}
	for i := range cp {
		if s := seq(cp[i]); s > v.max {
			v.max = s
		}
	}
	return v
}

func (v *indexedView[T]) dataset() string     { return v.ds }
func (v *indexedView[T]) route() string       { return v.rt }
func (v *indexedView[T]) length() int         { return len(v.items) }
func (v *indexedView[T]) keyAt(i int) string  { return v.key(v.items[i]) }
func (v *indexedView[T]) seqAt(i int) int64   { return v.seq(v.items[i]) }
func (v *indexedView[T]) recordAt(i int) any  { return v.items[i] }
func (v *indexedView[T]) maxChangeSeq() int64 { return v.max }

// subsetView is a view over selected positions of a parent view, in the parent's
// own order. It serves the bulk ids= form of the purchase order list without
// giving that form its own pagination code path.
type subsetView struct {
	parent view
	idx    []int
}

func (v *subsetView) dataset() string    { return v.parent.dataset() }
func (v *subsetView) route() string      { return v.parent.route() }
func (v *subsetView) length() int        { return len(v.idx) }
func (v *subsetView) keyAt(i int) string { return v.parent.keyAt(v.idx[i]) }
func (v *subsetView) seqAt(i int) int64  { return v.parent.seqAt(v.idx[i]) }
func (v *subsetView) recordAt(i int) any { return v.parent.recordAt(v.idx[i]) }

// maxChangeSeq is the parent's watermark, not the subset's: a bulk read of three
// purchase orders must not hand a client a watermark that would skip everything
// it did not ask for.
func (v *subsetView) maxChangeSeq() int64 { return v.parent.maxChangeSeq() }

// data is the loaded, indexed dataset one scenario serves. It is immutable once
// built: a reseed replaces the whole value rather than mutating it, so no request
// can observe a half-loaded landscape.
type data struct {
	set *seed.Dataset

	suppliers      *indexedView[seed.Supplier]
	purchaseOrders *indexedView[model.PurchaseOrder]
	poLines        *indexedView[model.PurchaseOrderLine]

	// uom is the conversion table in its documented (material, alt unit) order.
	uom []seed.UoMConversion

	// Lookups. Every map here is probed by key only and never ranged over into
	// a response body.
	supplierByNumber map[string]int
	poByCanonical    map[string]int
	costCenterByCode map[string]model.CostCenter
	// poNetTotal is the sum of quantity times unit price over a purchase
	// order's lines, keyed by canonical purchase order number. It is the
	// reference the amount tolerance check compares a posting against, computed
	// once with exact decimal arithmetic.
	poNetTotal map[string]model.Decimal
}

// loadData generates and indexes a scenario's dataset. It fails rather than
// serving a dataset the generator itself calls inconsistent.
func loadData(scenario string, seedValue int64) (*data, error) {
	set, err := seed.Generate(scenario, seedValue)
	if err != nil {
		return nil, err
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	return indexData(set)
}

// indexData builds the served views and lookups of a generated dataset.
func indexData(set *seed.Dataset) (*data, error) {
	d := &data{
		set: set,
		uom: set.UoMConversions,
		suppliers: newIndexedView(model.DatasetSupplier.String(), RouteSuppliers, set.Suppliers,
			func(s seed.Supplier) string { return s.Key() },
			func(s seed.Supplier) int64 { return s.ChangeSeq }),
		purchaseOrders: newIndexedView(model.DatasetPurchaseOrder.String(), RoutePurchaseOrders, set.PurchaseOrders,
			func(p model.PurchaseOrder) string { return p.Key() },
			func(p model.PurchaseOrder) int64 { return p.ChangeSeq }),
		poLines: newIndexedView(model.DatasetPurchaseOrderLine.String(), RoutePurchaseOrderLines, set.PurchaseOrderLines,
			func(l model.PurchaseOrderLine) string { return l.Key() },
			func(l model.PurchaseOrderLine) int64 { return l.ChangeSeq }),
		supplierByNumber: make(map[string]int, len(set.Suppliers)),
		poByCanonical:    make(map[string]int, len(set.PurchaseOrders)),
		costCenterByCode: make(map[string]model.CostCenter, len(set.CostCenters)),
		poNetTotal:       make(map[string]model.Decimal, len(set.PurchaseOrders)),
	}
	for i := range d.suppliers.items {
		d.supplierByNumber[d.suppliers.items[i].SupplierNumber] = i
	}
	for i := range d.purchaseOrders.items {
		d.poByCanonical[poKey(d.purchaseOrders.items[i].PONumber)] = i
	}
	for i := range set.CostCenters {
		d.costCenterByCode[set.CostCenters[i].Code] = set.CostCenters[i]
	}
	for i := range set.PurchaseOrderLines {
		l := set.PurchaseOrderLines[i]
		canon := poKey(l.PONumber)
		amount, err := l.Quantity.MulRate(l.UnitPrice, 1, 2)
		if err != nil {
			return nil, err
		}
		sum, err := d.poNetTotal[canon].Add(amount)
		if err != nil {
			return nil, err
		}
		d.poNetTotal[canon] = sum
	}
	return d, nil
}

// poKey is the ERP's lookup key for a purchase order number, and it implements
// the identifier rule exactly as docs/rules.md publishes it: trim, drop a leading
// "PO-", then compare, with leading zeros on a PO number not significant.
//
// It exists because seed.CanonicalPONumber does NOT drop the prefix: it compares
// a non-numeric remainder as-is, deliberately, because the generator resolves the
// three planted spellings back to one order with it and the expected exception set
// is derived from those keys. The twin does drop the prefix, so without this the
// two systems disagreed about the one identifier the documentation says is
// normalized, and a connector that applied the published rule before posting got
// ERP_PO_NOT_FOUND on the quarter of orders the generator spells with a prefix.
// Both sides of every lookup are normalized through here, so the spelling a
// client uses no longer decides whether the order exists. The numbers themselves
// are unique digits, so dropping the prefix can never resolve to a different
// order.
func poKey(number string) string {
	t := strings.TrimSpace(number)
	if rest, ok := strings.CutPrefix(t, "PO-"); ok {
		t = rest
	}
	return seed.CanonicalPONumber(t)
}

// supplier returns a creditor by its exact supplier number. Leading zeros are
// significant and nothing is normalized: "0000417" and "0000000417" are two
// different creditors in this landscape, which is the whole point of the pair.
func (d *data) supplier(number string) (seed.Supplier, bool) {
	i, ok := d.supplierByNumber[number]
	if !ok {
		return seed.Supplier{}, false
	}
	return d.suppliers.items[i], true
}

// purchaseOrder resolves a purchase order number in any of its delivered
// spellings, applying the published identifier rule through [poKey]: trim, drop a
// leading "PO-", then compare, numerically when the remainder is all digits.
func (d *data) purchaseOrder(number string) (model.PurchaseOrder, bool) {
	i, ok := d.poByCanonical[poKey(number)]
	if !ok {
		return model.PurchaseOrder{}, false
	}
	return d.purchaseOrders.items[i], true
}

// costCenter returns a cost center by its exact code. Cost center codes are the
// documented exception where leading zeros are significant: "0815" and "815" are
// two different cost centers.
func (d *data) costCenter(code string) (model.CostCenter, bool) {
	cc, ok := d.costCenterByCode[code]
	return cc, ok
}

// poSubset returns the view over the purchase orders named by numbers, in the
// list's own (change_seq, key) order, with unknown numbers simply absent. It
// backs the bulk ids= form.
func (d *data) poSubset(numbers []string) *subsetView {
	seen := make(map[int]bool, len(numbers))
	idx := make([]int, 0, len(numbers))
	for _, n := range numbers {
		i, ok := d.poByCanonical[poKey(n)]
		if !ok || seen[i] {
			continue
		}
		seen[i] = true
		idx = append(idx, i)
	}
	// The positions come out of a map probe in request order; the view contract
	// is the parent's order, so they are sorted explicitly.
	sort.Ints(idx)
	return &subsetView{parent: d.purchaseOrders, idx: idx}
}
