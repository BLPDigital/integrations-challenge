package erp

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// chaosProbes returns a fixed set of DISTINCT logical list requests, used to walk
// the injector deterministically.
//
// They differ in changed_since, which is part of the signature, rather than in
// limit, which is not: two pages of the same position with different page sizes are
// the same logical request and correctly meet the same fault once.
func chaosProbes() []string {
	probes := make([]string, 0, 80)
	for i := 0; i < 80; i++ {
		probes = append(probes, fmt.Sprintf("%s?changed_since=%d&limit=5", RouteSuppliers, 100000+i))
	}
	return probes
}

// TestInjectedFaultsAreContentAddressed asserts the validity fix of section 17.1:
// injection is keyed by the signature of the logical request, not by the request
// sequence, so the fault set does not depend on the order the client works in.
//
// Two servers on the same seed walk the same probes in opposite orders and must
// meet exactly the same faults on exactly the same requests. With sequence keying
// this test fails, which is the whole reason it exists.
func TestInjectedFaultsAreContentAddressed(t *testing.T) {
	forward := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	backward := newHarness(t, func(cfg *Config) { cfg.Chaos = true })

	probes := chaosProbes()
	forwardFaults := map[string]int{}
	for _, p := range probes {
		if rec := forward.get(p); rec.Code != http.StatusOK {
			forwardFaults[p] = rec.Code
		}
	}
	backwardFaults := map[string]int{}
	for i := len(probes) - 1; i >= 0; i-- {
		if rec := backward.get(probes[i]); rec.Code != http.StatusOK {
			backwardFaults[probes[i]] = rec.Code
		}
	}
	if len(forwardFaults) == 0 {
		t.Fatal("no fault fired over eighty logical requests: the injector is not wired up")
	}
	if len(forwardFaults) != len(backwardFaults) {
		t.Fatalf("forward met %v, backward met %v", forwardFaults, backwardFaults)
	}
	for p, code := range forwardFaults {
		if backwardFaults[p] != code {
			t.Fatalf("request %q met %d forward and %d backward: injection must not depend on order",
				p, code, backwardFaults[p])
		}
	}
}

// TestInjectedFaultSucceedsOnRetry asserts the fairness guarantee: a fault fires
// only on the first delivery of a signature, so retrying the same logical request
// always gets through.
func TestInjectedFaultSucceedsOnRetry(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	var faulted string
	var code int
	for _, p := range chaosProbes() {
		if rec := h.get(p); rec.Code != http.StatusOK {
			faulted, code = p, rec.Code
			break
		}
	}
	if faulted == "" {
		t.Fatal("no fault fired: the injector is not wired up")
	}
	if code != http.StatusTooManyRequests && code != http.StatusServiceUnavailable {
		t.Fatalf("injected status %d, want 429 or 503", code)
	}
	retry := h.get(faulted)
	if retry.Code != http.StatusOK {
		t.Fatalf("the retry of %q met %d as well: an injected fault must succeed on the retry",
			faulted, retry.Code)
	}
}

// TestInjectedFaultBodyIsRetriable asserts the injected faults are marked
// retriable, because they are, and that the 429 carries a retry hint. A client
// that retries them is doing the right thing.
func TestInjectedFaultBodyIsRetriable(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	for _, p := range chaosProbes() {
		rec := h.get(p)
		if rec.Code == http.StatusOK {
			continue
		}
		body := h.errorBody(rec)
		if !body.Retriable {
			t.Fatalf("injected %s on %q is not marked retriable", body.Code, p)
		}
		if rec.Code == http.StatusTooManyRequests {
			if body.Code != httpx.CodeRateLimited {
				t.Fatalf("429 code %q, want %q", body.Code, httpx.CodeRateLimited)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("a 429 must carry Retry-After")
			}
		}
	}
}

// TestChaosOffChangesNothingElse asserts --no-chaos changes only the injection:
// the same requests return the same bodies and leave the same state digest, so a
// candidate debugging without chaos is debugging the same landscape.
func TestChaosOffChangesNothingElse(t *testing.T) {
	on := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	off := newHarness(t, func(cfg *Config) { cfg.Chaos = false })

	for _, p := range chaosProbes()[:10] {
		// Two deliveries on the chaos server, so the injected first delivery is
		// out of the way and the comparison is of the real answers.
		on.get(p)
		got := on.get(p)
		want := off.get(p)
		if got.Body.String() != want.Body.String() {
			t.Fatalf("request %q differs with chaos on and off", p)
		}
	}
	digestOn, err := on.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	digestOff, err := off.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if digestOn.Digest != digestOff.Digest {
		t.Fatal("the state digest must be identical with and without chaos")
	}
}

// TestQuotaExhaustionIsPermanent asserts the hard quota: once it is spent every
// further non-admin request is 503 QUOTA_EXHAUSTED with retriable false, for the
// rest of the run, while the admin surface keeps answering.
func TestQuotaExhaustionIsPermanent(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.Quota = 3 })
	// The harness already spent one unit on its token request.
	for i := 0; i < 2; i++ {
		if rec := h.get(RouteSuppliers + "?limit=1"); rec.Code != http.StatusOK {
			t.Fatalf("request %d inside the quota: status %d", i, rec.Code)
		}
	}
	for i := 0; i < 3; i++ {
		rec := h.get(RouteSuppliers + "?limit=1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request past the quota: status %d, want 503", rec.Code)
		}
		body := h.errorBody(rec)
		if body.Code != httpx.CodeQuotaExhausted {
			t.Fatalf("code %q, want %q", body.Code, httpx.CodeQuotaExhausted)
		}
		if body.Retriable {
			t.Fatal("the quota 503 must be marked not retriable: retrying it cannot help")
		}
	}
	if m := h.metrics(); m.QuotaUsed != 3 || m.QuotaLimit != 3 {
		t.Fatalf("quota_used %d of %d, want 3 of 3", m.QuotaUsed, m.QuotaLimit)
	}
}
