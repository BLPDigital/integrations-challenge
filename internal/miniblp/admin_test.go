package miniblp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// TestAdminSurfaceRequiresTheAdminToken asserts that the admin surface fails
// closed and that a connector's bearer token cannot reach it.
func TestAdminSurfaceRequiresTheAdminToken(t *testing.T) {
	h := newHarness(t, nil)
	for _, path := range []string{
		"/admin/v1/state/digest", "/admin/v1/metrics", "/admin/v1/batches",
		"/admin/v1/exceptions", "/admin/v1/records/supplier/0000417",
	} {
		status, body, _ := h.request(http.MethodGet, path, nil, nil)
		if status != http.StatusForbidden {
			t.Fatalf("%s: status %d body %s, want 403", path, status, body)
		}
	}
	status, _, _ := h.request(http.MethodPost, "/admin/v1/inbox/scan", nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("scan without the admin token: status %d, want 403", status)
	}
}

// TestAdminSurfaceIsFree asserts that reading the admin surface cannot perturb
// the transcript it is observing.
func TestAdminSurfaceIsFree(t *testing.T) {
	h := newHarness(t, nil)
	before := h.metrics()
	for i := 0; i < 10; i++ {
		h.adminGet("/admin/v1/metrics")
		h.adminGet("/admin/v1/state/digest")
		h.adminPost("/admin/v1/inbox/scan", nil)
	}
	after := h.metrics()
	if after.RequestsTotal != before.RequestsTotal || after.QuotaUsed != before.QuotaUsed ||
		after.VirtualClockMs != before.VirtualClockMs {
		t.Fatalf("the admin surface was accounted for:\n before %+v\n after %+v",
			before.Metrics, after.Metrics)
	}
}

