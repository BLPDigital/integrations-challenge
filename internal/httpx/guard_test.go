package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// okHandler counts calls and answers 200.
type okHandler struct{ calls int }

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls++
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func classifyFixed(endpoint string, records int) func(*http.Request) (string, string, int) {
	return func(r *http.Request) (string, string, int) {
		return endpoint, r.Method + " " + r.URL.RequestURI(), records
	}
}

func TestGuardLogLine(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 7})
	var log bytes.Buffer
	h := &okHandler{}
	guard := GuardWith(GuardOptions{
		Governor: gov,
		Classify: classifyFixed(string(simclock.ERPListPage), 10),
		Log:      &log,
	})(h)

	r := httptest.NewRequest("GET", "/erp/v1/suppliers", nil)
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, r)

	if rec.Code != 200 || h.calls != 1 {
		t.Fatalf("status %d, handler calls %d", rec.Code, h.calls)
	}
	want := `{"svc":"erp","seq":1,"event":"request","method":"GET","path":"/erp/v1/suppliers","status":200,"vms":45,"quota_used":1,"sig":"GET /erp/v1/suppliers"}` + "\n"
	if got := log.String(); got != want {
		t.Fatalf("log = %s want %s", got, want)
	}
	m := gov.Metrics()
	if m.RequestsTotal != 1 || m.RecordsReturned != 10 || m.ByEndpoint[string(simclock.ERPListPage)] != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

func TestGuardServiceLabel(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/erp/v1/suppliers", "erp"},
		{"/soap/FinancialReferenceDataService", "erp"},
		{"/v1/ingest/batches", "miniblp"},
		{"/healthz", "miniblp"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := ServiceForPath(tc.path); got != tc.want {
				t.Errorf("ServiceForPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
	// An explicit service label wins over the path heuristic.
	var log bytes.Buffer
	gov := simclock.New(simclock.Config{})
	guard := GuardWith(GuardOptions{Governor: gov, Service: "miniblp", Log: &log})(&okHandler{})
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/erp/v1/suppliers", nil))
	if !strings.HasPrefix(log.String(), `{"svc":"miniblp",`) {
		t.Errorf("log = %s", log.String())
	}
}

func TestGuardAdminPathsAreFree(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 1, QuotaLimit: 1, BucketCapacity: 1, RefillIntervalVms: 1000, Chaos: true})
	var log bytes.Buffer
	h := &okHandler{}
	guard := GuardWith(GuardOptions{
		Governor: gov,
		Classify: classifyFixed(string(simclock.ERPListPage), 250),
		Log:      &log,
	})(h)

	for _, path := range []string{
		"/admin/v1/metrics",
		"/admin/v1/inbox/scan",
		"/erp-admin/v1/metrics",
		"/erp-admin/v1/reset",
	} {
		for i := 0; i < 5; i++ {
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
			if rec.Code != 200 {
				t.Fatalf("%s attempt %d: status %d", path, i, rec.Code)
			}
		}
	}
	m := gov.Metrics()
	if m.RequestsTotal != 0 || m.QuotaUsed != 0 || m.RecordsReturned != 0 || m.RateLimited != 0 || m.VirtualClockMs != 0 {
		t.Fatalf("admin traffic was accounted: %+v", m)
	}
	if len(m.ByEndpoint) != 0 {
		t.Fatalf("admin traffic reached by_endpoint: %+v", m.ByEndpoint)
	}
	if log.Len() != 0 {
		t.Fatalf("admin traffic was logged: %s", log.String())
	}
	if h.calls != 20 {
		t.Fatalf("handler calls = %d, want 20", h.calls)
	}

	// The accounted surface still works afterwards, with the first sequence
	// number: admin requests consumed no sequence either.
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, httptest.NewRequest("GET", "/erp/v1/suppliers", nil))
	if seq := gov.Metrics().RequestsTotal; seq != 1 {
		t.Fatalf("requests_total = %d, want 1", seq)
	}
	if !strings.Contains(log.String(), `"seq":1,`) {
		t.Fatalf("log = %s", log.String())
	}
}

