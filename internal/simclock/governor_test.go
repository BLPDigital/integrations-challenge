package simclock

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

// erpConfig is the published ERP configuration: 40 tokens, one token per 50 vms.
func erpConfig(seed int64, quota int) Config {
	return Config{
		Seed:               seed,
		QuotaLimit:         quota,
		BucketCapacity:     40,
		RefillIntervalVms:  50,
		Chaos:              true,
		TokenRequestTTL:    250,
		TokenVirtualTTLVms: 900_000,
	}
}

// twinConfig is the published twin configuration: 30 tokens, one per 40 vms.
func twinConfig(seed int64, quota int) Config {
	return Config{
		Seed:               seed,
		QuotaLimit:         quota,
		BucketCapacity:     30,
		RefillIntervalVms:  40,
		Chaos:              true,
		TokenRequestTTL:    250,
		TokenVirtualTTLVms: 900_000,
	}
}

func TestCost(t *testing.T) {
	tests := []struct {
		name    string
		class   EndpointClass
		records int
		want    int64
	}{
		{"list page empty", ERPListPage, 0, 4000},
		{"list page 250", ERPListPage, 250, 16500},
		{"list page 1", ERPListPage, 1, 4050},
		{"single get", ERPSingleGet, 0, 2500},
		{"single get ignores records", ERPSingleGet, 99, 2500},
		{"soap rate table 1000 rows", ERPSoapRateTable, 1000, 32000},
		{"document post", ERPDocumentPost, 0, 6000},
		{"batch post 25 items", ERPBatchDocumentPost, 25, 26000},
		{"erp auth", ERPAuthToken, 0, 1000},
		{"open batch", TwinOpenBatch, 0, 3000},
		{"records chunk 500", TwinRecordsChunk, 500, 9500},
		{"records chunk 1", TwinRecordsChunk, 1, 2015},
		{"commit", TwinCommit, 0, 2000},
		{"single put", TwinSinglePut, 0, 2000},
		{"outbox list 200", TwinOutboxList, 200, 5000},
		{"acks 400", TwinAcks, 400, 4000},
		{"twin auth", TwinAuthToken, 0, 1000},
		{"negative records clamped", TwinAcks, -5, 2000},
		{"unknown class is free", EndpointClass("nope"), 10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Cost(tt.class, tt.records); got != tt.want {
				t.Fatalf("Cost(%q,%d) = %d, want %d", tt.class, tt.records, got, tt.want)
			}
		})
	}
}

func TestClock(t *testing.T) {
	var c Clock
	if got := c.Now(); got != 0 {
		t.Fatalf("fresh clock Now = %d, want 0", got)
	}
	if got := c.Advance(16500); got != 16500 {
		t.Fatalf("Advance = %d, want 16500", got)
	}
	if got := c.Now(); got != 165 {
		t.Fatalf("Now = %d, want 165", got)
	}
	if got := c.Advance(50); got != 16550 {
		t.Fatalf("Advance = %d, want 16550", got)
	}
	if got := c.Now(); got != 165 {
		t.Fatalf("Now = %d after fractional advance, want 165 (truncated)", got)
	}
	if got := c.Now100(); got != 16550 {
		t.Fatalf("Now100 = %d, want 16550", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Advance with negative delta did not panic")
		}
	}()
	c.Advance(-1)
}

