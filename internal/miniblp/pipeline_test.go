package miniblp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// masterDataFiles is the canonical master data of the end-to-end fixture: one
// supplier, one cost center and one GBP rate whose conversion is a graded amount.
var masterDataFiles = map[string]string{
	"suppliers.csv": "supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\n" +
		"0000000417,Steinbach Industrie AG,CH,CHF,CH9300762011623852957,CHE-123.456.789,30,false,118422\n",
	"cost_centers.csv": "code,name,company_code,valid_from,valid_to,blocked\n" +
		"CC-2200,Werk Wil,CH10,2026-01-01,,false\n",
	"fx_rates.csv": "base,quote,valid_from,rate_type,valid_to,rate,rate_factor,sequence,status\n" +
		"GBP,CHF,2026-03-01,DAILY,,1.082250,1,1,ACTIVE\n",
}

// invoiceWithEmbeddedLines is one invoice whose lines travel inside it, the JSON
// and XML shape.
const invoiceWithEmbeddedLines = `[{"supplier_number":"0000000417","supplier_invoice_number":"0004711",
 "company_code":"CH10","document_type":"RE","document_date":"2026-03-16","receipt_date":"2026-03-17",
 "po_number":"","currency":"GBP","gross_amount":"100.00","vat_amount":"0.00","vat_code":"V81",
 "payment_terms_days":30,"discount_raw":"2.000","discount_days":10,"cost_center":"CC-2200","text":"",
 "lines":[{"line_no":"00010","gl_account":"400000","cost_center":"CC-2200","quantity":"1.000",
 "uom":"EA","unit_price":"100.00","line_amount":"100.00","tax_code":"V81"}]}]`

// invoiceWithoutLines is the same invoice with no embedded lines, the CSV shape,
// whose lines arrive as their own dataset.
const invoiceWithoutLines = `[{"supplier_number":"0000000417","supplier_invoice_number":"0004711",
 "company_code":"CH10","document_type":"RE","document_date":"2026-03-16","receipt_date":"2026-03-17",
 "po_number":"","currency":"GBP","gross_amount":"100.00","vat_amount":"0.00","vat_code":"V81",
 "payment_terms_days":30,"discount_raw":"2.000","discount_days":10,"cost_center":"CC-2200","text":""}]`

// invoiceLinesCSV delivers the same line as a standalone invoice_line record.
const invoiceLinesCSV = "supplier_number,supplier_invoice_number,line_no,gl_account,cost_center,quantity,uom,unit_price,line_amount,tax_code,currency\n" +
	"0000000417,0004711,00010,400000,CC-2200,1.000,EA,100.00,100.00,V81,GBP\n"

// TestEndToEndFileChannelEmitsAProposal drives the whole file channel: master
// data and an invoice arrive in one batch, the matching engine runs on the scan,
// the proposal lands on the outbox and the receipt names it.
func TestEndToEndFileChannelEmitsAProposal(t *testing.T) {
	h := newHarness(t, nil)
	files := map[string]string{"invoices.json": invoiceWithEmbeddedLines}
	for k, v := range masterDataFiles {
		files[k] = v
	}
	m := fileManifest("batch-e2e",
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1))
	h.writeBatchDir("batch-e2e", m, m2(m, files))

	rep := h.scan()
	if len(rep.Batches) != 1 || rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("scan = %+v", rep.Batches)
	}
	if rep.Proposals != 1 || rep.Exceptions != 0 {
		t.Fatalf("scan report = %+v, want one proposal and no exception", rep)
	}
	rows, _, _ := h.outboxPageOf("status=pending")
	if len(rows) != 1 {
		t.Fatalf("outbox = %+v", rows)
	}
	row := rows[0]
	if row.GrossAmountMinor != 10823 || row.Currency != "CHF" {
		t.Fatalf("converted amount = %s %s, want CHF 108.23", row.Currency, row.GrossAmount)
	}
	if row.SourceCurrency != "GBP" || row.SourceGrossAmount != "100.00" {
		t.Fatalf("source amount = %s %s", row.SourceCurrency, row.SourceGrossAmount)
	}
	if row.DiscountRaw != "2.000" {
		t.Fatalf("discount = %q, want the raw value", row.DiscountRaw)
	}
	// The audit chain closes from the invoice back to the delivered file.
	status, body := h.adminGet("/admin/v1/audit/" + pathKey(key("0000000417", "0004711")))
	if status != http.StatusOK {
		t.Fatalf("audit status = %d body %s", status, body)
	}
	if !strings.Contains(string(body), "invoices.json") {
		t.Fatalf("the audit chain does not name the source file:\n%s", body)
	}
}

