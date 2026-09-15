package canonical

import (
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// datasetFields lists the canonical field names of every dataset, in the
// declaration order of the entity. It is the header a CSV writer emits, the
// element order an XML writer emits, and the vocabulary the detectors recognize.
//
// The names are the `json` tags of the [model] entities, not a second spelling
// of them: a field renamed in the model and not here would produce records that
// validate and store nothing, so a test compares this table against the
// canonical JSON of each entity rather than trusting it.
var datasetFields = map[model.Dataset][]string{
	model.DatasetSupplier: {
		"supplier_number", "name", "country", "currency", "iban", "vat_number",
		"payment_terms_days", "blocked", "change_seq",
	},
	model.DatasetCostCenter: {
		"code", "name", "company_code", "valid_from", "valid_to", "blocked",
	},
	model.DatasetPurchaseOrder: {
		"po_number", "supplier_number", "company_code", "currency", "status",
		"order_date", "cost_center", "change_seq",
	},
	model.DatasetPurchaseOrderLine: {
		"po_number", "line_no", "material", "description", "quantity", "uom",
		"unit_price", "currency", "gl_account", "cost_center", "change_seq",
	},
	model.DatasetFxRate: {
		"base", "quote", "valid_from", "rate_type", "valid_to", "rate",
		"rate_factor", "sequence", "status",
	},
	model.DatasetInvoice: {
		"supplier_number", "supplier_invoice_number", "company_code", "document_type",
		"document_date", "receipt_date", "po_number", "currency", "gross_amount",
		"vat_amount", "vat_code", "payment_terms_days", "discount_raw", "discount_days",
		"cost_center", "text", "lines",
	},
	model.DatasetInvoiceLine: {
		"supplier_number", "supplier_invoice_number", "line_no", "gl_account",
		"cost_center", "quantity", "uom", "unit_price", "line_amount", "tax_code",
		"currency",
	},
	model.DatasetOutboxAck: {
		"proposal_id", "status", "external_document_number", "external_revision",
		"external_fiscal_year", "external_posting_date", "idempotency_key", "run_id",
		"posted_at", "attempts", "http_status", "idempotency_replay", "error", "reason",
	},
}

// Fields returns the canonical field names of a dataset, in the entity's
// declaration order, or nil for an unknown dataset. The slice is freshly
// allocated, so a caller cannot mutate the table.
//
// The invoice dataset's last field, "lines", is the embedded line list: it is a
// field of the JSON and XML shapes and has no CSV column, because a CSV cell
// cannot hold a list. A CSV sender delivers the lines as an invoice_line file
// instead, which is why the two datasets exist separately.
func Fields(ds model.Dataset) []string {
	f, ok := datasetFields[ds]
	if !ok {
		return nil
	}
	return append([]string(nil), f...)
}

// canonicalFieldNames is the union of every dataset's field names, used by the
// detectors: a header row that names none of them is not a canonical file,
// whatever it parses as.
var canonicalFieldNames = func() map[string]bool {
	out := make(map[string]bool)
	for _, fields := range datasetFields {
		for _, f := range fields {
			out[f] = true
		}
	}
	return out
}()

// CanonicalFieldNames returns every canonical field name across all datasets, in
// ascending byte order. It exists for the detectors' tests and for a writer that
// needs to know which columns are ours.
func CanonicalFieldNames() []string {
	out := make([]string, 0, len(canonicalFieldNames))
	for f := range canonicalFieldNames {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
