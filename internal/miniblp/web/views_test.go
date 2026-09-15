package web

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// TestDashboardShowsTheClosedExceptionCodeSet asserts that the histogram walks
// the published set, so a code with no entries is visible as clear rather than
// absent. A reviewer must be able to tell "nothing raised this" from "this code
// does not exist".
func TestDashboardShowsTheClosedExceptionCodeSet(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	body := f.body("/ui/", http.StatusOK)
	for _, code := range miniblp.ExceptionCodes() {
		if !strings.Contains(body, code) {
			t.Errorf("the dashboard does not name the published code %s", code)
		}
	}
	contains(t, "/ui/", body, "clear", "open")
}

// TestExceptionQueueDefaultsToOpen asserts the default filter and that the other
// two are one link away.
func TestExceptionQueueDefaultsToOpen(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	open := f.body("/ui/exceptions", http.StatusOK)
	contains(t, "/ui/exceptions", open,
		"Open entries", "EXC_SUPPLIER_UNKNOWN", "state=resolved", "state=all")

	resolved := f.body("/ui/exceptions?state=resolved", http.StatusOK)
	contains(t, "/ui/exceptions?state=resolved", resolved, "Resolved entries")
	if strings.Contains(resolved, "EXC_SUPPLIER_UNKNOWN</h2>") {
		t.Fatal("an open exception was rendered under the resolved filter")
	}

	all := f.body("/ui/exceptions?state=all", http.StatusOK)
	contains(t, "/ui/exceptions?state=all", all, "open and resolved", "EXC_SUPPLIER_UNKNOWN")
}

// TestExceptionSearch asserts the queue search reaches the subject, the code and
// the message.
func TestExceptionSearch(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	for _, needle := range []string{fixtureBadKey, "supplier_unknown", "batch-two"} {
		path := "/ui/exceptions?q=" + url.QueryEscape(needle)
		contains(t, path, f.body(path, http.StatusOK), "EXC_SUPPLIER_UNKNOWN")
	}
	path := "/ui/exceptions?q=" + url.QueryEscape("nothing-matches-this")
	body := f.body(path, http.StatusOK)
	contains(t, path, body, "this twin has raised no exception matching the filter")
}

// TestInvoiceRecordCarriesItsProposalAndExceptions asserts the two sections that
// make an invoice's record the place a reviewer starts from.
func TestInvoiceRecordCarriesItsProposalAndExceptions(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	matched := "/ui/record?dataset=invoice&key=" + url.QueryEscape(invoiceKey())
	body := f.body(matched, http.StatusOK)
	contains(t, matched, body, "Proposals of this invoice", f.proposalID(), fixtureERPDocument)

	broken := "/ui/record?dataset=invoice&key=" +
		url.QueryEscape(fixtureBadKey+"|"+fixtureBadInvoice)
	body = f.body(broken, http.StatusOK)
	contains(t, broken, body, "Exceptions about this invoice", "EXC_SUPPLIER_UNKNOWN")
	if strings.Contains(body, "Proposals of this invoice") {
		t.Fatal("an unmatched invoice was given a proposal section")
	}
}

// TestRawBytesTruncateAtTheConfiguredCap asserts that a 20 MiB legacy file
// cannot be pushed into a browser whole, and that the page says so.
func TestRawBytesTruncateAtTheConfiguredCap(t *testing.T) {
	f := newFixture(t, func(cfg *Config) { cfg.MaxRawBytes = 64 })
	f.seed()
	sha := f.sourceSHA("supplier", fixtureSupplier)
	path := "/ui/raw/" + sha
	body := f.body(path, http.StatusOK)
	contains(t, path, body, "Truncated to the first 64 bytes of")
	// The verified hash is computed over all the bytes, not the shown ones.
	contains(t, path, body, sha)
}

// TestProposalSearchReachesEveryIdentifier asserts the four keys a reviewer may
// arrive with.
func TestProposalSearchReachesEveryIdentifier(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	p := f.proposal()
	for _, needle := range []string{p.ProposalID, displayKey(p.InvoiceKey),
		p.Ack.IdempotencyKey, p.Ack.ExternalDocumentNumber} {
		path := "/ui/proposals?q=" + url.QueryEscape(needle)
		contains(t, path, f.body(path, http.StatusOK), p.ProposalID)
	}
	path := "/ui/proposals?q=" + url.QueryEscape("prp_no_such_proposal")
	contains(t, path, f.body(path, http.StatusOK), "no proposal matches")
}

