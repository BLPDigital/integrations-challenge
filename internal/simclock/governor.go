package simclock

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"strings"
	"sync"
)

// Response codes emitted by the Governor. They are the machine-readable code
// field of the shared error body.
const (
	// CodeRateLimited is returned with 429 when the token bucket is empty or
	// when the seed-keyed 429 injector fires.
	CodeRateLimited = "RATE_LIMITED"
	// CodeQuarantined is returned with 403 after 20 consecutive 429s on one
	// endpoint, for the next QuarantineQuotaUnits quota units.
	CodeQuarantined = "CLIENT_QUARANTINED"
	// CodeQuotaExhausted is returned with 503 once the hard quota is spent,
	// permanently for the rest of the run, with Retriable false.
	CodeQuotaExhausted = "QUOTA_EXHAUSTED"
	// CodeUnavailable is returned with 503 by the seed-keyed 503 injector. The
	// build spec fixes the status and retriability of that fault but not its
	// code; this is the simplest name that cannot be confused with the quota
	// 503, which is the one 503 a client must not retry.
	CodeUnavailable = "SERVICE_UNAVAILABLE"
)

// Quarantine and injection constants of section 3 of the build spec.
const (
	// QuarantineThreshold is the number of consecutive 429s on a single
	// endpoint, without an intervening success on that endpoint, that arms the
	// client quarantine.
	QuarantineThreshold = 20
	// QuarantineQuotaUnits is how long the quarantine lasts once armed,
	// measured in quota units rather than in virtual time.
	QuarantineQuotaUnits = 200
	// Inject429Modulus keys the 429 injector: it fires when the request
	// signature hash mod 23 is zero, on the first delivery of that signature.
	Inject429Modulus = 23
	// Inject503Modulus keys the 503 injector: it fires when the request
	// signature hash mod 37 is zero, on the first delivery of that signature.
	Inject503Modulus = 37
	// ApplyThenFailModulus keys the applied-then-500 chunk: the chunk whose
	// signature hash mod 17 is zero applies its records and then returns 500,
	// once per run.
	ApplyThenFailModulus = 17
)

// CodePermanentTransport is the code of the injected permanent transport error.
//
// It is a 503 that says retriable:false, which is the whole point of it: the
// status line looks like something to retry and the machine-readable flag says
// it is not. Everything in this landscape publishes that flag precisely so a
// client never has to guess from a status code, and a client that retries this
// spends its attempts on a request that will never succeed.
const CodePermanentTransport = "PERMANENT_TRANSPORT_FAILURE"

// Config configures a Governor. It is a value; New copies it and never mutates
// the caller's copy. Non-positive fields have documented meanings so that a
// zero Config is usable in tests: no bucket, no quota and no token expiry.
type Config struct {
	// Seed is the scenario seed. It keys fault injection and token derivation
	// and is the only source of variation in the package.
	Seed int64
	// QuotaLimit is the hard per-run request quota. Non-positive means
	// unlimited.
	QuotaLimit int
	// BucketCapacity is the token-bucket capacity, and its initial fill.
	// Non-positive disables the bucket: no request is ever rate limited.
	BucketCapacity int
	// RefillIntervalVms is the virtual-millisecond interval that earns one
	// token. Non-positive is treated as 1 vms when the bucket is enabled.
	RefillIntervalVms int64
	// Chaos enables fault injection. The --no-chaos flag sets it to false and
	// changes nothing else, so the state digest is identical either way.
	Chaos bool

	// PermanentFaultAt answers the nth matching request of the run with a
	// permanent, non-retriable transport error. Zero disables it. See
	// [Governor.InjectPermanent] for why this is a count and not a hash.
	PermanentFaultAt int
	// PermanentFaultPath narrows what counts as a matching request to the ones
	// whose path contains this substring. Empty matches every non-admin request.
	//
	// A scenario states it because WHERE a permanent failure lands decides what
	// a correct run can still achieve: on a master page it ends the run, and on
	// a posting it leaves those proposals pending and everything else intact.
	PermanentFaultPath string
	// TokenRequestTTL is the number of validated requests an access token
	// survives. Non-positive means it never expires by request count.
	TokenRequestTTL int
	// TokenVirtualTTLVms is the virtual-millisecond lifetime of an access
	// token. Non-positive means it never expires by virtual time.
	TokenVirtualTTLVms int64
	// RateLimitCostVms100 is the virtual cost charged for a 429 or a
	// quarantine 403, in hundredths of a virtual millisecond. Zero or negative
	// means one refill interval, which is what both servers use: the twin by
	// definition, the ERP because its 50 vms penalty is exactly its refill
	// interval.
	RateLimitCostVms100 int64
}

