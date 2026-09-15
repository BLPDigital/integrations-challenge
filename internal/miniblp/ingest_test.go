package miniblp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// TestHealthzIsFreeAndUnauthenticated asserts that the readiness probe costs
// nothing and needs no credential: the grader polls it before the run starts.
func TestHealthzIsFreeAndUnauthenticated(t *testing.T) {
	h := newHarness(t, nil)
	before := h.metrics().RequestsTotal
	req, _ := http.NewRequest(http.MethodGet, h.ts.URL+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
	if got := h.metrics().RequestsTotal; got != before {
		t.Fatalf("healthz charged the quota: requests_total %d -> %d", before, got)
	}
}

// TestVersionedSurfaceRequiresToken asserts the bearer check is installed inside
// the Guard.
func TestVersionedSurfaceRequiresToken(t *testing.T) {
	h := newHarness(t, nil)
	saved := h.token
	h.token = ""
	status, body := h.get("/v1/outbox/proposals")
	h.token = saved
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d body %s, want 401", status, body)
	}
	if !strings.Contains(string(body), httpx.CodeTokenMissing) {
		t.Fatalf("body = %s, want %s", body, httpx.CodeTokenMissing)
	}
}

// TestRESTBatchHappyPath walks the whole REST channel and asserts the closure
// invariant on the receipt it returns.
func TestRESTBatchHappyPath(t *testing.T) {
	h := newHarness(t, nil)
	status, ref := h.openBatch(restManifest("batch-rest-1"))
	if status != http.StatusCreated {
		t.Fatalf("open batch status = %d (%s)", status, ref)
	}
	chunkStatus, body := h.chunk(ref, "supplier", 1, "k1", []any{
		supplierRecord("0000417", "Steinbach", false),
		supplierRecord("0000418", "Meier", false),
	})
	if chunkStatus != http.StatusOK {
		t.Fatalf("chunk status = %d body %s", chunkStatus, body)
	}
	var chunkRes struct {
		Counts  Counts         `json:"counts"`
		Records []RecordResult `json:"records"`
	}
	if err := json.Unmarshal(body, &chunkRes); err != nil {
		t.Fatalf("chunk response: %v", err)
	}
	if chunkRes.Counts.Accepted != 2 || chunkRes.Counts.Seen != 2 {
		t.Fatalf("counts = %+v, want 2 seen and 2 accepted", chunkRes.Counts)
	}
	if len(chunkRes.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(chunkRes.Records))
	}
	if chunkRes.Records[0].NaturalKey != "0000417" || chunkRes.Records[0].Version != 1 {
		t.Fatalf("first record = %+v", chunkRes.Records[0])
	}

	commitStatus, receipt := h.commit(ref)
	if commitStatus != http.StatusOK {
		t.Fatalf("commit status = %d", commitStatus)
	}
	if !receipt.ClosureOK {
		t.Fatalf("receipt closure not ok: %+v", receipt.Counts)
	}
	if !receipt.Counts.closes() {
		t.Fatalf("counts do not close: %+v", receipt.Counts)
	}
	if receipt.Status != BatchAccepted {
		t.Fatalf("status = %q, want %q", receipt.Status, BatchAccepted)
	}
	if receipt.Channel != ChannelREST {
		t.Fatalf("channel = %q", receipt.Channel)
	}
}

// TestChunkGap asserts that a skipped ordinal is refused with the expected and
// the seen value and that nothing is applied.
func TestChunkGap(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-gap"))
	if status, body := h.chunk(ref, "supplier", 1, "g1",
		[]any{supplierRecord("0000417", "A", false)}); status != http.StatusOK {
		t.Fatalf("first chunk status = %d body %s", status, body)
	}
	status, body := h.chunk(ref, "supplier", 3, "g3",
		[]any{supplierRecord("0000419", "C", false)})
	if status != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", status, body)
	}
	var e struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body: %v", err)
	}
	if e.Code != httpx.CodeChunkGap {
		t.Fatalf("code = %q, want %q", e.Code, httpx.CodeChunkGap)
	}
	if e.Details["expected"] != float64(2) || e.Details["seen"] != float64(3) {
		t.Fatalf("details = %+v, want expected 2 seen 3", e.Details)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000419"); ok {
		t.Fatal("the gapped chunk was applied")
	}
}