// TestBatchingIsNetTokenPositive proves the property the whole rate-limit design
// exists for: a 250-record page costs one token and earns three back, so a
// client that pages properly never sees a 429, no matter how long it runs.
func TestBatchingIsNetTokenPositive(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		class   EndpointClass
		records int
	}{
		{"erp list page 250", erpConfig(7, 0), ERPListPage, 250},
		{"twin chunk 500", twinConfig(7, 0), TwinRecordsChunk, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := New(tt.cfg)
			cost := Cost(tt.class, tt.records)
			for i := 1; i <= 1000; i++ {
				d := g.Admit("page", cost, tt.records)
				if !d.Allow {
					t.Fatalf("call %d rejected with %d %s; batching must never rate limit", i, d.Status, d.Code)
				}
			}
			m := g.Metrics()
			if m.RateLimited != 0 {
				t.Fatalf("429s = %d, want 0", m.RateLimited)
			}
			if want := int64(1000 * tt.records); m.RecordsReturned != want {
				t.Fatalf("records_returned = %d, want %d", m.RecordsReturned, want)
			}
			if want := int64(1000 * cost / Vms100PerVms); m.VirtualClockMs != want {
				t.Fatalf("virtual_clock_ms = %d, want %d", m.VirtualClockMs, want)
			}
		})
	}
}

// TestSingleGetStarves is the mirror image: a single GET earns half a token and
// spends one, so a full ERP bucket dies after 79 admitted calls and the 80th is
// the first 429.
func TestSingleGetStarves(t *testing.T) {
	g := New(erpConfig(7, 0))
	cost := Cost(ERPSingleGet, 0)
	firstReject := 0
	for i := 1; i <= 200; i++ {
		d := g.Admit("supplier_get", cost, 1)
		if !d.Allow {
			if d.Status != 429 || d.Code != CodeRateLimited || !d.Retriable {
				t.Fatalf("call %d: got %d %s retriable=%v, want 429 %s retriable=true",
					i, d.Status, d.Code, d.Retriable, CodeRateLimited)
			}
			if d.RetryAfterHintMs != 50 {
				t.Fatalf("retry_after_hint_ms = %d, want 50 (the refill interval)", d.RetryAfterHintMs)
			}
			firstReject = i
			break
		}
	}
	if firstReject != 80 {
		t.Fatalf("first 429 at call %d, want 80", firstReject)
	}
}

// TestQuarantineTripsAtExactlyTwenty pins both edges: the twentieth consecutive
// 429 is still a 429, the next request is the 403, and the quarantine lifts after
// exactly 200 quota units and not one earlier.
//
// The 429 penalty is set below the refill interval on purpose. Both shipped
// servers charge exactly one refill interval for a 429, which means a bucket 429
// hands the client back the token it just failed to get and 429s can never run
// consecutively; the quarantine is a tripwire for a client that manufactures them
// some other way, so exercising it needs a configuration where they can.
func TestQuarantineTripsAtExactlyTwenty(t *testing.T) {
	g := New(Config{
		Seed:                1,
		QuotaLimit:          1000,
		BucketCapacity:      1,
		RefillIntervalVms:   1000,
		RateLimitCostVms100: 100,
	})
	if d := g.Admit("list", 0, 0); !d.Allow {
		t.Fatalf("call 1 rejected with %d %s, want allow", d.Status, d.Code)
	}
	for i := 2; i <= 21; i++ {
		d := g.Admit("list", 0, 0)
		if d.Status != 429 {
			t.Fatalf("call %d: got %d %s, want 429 (429 number %d of 20)", i, d.Status, d.Code, i-1)
		}
	}
	if got := g.Metrics().RateLimited; got != 20 {
		t.Fatalf("429s = %d, want 20", got)
	}
	// Quota used at the moment of the trip is 21; the quarantine covers the
	// next 200 quota units, so calls 22..221 are 403 and 222 is not.
	for i := 22; i <= 221; i++ {
		d := g.Admit("list", 0, 0)
		if d.Status != 403 || d.Code != CodeQuarantined || d.Retriable {
			t.Fatalf("call %d: got %d %s retriable=%v, want 403 %s retriable=false",
				i, d.Status, d.Code, d.Retriable, CodeQuarantined)
		}
	}
	if d := g.Admit("list", 0, 0); d.Status == 403 {
		t.Fatal("call 222 still quarantined; the quarantine must lift after exactly 200 quota units")
	}
	if got := g.Metrics().QuotaUsed; got != 222 {
		t.Fatalf("quota_used = %d, want 222", got)
	}
}