// A Decision is the verdict of Governor.Admit for one request. When Allow is
// true the handler proceeds and Status, Code, Retriable and RetryAfterHintMs are
// zero. When Allow is false the handler must return exactly Status with the
// shared error body built from Code, Retriable and RetryAfterHintMs, and must
// not do any work.
type Decision struct {
	// Allow reports whether the request may proceed.
	Allow bool
	// Status is the HTTP status to return when Allow is false, else 0.
	Status int
	// Code is the error body code to return when Allow is false, else "".
	Code string
	// Retriable is the authoritative machine-readable retry hint of the
	// rejection. A client that retries a Retriable:false response is making a
	// mistake the grader scores.
	Retriable bool
	// RetryAfterHintMs is the retry_after_hint_ms of the rejection: the exact
	// refill interval for a 429, 0 otherwise.
	RetryAfterHintMs int64
	// Seq is the request sequence number, counted from 1 over every non-admin
	// request whether admitted or rejected. It is the second half of the
	// injection key and belongs in the structured log line.
	Seq int64
	// Vms is the virtual clock in whole virtual milliseconds after this
	// request's cost was charged.
	Vms int64
}

// A Governor owns everything that makes a server's request handling
// deterministic: the virtual clock, the token bucket, the quota counters, the
// fault injectors, the access tokens and the metrics. One Governor per server
// per run, guarded by a single mutex, so a handler does one Admit call per
// request and gets back a Decision.
type Governor struct {
	cfg Config

	// Derived, immutable after New.
	quotaLimit    int64 // math.MaxInt64 when unlimited
	bucketEnabled bool
	capacity      int64
	refillVms100  int64
	rateLimitCost int64
	sigAttempts   map[string]int64 // per signature, monotone within a run

	mu    sync.Mutex
	clock Clock

	seq int64 // last assigned request sequence number

	tokens         int64 // current bucket level
	lastRefill100  int64 // clock reading the bucket was last credited from
	quotaUsed      int64
	quotaExhausted bool

	consecutive429  map[string]int64 // per endpoint, reset by a success
	quarantineUntil int64            // quota_used value at which the quarantine lifts
	quarantineArmed bool
	applyFailFired  bool
	// permanentRequests counts non-admin requests for the permanent injector,
	// and permanentFired makes it fire at most once per run.
	permanentRequests int64
	permanentFired    bool
	issuedTokenCount  int64
	accessTokens      map[string]*accessToken
	metrics           Metrics
}

// New returns a Governor for cfg. The bucket starts full and the clock starts at
// zero.
func New(cfg Config) *Governor {
	g := &Governor{
		cfg:            cfg,
		consecutive429: make(map[string]int64),
		sigAttempts:    make(map[string]int64),
		accessTokens:   make(map[string]*accessToken),
	}
	if cfg.QuotaLimit > 0 {
		g.quotaLimit = int64(cfg.QuotaLimit)
	} else {
		g.quotaLimit = math.MaxInt64
	}
	if cfg.BucketCapacity > 0 {
		g.bucketEnabled = true
		g.capacity = int64(cfg.BucketCapacity)
		g.tokens = g.capacity
		g.refillVms100 = cfg.RefillIntervalVms * Vms100PerVms
		if g.refillVms100 <= 0 {
			g.refillVms100 = Vms100PerVms
		}
	}
	g.rateLimitCost = cfg.RateLimitCostVms100
	if g.rateLimitCost <= 0 {
		g.rateLimitCost = g.refillVms100
	}
	g.metrics.ByEndpoint = make(map[string]int64)
	return g
}

