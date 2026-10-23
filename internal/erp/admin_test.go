package erp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// TestAdminSurfaceIsTokenGated asserts every admin endpoint refuses an absent and
// a wrong token with the same answer, so nothing leaks to a connector token.
func TestAdminSurfaceIsTokenGated(t *testing.T) {
	h := newHarness(t, nil)
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, AdminPathMetrics},
		{http.MethodGet, AdminPathDocuments},
		{http.MethodGet, AdminPathIdempotencyKeys},
		{http.MethodGet, AdminPathRequests},
		{http.MethodGet, AdminPathSOAPCalls},
		{http.MethodGet, AdminPathStateDigest},
		{http.MethodGet, AdminPathCredentials},
		{http.MethodPost, AdminPathReset},
		{http.MethodPost, AdminPathSeed},
	}
	for _, p := range paths {
		for _, token := range []string{"", "wrong", h.token} {
			req := httptest.NewRequest(p.method, p.path, strings.NewReader("{}"))
			if token != "" {
				req.Header.Set(httpx.HeaderAdminToken, token)
			}
			rec := h.do(req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s with token %q: status %d, want 403", p.method, p.path, token, rec.Code)
			}
			if got := h.errorBody(rec).Code; got != httpx.CodeAdminTokenRequired {
				t.Fatalf("%s %s: code %q, want %q", p.method, p.path, got, httpx.CodeAdminTokenRequired)
			}
		}
	}
}

// TestAdminSurfaceIsFreeOfCharge asserts the admin surface and the liveness probe
// cost nothing: no quota, no virtual time, no metric and no request log entry.
// The grader polls /healthz before every run, so a probe that spent quota would
// make the budget depend on how fast the process started.
func TestAdminSurfaceIsFreeOfCharge(t *testing.T) {
	// Chaos is on, because the admin surface must also never be injected into: a
	// grader that had to retry a metrics read would be reading a moving target.
	h := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	before := h.metrics()
	for i := 0; i < 60; i++ {
		if rec := h.get(RouteHealthz); rec.Code != http.StatusOK {
			t.Fatalf("healthz call %d: status %d, want 200 on every single one", i, rec.Code)
		}
		for _, path := range []string{
			AdminPathMetrics, AdminPathDocuments, AdminPathRequests,
			AdminPathSOAPCalls, AdminPathIdempotencyKeys,
		} {
			if rec := h.adminGet(path); rec.Code != http.StatusOK {
				t.Fatalf("%s call %d: status %d, want 200: the admin surface is never injected into",
					path, i, rec.Code)
			}
		}
	}
	after := h.metrics()
	if after.RequestsTotal != before.RequestsTotal {
		t.Fatalf("requests_total moved from %d to %d", before.RequestsTotal, after.RequestsTotal)
	}
	if after.QuotaUsed != before.QuotaUsed {
		t.Fatalf("quota_used moved from %d to %d", before.QuotaUsed, after.QuotaUsed)
	}
	if after.VirtualClockMs != before.VirtualClockMs {
		t.Fatalf("the virtual clock moved from %d to %d", before.VirtualClockMs, after.VirtualClockMs)
	}
	if after.RequestLogEntries != before.RequestLogEntries {
		t.Fatalf("the request log grew from %d to %d entries",
			before.RequestLogEntries, after.RequestLogEntries)
	}
}