// TestQuarantineResetsOnSuccess proves the counter is consecutive per endpoint
// and that a success on that endpoint clears it.
func TestQuarantineResetsOnSuccess(t *testing.T) {
	cfg := Config{Seed: 1, QuotaLimit: 1000, BucketCapacity: 1, RefillIntervalVms: 1, RateLimitCostVms100: 100}
	g := New(cfg)
	// Bucket capacity 1, refill every 1 vms (100 vms100), 429 penalty 1 vms:
	// every 429 refills the token, so successes and 429s alternate forever.
	rejects := 0
	for i := 0; i < 200; i++ {
		if d := g.Admit("list", 0, 0); !d.Allow {
			rejects++
			if d.Status != 429 {
				t.Fatalf("call %d: got %d %s, want 429", i, d.Status, d.Code)
			}
		}
	}
	if rejects == 0 {
		t.Fatal("expected some 429s")
	}
	if got := g.Metrics().ByEndpoint["list"]; got != 200 {
		t.Fatalf("by_endpoint[list] = %d, want 200", got)
	}
}

func TestQuotaCutoffIsExact(t *testing.T) {
	tests := []struct{ limit, calls int }{{1, 5}, {3, 10}, {17, 40}}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			g := New(Config{QuotaLimit: tt.limit})
			for i := 1; i <= tt.calls; i++ {
				d := g.Admit("list", Cost(ERPListPage, 10), 10)
				switch {
				case i <= tt.limit && !d.Allow:
					t.Fatalf("call %d of limit %d rejected with %d %s", i, tt.limit, d.Status, d.Code)
				case i > tt.limit && d.Allow:
					t.Fatalf("call %d exceeded limit %d but was admitted", i, tt.limit)
				case i > tt.limit:
					if d.Status != 503 || d.Code != CodeQuotaExhausted || d.Retriable {
						t.Fatalf("call %d: got %d %s retriable=%v, want 503 %s retriable=false",
							i, d.Status, d.Code, d.Retriable, CodeQuotaExhausted)
					}
				}
			}
			m := g.Metrics()
			if m.QuotaUsed != int64(tt.limit) {
				t.Fatalf("quota_used = %d, want %d", m.QuotaUsed, tt.limit)
			}
			if m.QuotaLimit != int64(tt.limit) {
				t.Fatalf("quota_limit = %d, want %d", m.QuotaLimit, tt.limit)
			}
			if m.RequestsTotal != int64(tt.calls) {
				t.Fatalf("requests_total = %d, want %d", m.RequestsTotal, tt.calls)
			}
			// The quota 503 must not move the clock: the request did no work.
			wantVms := int64(tt.limit) * Cost(ERPListPage, 10) / Vms100PerVms
			if m.VirtualClockMs != wantVms {
				t.Fatalf("virtual_clock_ms = %d, want %d", m.VirtualClockMs, wantVms)
			}
		})
	}
}

func TestQuotaCountsRejections(t *testing.T) {
	// Capacity 1 with a long refill: call 1 is admitted, the rest are 429, and
	// every one of them still costs a quota unit.
	g := New(Config{QuotaLimit: 5, BucketCapacity: 1, RefillIntervalVms: 10_000, RateLimitCostVms100: 1})
	for i := 1; i <= 5; i++ {
		g.Admit("list", 0, 0)
	}
	if d := g.Admit("list", 0, 0); d.Status != 503 || d.Code != CodeQuotaExhausted {
		t.Fatalf("call 6: got %d %s, want 503 %s", d.Status, d.Code, CodeQuotaExhausted)
	}
	m := g.Metrics()
	if m.QuotaUsed != 5 || m.RateLimited != 4 {
		t.Fatalf("quota_used = %d, 429s = %d; want 5 and 4", m.QuotaUsed, m.RateLimited)
	}
}

