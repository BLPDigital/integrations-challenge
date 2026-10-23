package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/all"
	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// adminTokenForTests is the admin credential the fixtures use.
const adminTokenForTests = "test-admin-token"

// The fixture's fixed keys, so an assertion can name what it expects.
const (
	fixtureSupplier   = "0000000417"
	fixtureInvoiceNo  = "0004711"
	fixtureBadInvoice = "0004712"
	fixtureBadKey     = "0000000999"
	// fixtureXSS is delivered as a supplier name. Nothing in this interface may
	// ever render it as markup.
	fixtureXSS = "Steinbach <script>alert(1)</script> AG"
)

// A fixture is a real twin with a real store, driven through the real file
// channel, plus the UI bound to it.
//
// The tests use the twin itself rather than a stub: a UI is worth nothing if it
// renders a fake, and the file channel needs no token, no port and no client, so
// the whole pipeline - manifest, checksums, importer, matching engine, receipt,
// proposal, ack - runs inside the test.
type fixture struct {
	t        *testing.T
	srv      *miniblp.Server
	ui       *UI
	inboxDir string
}

// newFixture starts a twin with the UI bound to it and nothing ingested yet.
func newFixture(t *testing.T, mutate func(*Config)) *fixture {
	t.Helper()
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	cfg := Config{AdminToken: adminTokenForTests}
	if mutate != nil {
		mutate(&cfg)
	}
	ui, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv, err := miniblp.New(miniblp.Config{
		DataDir:    filepath.Join(root, "data"),
		InboxDir:   inbox,
		OutboxDir:  filepath.Join(root, "outbox"),
		Scenario:   "S0",
		Seed:       20260416,
		AdminToken: adminTokenForTests,
		RunID:      "run_test",
		Log:        io.Discard,
		UI:         ui,
	})
	if err != nil {
		t.Fatalf("miniblp.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ui.Bind(srv)
	return &fixture{t: t, srv: srv, ui: ui, inboxDir: inbox}
}

// get renders one view and returns the recorded response.
func (f *fixture) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	f.ui.ServeHTTP(rec, req)
	return rec
}

// body renders one view, asserts the status and returns the body.
func (f *fixture) body(path string, want int) string {
	f.t.Helper()
	rec := f.get(path)
	if rec.Code != want {
		f.t.Fatalf("GET %s status = %d, want %d\n%s", path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

// fileEntry is one manifest file entry. The sha256 and the record count are
// filled in by writeBatch, which is where the bytes are.
func fileEntry(path, dataset, format string, records int) map[string]any {
	return map[string]any{
		"path":         path,
		"dataset":      dataset,
		"format":       format,
		"profile":      miniblp.ProfileCanonical,
		"encoding":     "UTF-8",
		"record_count": records,
	}
}

// writeBatch publishes one file batch the way the contract requires: everything
// into a staging directory, then one rename into incoming.
func (f *fixture) writeBatch(batchID string, entries []map[string]any, files map[string]string) {
	f.t.Helper()
	staging := filepath.Join(f.inboxDir, "incoming", ".staging-"+batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		f.t.Fatalf("staging: %v", err)
	}
	for _, entry := range entries {
		name := entry["path"].(string)
		content, ok := files[name]
		if !ok {
			f.t.Fatalf("no content for %s", name)
		}
		if err := os.WriteFile(filepath.Join(staging, name), []byte(content), 0o644); err != nil {
			f.t.Fatalf("write %s: %v", name, err)
		}
		entry["sha256"] = model.Sha256Hex([]byte(content))
	}
	manifest := map[string]any{
		"manifest_version": "1",
		"batch_id":         batchID,
		"run_id":           "run_test",
		"tenant":           "acme-ch",
		"source_system":    "erp-prod",
		"producer":         "web-test/1.0",
		"mode":             "upsert",
		"on_error":         "continue",
		"files":            entries,
	}
	buf, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		f.t.Fatalf("manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), buf, 0o644); err != nil {
		f.t.Fatalf("manifest: %v", err)
	}
	final := filepath.Join(f.inboxDir, "incoming", batchID)
	if err := os.Rename(staging, final); err != nil {
		f.t.Fatalf("publish: %v", err)
	}
}

// scan drives one inbox scan.
func (f *fixture) scan() miniblp.ScanReport {
	f.t.Helper()
	rep, err := f.srv.Scan()
	if err != nil {
		f.t.Fatalf("scan: %v", err)
	}
	return rep
}

// The fixture's data. One supplier, one cost center, one GBP rate, one invoice
// that matches, and in the second batch an older revision of the supplier plus
// an invoice whose supplier does not exist.
const (
	suppliersV1 = "supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\n" +
		fixtureSupplier + ",Steinbach Industrie AG,CH,CHF,CH9300762011623852957,CHE-123.456.789,30,false,118422\n"
	costCenters = "code,name,company_code,valid_from,valid_to,blocked\n" +
		"CC-2200,Werk Wil,CH10,2026-01-01,,false\n"
	fxRates = "base,quote,valid_from,rate_type,valid_to,rate,rate_factor,sequence,status\n" +
		"GBP,CHF,2026-03-01,DAILY,,1.082250,1,1,ACTIVE\n"
	invoiceOK = `[{"supplier_number":"` + fixtureSupplier + `","supplier_invoice_number":"` + fixtureInvoiceNo + `",
 "company_code":"CH10","document_type":"RE","document_date":"2026-03-16","receipt_date":"2026-03-17",
 "po_number":"","currency":"GBP","gross_amount":"100.00","vat_amount":"0.00","vat_code":"V81",
 "payment_terms_days":30,"discount_raw":"2.000","discount_days":10,"cost_center":"CC-2200","text":"",
 "lines":[{"line_no":"00010","gl_account":"400000","cost_center":"CC-2200","quantity":"1.000",
 "uom":"EA","unit_price":"100.00","line_amount":"100.00","tax_code":"V81"}]}]`
	invoiceUnknownSupplier = `[{"supplier_number":"` + fixtureBadKey + `","supplier_invoice_number":"` + fixtureBadInvoice + `",
 "company_code":"CH10","document_type":"RE","document_date":"2026-03-16","receipt_date":"",
 "po_number":"","currency":"CHF","gross_amount":"50.00","vat_amount":"0.00","vat_code":"V81",
 "payment_terms_days":30,"discount_raw":"","discount_days":0,"cost_center":"CC-2200","text":"",
 "lines":[{"line_no":"00010","gl_account":"400000","cost_center":"CC-2200","quantity":"1.000",
 "uom":"EA","unit_price":"50.00","line_amount":"50.00","tax_code":"V81"}]}]`
	ackHeader = "proposal_id,status,external_document_number,external_revision," +
		"external_fiscal_year,external_posting_date,idempotency_key,run_id,posted_at," +
		"attempts,http_status,idempotency_replay,error,reason\n"
	// fixtureERPDocument is the document number the ack reports.
	fixtureERPDocument = "AP-2026-0004311"
)

// suppliersV2 re-delivers the supplier with a LOWER change_seq and a changed
// name: an older version of the record, delivered later. It is the regression
// the record view has to make obvious, and the name doubles as an escaping
// probe.
var suppliersV2 = "supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\n" +
	fixtureSupplier + ",\"" + fixtureXSS + "\",CH,CHF,CH9300762011623852957,CHE-123.456.789,30,false,118400\n"

// seed drives the three batches the view tests read: the happy path, the
// regression plus an exception, and the ack that closes the chain.
func (f *fixture) seed() {
	f.t.Helper()
	f.writeBatch("batch-one", []map[string]any{
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1),
	}, map[string]string{
		"suppliers.csv":    suppliersV1,
		"cost_centers.csv": costCenters,
		"fx_rates.csv":     fxRates,
		"invoices.json":    invoiceOK,
	})
	if rep := f.scan(); rep.Proposals != 1 || rep.Exceptions != 0 {
		f.t.Fatalf("first scan = %+v, want one proposal and no exception", rep)
	}

	// The second batch carries three things: an older revision of the supplier,
	// an invoice whose supplier does not exist, and a byte-identical
	// re-delivery of the first invoice, which is skipped as unchanged and
	// appends a provenance-only revision.
	f.writeBatch("batch-two", []map[string]any{
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1),
		fileEntry("invoices_again.json", "invoice", "json", 1),
	}, map[string]string{
		"suppliers.csv":       suppliersV2,
		"invoices.json":       invoiceUnknownSupplier,
		"invoices_again.json": invoiceOK,
	})
	if rep := f.scan(); rep.Exceptions != 1 {
		f.t.Fatalf("second scan = %+v, want one exception", rep)
	}

	id := f.proposalID()
	acks := ackHeader + id + ",posted," + fixtureERPDocument + ",1,2026,2026-03-18," +
		"blp:acme-ch:" + id + ":9f21aa,run_test,2026-03-18T10:00:00Z,1,201,false,,\n"
	f.writeBatch("batch-three", []map[string]any{
		fileEntry("acks.csv", "outbox_ack", "csv", 1),
	}, map[string]string{"acks.csv": acks})
	f.scan()
	if p := f.proposal(); p.Status != store.ProposalAcknowledged {
		f.t.Fatalf("proposal status = %q, want acknowledged", p.Status)
	}
}

// proposal returns the fixture's single proposal.
func (f *fixture) proposal() store.Proposal {
	f.t.Helper()
	var out []store.Proposal
	if err := f.srv.Store().ScanProposals(func(p store.Proposal) bool {
		out = append(out, p)
		return true
	}); err != nil {
		f.t.Fatalf("proposals: %v", err)
	}
	if len(out) != 1 {
		f.t.Fatalf("proposals = %d, want 1", len(out))
	}
	return out[0]
}

// proposalID returns the fixture's proposal id.
func (f *fixture) proposalID() string { return f.proposal().ProposalID }

// invoiceKey is the fixture's matched invoice key.
func invoiceKey() string { return model.InvoiceKey(fixtureSupplier, fixtureInvoiceNo) }

// sourceSHA returns the content address of the bytes a record was parsed from.
func (f *fixture) sourceSHA(dataset, key string) string {
	f.t.Helper()
	rev, ok := f.srv.Store().Get(dataset, key)
	if !ok {
		f.t.Fatalf("no record %s/%s", dataset, key)
	}
	if rev.Provenance.SourceSHA256 == "" {
		f.t.Fatalf("record %s/%s carries no source sha256", dataset, key)
	}
	return rev.Provenance.SourceSHA256
}

// contains fails the test when the body does not carry every wanted fragment.
func contains(t *testing.T, what, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s does not contain %q", what, w)
		}
	}
}