// TestAdminRecordShowsEveryRevisionWithItsProvenance asserts the reviewer's view
// of a record: the full history, so an older version overwriting a newer one is
// visible rather than inferred.
func TestAdminRecordShowsEveryRevisionWithItsProvenance(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-admin"))
	h.chunk(ref, "supplier", 1, "k1", []any{supplierRecord("0000417", "First", false)})
	h.chunk(ref, "supplier", 2, "k2", []any{supplierRecord("0000417", "Second", false)})

	status, body := h.adminGet("/admin/v1/records/supplier/0000417")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var out struct {
		Version   int              `json:"version"`
		TwinID    string           `json:"twin_id"`
		Revisions []store.Revision `json:"revisions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	if out.Version != 2 || len(out.Revisions) != 2 {
		t.Fatalf("record = %+v", out)
	}
	if out.TwinID != twinID("supplier", "0000417") {
		t.Fatalf("twin id = %q", out.TwinID)
	}
	for i, rev := range out.Revisions {
		if rev.Provenance.Channel != ChannelREST {
			t.Fatalf("revision %d channel = %q", i, rev.Provenance.Channel)
		}
		if rev.Provenance.ChunkOrdinal == nil || *rev.Provenance.ChunkOrdinal != int64(i+1) {
			t.Fatalf("revision %d chunk ordinal = %v", i, rev.Provenance.ChunkOrdinal)
		}
		if rev.Provenance.RequestSeq == nil || *rev.Provenance.RequestSeq == 0 {
			t.Fatalf("revision %d has no request sequence", i)
		}
	}
	if status, _ := h.adminGet("/admin/v1/records/supplier/0000999"); status != http.StatusNotFound {
		t.Fatalf("unknown record: status %d, want 404", status)
	}
}

// TestAdminAuditResolvesBothDirections asserts the audit chain of BUILD-SPEC 8.5:
// an invoice key, a proposal id and an ERP document number all resolve to the
// same chain, and the chain reaches from the delivered bytes to the document
// number.
func TestAdminAuditResolvesBothDirections(t *testing.T) {
	h, p := oneProposal(t)
	h.postAck("ack-1", map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
		"idempotency_key":          "blp:acme-ch:" + p.ProposalID,
	})

	invoiceKey := key("0000000417", "0004711")
	for _, query := range []string{invoiceKey, p.ProposalID, "AP-2026-0004311"} {
		status, body := h.adminGet("/admin/v1/audit/" + pathKey(query))
		if status != http.StatusOK {
			t.Fatalf("audit %q: status %d body %s", query, status, body)
		}
		var chain AuditChain
		if err := json.Unmarshal(body, &chain); err != nil {
			t.Fatalf("audit %q: %v", query, err)
		}
		if chain.InvoiceKey != invoiceKey {
			t.Fatalf("audit %q resolved to %q", query, chain.InvoiceKey)
		}
		if len(chain.Revisions) != 1 || len(chain.Sources) != 1 {
			t.Fatalf("audit %q: revisions %d sources %d", query, len(chain.Revisions), len(chain.Sources))
		}
		if len(chain.Proposals) != 1 || chain.Proposals[0].ProposalID != p.ProposalID {
			t.Fatalf("audit %q proposals = %+v", query, chain.Proposals)
		}
		if len(chain.ERPDocumentNumbers) != 1 || chain.ERPDocumentNumbers[0] != "AP-2026-0004311" {
			t.Fatalf("audit %q documents = %v", query, chain.ERPDocumentNumbers)
		}
		if len(chain.IdempotencyKeys) != 1 {
			t.Fatalf("audit %q idempotency keys = %v", query, chain.IdempotencyKeys)
		}
	}
	if status, _ := h.adminGet("/admin/v1/audit/nothing-like-this"); status != http.StatusNotFound {
		t.Fatalf("unknown audit key: status %d, want 404", status)
	}
}

// TestAdminAuditReachesTheRawBytes asserts the far end of the chain: the source
// content address in the provenance resolves to the bytes that were delivered.
func TestAdminAuditReachesTheRawBytes(t *testing.T) {
	h := newHarness(t, nil)
	invoices := mustJSON(t, []any{
		invoiceRecord("0000417", "0004711", "CHF", "1250.00", "89.35", "1160.65"),
	})
	m := fileManifest("batch-raw", fileEntry("invoices.json", "invoice", "json", 1))
	h.writeBatchDir("batch-raw", m, m2(m, map[string]string{"invoices.json": string(invoices)}))
	h.scan()

	status, body := h.adminGet("/admin/v1/audit/" + pathKey(key("0000417", "0004711")))
	if status != http.StatusOK {
		t.Fatalf("audit: status %d body %s", status, body)
	}
	var chain AuditChain
	if err := json.Unmarshal(body, &chain); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(chain.Sources) != 1 || chain.Sources[0].RawURL == "" {
		t.Fatalf("sources = %+v", chain.Sources)
	}
	if chain.Sources[0].SourceFile != "invoices.json" || chain.Sources[0].SourceLine == 0 {
		t.Fatalf("source locator = %+v", chain.Sources[0])
	}
	status, raw := h.adminGet(chain.Sources[0].RawURL)
	if status != http.StatusOK {
		t.Fatalf("raw: status %d", status)
	}
	if string(raw) != string(invoices) {
		t.Fatalf("raw bytes differ from what was delivered:\n%s\n%s", raw, invoices)
	}
}

// TestAdminBatchRecordsFiltersByOutcome asserts the per-record view of one
// delivery.
func TestAdminBatchRecordsFiltersByOutcome(t *testing.T) {
	h := newHarness(t, nil)
	bad := supplierCSV + "0000419,Bad,Switzerland,CHF,,,,false,1\r\n"
	m := fileManifest("batch-records", fileEntry("suppliers.csv", "supplier", "csv", 3))
	h.writeBatchDir("batch-records", m, m2(m, map[string]string{"suppliers.csv": bad}))
	h.scan()

	status, body := h.adminGet("/admin/v1/batches/batch-records/records?outcome=" + OutcomeRejected)
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var out struct {
		Returned int            `json:"returned"`
		Records  []RecordResult `json:"records"`
		Receipt  *Receipt       `json:"receipt"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	if out.Returned != 1 || out.Records[0].Ordinal != 3 {
		t.Fatalf("records = %+v", out.Records)
	}
	if out.Receipt == nil || !out.Receipt.ClosureOK {
		t.Fatalf("receipt = %+v", out.Receipt)
	}
	if status, _ := h.adminGet("/admin/v1/batches/nope/records"); status != http.StatusNotFound {
		t.Fatalf("unknown batch: status %d, want 404", status)
	}
}

// TestAdminMetricsCarriesTheGovernorFieldsAtTopLevel asserts that the published
// counter names are where the grader reads them.
func TestAdminMetricsCarriesTheGovernorFieldsAtTopLevel(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-metrics"))
	h.chunk(ref, "supplier", 1, "k1", []any{supplierRecord("0000417", "A", false)})

	status, body := h.adminGet("/admin/v1/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body: %v", err)
	}
	for _, field := range []string{
		"requests_total", "by_endpoint", "429s", "5xx_injected", "5xx_retried",
		"quota_used", "quota_limit", "records_returned", "virtual_clock_ms",
		"duplicate_document_attempts", "duplicate_apply_attempts",
	} {
		if _, ok := raw[field]; !ok {
			t.Fatalf("metrics is missing the published field %q:\n%s", field, body)
		}
	}
	if raw["records_by_dataset"] == nil || raw["state_digest"] == nil {
		t.Fatalf("metrics is missing the twin's own counters:\n%s", body)
	}
	m := h.metrics()
	if m.RequestsTotal < 3 {
		t.Fatalf("requests_total = %d, want the token, the open and the chunk", m.RequestsTotal)
	}
	if m.Records["supplier"] != 1 {
		t.Fatalf("records_by_dataset = %v", m.Records)
	}
}