// reportedQuotaLimit is the quota limit as it appears in Metrics: the configured
// value, or 0 when the quota is unlimited, because an unlimited quota has no
// meaningful ceiling to publish.
func (g *Governor) reportedQuotaLimit() int64 {
	if g.quotaLimit == math.MaxInt64 {
		return 0
	}
	return g.quotaLimit
}

// Admit accounts for one non-admin request and returns the Decision the handler
// must honor. endpoint is the metrics and quarantine label (a stable route
// name, not a URL with ids in it), costVms100 the request's virtual cost from
// Cost, and records the number of records the request returns or carries, which
// feeds both the cost and records_returned.
//
// Admit is the only place the clock moves and the only place quota is spent.
// The order is fixed and total: quota, quarantine, bucket, success. Rejections
// still consume a quota unit and still advance the clock, because the build spec
// counts every non-admin request including 429s; the one exception is the quota
// 503 itself, which cannot spend a quota that is already gone and therefore
// leaves both the clock and quota_used untouched.
func (g *Governor) Admit(endpoint string, costVms100 int64, records int) Decision {
	if costVms100 < 0 {
		costVms100 = 0
	}
	if records < 0 {
		records = 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.seq++
	g.metrics.RequestsTotal++
	g.metrics.ByEndpoint[endpoint]++
	d := Decision{Seq: g.seq}

	// 1. Hard quota, permanent for the rest of the run.
	if g.quotaExhausted || g.quotaUsed >= g.quotaLimit {
		g.quotaExhausted = true
		d.Status = 503
		d.Code = CodeQuotaExhausted
		d.Retriable = false
		d.Vms = g.clock.Now()
		return d
	}

	// 2. Client quarantine, measured in quota units.
	if g.quarantineArmed {
		if g.quotaUsed >= g.quarantineUntil {
			g.quarantineArmed = false
		} else {
			g.spendQuota()
			g.clock.Advance(g.rateLimitCost)
			d.Status = 403
			d.Code = CodeQuarantined
			d.Retriable = false
			d.Vms = g.clock.Now()
			return d
		}
	}

	// 3. Token bucket, refilled from the virtual clock.
	if g.bucketEnabled {
		g.refill()
		if g.tokens < 1 {
			g.spendQuota()
			g.clock.Advance(g.rateLimitCost)
			g.metrics.RateLimited++
			g.note429(endpoint)
			d.Status = 429
			d.Code = CodeRateLimited
			d.Retriable = true
			d.RetryAfterHintMs = g.refillVms100 / Vms100PerVms
			d.Vms = g.clock.Now()
			return d
		}
		g.tokens--
	}

	// 4. Admitted.
	g.spendQuota()
	delete(g.consecutive429, endpoint)
	g.metrics.RecordsReturned += int64(records)
	g.clock.Advance(costVms100)
	d.Allow = true
	d.Vms = g.clock.Now()
	return d
}

// spendQuota consumes one quota unit. quota_used and virtual_clock_ms are read
// straight off the counters by Metrics rather than mirrored here, so there is one
// source of truth for each. The caller holds the mutex.
func (g *Governor) spendQuota() {
	g.quotaUsed++
	if g.quotaUsed >= g.quotaLimit {
		g.quotaExhausted = true
	}
}

// refill credits whole tokens earned since the last credit, from the virtual
// clock and never from elapsed real time, capped at capacity. The unspent
// remainder of a partial interval is carried forward, except when the bucket
// fills: a full bucket earns nothing, so the carry is dropped rather than banked
// as future credit. The caller holds the mutex.
func (g *Governor) refill() {
	elapsed := g.clock.Now100() - g.lastRefill100
	if elapsed < g.refillVms100 {
		return
	}
	earned := elapsed / g.refillVms100
	g.tokens += earned
	g.lastRefill100 += earned * g.refillVms100
	if g.tokens >= g.capacity {
		g.tokens = g.capacity
		g.lastRefill100 = g.clock.Now100()
	}
}

// note429 records a token-bucket 429 against endpoint and arms the quarantine on
// the QuarantineThreshold-th consecutive one. Only bucket 429s count: injected
// 429s are a property of the seed rather than of the client's behavior, and the
// quarantine exists to punish a client that ignores retry hints. The caller
// holds the mutex.
func (g *Governor) note429(endpoint string) {
	g.consecutive429[endpoint]++
	if g.consecutive429[endpoint] < QuarantineThreshold {
		return
	}
	g.consecutive429[endpoint] = 0
	g.quarantineArmed = true
	g.quarantineUntil = g.quotaUsed + QuarantineQuotaUnits
}

// InjectFault reports the fault, if any, that applies to the logical request
// identified by sig. ok is false when no fault applies, and always false when
// Config.Chaos is off.
//
// Injection is CONTENT-ADDRESSED, never keyed by request sequence. sig is the
// caller's signature for the logical request: METHOD, route template and the
// canonicalized subset of parameters that identify it (a decoded cursor
// position, not the opaque cursor string; the item keys of a posting; the
// company code of a SOAP call). Never the wall clock, the request sequence, the
// goroutine or the connection. The reason is validity: with sequence keying, a
// client that issues the same logical requests in a different order, or in
// parallel, meets a different fault set, so two runs of one correct submission
// score differently. Difficulty must not depend on the client's concurrency
// architecture.
//
// A fault fires only on the FIRST delivery of a signature (attempt 0), so any
// retry of the same logical request passes. That is the fairness guarantee
// behind the published "an injected fault always succeeds on the retry". The
// token bucket's own 429 is a real limiter and is unaffected by this rule.
//
// The 429 injector fires when the signature hash mod 23 is zero, the 503
// injector when it is zero mod 37; when both match the 429 wins, so the two
// never collide on one response.
//
// InjectFault does not advance the clock. The request's own cost was charged by
// Admit; a handler that wants to add the 429 penalty on top calls Advance.
func (g *Governor) InjectFault(sig string) (status int, code string, retriable bool, ok bool) {
	if !g.cfg.Chaos {
		return 0, "", false, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	attempt := g.sigAttempts[sig]
	g.sigAttempts[sig] = attempt + 1
	if attempt != 0 {
		return 0, "", false, false
	}
	h := signatureHash(g.cfg.Seed, sig, attempt)
	switch {
	case h%Inject429Modulus == 0:
		g.metrics.RateLimited++
		return 429, CodeRateLimited, true, true
	case h%Inject503Modulus == 0:
		g.metrics.FiveXXInjected++
		return 503, CodeUnavailable, true, true
	}
	return 0, "", false, false
}

// InjectPermanent reports whether the request at this path must be answered with
// a permanent, non-retriable transport error.
//
// It fires at most once per run, on the nth request matching
// Config.PermanentFaultPath, where n is Config.PermanentFaultAt. Unlike the other injectors this one is a COUNT and
// not a hash: a permanent failure changes what a correct run achieves, so which
// request it lands on has to be something a scenario states rather than something
// a seed decides. Zero disables it, which is every scenario but the one written
// for it.
//
// It is independent of Config.Chaos for the same reason SOAPTruncateAt is: it is a
// property of the scenario, not of the noise.
func (g *Governor) InjectPermanent(path string) (status int, code string, ok bool) {
	if g.cfg.PermanentFaultAt <= 0 {
		return 0, "", false
	}
	if g.cfg.PermanentFaultPath != "" && !strings.Contains(path, g.cfg.PermanentFaultPath) {
		return 0, "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.permanentFired {
		return 0, "", false
	}
	g.permanentRequests++
	if g.permanentRequests != int64(g.cfg.PermanentFaultAt) {
		return 0, "", false
	}
	g.permanentFired = true
	g.metrics.PermanentInjected++
	return 503, CodePermanentTransport, true
}

// ApplyThenFail reports whether the records chunk identified by sig must be
// applied and then answered with 500. It fires for the first chunk signature
// whose hash mod 17 is zero, at most once per run, and only on that signature's
// first delivery, so a client retrying the chunk with the same idempotency key
// gets the stored 207 back and its counts stay right. A client that regenerates
// the key on retry double-applies, which is what duplicate_apply_attempts
// counts.
//
// sig must live in its own namespace (prefix it, e.g. "APPLYFAIL|..."), because
// the attempt counter is shared with InjectFault.
//
// It always returns false when Config.Chaos is off.
func (g *Governor) ApplyThenFail(sig string) bool {
	if !g.cfg.Chaos {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.applyFailFired {
		return false
	}
	attempt := g.sigAttempts[sig]
	g.sigAttempts[sig] = attempt + 1
	if attempt != 0 {
		return false
	}
	if signatureHash(g.cfg.Seed, sig, attempt)%ApplyThenFailModulus != 0 {
		return false
	}
	g.applyFailFired = true
	g.metrics.FiveXXInjected++
	return true
}

// signatureHash is the FNV-1a 64-bit hash of the seed, the request signature and
// the attempt index. It is the only hash in the package and the only source of
// variation in fault injection: no PRNG, no clock, no sequence.
func signatureHash(seed int64, sig string, attempt int64) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(seed))
	h.Write(buf[:])
	h.Write([]byte{0x1f})
	h.Write([]byte(sig))
	h.Write([]byte{0x1f})
	binary.LittleEndian.PutUint64(buf[:], uint64(attempt))
	h.Write(buf[:])
	return h.Sum64()
}

// Advance adds delta hundredths of a virtual millisecond outside the Admit path
// and returns the new clock reading in whole virtual milliseconds. Handlers use
// it to charge a penalty an injected fault adds to a request whose base cost
// Admit already charged.
func (g *Governor) Advance(delta int64) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clock.Advance(delta)
	return g.clock.Now()
}