// TestChunkIdempotency asserts the three verdicts of the idempotency store: a
// new key does the work, the same key with the same body replays the stored
// response verbatim, and the same key with a different body is a conflict.
func TestChunkIdempotency(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-idem"))
	records := []any{supplierRecord("0000417", "A", false)}

	status, first := h.chunk(ref, "supplier", 1, "same-key", records)
	if status != http.StatusOK {
		t.Fatalf("first status = %d body %s", status, first)
	}
	status, replay := h.chunk(ref, "supplier", 1, "same-key", records)
	if status != http.StatusOK {
		t.Fatalf("replay status = %d", status)
	}
	if string(first) != string(replay) {
		t.Fatalf("replay body differs:\n%s\n%s", first, replay)
	}
	if got := h.srv.Governor().Metrics().DuplicateApplyAttempts; got != 0 {
		t.Fatalf("duplicate_apply_attempts = %d after a correct retry, want 0", got)
	}

	status, body := h.chunk(ref, "supplier", 1, "same-key",
		[]any{supplierRecord("0000418", "B", false)})
	if status != http.StatusConflict {
		t.Fatalf("conflict status = %d body %s, want 409", status, body)
	}
	if !strings.Contains(string(body), httpx.CodeIdempotencyKeyReused) {
		t.Fatalf("body = %s", body)
	}
}

// TestDuplicateApplyAttemptsCountsRegeneratedKey asserts the counter that
// catches a client whose idempotency key is a function of its attempt rather
// than of its payload.
func TestDuplicateApplyAttemptsCountsRegeneratedKey(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-dup"))
	records := []any{supplierRecord("0000417", "A", false)}
	if status, body := h.chunk(ref, "supplier", 1, "key-attempt-1", records); status != http.StatusOK {
		t.Fatalf("first status = %d body %s", status, body)
	}
	if status, body := h.chunk(ref, "supplier", 1, "key-attempt-2", records); status != http.StatusOK {
		t.Fatalf("second status = %d body %s", status, body)
	}
	if got := h.srv.Governor().Metrics().DuplicateApplyAttempts; got != 1 {
		t.Fatalf("duplicate_apply_attempts = %d, want 1", got)
	}
	// The second application changed nothing, which is what makes the twin safe
	// in the face of a client that gets its keys wrong.
	if _, hash, _, _, revisions, ok := h.srv.Store().Head("supplier", "0000417"); !ok ||
		revisions != 2 || hash == "" {
		t.Fatalf("head after re-apply: revisions %d ok %v", revisions, ok)
	}
}

// TestChunkRecordCap asserts the published 1'000 record cap.
func TestChunkRecordCap(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-cap"))
	records := make([]any, MaxChunkRecords+1)
	for i := range records {
		records[i] = supplierRecord("S"+strconv.Itoa(i), "name", false)
	}
	status, body := h.chunk(ref, "supplier", 1, "cap", records)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body %s, want 413", status, body)
	}
	if !strings.Contains(string(body), CodeBatchTooLarge) {
		t.Fatalf("body = %s, want %s", body, CodeBatchTooLarge)
	}
}