// TestAdminResetClearsEverything asserts that a reset leaves nothing of the
// previous run behind, including the counters and the directories.
func TestAdminResetClearsEverything(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-reset", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-reset", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
	h.scan()
	empty := newHarness(t, nil).digest()
	if h.digest() == empty {
		t.Fatal("the fixture did not change the digest")
	}

	if status, body := h.adminPost("/admin/v1/reset", nil); status != http.StatusOK {
		t.Fatalf("reset: status %d body %s", status, body)
	}
	if h.digest() != empty {
		t.Fatal("the digest survived a reset")
	}
	metrics := h.metrics()
	if metrics.RequestsTotal != 0 || metrics.Scans != 0 || metrics.Batches != 0 {
		t.Fatalf("counters survived a reset: %+v", metrics)
	}
	for _, dir := range []string{dirProcessed, dirReceipts} {
		entries, err := os.ReadDir(filepath.Join(h.inboxDir, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Fatalf("%s survived a reset: %v", dir, entries)
		}
	}
	// The twin still works afterwards: a fresh token, a fresh batch.
	h.token = h.authToken()
	h.writeBatchDir("batch-after-reset", m2m(m, "batch-after-reset"),
		map[string]string{"suppliers.csv": supplierCSV})
	if rep := h.scan(); len(rep.Batches) != 1 || rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("scan after reset = %+v", rep)
	}
}