// NowVms returns the virtual clock in whole virtual milliseconds.
func (g *Governor) NowVms() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clock.Now()
}

// NowVms100 returns the virtual clock in hundredths of a virtual millisecond.
func (g *Governor) NowVms100() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clock.Now100()
}

// RateLimitCostVms100 returns the virtual cost of a 429 or quarantine 403, in
// hundredths of a virtual millisecond.
func (g *Governor) RateLimitCostVms100() int64 { return g.rateLimitCost }

// Metrics returns a deep copy of the request accounting.
func (g *Governor) Metrics() Metrics {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := g.metrics.clone()
	m.VirtualClockMs = g.clock.Now()
	m.QuotaUsed = g.quotaUsed
	m.QuotaLimit = g.reportedQuotaLimit()
	return m
}

// IncDuplicateDocumentAttempts records one repeated ERP document posting under a
// known idempotency key.
func (g *Governor) IncDuplicateDocumentAttempts() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.metrics.DuplicateDocumentAttempts++
}

// IncDuplicateApplyAttempts records one repeated twin chunk application under a
// known idempotency key.
func (g *Governor) IncDuplicateApplyAttempts() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.metrics.DuplicateApplyAttempts++
}

// Inc5xxRetried records one injected 5xx that the client retried.
func (g *Governor) Inc5xxRetried() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.metrics.FiveXXRetried++
}

// floorMod returns a mod m with the sign of m, so a negative seed keys the same
// injector slot as its positive residue instead of never matching.
func floorMod(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}