// TestPartialChunkIs207 asserts the mixed status and that a rejected record
// carries a pointer into the bytes the client sent.
func TestPartialChunkIs207(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-207"))
	bad := supplierRecord("0000418", "B", false)
	bad["country"] = "Switzerland" // not two uppercase letters
	status, body := h.chunk(ref, "supplier", 1, "mixed", []any{
		supplierRecord("0000417", "A", false), bad,
	})
	if status != http.StatusMultiStatus {
		t.Fatalf("status = %d body %s, want 207", status, body)
	}
	var res struct {
		Counts  Counts         `json:"counts"`
		Records []RecordResult `json:"records"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("response: %v", err)
	}
	if res.Counts.Accepted != 1 || res.Counts.Rejected != 1 || !res.Counts.closes() {
		t.Fatalf("counts = %+v", res.Counts)
	}
	rejected := res.Records[1]
	if rejected.Outcome != OutcomeRejected || len(rejected.Errors) == 0 {
		t.Fatalf("second record = %+v", rejected)
	}
	if rejected.Errors[0].Pointer == "" {
		t.Fatalf("rejected record has no pointer: %+v", rejected.Errors[0])
	}
}

// TestAllRejectedIs422 asserts the all-rejected status.
func TestAllRejectedIs422(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-422"))
	bad := supplierRecord("", "B", false)
	status, body := h.chunk(ref, "supplier", 1, "all-bad", []any{bad})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body %s, want 422", status, body)
	}
}

// TestBatchIDConflictOnREST asserts that a known batch id with a different
// manifest is refused rather than silently overwritten.
func TestBatchIDConflictOnREST(t *testing.T) {
	h := newHarness(t, nil)
	first := restManifest("batch-conflict")
	if status, ref := h.openBatch(first); status != http.StatusCreated {
		t.Fatalf("open status = %d (%s)", status, ref)
	}
	second := restManifest("batch-conflict")
	second["producer"] = "other/2.0"
	status, body := h.openBatch(second)
	if status != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", status, body)
	}
	if !strings.Contains(body, CodeBatchIDConflict) {
		t.Fatalf("body = %s", body)
	}
}

// TestIfVersionConflictPerRecord asserts the optional per-record precondition.
func TestIfVersionConflictPerRecord(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-ifversion"))
	if status, body := h.chunk(ref, "supplier", 1, "v1",
		[]any{supplierRecord("0000417", "A", false)}); status != http.StatusOK {
		t.Fatalf("first status = %d body %s", status, body)
	}
	changed := supplierRecord("0000417", "A changed", false)
	changed[IfVersionField] = 7
	status, body := h.chunk(ref, "supplier", 2, "v2", []any{changed})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body %s, want 422", status, body)
	}
	var res struct {
		Records []RecordResult `json:"records"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("response: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0].Errors[0].Code != CodeVersionConflict {
		t.Fatalf("records = %+v", res.Records)
	}
	rev, _ := h.srv.Store().Get("supplier", "0000417")
	if rev.Version != 1 {
		t.Fatalf("version = %d, want 1: the conflicting record must not be applied", rev.Version)
	}
}

// TestSingleRecordPUT asserts the correction path, including If-Match and
// If-None-Match.
func TestSingleRecordPUT(t *testing.T) {
	h := newHarness(t, nil)
	body := mustJSON(t, supplierRecord("0000417", "A", false))

	status, out, hdr := h.request(http.MethodPut, "/v1/ingest/records/supplier/0000417", body,
		map[string]string{HeaderIfNoneMatch: "*"})
	if status != http.StatusOK {
		t.Fatalf("create status = %d body %s", status, out)
	}
	if hdr.Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", hdr.Get("ETag"))
	}
	// The same If-None-Match now has to fail: the record exists.
	status, out, _ = h.request(http.MethodPut, "/v1/ingest/records/supplier/0000417", body,
		map[string]string{HeaderIfNoneMatch: "*"})
	if status != http.StatusPreconditionFailed {
		t.Fatalf("second create status = %d body %s, want 412", status, out)
	}
	// If-Match with the right version updates.
	updated := mustJSON(t, supplierRecord("0000417", "A renamed", false))
	status, out, _ = h.request(http.MethodPut, "/v1/ingest/records/supplier/0000417", updated,
		map[string]string{HeaderIfMatch: `"1"`})
	if status != http.StatusOK {
		t.Fatalf("update status = %d body %s", status, out)
	}
	// A key that disagrees with the path is a mismatch and not a new record.
	status, out, _ = h.request(http.MethodPut, "/v1/ingest/records/supplier/0000999", updated, nil)
	if status != http.StatusBadRequest || !strings.Contains(string(out), CodeKeyMismatch) {
		t.Fatalf("mismatch status = %d body %s", status, out)
	}
}

