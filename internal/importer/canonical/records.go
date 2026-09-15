package canonical

import (
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// An InvoiceLineRecord is the wire shape of the invoice_line dataset: one invoice
// line delivered on its own, with the two key components of its invoice.
//
// It exists because invoice and invoice_line are separate datasets, so a CSV
// file of lines has to name the invoice each line belongs to; the JSON and XML
// formats may instead embed the lines in their invoice, which is why an invoice
// document can carry lines that no invoice_line record delivered. Both routes
// produce the same stored content.
type InvoiceLineRecord struct {
	// SupplierNumber is the first key component of the invoice.
	SupplierNumber string `json:"supplier_number"`
	// SupplierInvoiceNumber is the second key component of the invoice.
	SupplierInvoiceNumber string `json:"supplier_invoice_number"`
	// APInvoiceLine is the line itself. It is embedded, so the canonical JSON
	// of an InvoiceLineRecord is one flat object.
	model.APInvoiceLine
}

// Key returns the natural key of the record: invoice key plus line number.
func (r InvoiceLineRecord) Key() string {
	return model.InvoiceLineKey(r.SupplierNumber, r.SupplierInvoiceNumber, r.LineNo)
}

// An AckRecord is the wire shape of the outbox_ack dataset: the connector's
// report of one posting attempt, addressed to the proposal it is about.
//
// The ack body is [store.ProposalAck] rather than a shape of this package,
// because the file channel and `POST /v1/outbox/acks` must accept exactly the
// same fields: an ack delivered as a CSV row in a batch and the same ack posted
// over HTTP have to reach the same proposal state, or the double-post detector
// depends on which route the connector chose.
type AckRecord struct {
	// ProposalID is the natural key: the proposal being acknowledged.
	ProposalID string `json:"proposal_id"`
	// ProposalAck is the ack itself, embedded, so the canonical JSON is one
	// flat object.
	store.ProposalAck
}

// Key returns the natural key of the record.
func (r AckRecord) Key() string { return r.ProposalID }

// Fraction-digit limits per field, from BUILD-SPEC 16.1 and the customer's own
// field table. They are the reason an input is never rounded: a value with more
// digits than its field allows is a defect the sender has to fix, and rounding
// it here would change what a supplier gets paid.
const (
	// scaleAmount is the limit for a monetary amount: exactly the minor units
	// of a currency, and no currency in the table has more than two.
	scaleAmount = 2
	// scaleUnitPrice is the limit for a unit price, which the customer's field
	// table gives as two to four decimals.
	scaleUnitPrice = 4
	// scaleQuantity is the limit for a quantity, up to three decimals, which is
	// also the limit a unit-of-measure conversion may need.
	scaleQuantity = 3
	// scaleRate is the limit for an exchange rate, delivered with six.
	scaleRate = 6
)

// An emitted is what one record produced.
type emitted struct {
	// accepted reports that a document was added.
	accepted bool
	// lines is how many subordinate line records the document carried.
	lines int
}

// emitRecord interprets one parsed record as a document of the given dataset,
// validates it, and adds either a document or the findings that stopped it to b.
//
// The order is deliberate. The reader's syntactic findings come first, and when
// any of them blocked the record the validator does not run at all: a record
// whose amount did not parse would otherwise also be reported as an amount that
// is not expressible in minor units, and two findings for one defect send an
// operator looking for two problems.
func emitRecord(b *importer.Builder, ds model.Dataset, rec record, dial dialect, ordinal int) emitted {
	r := newFieldReader(rec, dial, ordinal)
	key, payload, lines := buildDocument(ds, r)
	r.unknownFields()

	if !r.blocked {
		if errs := validate(ds, payload); len(errs) > 0 {
			for _, e := range errs {
				r.reject(e.Code, e.Field, "", e.Message)
			}
		}
	}
	diags := r.stamp(key)
	for _, d := range diags {
		b.Add(d)
	}
	if r.blocked {
		return emitted{}
	}
	raw, err := model.CanonicalJSON(payload)
	if err != nil {
		// Canonicalization can only fail on a value this package built, so a
		// failure is a programming fault in the builders above, not a data
		// defect. It is reported as fatal rather than panicked on: a twin that
		// crashes on one malformed record loses the whole batch.
		b.Add(importer.Diagnostic{
			Code: importer.CodeFieldInvalid, Severity: importer.SeverityFatal,
			Line: rec.line, Record: ordinal, Document: key,
			Message: "internal: the record could not be canonicalized: " + err.Error(),
		})
		return emitted{}
	}
	b.AddDocument(importer.Document{Dataset: ds.String(), Key: key, Line: rec.line, Payload: raw})
	return emitted{accepted: true, lines: lines}
}

// buildDocument reads the fields of one dataset and returns the natural key, the
// entity, and how many subordinate lines it carried.
func buildDocument(ds model.Dataset, r *fieldReader) (string, any, int) {
	switch ds {
	case model.DatasetSupplier:
		s := model.Supplier{
			SupplierNumber:   r.key("supplier_number"),
			Name:             r.text("name"),
			Country:          r.text("country"),
			Currency:         r.text("currency"),
			IBAN:             r.text("iban"),
			VATNumber:        r.text("vat_number"),
			PaymentTermsDays: r.integer("payment_terms_days"),
			Blocked:          r.boolean("blocked"),
			ChangeSeq:        r.integer64("change_seq"),
		}
		return s.Key(), s, 0

	case model.DatasetCostCenter:
		c := model.CostCenter{
			Code:        r.key("code"),
			Name:        r.text("name"),
			CompanyCode: r.text("company_code"),
			ValidFrom:   r.date("valid_from"),
			ValidTo:     r.date("valid_to"),
			Blocked:     r.boolean("blocked"),
		}
		return c.Key(), c, 0

	case model.DatasetPurchaseOrder:
		p := model.PurchaseOrder{
			PONumber:       r.key("po_number"),
			SupplierNumber: r.text("supplier_number"),
			CompanyCode:    r.text("company_code"),
			Currency:       r.text("currency"),
			Status:         r.text("status"),
			OrderDate:      r.date("order_date"),
			CostCenter:     r.text("cost_center"),
			ChangeSeq:      r.integer64("change_seq"),
		}
		return p.Key(), p, 0

	case model.DatasetPurchaseOrderLine:
		l := model.PurchaseOrderLine{
			PONumber:    r.key("po_number"),
			LineNo:      r.key("line_no"),
			Material:    r.text("material"),
			Description: r.text("description"),
			Quantity:    r.decimal("quantity", scaleQuantity),
			UoM:         r.text("uom"),
			Currency:    r.text("currency"),
			GLAccount:   r.text("gl_account"),
			CostCenter:  r.text("cost_center"),
			ChangeSeq:   r.integer64("change_seq"),
		}
		price, priceCur := r.money("unit_price", scaleUnitPrice)
		l.UnitPrice = price
		r.checkCurrency("unit_price", priceCur, l.Currency)
		return l.Key(), l, 0

	case model.DatasetFxRate:
		f := model.FxRate{
			Base:       r.key("base"),
			Quote:      r.key("quote"),
			ValidFrom:  r.date("valid_from"),
			RateType:   r.key("rate_type"),
			ValidTo:    r.date("valid_to"),
			Rate:       r.decimal("rate", scaleRate),
			RateFactor: r.integer64("rate_factor"),
			Sequence:   r.integer("sequence"),
			Status:     r.text("status"),
		}
		if f.RateFactor == 0 {
			// A rate quoted per unit has factor 1. Zero is what an absent
			// column produces, and dividing by it would be a hundredfold
			// posting error waiting for a JPY invoice, so it is defaulted here
			// and never inferred from the currency.
			f.RateFactor = 1
		}
		return f.Key(), f, 0

	case model.DatasetInvoice:
		i := model.APInvoice{
			SupplierNumber:        r.key("supplier_number"),
			SupplierInvoiceNumber: r.key("supplier_invoice_number"),
			CompanyCode:           r.text("company_code"),
			DocumentType:          r.text("document_type"),
			DocumentDate:          r.date("document_date"),
			ReceiptDate:           r.date("receipt_date"),
			PONumber:              r.text("po_number"),
			Currency:              r.text("currency"),
			VATCode:               r.text("vat_code"),
			PaymentTermsDays:      r.integer("payment_terms_days"),
			DiscountRaw:           r.text("discount_raw"),
			DiscountDays:          r.integer("discount_days"),
			CostCenter:            r.text("cost_center"),
			Text:                  r.text("text"),
			Lines:                 []model.APInvoiceLine{},
		}
		gross, grossCur := r.money("gross_amount", scaleAmount)
		vat, vatCur := r.money("vat_amount", scaleAmount)
		i.GrossAmount, i.VATAmount = gross, vat
		r.checkCurrency("gross_amount", grossCur, i.Currency)
		r.checkCurrency("vat_amount", vatCur, i.Currency)
		if i.DiscountRaw != "" {
			// BUILD-SPEC 5.1: the source specification calls this field an
			// amount in its record layout and a percentage rate in its
			// glossary, and the example value is compatible with both. The
			// value is carried verbatim and never converted; the warning is how
			// the ambiguity reaches a human.
			r.warn(importer.CodeAmbiguousFieldSemantics, "discount_raw", i.DiscountRaw,
				"the source specification documents this field as an amount in one place and as a percentage rate in another; the value is carried verbatim and never converted")
		}
		lines := r.get("lines")
		if lines.kind == cellArray {
			for n, sub := range lines.arr {
				i.Lines = append(i.Lines, r.invoiceLine(sub, "lines["+strconv.Itoa(n)+"]", i.Currency))
			}
		} else if !lines.empty() {
			r.reject(importer.CodeFieldInvalid, "lines", lines.display(),
				"lines is a list of invoice line objects")
		}
		return i.Key(), i, len(i.Lines)

	case model.DatasetInvoiceLine:
		rec := InvoiceLineRecord{
			SupplierNumber:        r.key("supplier_number"),
			SupplierInvoiceNumber: r.key("supplier_invoice_number"),
		}
		rec.APInvoiceLine = r.lineFields(r.text("currency"))
		return rec.Key(), rec, 1

	case model.DatasetOutboxAck:
		a := AckRecord{ProposalID: r.key("proposal_id")}
		a.Status = r.text("status")
		a.ExternalDocumentNumber = r.text("external_document_number")
		a.ExternalRevision = r.integer("external_revision")
		a.ExternalFiscalYear = r.integer("external_fiscal_year")
		a.ExternalPostingDate = r.date("external_posting_date")
		a.IdempotencyKey = r.text("idempotency_key")
		a.RunID = r.text("run_id")
		a.PostedAt = r.text("posted_at")
		a.Attempts = r.integer("attempts")
		a.HTTPStatus = r.integer("http_status")
		a.IdempotencyReplay = r.boolean("idempotency_replay")
		a.Error = r.text("error")
		a.Reason = r.text("reason")
		return a.Key(), a, 0

	default:
		r.reject(importer.CodeFieldInvalid, "dataset", ds.String(),
			"the manifest declared a dataset this profile does not carry")
		return "", struct{}{}, 0
	}
}

// invoiceLine reads one embedded invoice line out of a nested record, prefixing
// every finding's field with the line's path so a diagnostic points at
// lines[2].uom rather than at uom.
func (r *fieldReader) invoiceLine(sub record, path, currency string) model.APInvoiceLine {
	nested := newFieldReader(sub, r.dial, r.ordinal)
	nested.line = r.line
	if sub.line > 0 {
		nested.line = sub.line
	}
	line := nested.lineFields(currency)
	nested.unknownFields()
	nested.prefixFields(path)
	r.diags = append(r.diags, nested.diags...)
	if nested.blocked {
		r.blocked = true
	}
	return line
}

// lineFields reads the fields common to an embedded and a standalone invoice
// line. Findings carry the plain field name; an embedded line's caller prefixes
// them with the line's path once, after the unknown-field pass, so no path is
// ever applied twice.
func (r *fieldReader) lineFields(currency string) model.APInvoiceLine {
	l := model.APInvoiceLine{
		LineNo:     r.key("line_no"),
		GLAccount:  r.text("gl_account"),
		CostCenter: r.text("cost_center"),
		Quantity:   r.decimal("quantity", scaleQuantity),
		UoM:        r.text("uom"),
		TaxCode:    r.text("tax_code"),
	}
	price, priceCur := r.money("unit_price", scaleUnitPrice)
	amount, amountCur := r.money("line_amount", scaleAmount)
	l.UnitPrice, l.LineAmount = price, amount
	r.checkCurrency("unit_price", priceCur, currency)
	r.checkCurrency("line_amount", amountCur, currency)
	if l.CostCenter == "" {
		// BUILD-SPEC 5.2: whether an empty line cost center inherits the
		// header's is undefined in the source specification, and the sending
		// system allows both readings. It is left empty and surfaced. Inheriting
		// it silently books a cost to a center nobody chose.
		r.warn(importer.CodeAmbiguousFieldSemantics, "cost_center", "",
			"the source specification does not define whether an empty line cost center inherits the document's; it is left empty rather than inherited")
	}
	return l
}

// prefixFields rewrites the field names of the findings this reader produced so
// they carry the nested path.
func (r *fieldReader) prefixFields(path string) {
	for i := range r.diags {
		f := r.diags[i].Field
		if f == "" || len(f) > len(path) && f[:len(path)] == path {
			continue
		}
		r.diags[i].Field = fieldPath(path, f)
	}
}

// fieldPath joins a nested path and a field name.
func fieldPath(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

// checkCurrency reports a monetary value whose own currency contradicts its
// record's. It is a rejection, not a warning: an amount labeled EUR in a CHF
// invoice is not a formatting question, and picking either one would be picking
// which of two contradictory statements about money to believe.
func (r *fieldReader) checkCurrency(field, valueCurrency, recordCurrency string) {
	if valueCurrency == "" || recordCurrency == "" || valueCurrency == recordCurrency {
		return
	}
	r.reject(importer.CodeFieldInvalid, field, valueCurrency,
		"the currency of this amount contradicts the currency of the record")
}

// validate runs the model's own validators for the dataset. The validators are
// the single definition of what a well-formed record is, so a supplier delivered
// by file and the same supplier delivered over HTTP are accepted or rejected
// identically.
func validate(ds model.Dataset, payload any) model.FieldErrors {
	switch v := payload.(type) {
	case model.Supplier:
		return v.Validate()
	case model.CostCenter:
		return v.Validate()
	case model.PurchaseOrder:
		return v.Validate()
	case model.PurchaseOrderLine:
		return v.Validate()
	case model.FxRate:
		return v.Validate()
	case model.APInvoice:
		return v.Validate()
	case InvoiceLineRecord:
		return validateInvoiceLineRecord(v)
	case AckRecord:
		return validateAckRecord(v)
	default:
		return nil
	}
}

// validateInvoiceLineRecord validates a standalone invoice line, including the
// invoice key components a line delivered on its own has to carry.
func validateInvoiceLineRecord(r InvoiceLineRecord) model.FieldErrors {
	var errs model.FieldErrors
	if r.SupplierNumber == "" {
		errs = append(errs, model.FieldError{Code: model.CodeKeyMissing, Field: "supplier_number",
			Message: "a standalone invoice line has to name the invoice it belongs to"})
	}
	if r.SupplierInvoiceNumber == "" {
		errs = append(errs, model.FieldError{Code: model.CodeKeyMissing, Field: "supplier_invoice_number",
			Message: "a standalone invoice line has to name the invoice it belongs to"})
	}
	return append(errs, r.APInvoiceLine.Validate()...)
}

// validateAckRecord validates an ack. The status set is the store's, and a
// posted ack has to name the ERP document it created: an ack that closes the
// chain without a document number cannot be reconciled and cannot detect a
// second posting of the same invoice.
func validateAckRecord(r AckRecord) model.FieldErrors {
	var errs model.FieldErrors
	if r.ProposalID == "" {
		errs = append(errs, model.FieldError{Code: model.CodeKeyMissing, Field: "proposal_id",
			Message: "an ack names the proposal it acknowledges"})
	}
	switch r.Status {
	case store.AckPosted:
		if r.ExternalDocumentNumber == "" {
			errs = append(errs, model.FieldError{Code: model.CodeFieldRequired, Field: "external_document_number",
				Message: "a posted ack names the ERP document number it created"})
		}
	case store.AckRejected, store.AckFailed, store.AckSkipped:
	case "":
		errs = append(errs, model.FieldError{Code: model.CodeFieldRequired, Field: "status",
			Message: "an ack carries a status: posted, rejected, failed or skipped"})
	default:
		errs = append(errs, model.FieldError{Code: model.CodeEnumUnknown, Field: "status",
			Message: "an ack status is posted, rejected, failed or skipped"})
	}
	if r.ExternalPostingDate != "" && !model.IsValidDate(r.ExternalPostingDate) {
		errs = append(errs, model.FieldError{Code: model.CodeDateFormat, Field: "external_posting_date",
			Message: "expected a calendar date as YYYY-MM-DD"})
	}
	return errs
}
