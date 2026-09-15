package web

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// A column is one column of the dataset browser: the heading, the payload field
// it reads, and whether the value is minimized before it is shown.
type column struct {
	Header string
	Field  string
	// Mask masks the value, for a payment detail nobody needs in a browser
	// table. BUILD-SPEC 17.7 asks for masking in candidate artifacts; masking
	// it here too costs nothing and keeps an IBAN out of a screenshot.
	Mask bool
	// Num right-aligns and monospaces a numeric column.
	Num bool
}

// datasetColumns is the columns that matter per dataset: the identifying
// fields, the ones a matching decision is made from, and the change sequence,
// and nothing else. The fallback for a dataset with no entry is the payload's
// own scalar fields in ascending name order, so a dataset added later still
// browses.
var datasetColumns = map[string][]column{
	model.DatasetSupplier.String(): {
		{Header: "Supplier", Field: "supplier_number"},
		{Header: "Name", Field: "name"},
		{Header: "Country", Field: "country"},
		{Header: "Currency", Field: "currency"},
		{Header: "IBAN", Field: "iban", Mask: true},
		{Header: "Terms", Field: "payment_terms_days", Num: true},
		{Header: "Blocked", Field: "blocked"},
		{Header: "Change seq", Field: "change_seq", Num: true},
	},
	model.DatasetCostCenter.String(): {
		{Header: "Code", Field: "code"},
		{Header: "Name", Field: "name"},
		{Header: "Company", Field: "company_code"},
		{Header: "Valid from", Field: "valid_from"},
		{Header: "Valid to", Field: "valid_to"},
		{Header: "Blocked", Field: "blocked"},
	},
	model.DatasetPurchaseOrder.String(): {
		{Header: "PO", Field: "po_number"},
		{Header: "Supplier", Field: "supplier_number"},
		{Header: "Company", Field: "company_code"},
		{Header: "Currency", Field: "currency"},
		{Header: "Status", Field: "status"},
		{Header: "Order date", Field: "order_date"},
		{Header: "Cost center", Field: "cost_center"},
		{Header: "Change seq", Field: "change_seq", Num: true},
	},
	model.DatasetPurchaseOrderLine.String(): {
		{Header: "PO", Field: "po_number"},
		{Header: "Line", Field: "line_no"},
		{Header: "Material", Field: "material"},
		{Header: "Quantity", Field: "quantity", Num: true},
		{Header: "UoM", Field: "uom"},
		{Header: "Unit price", Field: "unit_price", Num: true},
		{Header: "Currency", Field: "currency"},
		{Header: "GL account", Field: "gl_account"},
		{Header: "Cost center", Field: "cost_center"},
	},
	model.DatasetFxRate.String(): {
		{Header: "Base", Field: "base"},
		{Header: "Quote", Field: "quote"},
		{Header: "Type", Field: "rate_type"},
		{Header: "Valid from", Field: "valid_from"},
		{Header: "Valid to", Field: "valid_to"},
		{Header: "Rate", Field: "rate", Num: true},
		{Header: "Factor", Field: "rate_factor", Num: true},
		{Header: "Sequence", Field: "sequence", Num: true},
		{Header: "Status", Field: "status"},
	},
	model.DatasetInvoice.String(): {
		{Header: "Supplier", Field: "supplier_number"},
		{Header: "Invoice no.", Field: "supplier_invoice_number"},
		{Header: "Company", Field: "company_code"},
		{Header: "Type", Field: "document_type"},
		{Header: "Doc date", Field: "document_date"},
		{Header: "PO", Field: "po_number"},
		{Header: "Currency", Field: "currency"},
		{Header: "Gross", Field: "gross_amount", Num: true},
		{Header: "VAT", Field: "vat_amount", Num: true},
		{Header: "Cost center", Field: "cost_center"},
	},
	model.DatasetInvoiceLine.String(): {
		{Header: "Line", Field: "line_no"},
		{Header: "GL account", Field: "gl_account"},
		{Header: "Cost center", Field: "cost_center"},
		{Header: "Quantity", Field: "quantity", Num: true},
		{Header: "UoM", Field: "uom"},
		{Header: "Unit price", Field: "unit_price", Num: true},
		{Header: "Line amount", Field: "line_amount", Num: true},
		{Header: "Tax code", Field: "tax_code"},
	},
	model.DatasetOutboxAck.String(): {
		{Header: "Proposal", Field: "proposal_id"},
		{Header: "Status", Field: "status"},
		{Header: "ERP document", Field: "external_document_number"},
		{Header: "Idempotency key", Field: "idempotency_key"},
		{Header: "Run", Field: "run_id"},
		{Header: "Error", Field: "error"},
	},
	// The two segments the store keeps for its own bookkeeping browse too. They
	// have richer views of their own, so the columns here are the identifying
	// ones only.
	store.DatasetProposal: {
		{Header: "Proposal", Field: "proposal_id"},
		{Header: "Invoice", Field: "invoice_key"},
		{Header: "Status", Field: "status"},
		{Header: "Amount", Field: "amount", Num: true},
		{Header: "Source amount", Field: "source_amount", Num: true},
		{Header: "Attempts", Field: "attempts", Num: true},
		{Header: "Created in run", Field: "created_in_run"},
	},
	store.DatasetException: {
		{Header: "Subject", Field: "subject_key"},
		{Header: "Type", Field: "subject_type"},
		{Header: "Code", Field: "code"},
		{Header: "State", Field: "state"},
		{Header: "Stage", Field: "stage"},
		{Header: "Field", Field: "field"},
	},
	miniblp.DatasetUoMConversion: {
		{Header: "Material", Field: "material"},
		{Header: "Alt UoM", Field: "alt_uom"},
		{Header: "Numerator", Field: "numerator", Num: true},
		{Header: "Denominator", Field: "denominator", Num: true},
		{Header: "Base UoM", Field: "base_uom"},
	},
}