// TestAppliedThenFailRetryReplaysStoredResponse asserts the deliberate
// applied-then-500: the records are applied, the client sees a 500, and a retry
// under the same idempotency key gets the stored per-record response back.
func TestAppliedThenFailRetryReplaysStoredResponse(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Chaos = true })

	// Find the chunk signature the injector selected. It is content-addressed,
	// so the batch ref, the dataset and the ordinal decide - never the request
	// sequence.
	ref, ordinal := "", 0
	for i := 1; i <= 40 && ordinal == 0; i++ {
		candidate := "batch-af-" + strconv.Itoa(i)
		sig := "APPLYFAIL|POST /v1/ingest/records|" + candidate + "|supplier|1"
		if fires(h, sig) {
			ref, ordinal = candidate, 1
		}
	}
	if ordinal == 0 {
		t.Skip("no applied-then-500 signature found in the probed space")
	}

	status, got := h.openBatch(restManifest(ref))
	if status != http.StatusCreated {
		t.Fatalf("open status = %d (%s)", status, got)
	}
	records := []any{supplierRecord("0000417", "A", false)}
	status, body := h.chunk(ref, "supplier", ordinal, "af-key", records)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d body %s, want 500", status, body)
	}
	// Applied, despite the 500.
	if _, ok := h.srv.Store().Get("supplier", "0000417"); !ok {
		t.Fatal("the applied-then-500 chunk did not apply its records")
	}
	status, replay := h.chunk(ref, "supplier", ordinal, "af-key", records)
	if status != http.StatusOK {
		t.Fatalf("retry status = %d body %s, want the stored 200", status, replay)
	}
	var res struct {
		Counts Counts `json:"counts"`
	}
	if err := json.Unmarshal(replay, &res); err != nil {
		t.Fatalf("replay body: %v (%s)", err, replay)
	}
	if res.Counts.Accepted != 1 {
		t.Fatalf("replayed counts = %+v, want 1 accepted", res.Counts)
	}
	m := h.srv.Governor().Metrics()
	if m.FiveXXInjected != 1 {
		t.Fatalf("5xx_injected = %d, want 1", m.FiveXXInjected)
	}
	if m.FiveXXRetried != 1 {
		t.Fatalf("5xx_retried = %d, want 1", m.FiveXXRetried)
	}
	if m.DuplicateApplyAttempts != 0 {
		t.Fatalf("duplicate_apply_attempts = %d, want 0 for a correct retry", m.DuplicateApplyAttempts)
	}
}

// fires probes whether the applied-then-500 injector would select a signature,
// without consuming it: the probe runs on a throwaway Governor built from the
// same seed, so the real one is untouched.
func fires(h *harness, sig string) bool {
	probe := newHarness(h.t, func(c *Config) {
		c.Chaos = true
		c.Seed = h.srv.cfg.Seed
	})
	return probe.srv.Governor().ApplyThenFail(sig)
}

// TestAuthTokenAcceptsJSONAndForm asserts that the first request a connector
// makes works in both spellings, and that a wrong credential is refused.
func TestAuthTokenAcceptsJSONAndForm(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.ClientID = "twin-client"
		c.ClientSecret = "s3cret"
	})
	initial := h.token
	h.token = ""

	status, body := h.post("/v1/auth/token",
		[]byte(`{"client_id":"twin-client","client_secret":"s3cret"}`), nil)
	if status != http.StatusOK {
		t.Fatalf("json: status %d body %s", status, body)
	}
	var out struct {
		AccessToken           string `json:"access_token"`
		ExpiresAfterRequests  int    `json:"expires_after_requests"`
		ExpiresAfterVirtualMs int64  `json:"expires_after_virtual_ms"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("token body: %v", err)
	}
	if out.AccessToken == "" || out.ExpiresAfterRequests != TokenRequestTTL ||
		out.ExpiresAfterVirtualMs != TokenVirtualTTLVms {
		t.Fatalf("token = %+v", out)
	}

	status, body = h.post("/v1/auth/token",
		[]byte("grant_type=client_credentials&client_id=twin-client&client_secret=s3cret"),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if status != http.StatusOK {
		t.Fatalf("form: status %d body %s", status, body)
	}

	status, body = h.post("/v1/auth/token",
		[]byte(`{"client_id":"twin-client","client_secret":"wrong"}`), nil)
	if status != http.StatusUnauthorized || !strings.Contains(string(body), CodeAuthFailed) {
		t.Fatalf("wrong secret: status %d body %s", status, body)
	}
	// The issued token works, and it is derived from the seed rather than drawn
	// at random, so two runs of one scenario issue the same one.
	h.token = out.AccessToken
	if status, _ := h.get("/v1/outbox/proposals"); status != http.StatusOK {
		t.Fatalf("outbox with the issued token: status %d", status)
	}
	other := newHarness(t, func(c *Config) {
		c.ClientID = "twin-client"
		c.ClientSecret = "s3cret"
	})
	if other.token != initial {
		t.Fatalf("two runs of one scenario issued different first tokens: %q and %q",
			other.token, initial)
	}
}
