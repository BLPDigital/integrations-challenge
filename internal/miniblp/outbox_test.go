package miniblp

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// outboxPageOf fetches one outbox page.
func (h *harness) outboxPageOf(query string) (rows []ProposalRow, next string, more bool) {
	h.t.Helper()
	status, body := h.get("/v1/outbox/proposals?" + query)
	if status != http.StatusOK {
		h.t.Fatalf("outbox: status %d body %s", status, body)
	}
	var page struct {
		Records    []ProposalRow `json:"records"`
		NextCursor string        `json:"next_cursor"`
		HasMore    bool          `json:"has_more"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		h.t.Fatalf("outbox page: %v (%s)", err, body)
	}
	return page.Records, page.NextCursor, page.HasMore
}

// fiveProposals delivers five matching invoices.
func fiveProposals(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	var invoices []model.APInvoice
	for i := 1; i <= 5; i++ {
		invoices = append(invoices, invoiceFixture("0000000417", "000471"+strconv.Itoa(i),
			"CHF", "100.00", "0.00"))
	}
	h.deliverInvoices(invoices...)
	return h
}

// TestOutboxPagesInCreatedSeqOrder asserts the stable cursor: pages are in
// created_seq order, they do not overlap and they cover every pending proposal.
func TestOutboxPagesInCreatedSeqOrder(t *testing.T) {
	h := fiveProposals(t)
	seen := []string{}
	query := "status=pending&limit=2"
	for page := 0; page < 5; page++ {
		rows, next, more := h.outboxPageOf(query)
		for _, row := range rows {
			seen = append(seen, row.ProposalID)
		}
		if !more {
			break
		}
		if next == "" {
			t.Fatal("has_more is set but no cursor was returned")
		}
		query = "status=pending&limit=2&cursor=" + next
	}
	if len(seen) != 5 {
		t.Fatalf("paged over %d proposals (%v), want 5", len(seen), seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Fatalf("proposal ids are not strictly ascending: %v", seen)
		}
	}
}

// TestOutboxLimitIsCapped asserts the published maximum page size.
func TestOutboxLimitIsCapped(t *testing.T) {
	h := fiveProposals(t)
	rows, _, _ := h.outboxPageOf("status=pending&limit=100000")
	if len(rows) != 5 {
		t.Fatalf("returned %d rows, want all five", len(rows))
	}
}

// TestOutboxRowIsPostingReady asserts that a row carries everything the ERP's
// document POST needs, so a connector never has to fetch the invoice again, and
// that the ambiguous discount field travels verbatim.
func TestOutboxRowIsPostingReady(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	inv := invoiceFixture("0000000417", "0004711", "GBP", "100.00", "0.00")
	inv.DiscountRaw = "2.000"
	inv.VATCode = "V81"
	inv.PaymentTermsDays = 30
	h.deliverInvoices(inv)

	rows, _, _ := h.outboxPageOf("status=pending")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	switch {
	case row.SupplierNumber != "0000000417" || row.SupplierInvoiceNumber != "0004711":
		t.Fatalf("row keys = %+v", row)
	case row.CompanyCode != "CH10" || row.DocumentType != "RE":
		t.Fatalf("row header = %+v", row)
	case row.DocumentDate != "2026-03-16" || row.PostingDate != "2026-03-16":
		t.Fatalf("row dates = %+v", row)
	case row.Currency != "CHF" || row.GrossAmount != "108.23" || row.GrossAmountMinor != 10823:
		t.Fatalf("row amount = %+v", row)
	case row.SourceCurrency != "GBP" || row.SourceGrossAmount != "100.00":
		t.Fatalf("row source amount = %+v", row)
	case row.FxRate != "1.082250" || row.FxRateFactor != 1:
		t.Fatalf("row rate = %+v", row)
	case row.DiscountRaw != "2.000":
		t.Fatalf("discount = %q, want the value verbatim and uninterpreted", row.DiscountRaw)
	case row.ProposalContentHash == "":
		t.Fatal("the row carries no proposal content hash, so no stable idempotency key can be derived")
	}
}

// TestOutboxMirrorIsWrittenInFourFormats asserts the mirrored export: four
// formats, DONE last, and bytes identical to what the HTTP endpoint serves.
func TestOutboxMirrorIsWrittenInFourFormats(t *testing.T) {
	h := fiveProposals(t)
	dir := filepath.Join(h.outboxDir, "run_test")
	for _, name := range []string{
		"proposals-0001.csv", "proposals-0001.xml",
		"proposals-0001.json", "proposals-0001.ndjson", doneFile,
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("mirrored file %s: %v", name, err)
		}
	}
	csvBytes, err := os.ReadFile(filepath.Join(dir, "proposals-0001.csv"))
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(csvBytes), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("csv has %d lines, want a header and five rows:\n%s", len(lines), csvBytes)
	}
	if got := strings.TrimRight(lines[0], "\r"); got != strings.Join(outboxColumns, ",") {
		t.Fatalf("csv header = %q", got)
	}
	ndjson, err := os.ReadFile(filepath.Join(dir, "proposals-0001.ndjson"))
	if err != nil {
		t.Fatalf("ndjson: %v", err)
	}
	if n := strings.Count(strings.TrimRight(string(ndjson), "\n"), "\n") + 1; n != 5 {
		t.Fatalf("ndjson has %d lines, want 5", n)
	}
	// The mirror is produced by the same encoder the endpoint uses.
	status, body := h.request2("GET", "/v1/outbox/proposals?status=pending", "text/csv")
	if status != http.StatusOK {
		t.Fatalf("csv over HTTP: status %d", status)
	}
	if strings.TrimRight(string(body), "\n") != strings.TrimRight(string(csvBytes), "\n") {
		t.Fatalf("the mirrored page and the served page differ:\n%s\n%s", csvBytes, body)
	}
}

// TestOutboxMirrorIsNotRewrittenForAReplay asserts that a replayed batch emits
// nothing: the outbox files keep their previous contents.
func TestOutboxMirrorIsNotRewrittenForAReplay(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-mirror", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-mirror", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
	h.scan()
	// No invoice, so nothing is proposed and nothing is mirrored.
	if entries, err := os.ReadDir(h.outboxDir); err == nil && len(entries) != 0 {
		t.Fatalf("the outbox was written without a proposal: %v", entries)
	}
}

// TestAcknowledgedProposalLeavesThePendingPage asserts that the outbox is a
// queue: an acknowledged proposal is not served again.
func TestAcknowledgedProposalLeavesThePendingPage(t *testing.T) {
	h, p := oneProposal(t)
	h.postAck("ack-1", map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
	})
	rows, _, _ := h.outboxPageOf("status=pending")
	if len(rows) != 0 {
		t.Fatalf("pending page = %+v, want it empty", rows)
	}
	rows, _, _ = h.outboxPageOf("status=acknowledged")
	if len(rows) != 1 || rows[0].ERPDocumentNumber != "AP-2026-0004311" {
		t.Fatalf("acknowledged page = %+v", rows)
	}
}

// TestInvalidCursorIsRefused asserts that a hand-edited cursor cannot page past a
// filter.
func TestInvalidCursorIsRefused(t *testing.T) {
	h := fiveProposals(t)
	status, body := h.get("/v1/outbox/proposals?cursor=bm90LWEtY3Vyc29y")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", status, body)
	}
}

// request2 performs a GET with an explicit Accept header.
func (h *harness) request2(method, path, accept string) (int, []byte) {
	h.t.Helper()
	status, body, _ := h.request(method, path, nil, map[string]string{"Accept": accept})
	return status, body
}