// TestEmbeddedAndSeparateInvoiceLinesMatchIdentically asserts that the two shapes
// of one invoice - lines inside the invoice, and lines as their own dataset -
// produce the same matching decision.
//
// The shapes exist because a CSV cell cannot hold a list. If the totals rule saw
// only the embedded lines, a CSV sender's invoices would all fail the header
// total check, and the wire format would decide whether an invoice can be posted.
func TestEmbeddedAndSeparateInvoiceLinesMatchIdentically(t *testing.T) {
	embedded := newHarness(t, nil)
	files := map[string]string{"invoices.json": invoiceWithEmbeddedLines}
	for k, v := range masterDataFiles {
		files[k] = v
	}
	m := fileManifest("batch-embedded",
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1))
	embedded.writeBatchDir("batch-embedded", m, m2(m, files))
	if rep := embedded.scan(); rep.Proposals != 1 {
		t.Fatalf("embedded shape: proposals = %d, exceptions = %d", rep.Proposals, rep.Exceptions)
	}

	separate := newHarness(t, nil)
	files2 := map[string]string{
		"invoices.json":     invoiceWithoutLines,
		"invoice_lines.csv": invoiceLinesCSV,
	}
	for k, v := range masterDataFiles {
		files2[k] = v
	}
	m2nd := fileManifest("batch-separate",
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoice_lines.csv", "invoice_line", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1))
	separate.writeBatchDir("batch-separate", m2nd, m2(m2nd, files2))
	rep := separate.scan()
	if rep.Proposals != 1 || rep.Exceptions != 0 {
		t.Fatalf("separate shape: proposals = %d, exceptions = %d (%v)",
			rep.Proposals, rep.Exceptions, separate.openPairs())
	}

	a := embedded.proposalForInvoice(key("0000000417", "0004711"))
	b := separate.proposalForInvoice(key("0000000417", "0004711"))
	if a.Amount != b.Amount || a.SourceAmount != b.SourceAmount {
		t.Fatalf("the two shapes matched differently:\n %+v\n %+v", a.Amount, b.Amount)
	}
	if a.ProposalContentHash != b.ProposalContentHash {
		t.Fatalf("proposal content hashes differ: %q and %q",
			a.ProposalContentHash, b.ProposalContentHash)
	}
}

// TestEndToEndRESTChannelEmitsTheSameProposal asserts that the REST channel
// reaches the identical proposal, which is what makes the two channels
// interchangeable for a connector that has to choose one.
func TestEndToEndRESTChannelEmitsTheSameProposal(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-e2e-rest"))
	for i, chunk := range []struct {
		dataset string
		body    string
		mime    string
	}{
		{"supplier", masterDataFiles["suppliers.csv"], "text/csv"},
		{"cost_center", masterDataFiles["cost_centers.csv"], "text/csv"},
		{"fx_rate", masterDataFiles["fx_rates.csv"], "text/csv"},
		{"invoice", invoiceWithEmbeddedLines, "application/json"},
	} {
		status, body := h.rawChunk(ref, chunk.dataset, 1, "k"+string(rune('a'+i)), chunk.mime,
			[]byte(chunk.body))
		if status != http.StatusOK {
			t.Fatalf("%s chunk: status %d body %s", chunk.dataset, status, body)
		}
	}
	status, receipt := h.commit(ref)
	if status != http.StatusOK {
		t.Fatalf("commit status = %d", status)
	}
	if len(receipt.Proposals) != 1 {
		t.Fatalf("receipt proposals = %v", receipt.Proposals)
	}
	if len(receipt.Exceptions) != 0 {
		t.Fatalf("receipt exceptions = %v", receipt.Exceptions)
	}
	p := h.proposalForInvoice(key("0000000417", "0004711"))
	if p.Amount.AmountMinor != 10823 {
		t.Fatalf("amount = %d, want 10823", p.Amount.AmountMinor)
	}
	if p.Status != store.ProposalPending {
		t.Fatalf("status = %q", p.Status)
	}
	if p.Warnings == nil {
		t.Fatal("warnings must be a list, never null")
	}
}