// TestHealthz asserts the probe answers with what it is.
func TestHealthz(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.get(RouteHealthz)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var body struct {
		Status   string `json:"status"`
		Svc      string `json:"svc"`
		Scenario string `json:"scenario"`
		Seed     int64  `json:"seed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if body.Status != "ok" || body.Svc != "erp" || body.Scenario != seed.ScenarioS0 || body.Seed != testSeed {
		t.Fatalf("healthz says %+v", body)
	}
}

// TestAdminRequestLog asserts the request log records what the build spec asks
// for: the sequence, the endpoint class, the content-addressed signature, the
// status, the virtual cost, the quota and any injected fault.
func TestAdminRequestLog(t *testing.T) {
	h := newHarness(t, nil)
	h.list(RouteSuppliers + "?limit=10")
	h.get(RouteSuppliers + "/" + seed.CollisionSupplierShort)
	h.postSingle(h.goodItem("prp_log"), "key-log")

	rec := h.adminGet(AdminPathRequests)
	var log AdminRequests
	if err := decodeJSON(rec.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode request log: %v", err)
	}
	if log.Dropped != 0 {
		t.Fatalf("the log dropped %d entries", log.Dropped)
	}
	// The token request from the harness plus the three above.
	if log.Count != 4 {
		t.Fatalf("%d entries, want 4: %+v", log.Count, log.Requests)
	}
	want := []struct {
		endpoint string
		sig      string
		cost     int64
	}{
		{string(simclock.ERPAuthToken), "POST " + RouteAuthToken, 10},
		{string(simclock.ERPListPage), "GET " + RouteSuppliers + "|changed_since=0|after=", 45},
		{string(simclock.ERPSingleGet), "GET " + RouteSupplier + "|" + seed.CollisionSupplierShort, 25},
		{string(simclock.ERPDocumentPost), "POST " + RouteDocuments + "|prp_log", 60},
	}
	for i, w := range want {
		got := log.Requests[i]
		if got.Sequence != int64(i+1) {
			t.Fatalf("entry %d: sequence %d, want %d", i, got.Sequence, i+1)
		}
		if got.Endpoint != w.endpoint {
			t.Fatalf("entry %d: endpoint %q, want %q", i, got.Endpoint, w.endpoint)
		}
		if got.Signature != w.sig {
			t.Fatalf("entry %d: signature %q, want %q", i, got.Signature, w.sig)
		}
		if got.VirtualCostVms != w.cost {
			t.Fatalf("entry %d: virtual cost %d vms, want %d", i, got.VirtualCostVms, w.cost)
		}
		if got.Status != http.StatusOK {
			t.Fatalf("entry %d: status %d", i, got.Status)
		}
		if got.InjectedFault != "" {
			t.Fatalf("entry %d: injected %q with chaos off", i, got.InjectedFault)
		}
	}
}

// TestAdminIdempotencyKeys asserts the retained keys are listed in insertion
// order, which is the eviction order and never a map order.
func TestAdminIdempotencyKeys(t *testing.T) {
	h := newHarness(t, nil)
	for i, key := range []string{"key-a", "key-b", "key-c"} {
		h.postSingle(h.goodItem("prp_keys_"+key), key)
		_ = i
	}
	rec := h.adminGet(AdminPathIdempotencyKeys)
	var keys AdminIdempotencyKeys
	if err := decodeJSON(rec.Body.Bytes(), &keys); err != nil {
		t.Fatalf("decode keys: %v", err)
	}
	if keys.Count != 3 {
		t.Fatalf("%d keys, want 3", keys.Count)
	}
	for i, want := range []string{"key-a", "key-b", "key-c"} {
		if !strings.HasSuffix(keys.Keys[i], want) {
			t.Fatalf("key %d is %q, want one ending in %q", i, keys.Keys[i], want)
		}
		if !strings.Contains(keys.Keys[i], Tenant) || !strings.Contains(keys.Keys[i], RouteDocuments) {
			t.Fatalf("key %d is %q: an idempotency key is scoped to a tenant and an operation",
				i, keys.Keys[i])
		}
	}
}

// TestAdminResetClearsTheRunAndKeepsTheData asserts a reset is a new run: zeroed
// counters, no documents, no idempotency keys, an empty request log, and the same
// dataset served afterwards.
func TestAdminResetClearsTheRunAndKeepsTheData(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(cfg *Config) { cfg.ExportDir = dir })
	h.postSingle(h.goodItem("prp_reset"), "key-reset")
	h.do(h.soapRequest(soapRequestOptions{}))
	before, err := h.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	rec := h.adminPost(AdminPathReset, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: status %d", rec.Code)
	}
	var state AdminState
	if err := decodeJSON(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Scenario != seed.ScenarioS0 || len(state.ExportFiles) == 0 {
		t.Fatalf("reset state: %+v", state)
	}

	m := h.metrics()
	if m.RequestsTotal != 0 || m.QuotaUsed != 0 || m.VirtualClockMs != 0 {
		t.Fatalf("a reset left counters behind: %+v", m.Metrics)
	}
	if m.DocumentsCreated != 0 || m.IdempotencyKeys != 0 || m.SOAPCalls != 0 {
		t.Fatalf("a reset left run state behind: %+v", m)
	}
	if m.RequestLogEntries != 0 {
		t.Fatalf("a reset left %d request log entries behind", m.RequestLogEntries)
	}

	// The old token belongs to the old run and must no longer work.
	if rec := h.get(RouteSuppliers); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the token of the previous run: status %d, want 401", rec.Code)
	}
	h.token = h.authToken()

	after, err := h.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if after.Digest == before.Digest {
		t.Fatal("the digest must change when the booked documents disappear")
	}
	// The master data half of the digest is untouched.
	for i := range before.Components {
		if before.Components[i].Name == "document" {
			continue
		}
		if before.Components[i] != after.Components[i] {
			t.Fatalf("a reset changed the %s component of the digest", before.Components[i].Name)
		}
	}
	// And the retryable SOAP fault is armed again, because it is a property of
	// the run and not of the dataset.
	soap := h.do(h.soapRequest(soapRequestOptions{}))
	if got := decodeFault(t, soap.Body.Bytes()).Code; got != FaultTemporary {
		t.Fatalf("after a reset the first SOAP delivery faulted with %q, want %q", got, FaultTemporary)
	}
}

// TestAdminSeedLoadsAnotherScenario asserts a reseed replaces the dataset, the
// credentials that follow the seed and the export drop, and keeps the admin token
// so the caller can still read what it created.
func TestAdminSeedLoadsAnotherScenario(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(cfg *Config) { cfg.ExportDir = dir })
	firstDigest, _ := h.srv.Digest()

	rec := h.adminPost(AdminPathSeed, `{"scenario":"S0","seed":777}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed: status %d, body %.200s", rec.Code, rec.Body.String())
	}
	var state AdminState
	if err := decodeJSON(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Seed != 777 {
		t.Fatalf("seed is %d, want 777", state.Seed)
	}
	secondDigest, _ := h.srv.Digest()
	if secondDigest.Digest == firstDigest.Digest {
		t.Fatal("a different seed must produce a different dataset")
	}
	if h.srv.Credentials().ClientSecret != CredentialsFor(777).ClientSecret {
		t.Fatal("the client secret must follow the seed")
	}
	if h.srv.Credentials().AdminToken != h.creds.AdminToken {
		t.Fatal("the admin token must survive a reseed")
	}
	if rec := h.adminPost(AdminPathSeed, `{"scenario":"NOPE","seed":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown scenario: status %d, want 400", rec.Code)
	}
}

// TestStateDigestIsStableAndDataDependent asserts the digest is a function of the
// data alone: identical across two servers on the same seed, unchanged by reading
// metrics, and different once a document is booked.
func TestStateDigestIsStableAndDataDependent(t *testing.T) {
	a := newHarness(t, nil)
	b := newHarness(t, nil)
	da, err := a.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	db, err := b.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if da.Digest != db.Digest {
		t.Fatalf("two servers on the same seed disagree:\n%s\n%s", da.Digest, db.Digest)
	}

	a.list(RouteSuppliers)
	a.metrics()
	again, _ := a.srv.Digest()
	if again.Digest != da.Digest {
		t.Fatal("reading the surface changed the digest")
	}

	a.postSingle(a.goodItem("prp_digest"), "key-digest")
	after, _ := a.srv.Digest()
	if after.Digest == da.Digest {
		t.Fatal("booking a document did not change the digest")
	}

	rec := a.adminGet(AdminPathStateDigest)
	var served StateDigest
	if err := decodeJSON(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("decode digest: %v", err)
	}
	if served.Digest != after.Digest {
		t.Fatal("the admin surface serves a different digest from the one Digest computes")
	}
	if len(served.Components) != 7 {
		t.Fatalf("%d components, want 7", len(served.Components))
	}
}

// TestExportDropWritesTheSentinelLast asserts the file drop contract: every
// delivery's data file exists, its .ok sentinel exists, the sentinel is empty, the
// bytes are the generator's own, and the sentinel is written after the data file.
func TestExportDropWritesTheSentinelLast(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(cfg *Config) { cfg.ExportDir = dir })

	set := h.dataset().set
	if len(set.Deliveries) == 0 {
		t.Fatal("the scenario has no legacy delivery")
	}
	files := h.srv.ExportFiles()
	if len(files) != 2*len(set.Deliveries) {
		t.Fatalf("%d files written, want two per delivery", len(files))
	}
	for i, del := range set.Deliveries {
		if files[2*i] != del.Name || files[2*i+1] != del.OKName {
			t.Fatalf("write order is %v: every data file must be followed by its own sentinel", files)
		}
		got, err := os.ReadFile(filepath.Join(dir, del.Name))
		if err != nil {
			t.Fatalf("read delivery: %v", err)
		}
		if !bytesEqual(got, del.Bytes) {
			t.Fatalf("delivery %s is not the generator's bytes (%d vs %d)", del.Name, len(got), len(del.Bytes))
		}
		ok, err := os.ReadFile(filepath.Join(dir, del.OKName))
		if err != nil {
			t.Fatalf("read sentinel: %v", err)
		}
		if len(ok) != 0 {
			t.Fatalf("the sentinel %s must be empty, got %d bytes", del.OKName, len(ok))
		}
		dataInfo, err := os.Stat(filepath.Join(dir, del.Name))
		if err != nil {
			t.Fatalf("stat delivery: %v", err)
		}
		okInfo, err := os.Stat(filepath.Join(dir, del.OKName))
		if err != nil {
			t.Fatalf("stat sentinel: %v", err)
		}
		if okInfo.ModTime().Before(dataInfo.ModTime()) {
			t.Fatalf("the sentinel of %s is older than its data file", del.Name)
		}
	}
}

// TestExportDropRemovesStaleDeliveriesOnReseed asserts a reseed leaves no file of
// the previous scenario behind for a connector to find, and touches nothing else
// in the directory.
func TestExportDropRemovesStaleDeliveriesOnReseed(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(cfg *Config) { cfg.ExportDir = dir })
	stale := filepath.Join(dir, "KRED_0100_20250101_009.txt")
	if err := os.WriteFile(stale, []byte("old run"), 0o644); err != nil {
		t.Fatalf("plant a stale delivery: %v", err)
	}
	keep := filepath.Join(dir, "connector-state.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o644); err != nil {
		t.Fatalf("plant a connector file: %v", err)
	}

	if err := h.srv.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale delivery survived the reset")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("the reset deleted a file that is not a delivery")
	}
	for _, name := range h.srv.ExportFiles() {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("delivery %s missing after the reset", name)
		}
	}
}

// TestAdminCredentialsAreSeedDerived asserts the credentials are a pure function
// of the seed, which is why the repository ships no credential fixture.
func TestAdminCredentialsAreSeedDerived(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.adminGet(AdminPathCredentials)
	var got Credentials
	if err := decodeJSON(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	if got != CredentialsFor(testSeed) {
		t.Fatalf("credentials %+v, want the seed-derived set", got)
	}
	if got.ClientSecret == got.SOAPPassword {
		t.Fatal("the REST and SOAP secrets must be different credentials")
	}
}

// TestAuthTokenRejectsWrongCredentials asserts the token endpoint refuses a wrong
// secret and never echoes it.
func TestAuthTokenRejectsWrongCredentials(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.post(RouteAuthToken, map[string]string{
		"client_id": ClientID, "client_secret": "wrong-secret",
	}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	body := h.errorBody(rec)
	if body.Code != httpx.CodeTokenInvalid {
		t.Fatalf("code %q, want %q", body.Code, httpx.CodeTokenInvalid)
	}
	if strings.Contains(rec.Body.String(), "wrong-secret") {
		t.Fatal("the error body echoed the credential it was given")
	}
}

// TestAuthTokenAcceptsFormEncodedCredentials asserts the convenience form works,
// because half the HTTP clients in the world post credentials that way.
func TestAuthTokenAcceptsFormEncodedCredentials(t *testing.T) {
	h := newHarness(t, nil)
	req := httptest.NewRequest(http.MethodPost, RouteAuthToken,
		strings.NewReader("client_id="+ClientID+"&client_secret="+h.creds.ClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := h.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (%.200s)", rec.Code, rec.Body.String())
	}
	var resp TokenResponse
	if err := decodeJSON(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if resp.AccessToken == "" || resp.ExpiresAfterRequests != TokenRequestTTL ||
		resp.ExpiresAfterVirtualMs != TokenVirtualTTLVms {
		t.Fatalf("token response %+v", resp)
	}
}

// bytesEqual compares two byte slices.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