func TestGuardQuotaExhausted(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 3, QuotaLimit: 2})
	var log bytes.Buffer
	h := &okHandler{}
	guard := GuardWith(GuardOptions{
		Governor: gov,
		Classify: classifyFixed(string(simclock.ERPSingleGet), 0),
		Log:      &log,
	})(h)

	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, httptest.NewRequest("GET", "/erp/v1/suppliers/0000417", nil))
		if rec.Code != 200 {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, httptest.NewRequest("GET", "/erp/v1/suppliers/0000417", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Code != CodeQuotaExhausted || body.Retriable {
		t.Fatalf("body = %+v", body)
	}
	if h.calls != 2 {
		t.Fatalf("handler calls = %d, want 2", h.calls)
	}
}

func TestGuardRateLimited(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 5, BucketCapacity: 1, RefillIntervalVms: 1000})
	var log bytes.Buffer
	h := &okHandler{}
	guard := GuardWith(GuardOptions{
		Governor: gov,
		Classify: classifyFixed("", 0), // unknown class: costs nothing, so no refill
		Log:      &log,
	})(h)

	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/outbox/proposals", nil))
	if rec.Code != 200 {
		t.Fatalf("first status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	guard.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/outbox/proposals", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	var body Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Code != CodeRateLimited || !body.Retriable || body.RetryAfterHintMs != 1000 {
		t.Fatalf("body = %+v", body)
	}
	if h.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", h.calls)
	}
	if m := gov.Metrics(); m.RateLimited != 1 {
		t.Fatalf("429 counter = %d", m.RateLimited)
	}
}

func TestGuardInjectedFaults(t *testing.T) {
	tests := []struct {
		name       string
		seed       int64
		wantStatus int
		wantCode   string
		retriable  bool
		wantVms    int64
	}{
		// Injection is content addressed: for this signature, seed 26 hashes to
		// a multiple of 23 and fires the 429 injector. The response carries the
		// rate-limit penalty on top of the request's own cost: 25 vms for the
		// single GET plus 50 vms of penalty.
		{"429", 26, http.StatusTooManyRequests, CodeRateLimited, true, 75},
		// Seed 51 hashes to a multiple of 37 and not of 23, so the 503 injector
		// fires alone.
		{"503", 51, http.StatusServiceUnavailable, CodeUnavailable, true, 25},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gov := simclock.New(simclock.Config{
				Seed:                tc.seed,
				Chaos:               true,
				RateLimitCostVms100: 5000,
			})
			var log bytes.Buffer
			h := &okHandler{}
			guard := GuardWith(GuardOptions{
				Governor: gov,
				Classify: classifyFixed(string(simclock.ERPSingleGet), 0),
				Log:      &log,
			})(h)
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, httptest.NewRequest("GET", "/erp/v1/suppliers/0000417", nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			var body Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body: %v", err)
			}
			if body.Code != tc.wantCode || body.Retriable != tc.retriable {
				t.Fatalf("body = %+v", body)
			}
			if h.calls != 0 {
				t.Fatalf("handler ran despite an injected fault")
			}
			var line logLine
			if err := json.Unmarshal(log.Bytes(), &line); err != nil {
				t.Fatalf("log line: %v (%s)", err, log.String())
			}
			if line.Injected != tc.wantCode {
				t.Errorf("log injected = %q, want %q", line.Injected, tc.wantCode)
			}
			if line.Status != tc.wantStatus {
				t.Errorf("log status = %d, want %d", line.Status, tc.wantStatus)
			}
			if line.Vms != tc.wantVms {
				t.Errorf("log vms = %d, want %d", line.Vms, tc.wantVms)
			}
		})
	}
}

func TestGuardNoChaosInjectsNothing(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 24, Chaos: false})
	h := &okHandler{}
	guard := GuardWith(GuardOptions{
		Governor: gov,
		Classify: classifyFixed(string(simclock.ERPSingleGet), 0),
		Log:      &bytes.Buffer{},
	})(h)
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, httptest.NewRequest("GET", "/erp/v1/suppliers/0000417", nil))
		if rec.Code != 200 {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	if m := gov.Metrics(); m.FiveXXInjected != 0 || m.RateLimited != 0 {
		t.Fatalf("faults were injected with chaos off: %+v", m)
	}
}

func TestGuardStampsTheDecision(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 1})
	var seen []int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, ok := DecisionFrom(r.Context())
		if !ok {
			t.Error("no decision in the context")
		}
		if !d.Allow {
			t.Error("handler ran on a rejected request")
		}
		seen = append(seen, RequestSeq(r.Context()))
	})
	guard := GuardWith(GuardOptions{Governor: gov, Log: &bytes.Buffer{}})(h)
	for i := 0; i < 3; i++ {
		guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/x", nil))
	}
	want := []int64{1, 2, 3}
	if len(seen) != len(want) {
		t.Fatalf("sequences = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("sequences = %v, want %v", seen, want)
		}
	}
	// A request that never passed a Guard has sequence 0.
	if got := RequestSeq(httptest.NewRequest("GET", "/v1/x", nil).Context()); got != 0 {
		t.Errorf("RequestSeq without a Guard = %d, want 0", got)
	}
}