// fieldsOf returns a payload's top-level fields as display strings.
//
// The payload is either the struct the importer built or the generic tree the
// store replayed from disk, and the two must render identically, so both go
// through canonical JSON first. Canonical JSON is also the one form with no
// float in it: model.DecodeJSONTree keeps every number as a json.Number, so a
// quantity or an amount is displayed exactly as it was delivered.
func fieldsOf(payload any) map[string]string {
	buf, err := model.CanonicalJSON(payload)
	if err != nil {
		return nil
	}
	tree, err := model.DecodeJSONTree(buf)
	if err != nil {
		return nil
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		out[k] = formatValue(v)
	}
	return out
}

// scalarFieldNames returns the fields of a payload that render as one value, in
// ascending name order. It is the fallback column set of the dataset browser.
func scalarFieldNames(fields map[string]string, max int) []string {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	if max > 0 && len(names) > max {
		names = names[:max]
	}
	return names
}

// formatValue renders one decoded JSON value for a table cell. Money is
// rendered through model.Money so the minor units, the currency and the scale
// are never shown as three separate columns or, worse, multiplied by a float.
func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case []any:
		if len(t) == 0 {
			return "none"
		}
		if len(t) == 1 {
			return "1 item"
		}
		return group(int64(len(t))) + " items"
	case map[string]any:
		if m, ok := asMoney(t); ok {
			return m.String()
		}
		return "{…}"
	default:
		return ""
	}
}

// asMoney recognizes the canonical Money object and returns it.
func asMoney(obj map[string]any) (model.Money, bool) {
	minor, ok1 := obj["amount_minor"].(json.Number)
	currency, ok2 := obj["currency"].(string)
	scale, ok3 := obj["scale"].(json.Number)
	if !ok1 || !ok2 || !ok3 {
		return model.Money{}, false
	}
	amount, err1 := minor.Int64()
	sc, err2 := scale.Int64()
	if err1 != nil || err2 != nil || sc < 0 || sc > 18 {
		return model.Money{}, false
	}
	return model.Money{AmountMinor: amount, Currency: currency, Scale: uint8(sc)}, true
}

// maskValue minimizes a payment detail: the first four and the last four
// characters survive, which is the masked form BUILD-SPEC 17.7 promises passes
// the data-minimization assertions.
func maskValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 8 {
		if v == "" {
			return ""
		}
		return strings.Repeat("*", len(v))
	}
	return v[:4] + strings.Repeat("*", len(v)-8) + v[len(v)-4:]
}

// prettyPayload returns the payload as indented canonical JSON: sorted keys, no
// floats, exact decimals as strings. json.Indent preserves byte order, so what
// is shown is the same object, and in the same order, that the content hash was
// computed over.
func prettyPayload(payload any) string {
	buf, err := model.CanonicalJSON(payload)
	if err != nil {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf, "", "  "); err != nil {
		return string(buf)
	}
	return out.String()
}