// TestFileAndRESTProposalsAreIdentical closes the loop on channel blindness for
// the whole pipeline: the same data through the two channels produces the same
// digest, the same proposal id and the same proposal content hash, so an
// idempotency key derived from a proposal is stable across channels.
func TestFileAndRESTProposalsAreIdentical(t *testing.T) {
	byFile := newHarness(t, nil)
	files := map[string]string{"invoices.json": invoiceWithEmbeddedLines}
	for k, v := range masterDataFiles {
		files[k] = v
	}
	m := fileManifest("batch-both",
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1))
	byFile.writeBatchDir("batch-both", m, m2(m, files))
	byFile.scan()

	byREST := newHarness(t, nil)
	_, ref := byREST.openBatch(restManifest("batch-both-rest"))
	byREST.rawChunk(ref, "supplier", 1, "k1", "text/csv", []byte(masterDataFiles["suppliers.csv"]))
	byREST.rawChunk(ref, "cost_center", 1, "k2", "text/csv", []byte(masterDataFiles["cost_centers.csv"]))
	byREST.rawChunk(ref, "fx_rate", 1, "k3", "text/csv", []byte(masterDataFiles["fx_rates.csv"]))
	byREST.rawChunk(ref, "invoice", 1, "k4", "application/json", []byte(invoiceWithEmbeddedLines))
	byREST.commit(ref)

	if byFile.digest() != byREST.digest() {
		t.Fatalf("digests differ:\n file %s\n rest %s", byFile.digest(), byREST.digest())
	}
	a := byFile.proposalForInvoice(key("0000000417", "0004711"))
	b := byREST.proposalForInvoice(key("0000000417", "0004711"))
	if a.ProposalID != b.ProposalID {
		t.Fatalf("proposal ids differ: %q and %q", a.ProposalID, b.ProposalID)
	}
	if a.ProposalContentHash != b.ProposalContentHash {
		t.Fatalf("content hashes differ: %q and %q", a.ProposalContentHash, b.ProposalContentHash)
	}
	if a.Amount != b.Amount {
		t.Fatalf("amounts differ: %+v and %+v", a.Amount, b.Amount)
	}
}

// TestInvoiceLineChangeRematchesItsInvoice asserts that a line delivered on its
// own marks its invoice for matching: the totals rule reads the lines, so a line
// that changes changes the decision.
func TestInvoiceLineChangeRematchesItsInvoice(t *testing.T) {
	h := newHarness(t, nil)
	files := map[string]string{
		"invoices.json": invoiceWithoutLines,
	}
	for k, v := range masterDataFiles {
		files[k] = v
	}
	m := fileManifest("batch-lines-1",
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1))
	h.writeBatchDir("batch-lines-1", m, m2(m, files))
	rep := h.scan()
	// With no lines at all the header total cannot be the sum of the lines.
	if rep.Exceptions != 1 || rep.Proposals != 0 {
		t.Fatalf("first scan = %+v, want the totals mismatch", rep)
	}
	if got := h.openPairs(); len(got) != 1 ||
		got[0] != key("0000000417", "0004711")+"|"+ExcTotalsMismatch {
		t.Fatalf("open queue = %v", got)
	}

	m2nd := fileManifest("batch-lines-2", fileEntry("invoice_lines.csv", "invoice_line", "csv", 1))
	h.writeBatchDir("batch-lines-2", m2nd,
		m2(m2nd, map[string]string{"invoice_lines.csv": invoiceLinesCSV}))
	rep = h.scan()
	if rep.Proposals != 1 {
		t.Fatalf("second scan = %+v, want the invoice matched once its line arrived", rep)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("open queue = %v, want the exception resolved", got)
	}
	if _, ok := h.srv.Store().Get(model.DatasetInvoiceLine.String(),
		model.InvoiceLineKey("0000000417", "0004711", "00010")); !ok {
		t.Fatal("the standalone line was not stored")
	}
}