func TestGuardIsDeterministicAcrossRuns(t *testing.T) {
	// 40 distinct logical requests, the way a paginated pull looks: the
	// signature carries the page cursor position, so every page is its own
	// signature and can be faulted independently.
	paths := make([]string, 40)
	for i := range paths {
		paths[i] = fmt.Sprintf("/erp/v1/suppliers?after=%d", i*250)
	}
	run := func(order []int) string {
		gov := simclock.New(simclock.Config{Seed: 24, Chaos: true, QuotaLimit: 200})
		var log bytes.Buffer
		guard := GuardWith(GuardOptions{
			Governor: gov,
			Classify: classifyFixed(string(simclock.ERPListPage), 250),
			Log:      &log,
		})(&okHandler{})
		for _, i := range order {
			guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", paths[i], nil))
		}
		return log.String()
	}
	// The fault set is the set of signatures that were faulted, independent of
	// the order they were issued in.
	faultSet := func(log string) map[string]string {
		out := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			var l struct {
				Sig      string `json:"sig"`
				Injected string `json:"injected"`
			}
			if err := json.Unmarshal([]byte(line), &l); err != nil {
				t.Fatalf("log line is not JSON: %q: %v", line, err)
			}
			if l.Injected != "" {
				out[l.Sig] = l.Injected
			}
		}
		return out
	}

	serial := make([]int, len(paths))
	for i := range serial {
		serial[i] = i
	}
	first := run(serial)
	for i := 0; i < 5; i++ {
		if got := run(serial); got != first {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, got)
		}
	}
	base := faultSet(first)
	if len(base) == 0 {
		t.Fatalf("the fixture never met an injected fault:\n%s", first)
	}

	// THE VALIDITY PROPERTY: a client that issues the same logical requests in
	// a different order meets exactly the same faults. With sequence-keyed
	// injection this assertion fails, which is why injection is content
	// addressed.
	shuffled := []int{7, 0, 39, 12, 3, 25, 1, 38, 19, 5, 31, 2, 14, 27, 8, 36, 4,
		21, 11, 33, 6, 17, 29, 9, 22, 35, 10, 24, 13, 30, 15, 37, 16, 26, 18, 32,
		20, 34, 23, 28}
	if got := faultSet(run(shuffled)); !reflect.DeepEqual(got, base) {
		t.Fatalf("fault set depends on request order:\nserial   %v\nshuffled %v", base, got)
	}

	// A retry of a faulted request always passes: faults fire on the first
	// delivery of a signature only.
	gov := simclock.New(simclock.Config{Seed: 24, Chaos: true, QuotaLimit: 200})
	for sig := range base {
		if _, _, _, ok := gov.InjectFault(sig); !ok {
			t.Fatalf("first delivery of %q was not faulted", sig)
		}
		for attempt := 1; attempt <= 3; attempt++ {
			if _, _, _, ok := gov.InjectFault(sig); ok {
				t.Fatalf("retry %d of %q was faulted; retries must pass", attempt, sig)
			}
		}
	}

	// A different seed produces a different fault set, or the seed does nothing.
	other := simclock.New(simclock.Config{Seed: 25, Chaos: true, QuotaLimit: 200})
	diff := false
	for _, p := range paths {
		sig := "GET " + p
		_, _, _, ok := other.InjectFault(sig)
		if ok != (base[sig] != "") {
			diff = true
			break
		}
	}
	if !diff {
		t.Error("seed 25 injected exactly the same faults as seed 24")
	}
}

func TestGuardMandatedConstructor(t *testing.T) {
	// Guard is the two-argument form the build spec fixes; it must work with a
	// nil-free classify and default to stdout without panicking.
	gov := simclock.New(simclock.Config{Seed: 1})
	guard := Guard(gov, func(r *http.Request) (string, string, int) {
		return string(simclock.TwinOutboxList), "GET /v1/outbox/proposals", 500
	})
	if guard == nil {
		t.Fatal("Guard returned nil")
	}
	h := guard(&okHandler{})
	rec := httptest.NewRecorder()
	// Admin paths write no log line, so stdout stays clean in this test.
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/v1/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestIsAdminPath(t *testing.T) {
	tests := map[string]bool{
		"/admin/v1/metrics":     true,
		"/admin/v1":             true,
		"/erp-admin/v1/metrics": true,
		"/v1/ingest/batches":    false,
		"/erp/v1/suppliers":     false,
		"/admin":                false,
		"/ui/admin/v1":          false,
	}
	for path, want := range tests {
		if got := IsAdminPath(path); got != want {
			t.Errorf("IsAdminPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestStatusRecorderDefaults(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec}
	if got := s.statusCode(); got != 200 {
		t.Errorf("empty handler status = %d, want 200", got)
	}
	if _, err := s.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := s.statusCode(); got != 200 {
		t.Errorf("implicit status = %d, want 200", got)
	}
	s2 := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	s2.WriteHeader(404)
	s2.WriteHeader(500)
	if got := s2.statusCode(); got != 404 {
		t.Errorf("first status wins: got %d", got)
	}
	s2.Flush()
}
