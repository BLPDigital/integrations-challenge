package web

import (
	"net/http"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// A BucketState is the token bucket as the overview reports it: the published
// parameters plus the level.
//
// Level is RECONSTRUCTED rather than read. The ERP's admin surface publishes the
// bucket's parameters and its effects (the 429 count) but not its live level, and
// this package does not reach inside the Governor to get one, because the UI is a
// reader of the published read model and nothing else. The reconstruction replays
// the request log through the documented admission rules of
// [simclock.Governor.Admit], every input of which is in the log: arrival order,
// the status the client saw, the injected-fault marker and the virtual clock
// after each request.
//
// The rules it replays, in the Governor's own order:
//
//   - a quota 503 (status 503 with no injected fault) spends nothing and moves
//     nothing, so it leaves the bucket alone,
//   - a quarantine 403 (status 403 with no injected fault) is rejected before the
//     bucket is consulted, so it leaves the level alone,
//   - every other request refills the bucket from the virtual clock as it stood
//     BEFORE the request, which is the previous entry's clock reading,
//   - a token-bucket 429 (status 429 with no injected fault) is the request that
//     found the bucket empty and spends no token,
//   - anything else was admitted and spends exactly one token, injected faults
//     included: injection happens after admission, so an injected 429 or 503 has
//     already paid for its token.
type BucketState struct {
	// Capacity is the bucket capacity, which is also its initial fill.
	Capacity int64
	// Level is the reconstructed number of tokens available.
	Level int64
	// RefillIntervalVms is the virtual-millisecond interval that earns one
	// token.
	RefillIntervalVms int64
	// Reconstructed is always true and is rendered as a caveat next to the
	// level, so nobody reads the number as something the server reported.
	Reconstructed bool
}

// bucketState reconstructs the token bucket level from the ERP's request log,
// using the ERP's own published bucket parameters.
func bucketState(entries []erp.RequestLogEntry) BucketState {
	return reconstructBucket(entries, erp.BucketCapacity, erp.BucketRefillIntervalVms)
}

// reconstructBucket replays entries through the documented bucket rules. It is
// separate from [bucketState] so a test can drive it with hand-computed
// parameters and hand-computed expectations.
//
// A non-positive capacity means the bucket is disabled, in which case the level
// is reported as the capacity and no request ever spends a token, exactly as the
// Governor behaves.
func reconstructBucket(entries []erp.RequestLogEntry, capacity, refillIntervalVms int64) BucketState {
	out := BucketState{Capacity: capacity, RefillIntervalVms: refillIntervalVms, Reconstructed: true}
	if capacity <= 0 {
		out.Level = capacity
		return out
	}
	refill100 := refillIntervalVms * simclock.Vms100PerVms
	if refill100 <= 0 {
		refill100 = simclock.Vms100PerVms
	}

	tokens := capacity
	var lastRefill100, prevVms int64
	for _, e := range entries {
		before100 := prevVms * simclock.Vms100PerVms
		spend := true
		switch {
		case e.Status == http.StatusServiceUnavailable && e.InjectedFault == "":
			// The quota 503: it cannot spend a quota that is already gone, so
			// it touches neither the clock nor the bucket.
			spend = false
		case e.Status == http.StatusForbidden && e.InjectedFault == "":
			// The quarantine 403 is decided before the bucket is consulted.
			spend = false
		default:
			if elapsed := before100 - lastRefill100; elapsed >= refill100 {
				earned := elapsed / refill100
				tokens += earned
				lastRefill100 += earned * refill100
				if tokens >= capacity {
					// A full bucket earns nothing, so the partial-interval
					// carry is dropped rather than banked as future credit.
					tokens = capacity
					lastRefill100 = before100
				}
			}
			if e.Status == http.StatusTooManyRequests && e.InjectedFault == "" {
				// The request that found the bucket empty.
				spend = false
			}
		}
		if spend && tokens > 0 {
			tokens--
		}
		prevVms = e.VirtualClockMs
	}
	out.Level = tokens
	return out
}