func TestUnlimitedQuotaAndDisabledBucket(t *testing.T) {
	g := New(Config{})
	for i := 0; i < 5000; i++ {
		if d := g.Admit("list", Cost(ERPSingleGet, 0), 1); !d.Allow {
			t.Fatalf("call %d rejected with %d %s; zero Config must not limit anything", i, d.Status, d.Code)
		}
	}
	if got := g.Metrics().QuotaLimit; got != 0 {
		t.Fatalf("quota_limit = %d, want 0 for an unlimited quota", got)
	}
}

func TestInjectFaultIsContentAddressed(t *testing.T) {
	// Injection is a pure function of (seed, signature, attempt). These
	// signature indexes were computed for seed 42 and pin every branch.
	const seed = 42
	tests := []struct {
		sig        string
		wantOK     bool
		wantStatus int
		wantCode   string
		note       string
	}{
		{"sig-0", false, 0, "", "clean"},
		{"sig-1", false, 0, "", "clean"},
		{"sig-31", true, 429, CodeRateLimited, "429 slot only"},
		{"sig-32", true, 429, CodeRateLimited, "429 slot only"},
		{"sig-8", true, 503, CodeUnavailable, "503 slot only"},
		{"sig-39", true, 503, CodeUnavailable, "503 slot only"},
		{"sig-97", true, 429, CodeRateLimited, "both injectors match; 429 wins"},
	}
	g := New(Config{Seed: seed, Chaos: true})
	for _, tt := range tests {
		status, code, retriable, ok := g.InjectFault(tt.sig)
		if ok != tt.wantOK || status != tt.wantStatus || code != tt.wantCode {
			t.Fatalf("InjectFault(%q) = (%d,%q,%v,%v), want (%d,%q,_,%v)  [%s]",
				tt.sig, status, code, retriable, ok, tt.wantStatus, tt.wantCode, tt.wantOK, tt.note)
		}
		if ok && !retriable {
			t.Fatalf("InjectFault(%q) not retriable; every injected fault is retriable", tt.sig)
		}
	}
}

func TestInjectFaultFiresOnlyOnFirstDelivery(t *testing.T) {
	// The fairness guarantee: an injected fault always succeeds on the retry of
	// the same logical request, so a capped-attempts client always makes
	// progress. Attempt 0 fires, every later attempt of that signature passes.
	g := New(Config{Seed: 42, Chaos: true})
	if _, _, _, ok := g.InjectFault("sig-31"); !ok {
		t.Fatal("first delivery of sig-31 was not faulted")
	}
	for attempt := 1; attempt <= 10; attempt++ {
		if _, _, _, ok := g.InjectFault("sig-31"); ok {
			t.Fatalf("attempt %d of sig-31 was faulted; retries must pass", attempt)
		}
	}
}

func TestInjectFaultIsIndependentOfCallOrder(t *testing.T) {
	// The validity property. A client that issues the same logical requests in
	// a different order must meet exactly the same faults, or the score depends
	// on the client's concurrency architecture rather than on its correctness.
	sigs := make([]string, 200)
	for i := range sigs {
		sigs[i] = "sig-" + strconv.Itoa(i)
	}
	collect := func(order []int) map[string]int {
		g := New(Config{Seed: 42, Chaos: true})
		out := map[string]int{}
		for _, i := range order {
			if status, _, _, ok := g.InjectFault(sigs[i]); ok {
				out[sigs[i]] = status
			}
		}
		return out
	}
	forward := make([]int, len(sigs))
	for i := range forward {
		forward[i] = i
	}
	reverse := make([]int, len(sigs))
	for i := range reverse {
		reverse[i] = len(sigs) - 1 - i
	}
	interleaved := make([]int, 0, len(sigs))
	for i := 0; i < len(sigs); i += 2 {
		interleaved = append(interleaved, i)
	}
	for i := 1; i < len(sigs); i += 2 {
		interleaved = append(interleaved, i)
	}
	base := collect(forward)
	if len(base) == 0 {
		t.Fatal("no fault fired over 200 signatures; the fixture proves nothing")
	}
	for name, order := range map[string][]int{"reverse": reverse, "interleaved": interleaved} {
		if got := collect(order); !reflect.DeepEqual(got, base) {
			t.Fatalf("fault set depends on call order (%s):\n%v\n%v", name, base, got)
		}
	}
}

