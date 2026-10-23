package web

import (
	"net/http"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
)

// entry builds one request log entry for the bucket reconstruction: the status
// the client saw, the injected fault marker and the virtual clock after the
// request are the only fields the reconstruction reads.
func entry(status int, injected string, clockVms int64) erp.RequestLogEntry {
	return erp.RequestLogEntry{
		Status:         status,
		InjectedFault:  injected,
		VirtualClockMs: clockVms,
	}
}

// TestReconstructBucket walks the documented admission rules with hand-computed
// expectations. The parameters are small and round on purpose: capacity 4 and one
// token per 10 virtual milliseconds, so every expected level can be checked by
// counting on paper.
func TestReconstructBucket(t *testing.T) {
	const capacity, refill = int64(4), int64(10)

	tests := []struct {
		name    string
		entries []erp.RequestLogEntry
		want    int64
		why     string
	}{
		{
			name: "a fresh run starts full",
			want: 4,
			why:  "the bucket starts at capacity and the clock starts at zero",
		},
		{
			name: "three cheap admitted requests spend three tokens",
			entries: []erp.RequestLogEntry{
				entry(http.StatusOK, "", 1),
				entry(http.StatusOK, "", 2),
				entry(http.StatusOK, "", 3),
			},
			want: 1,
			why:  "3 vms of virtual time earns no whole token at one per 10 vms",
		},
		{
			name: "virtual time refills whole tokens and a full bucket earns nothing",
			entries: []erp.RequestLogEntry{
				entry(http.StatusOK, "", 1),
				entry(http.StatusOK, "", 2),
				entry(http.StatusOK, "", 3),
				entry(http.StatusOK, "", 4),
				// The fifth request found the bucket empty: a token-bucket 429,
				// which spends no token and charges the 50 vms penalty.
				entry(http.StatusTooManyRequests, "", 54),
				// By now 54 vms have passed, which is 5 earned tokens capped at
				// capacity, and this request spends one of them.
				entry(http.StatusOK, "", 55),
			},
			want: 3,
			why:  "the refill is capped at capacity and the partial carry is dropped",
		},
		{
			name: "the quota 503 touches neither the clock nor the bucket",
			entries: []erp.RequestLogEntry{
				entry(http.StatusOK, "", 1),
				entry(http.StatusOK, "", 2),
				entry(http.StatusServiceUnavailable, "", 2),
				entry(http.StatusServiceUnavailable, "", 2),
			},
			want: 2,
			why:  "a spent quota cannot be spent again, so nothing moves",
		},
		{
			name: "the quarantine 403 is decided before the bucket is consulted",
			entries: []erp.RequestLogEntry{
				entry(http.StatusOK, "", 1),
				entry(http.StatusForbidden, "", 51),
				entry(http.StatusForbidden, "", 101),
			},
			want: 3,
			why:  "a quarantined request never reaches the bucket step",
		},
		{
			name: "an injected 429 has already paid for its token",
			entries: []erp.RequestLogEntry{
				entry(http.StatusTooManyRequests, "RATE_LIMITED", 50),
			},
			want: 3,
			why:  "injection happens after admission, so the token was spent",
		},
		{
			name: "an injected 503 has already paid for its token",
			entries: []erp.RequestLogEntry{
				entry(http.StatusServiceUnavailable, "UNAVAILABLE", 4),
				entry(http.StatusServiceUnavailable, "UNAVAILABLE", 8),
			},
			want: 2,
			why:  "an injected 503 is an admitted request the Governor replaced",
		},
		{
			name: "a handler 5xx is an admitted request",
			entries: []erp.RequestLogEntry{
				entry(http.StatusInternalServerError, "", 1),
			},
			want: 3,
			why:  "the handler produced it, so the request was admitted",
		},
		{
			name: "the level never goes below zero",
			entries: []erp.RequestLogEntry{
				entry(http.StatusOK, "", 1), entry(http.StatusOK, "", 2),
				entry(http.StatusOK, "", 3), entry(http.StatusOK, "", 4),
				entry(http.StatusOK, "", 5), entry(http.StatusOK, "", 6),
			},
			want: 0,
			why:  "six spends against a capacity of four cannot go negative",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reconstructBucket(tc.entries, capacity, refill)
			if got.Level != tc.want {
				t.Fatalf("level %d, want %d: %s", got.Level, tc.want, tc.why)
			}
			if got.Capacity != capacity || got.RefillIntervalVms != refill || !got.Reconstructed {
				t.Fatalf("unexpected parameters: %+v", got)
			}
		})
	}
}

// TestReconstructBucketWithTheBucketDisabled asserts the Governor's documented
// zero value: a non-positive capacity disables the bucket, so no request is ever
// rate limited and there is no level to report.
func TestReconstructBucketWithTheBucketDisabled(t *testing.T) {
	got := reconstructBucket([]erp.RequestLogEntry{entry(http.StatusOK, "", 10)}, 0, 50)
	if got.Level != 0 || got.Capacity != 0 {
		t.Fatalf("%+v, want a zero level and a zero capacity", got)
	}
}

// TestBucketStateUsesThePublishedParameters asserts the overview reports the ERP's
// own bucket, and that a real run stays inside it. The level is a reconstruction
// rather than a reading, so what is asserted here is the invariant, not a
// hand-computed number: a reconstruction that drifted out of range would be worse
// than no number at all.
func TestBucketStateUsesThePublishedParameters(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		if rec := h.apiGet("/erp/v1/suppliers?limit=5"); rec.Code != http.StatusOK {
			t.Fatalf("list page %d: status %d", i, rec.Code)
		}
	}
	entries, err := NewAdminSource(h.srv).Requests()
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	got := bucketState(entries)
	if got.Capacity != erp.BucketCapacity || got.RefillIntervalVms != erp.BucketRefillIntervalVms {
		t.Fatalf("%+v, want the ERP's published bucket parameters", got)
	}
	if got.Level < 0 || got.Level > got.Capacity {
		t.Fatalf("level %d outside [0, %d]", got.Level, got.Capacity)
	}
	// Every one of these requests cost at least one refill interval of virtual
	// time (a list page is 40 vms plus half a vms per record, the token endpoint
	// 10), so the bucket had time to refill and is not empty.
	if got.Level == 0 {
		t.Fatalf("level 0 after %d cheap requests: the reconstruction is not refilling",
			len(entries))
	}
}