// TestFxRateAndFactorAreShownSeparately asserts that a proposal's rate and its
// factor are two columns. A factor of 100 read as 1 is a hundredfold posting
// error, and this view is where a reviewer catches it.
func TestFxRateAndFactorAreShownSeparately(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	body := f.body("/ui/proposals", http.StatusOK)
	contains(t, "/ui/proposals", body, "FX rate", "Factor", "1.082250", "108.23 CHF",
		"100.00 GBP")
}

// TestTheStoresOwnSegmentsBrowseToo asserts that the two segments the store
// keeps for itself - proposals and exceptions - are browsable like any dataset,
// so a reviewer who arrives from the dataset index is never told a segment
// exists and then refused it.
func TestTheStoresOwnSegmentsBrowseToo(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	path := "/ui/datasets/" + store.DatasetProposal
	body := f.body(path, http.StatusOK)
	contains(t, path, body, "Natural key", "Proposal", "Amount", f.proposalID(), "108.23 CHF")

	path = "/ui/datasets/" + store.DatasetException
	body = f.body(path, http.StatusOK)
	contains(t, path, body, "Subject", "Code", "EXC_SUPPLIER_UNKNOWN")
}

// TestScalarFieldNamesIsTheDeterministicFallback asserts the column fallback for
// a dataset with no specification: ascending name order, capped, never map
// order.
func TestScalarFieldNamesIsTheDeterministicFallback(t *testing.T) {
	fields := map[string]string{"zulu": "1", "alpha": "2", "mike": "3", "bravo": "4"}
	got := scalarFieldNames(fields, 3)
	want := []string{"alpha", "bravo", "mike"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if all := scalarFieldNames(fields, 0); len(all) != 4 {
		t.Fatalf("an uncapped call returned %v", all)
	}
}

// TestBatchWithoutAReceiptSaysSo asserts the one batch state the file channel
// cannot produce but the REST channel can: open, hence no receipt yet.
func TestBatchWithoutAReceiptSaysSo(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	// A batch directory with no manifest is pending on its first sighting and
	// reported as MANIFEST_MISSING on the second, which is a batch-level code a
	// reviewer has to be able to see.
	dir := f.inboxDir + "/incoming/batch-no-manifest"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f.scan()
	f.scan()
	body := f.body("/ui/batches/", http.StatusOK)
	contains(t, "/ui/batches/", body, "batch-no-manifest", "MANIFEST_MISSING", "rejected")
}

// tileValue extracts the value of the tile with the given label from a rendered
// page. It exists so a test can assert a number rather than the presence of a
// word.
func tileValue(t *testing.T, body, label string) string {
	t.Helper()
	needle := ">" + label + "</a>"
	i := strings.Index(body, needle)
	if i < 0 {
		needle = ">" + label + "</span>"
		i = strings.Index(body, needle)
	}
	if i < 0 {
		t.Fatalf("no tile labeled %q", label)
	}
	rest := body[i:]
	const open = `class="value">`
	j := strings.Index(rest, open)
	if j < 0 {
		t.Fatalf("tile %q has no value", label)
	}
	rest = rest[j+len(open):]
	k := strings.Index(rest, "<")
	if k < 0 {
		t.Fatalf("tile %q has no value", label)
	}
	return rest[:k]
}

// TestInvoiceFunnelCounts asserts the projection itself, on a twin whose state
// is known: two invoices arrived, one matched and was posted, one is stuck on an
// unknown supplier.
func TestInvoiceFunnelCounts(t *testing.T) {
	f := newFixture(t, nil)
	f.seed()
	body := f.body("/ui/", http.StatusOK)
	tests := []struct {
		label string
		want  string
	}{
		{stageIngested, "2"},
		{stageMatched, "1"},
		{stageProposed, "0"},
		{statePosted, "1"},
		{stageException, "1"},
	}
	for _, tc := range tests {
		if got := tileValue(t, body, tc.label); got != tc.want {
			t.Errorf("funnel stage %q = %q, want %q", tc.label, got, tc.want)
		}
	}
	if got := tileValue(t, body, "acknowledged"); got != "1" {
		t.Errorf("proposal state acknowledged = %q, want 1", got)
	}
	if got := tileValue(t, body, "invoice"); got != "2" {
		t.Errorf("invoice record count = %q, want 2", got)
	}
}