func TestInjectFaultChaosOff(t *testing.T) {
	g := New(Config{Seed: 42})
	for i := 0; i < 500; i++ {
		if _, _, _, ok := g.InjectFault("sig-" + strconv.Itoa(i)); ok {
			t.Fatalf("sig-%d injected a fault with chaos off", i)
		}
	}
	for i := 0; i < 200; i++ {
		if g.ApplyThenFail("APPLYFAIL|" + strconv.Itoa(i)) {
			t.Fatal("ApplyThenFail fired with chaos off")
		}
	}
	if m := g.Metrics(); m.RateLimited != 0 || m.FiveXXInjected != 0 {
		t.Fatalf("chaos off must inject nothing, got 429s=%d 5xx_injected=%d", m.RateLimited, m.FiveXXInjected)
	}
}

func TestInjectFaultNegativeSeed(t *testing.T) {
	// A negative seed is a seed like any other: the hash takes its two's
	// complement bytes, so nothing silently never matches.
	g := New(Config{Seed: -1, Chaos: true})
	if _, _, _, ok := g.InjectFault("sig-1"); !ok {
		t.Fatal("seed -1 should fault sig-1")
	}
}

func TestApplyThenFailFiresExactlyOnce(t *testing.T) {
	// For seed 42 the first applied-then-500 signature is APPLYFAIL|124.
	g := New(Config{Seed: 42, Chaos: true})
	fired := -1
	for i := 0; i < 200; i++ {
		if g.ApplyThenFail("APPLYFAIL|" + strconv.Itoa(i)) {
			if fired >= 0 {
				t.Fatalf("ApplyThenFail fired twice, at %d and %d; exactly one chunk per run fails", fired, i)
			}
			fired = i
		}
	}
	if fired != 124 {
		t.Fatalf("ApplyThenFail fired at %d, want 124", fired)
	}
	if g.ApplyThenFail("APPLYFAIL|124") {
		t.Fatal("the same chunk signature failed twice")
	}
	if got := g.Metrics().FiveXXInjected; got != 1 {
		t.Fatalf("5xx_injected = %d, want 1", got)
	}
}

func TestTokenLifecycle(t *testing.T) {
	g := New(Config{Seed: 3, TokenRequestTTL: 250, TokenVirtualTTLVms: 900_000})
	if got := g.ValidateToken("tok_nope"); got != TokenInvalid {
		t.Fatalf("unissued token = %v, want invalid", got)
	}
	tok := g.IssueToken()
	if tok == "" {
		t.Fatal("IssueToken returned an empty token")
	}
	for i := 1; i <= 250; i++ {
		if got := g.ValidateToken(tok); got != TokenOK {
			t.Fatalf("validation %d = %v, want ok", i, got)
		}
	}
	if got := g.ValidateToken(tok); got != TokenExpired {
		t.Fatalf("validation 251 = %v, want expired", got)
	}
	if got := g.ValidateToken(tok); got != TokenExpired {
		t.Fatalf("expiry must latch, got %v", got)
	}

	// Virtual deadline: a fresh token dies once the clock passes it, whatever
	// the request count.
	tok2 := g.IssueToken()
	g.Advance(900_000 * Vms100PerVms)
	if got := g.ValidateToken(tok2); got != TokenOK {
		t.Fatalf("at exactly the deadline = %v, want ok", got)
	}
	g.Advance(1)
	if got := g.ValidateToken(tok2); got != TokenExpired {
		t.Fatalf("past the deadline = %v, want expired", got)
	}
	if reqs, vms := g.TokenTTL(); reqs != 250 || vms != 900_000 {
		t.Fatalf("TokenTTL = (%d,%d), want (250,900000)", reqs, vms)
	}
}

