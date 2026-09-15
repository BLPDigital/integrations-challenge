package model

import "sort"

// Dataset names the logical collection a record belongs to. The set is closed:
// it is the vocabulary of the store's segment files, the ingest manifests and
// the wire contracts, so an unknown name is rejected rather than created.
type Dataset string

// The datasets of this landscape.
const (
	DatasetSupplier          Dataset = "supplier"
	DatasetCostCenter        Dataset = "cost_center"
	DatasetPurchaseOrder     Dataset = "purchase_order"
	DatasetPurchaseOrderLine Dataset = "purchase_order_line"
	DatasetFxRate            Dataset = "fx_rate"
	DatasetInvoice           Dataset = "invoice"
	DatasetInvoiceLine       Dataset = "invoice_line"
	DatasetOutboxAck         Dataset = "outbox_ack"
)

// datasets is the closed set, probed by key only.
var datasets = map[Dataset]bool{
	DatasetSupplier:          true,
	DatasetCostCenter:        true,
	DatasetPurchaseOrder:     true,
	DatasetPurchaseOrderLine: true,
	DatasetFxRate:            true,
	DatasetInvoice:           true,
	DatasetInvoiceLine:       true,
	DatasetOutboxAck:         true,
}

// digestExcluded names the datasets the state digest never walks, even though
// they belong to the closed vocabulary above and are stored like any other.
//
// The outbound acknowledgment journal is provenance of one run, not logical
// state: its payload carries the run id that issued the posting, the number of
// attempts that reached the ERP, the HTTP status the ERP answered with and the
// document number the ERP assigned in arrival order. None of those is determined
// by the data a correct connector moves. Hashing them would make the digest
// depend on the client's run id and on the order in which it happened to post,
// which is exactly what the digest exists to be independent of: a pinned digest
// has to survive a different run id, and two correct connectors with different
// concurrency have to reach the same one.
var digestExcluded = map[Dataset]bool{
	DatasetOutboxAck: true,
}

// InDigest reports whether the state digest walks d.
func (d Dataset) InDigest() bool { return datasets[d] && !digestExcluded[d] }

// String returns the dataset name as it appears on the wire and on disk.
func (d Dataset) String() string { return string(d) }

// Valid reports whether d is a known dataset.
func (d Dataset) Valid() bool { return datasets[d] }

// IsKnownDataset reports whether name is a known dataset name.
func IsKnownDataset(name string) bool { return datasets[Dataset(name)] }

// Datasets returns every dataset in ascending byte order of its name. It is the
// closed vocabulary the wire contracts and the ingest manifests validate against,
// and it never depends on map iteration.
func Datasets() []Dataset {
	out := make([]Dataset, 0, len(datasets))
	for d := range datasets {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// DigestDatasets returns, in ascending byte order of name, the datasets the
// state digest walks: [Datasets] minus [digestExcluded]. The order is the walk
// order, so it is sorted here and never left to map iteration.
func DigestDatasets() []Dataset {
	out := make([]Dataset, 0, len(datasets))
	for d := range datasets {
		if digestExcluded[d] {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