// TestAdminSeedPreloadsCostCentersAndConversions asserts that the twin arrives
// with the two tables another integration is supposed to have loaded already.
func TestAdminSeedPreloadsCostCentersAndConversions(t *testing.T) {
	h := newHarness(t, nil)
	status, body := h.adminPost("/admin/v1/seed", []byte(`{"scenario":"S0","seed":20260416}`))
	if status != http.StatusOK {
		t.Fatalf("seed: status %d body %s", status, body)
	}
	var res SeedResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	if res.CostCenters == 0 || res.Conversions == 0 {
		t.Fatalf("seed result = %+v", res)
	}
	if got := h.srv.Store().Count(model.DatasetCostCenter.String()); got != res.CostCenters {
		t.Fatalf("cost centers stored = %d, reported %d", got, res.CostCenters)
	}
	// Pre-seeded records say where they came from.
	var anyKey string
	if err := h.srv.Store().Scan(model.DatasetCostCenter.String(), func(rev store.Revision) bool {
		anyKey = rev.Key
		if rev.Provenance.SourceSystem != "other-integration" ||
			rev.Provenance.Channel != store.ChannelInternal {
			t.Fatalf("provenance = %+v, want a pre-seeded record", rev.Provenance)
		}
		return false
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if anyKey == "" {
		t.Fatal("no cost center was seeded")
	}
	// Seeding twice is idempotent: the second pass changes nothing.
	digest := h.digest()
	if status, _ := h.adminPost("/admin/v1/seed", []byte(`{"scenario":"S0","seed":20260416}`)); status != http.StatusOK {
		t.Fatalf("second seed: status %d", status)
	}
	if h.digest() != digest {
		t.Fatal("seeding twice changed the digest")
	}
	// The conversion table is customizing, not delivered content, so it stays
	// out of the digest.
	if h.srv.Store().Count(DatasetUoMConversion) == 0 {
		t.Fatal("the conversion table was not loaded")
	}
}

// TestAdminRunsReportsTheRun asserts the run report, including the full-load
// detector.
func TestAdminRunsReportsTheRun(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-run", fileEntry("suppliers.csv", "supplier", "csv", 2))
	m["run_id"] = "run_abc"
	m["mode"] = ModeReplaceDataset
	m["full_load"] = true
	m["watermark"] = map[string]string{"supplier": "118422"}
	h.writeBatchDir("batch-run", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
	h.scan()

	status, body := h.adminGet("/admin/v1/runs/run_abc")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var run runReport
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("body: %v", err)
	}
	if !run.FullLoad {
		t.Fatal("full_load was not reported: the full-reload detector is blind")
	}
	if run.Watermark["supplier"] != "118422" {
		t.Fatalf("watermark = %v", run.Watermark)
	}
	if run.Status != "ok" || run.Counts.Accepted != 2 {
		t.Fatalf("run = %+v", run)
	}
	status, body = h.adminGet("/admin/v1/runs/last")
	if status != http.StatusOK {
		t.Fatalf("runs/last status = %d body %s", status, body)
	}
}

// TestAdminExceptionsPublishesItsCodeSet asserts that the queue answer carries
// the closed set, so a consumer can tell a code it does not know from a typo.
func TestAdminExceptionsPublishesItsCodeSet(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	h.deliverInvoices(invoiceFixture("0000000900", "0004801", "CHF", "10.00", "0.00"))
	status, body := h.adminGet("/admin/v1/exceptions?state=open")
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var out struct {
		Returned   int               `json:"returned"`
		Exceptions []store.Exception `json:"exceptions"`
		Codes      []string          `json:"codes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	if out.Returned != 1 || out.Exceptions[0].Code != ExcSupplierUnknown {
		t.Fatalf("exceptions = %+v", out.Exceptions)
	}
	if len(out.Codes) != len(ExceptionCodes()) {
		t.Fatalf("codes = %v", out.Codes)
	}
	// A filter on a code the queue does not hold returns nothing rather than
	// everything.
	status, body = h.adminGet("/admin/v1/exceptions?state=open&code=" + ExcPOClosed)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	if out.Returned != 0 {
		t.Fatalf("filtered queue = %+v", out.Exceptions)
	}
}

// TestGuardSignatureIsContentAddressed asserts the fault-injection signature
// rule of BUILD-SPEC 17.1: the signature of a chunk is its batch, dataset and
// ordinal, and never the request sequence, so the same logical request issued in
// a different order meets the same fault.
func TestGuardSignatureIsContentAddressed(t *testing.T) {
	h := newHarness(t, nil)
	req, err := http.NewRequest(http.MethodPost,
		"/v1/ingest/batches/b-1/records?dataset=supplier", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set(HeaderChunkOrdinal, "7")
	endpoint, sig, records := h.srv.classify(req)
	if endpoint != "twin_records_chunk" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if sig != "POST /v1/ingest/records|b-1|supplier|7" {
		t.Fatalf("signature = %q", sig)
	}
	if records != 0 {
		t.Fatalf("records = %d for an empty body", records)
	}

	// An outbox page is keyed by the decoded cursor position, never by the
	// opaque cursor string.
	cursor := httpx.Cursor{Dataset: store.DatasetProposal, LastKey: "prp_0000003", ChangeSeqHigh: 3}
	encoded := cursor.Encode(h.srv.cursorSecret)
	req, _ = http.NewRequest(http.MethodGet,
		"/v1/outbox/proposals?status=pending&cursor="+encoded, nil)
	_, sig, _ = h.srv.classify(req)
	if sig != "GET /v1/outbox/proposals|pending|prp_0000003:3" {
		t.Fatalf("outbox signature = %q", sig)
	}

	// An ack submission is keyed by the sorted proposal ids.
	body := ackBody(
		map[string]any{"proposal_id": "prp_0000002", "status": "posted"},
		map[string]any{"proposal_id": "prp_0000001", "status": "posted"},
	)
	req, _ = http.NewRequest(http.MethodPost, "/v1/outbox/acks", readerOf(body))
	req.Header.Set("Content-Type", httpx.MediaJSON)
	_, sig, n := h.srv.classify(req)
	if sig != "POST /v1/outbox/acks|prp_0000001,prp_0000002" {
		t.Fatalf("ack signature = %q", sig)
	}
	if n != 2 {
		t.Fatalf("ack records = %d, want 2", n)
	}
}

// m2m returns a copy of a manifest under a different batch id.
func m2m(m map[string]any, batchID string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	out["batch_id"] = batchID
	return out
}

// pathKey percent-encodes the unit separator of a composite key for a URL path.
func pathKey(key string) string {
	out := ""
	for _, r := range key {
		if r == 0x1f {
			out += "%1F"
			continue
		}
		out += string(r)
	}
	return out
}

// readerOf wraps bytes for a synthetic request body.
func readerOf(b []byte) io.Reader { return bytes.NewReader(b) }