func TestTokenNeverExpiresWithZeroTTL(t *testing.T) {
	g := New(Config{})
	tok := g.IssueToken()
	g.Advance(1 << 40)
	for i := 0; i < 1000; i++ {
		if got := g.ValidateToken(tok); got != TokenOK {
			t.Fatalf("validation %d = %v, want ok with no TTL configured", i, got)
		}
	}
}

func TestTokenValuesAreSeedDerived(t *testing.T) {
	a, b := New(Config{Seed: 99}), New(Config{Seed: 99})
	c := New(Config{Seed: 100})
	var av, bv, cv [3]string
	for i := 0; i < 3; i++ {
		av[i], bv[i], cv[i] = a.IssueToken(), b.IssueToken(), c.IssueToken()
	}
	if av != bv {
		t.Fatalf("same seed issued different tokens: %v vs %v", av, bv)
	}
	if av == cv {
		t.Fatalf("different seeds issued identical tokens: %v", av)
	}
	if av[0] == av[1] {
		t.Fatalf("issue counter ignored: %q issued twice", av[0])
	}
	// A token from one Governor is meaningless in another.
	if got := c.ValidateToken(av[0]); got != TokenInvalid {
		t.Fatalf("foreign token = %v, want invalid", got)
	}
}

func TestExplicitCounters(t *testing.T) {
	g := New(Config{})
	g.IncDuplicateDocumentAttempts()
	g.IncDuplicateDocumentAttempts()
	g.IncDuplicateApplyAttempts()
	g.Inc5xxRetried()
	g.Inc5xxRetried()
	g.Inc5xxRetried()
	m := g.Metrics()
	if m.DuplicateDocumentAttempts != 2 || m.DuplicateApplyAttempts != 1 || m.FiveXXRetried != 3 {
		t.Fatalf("counters = (%d,%d,%d), want (2,1,3)",
			m.DuplicateDocumentAttempts, m.DuplicateApplyAttempts, m.FiveXXRetried)
	}
}

func TestMetricsJSONShape(t *testing.T) {
	g := New(erpConfig(5, 100))
	g.Admit("suppliers_list", Cost(ERPListPage, 250), 250)
	g.Admit("supplier_get", Cost(ERPSingleGet, 0), 1)
	g.IncDuplicateDocumentAttempts()
	g.IncDuplicateApplyAttempts()
	g.Inc5xxRetried()
	b, err := json.Marshal(g.Metrics())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"requests_total":2,"by_endpoint":{"supplier_get":1,"suppliers_list":1},` +
		`"429s":0,"5xx_injected":0,"5xx_retried":1,"permanent_injected":0,"quota_used":2,"quota_limit":100,` +
		`"records_returned":251,"virtual_clock_ms":190,"duplicate_document_attempts":1,` +
		`"duplicate_apply_attempts":1}`
	if string(b) != want {
		t.Fatalf("metrics JSON mismatch\n got: %s\nwant: %s", b, want)
	}
}

func TestMetricsIsACopy(t *testing.T) {
	g := New(Config{})
	g.Admit("list", 0, 0)
	m := g.Metrics()
	m.ByEndpoint["list"] = 999
	m.RequestsTotal = 999
	if got := g.Metrics(); got.ByEndpoint["list"] != 1 || got.RequestsTotal != 1 {
		t.Fatalf("Metrics leaked internal state: %+v", got)
	}
	if got := New(Config{}).Metrics().ByEndpoint; got == nil {
		t.Fatal("by_endpoint is nil; it must marshal as {} not null")
	}
	_ = reflect.DeepEqual(m, m)
}
