package grader

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func init() {
	register(KindCounter, checkCounter)
	register(KindQuotaWithin, checkQuotaWithin)
	register(KindRequestBudget, checkRequestBudget)
	register(KindERPRequestsRouteMax, checkERPRequestsRouteMax)
	register(KindNoRetryOnNonRetriable, checkNoRetryOnNonRetriable)
	register(KindWastedRetriesMax, checkWastedRetriesMax)
	register(KindERPLockoutsEqZero, checkERPLockoutsEqZero)
	register(KindERPAdminPortRequestsEqZero, checkERPAdminRequestsEqZero)
	register(KindERPForbiddenPathHitsEqZero, checkERPForbiddenPathHits)
	register(KindIdempotencyKeysStableAcross, checkIdempotencyKeysStable)
	register(KindNoRepostOnResume, checkNoRepostOnResume)
	register(KindLegacyProfileUsed, checkLegacyProfileUsed)
	register(KindNoUnproposedPostings, checkNoUnproposedPostings)
	register(KindHonoredRetryAfter, checkRetryAfterStruck)
	register(KindERPRetryAfterViolations, checkRetryAfterStruck)
}

// The assertion kinds about efficiency, retry discipline and delivery semantics.
const (
	// KindCounter compares one named server counter.
	KindCounter = "counter"
	// KindQuotaWithin asserts the run stayed inside the published hard quota.
	KindQuotaWithin = "quota_within"
	// KindRequestBudget is the scored efficiency assertion: binary in-budget,
	// with the actual ratio reported as information only.
	KindRequestBudget = "request_budget"
	// KindERPRequestsRouteMax bounds requests per route, which is where an N+1
	// becomes visible even when the total happens to fit.
	KindERPRequestsRouteMax = "erp_requests_route_max"
	// KindNoRetryOnNonRetriable asserts nothing marked retriable:false was
	// retried.
	KindNoRetryOnNonRetriable = "no_retry_on_nonretriable"
	// KindWastedRetriesMax bounds re-requests of a logical request that already
	// succeeded.
	KindWastedRetriesMax = "wasted_retries_max"
	// KindERPLockoutsEqZero asserts the connector was never quarantined.
	KindERPLockoutsEqZero = "erp_lockouts_eq_0"
	// KindERPAdminPortRequestsEqZero asserts the connector never touched an
	// admin surface. It has no admin token, so a hit means it went looking.
	KindERPAdminPortRequestsEqZero = "erp_admin_port_requests_eq_0"
	// KindERPForbiddenPathHitsEqZero asserts the connector never touched a path
	// outside its contract.
	KindERPForbiddenPathHitsEqZero = "erp_forbidden_path_hits_eq_0"
	// KindIdempotencyKeysStableAcross asserts the same proposal produced the
	// same key in two phases.
	KindIdempotencyKeysStableAcross = "idempotency_keys_stable_across"
	// KindNoRepostOnResume asserts a resumed run created no second document.
	KindNoRepostOnResume = "no_repost_on_resume"
	// KindLegacyProfileUsed asserts the kredexp-2.1 profile actually ran inside
	// the twin. Pre-transforming the legacy file outside the twin is an
	// automatic disqualifier per BUILD-SPEC 13.
	KindLegacyProfileUsed = "legacy_profile_used"
	// KindNoUnproposedPostings asserts every ERP document corresponds to a
	// proposal the twin emitted: the mechanical form of "nothing unmatched is
	// ever posted".
	KindNoUnproposedPostings = "no_unproposed_postings"
	// KindHonoredRetryAfter and KindERPRetryAfterViolations are struck from the
	// vocabulary by BUILD-SPEC 17.1 and are registered only so a scenario file
	// that still names one gets the reason instead of an unknown-kind error.
	KindHonoredRetryAfter       = "honored_retry_after"
	KindERPRetryAfterViolations = "erp_retry_after_violations_eq_0"
)

// counterArgs are the arguments of a counter assertion.
type counterArgs struct {
	// Name is the counter, e.g. "erp.duplicate_document_attempts" or
	// "twin.duplicate_apply_attempts".
	Name string `json:"name"`
	// Equals, Max and Min constrain it. At least one is required.
	Equals *int64 `json:"equals,omitempty"`
	Max    *int64 `json:"max,omitempty"`
	Min    *int64 `json:"min,omitempty"`
	// Hint replaces the generic explanation in a failure, for a counter whose
	// meaning is worth spelling out.
	Hint string `json:"hint,omitempty"`
}

// counterValue resolves a named counter out of the observed state.
func counterValue(name string, obs *Observed) (int64, string, bool) {
	switch name {
	case "erp.requests_total":
		return obs.ERPMetrics.RequestsTotal, obs.Ev("erp-metrics.json"), true
	case "erp.429s":
		return obs.ERPMetrics.RateLimited, obs.Ev("erp-metrics.json"), true
	case "erp.5xx_injected":
		return obs.ERPMetrics.FiveXXInjected, obs.Ev("erp-metrics.json"), true
	case "erp.5xx_retried":
		return obs.ERPMetrics.FiveXXRetried, obs.Ev("erp-metrics.json"), true
	case "erp.quota_used":
		return obs.ERPMetrics.QuotaUsed, obs.Ev("erp-metrics.json"), true
	case "erp.duplicate_document_attempts":
		return obs.ERPMetrics.DuplicateDocumentAttempts, obs.Ev("erp-metrics.json"), true
	case "erp.documents_created":
		return int64(obs.ERPMetrics.DocumentsCreated), obs.Ev("erp-metrics.json"), true
	case "erp.idempotency_keys":
		return int64(obs.ERPMetrics.IdempotencyKeys), obs.Ev("erp-metrics.json"), true
	case "erp.soap_calls":
		return int64(obs.ERPMetrics.SOAPCalls), obs.Ev("erp-soap-calls.json"), true
	case "twin.requests_total":
		return obs.TwinMetrics.RequestsTotal, obs.Ev("twin-metrics.json"), true
	case "twin.429s":
		return obs.TwinMetrics.RateLimited, obs.Ev("twin-metrics.json"), true
	case "twin.quota_used":
		return obs.TwinMetrics.QuotaUsed, obs.Ev("twin-metrics.json"), true
	case "twin.duplicate_apply_attempts":
		return obs.TwinMetrics.DuplicateApplyAttempts, obs.Ev("twin-metrics.json"), true
	case "twin.batches":
		return int64(obs.TwinMetrics.Batches), obs.Ev("twin-metrics.json"), true
	case "twin.exceptions_open":
		return int64(obs.TwinMetrics.ExceptionsOpen), obs.Ev("twin-metrics.json"), true
	case "twin.closure_violations":
		return int64(obs.TwinMetrics.ClosureViolations), obs.Ev("twin-metrics.json"), true
	case "twin.inbox_scans":
		return obs.TwinMetrics.Scans, obs.Ev("twin-metrics.json"), true
	}
	return 0, "", false
}

// CounterNames returns the counters a counter assertion may name, in ascending
// order. It exists so a scenario file's typo produces a list rather than a zero.
func CounterNames() []string {
	names := []string{
		"erp.requests_total", "erp.429s", "erp.5xx_injected", "erp.5xx_retried",
		"erp.quota_used", "erp.duplicate_document_attempts", "erp.documents_created",
		"erp.idempotency_keys", "erp.soap_calls",
		"twin.requests_total", "twin.429s", "twin.quota_used",
		"twin.duplicate_apply_attempts", "twin.batches", "twin.exceptions_open",
		"twin.closure_violations", "twin.inbox_scans",
	}
	sort.Strings(names)
	return names
}

// checkCounter compares one named server counter.
func checkCounter(a Assertion, obs *Observed) (Result, error) {
	var args counterArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	got, evidence, ok := counterValue(args.Name, obs)
	if !ok {
		return Result{}, fmt.Errorf("assertion %s: unknown counter %q; known counters are %s",
			a.ID, args.Name, strings.Join(CounterNames(), ", "))
	}
	if args.Equals == nil && args.Max == nil && args.Min == nil {
		return Result{}, fmt.Errorf("assertion %s: one of equals, max or min is required", a.ID)
	}
	hint := args.Hint
	if hint == "" && strings.HasSuffix(args.Name, "duplicate_document_attempts") {
		hint = "a duplicate attempt means the same source document was posted twice under different keys; " +
			"there is no reversal endpoint in a customer ledger"
	}
	res := Result{Status: StatusPass}
	addFail := func(expected string) {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject: args.Name, Expected: expected, Actual: formatInt(got),
			Hint: hint, EvidencePath: evidence})
	}
	if args.Equals != nil && got != *args.Equals {
		addFail(formatInt(*args.Equals))
	}
	if args.Max != nil && got > *args.Max {
		addFail(fmt.Sprintf("at most %d", *args.Max))
	}
	if args.Min != nil && got < *args.Min {
		addFail(fmt.Sprintf("at least %d", *args.Min))
	}
	res.Summary = fmt.Sprintf("%s is %d", args.Name, got)
	return res, nil
}

// quotaArgs are the arguments of a quota_within assertion.
type quotaArgs struct {
	// Service is "erp", "twin" or "" for both.
	Service string `json:"service,omitempty"`
}

// checkQuotaWithin asserts the run stayed inside the published hard quota.
//
// Exceeding a quota is not measured by comparing a counter to the limit: the
// Governor refuses the request and answers 503 QUOTA_EXHAUSTED with
// retriable:false, so the observable fact is the refusal, and that is what this
// looks for. A run that spent exactly its quota and stopped is inside budget.
func checkQuotaWithin(a Assertion, obs *Observed) (Result, error) {
	var args quotaArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	res := Result{Status: StatusPass, Info: map[string]string{}}
	type svc struct {
		name        string
		used, limit int64
		evidence    string
	}
	var services []svc
	if args.Service == "" || args.Service == "erp" {
		services = append(services, svc{"erp", obs.ERPMetrics.QuotaUsed,
			obs.ERPMetrics.QuotaLimit, obs.Ev("erp-metrics.json")})
	}
	if args.Service == "" || args.Service == "twin" {
		services = append(services, svc{"twin", obs.TwinMetrics.QuotaUsed,
			obs.TwinMetrics.QuotaLimit, obs.Ev("twin-metrics.json")})
	}
	for _, s := range services {
		res.Info[s.name+"_quota_used"] = formatInt(s.used)
		res.Info[s.name+"_quota_limit"] = formatInt(s.limit)
		if s.limit > 0 {
			res.Info[s.name+"_quota_ratio_permille"] = formatInt(s.used * 1000 / s.limit)
		}
	}
	exhausted := 0
	for _, req := range obs.ERPRequests {
		if req.Status == http.StatusServiceUnavailable && req.InjectedFault == "" {
			exhausted++
		}
	}
	if exhausted > 0 && (args.Service == "" || args.Service == "erp") {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "erp quota",
			Expected:     fmt.Sprintf("the run fits in %d requests", obs.ERPMetrics.QuotaLimit),
			Actual:       fmt.Sprintf("%d requests were refused with QUOTA_EXHAUSTED", exhausted),
			Hint:         "QUOTA_EXHAUSTED is retriable:false; the work after it never happened",
			EvidencePath: obs.Ev("erp-requests.json")})
	}
	for _, s := range services {
		if s.limit > 0 && s.used > s.limit {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      s.name + " quota",
				Expected:     fmt.Sprintf("at most %d", s.limit),
				Actual:       formatInt(s.used),
				EvidencePath: s.evidence})
		}
	}
	if res.Status == StatusPass {
		var parts []string
		for _, s := range services {
			parts = append(parts, fmt.Sprintf("%s %d/%d", s.name, s.used, s.limit))
		}
		res.Summary = "inside the published quota: " + strings.Join(parts, ", ")
	} else {
		res.Summary = "the run left the published quota"
	}
	return res, nil
}

// budgetArgs are the arguments of a request_budget assertion.
type budgetArgs struct {
	// ERPMax and TwinMax are the request budgets. Zero means the published hard
	// quota of the scenario.
	ERPMax  int64 `json:"erp_max,omitempty"`
	TwinMax int64 `json:"twin_max,omitempty"`
}

// checkRequestBudget is the scored efficiency assertion.
//
// It is binary: in budget or not. The actual ratio is reported as information
// and never converted into partial credit, because a graded ratio would reward
// micro-optimizing a request count that the virtual clock already makes free,
// and because BUILD-SPEC 17.1 settles that the scored efficiency assertion is the
// order-independent request budget and nothing finer. 429s are informational and
// never scored, for the same reason: under a virtual clock nobody waits.
func checkRequestBudget(a Assertion, obs *Observed) (Result, error) {
	var args budgetArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	erpMax := args.ERPMax
	if erpMax == 0 {
		erpMax = int64(obs.ERPQuota)
	}
	twinMax := args.TwinMax
	if twinMax == 0 {
		twinMax = int64(obs.TwinQuota)
	}
	erp := obs.ERPMetrics.RequestsTotal
	twin := obs.TwinMetrics.RequestsTotal
	res := Result{Status: StatusPass, Info: map[string]string{
		"erp_requests":  formatInt(erp),
		"erp_budget":    formatInt(erpMax),
		"twin_requests": formatInt(twin),
		"twin_budget":   formatInt(twinMax),
		"erp_429s":      formatInt(obs.ERPMetrics.RateLimited) + " (informational, never scored)",
		"twin_429s":     formatInt(obs.TwinMetrics.RateLimited) + " (informational, never scored)",
	}}
	if erpMax > 0 {
		res.Info["erp_ratio_permille"] = formatInt(erp * 1000 / erpMax)
	}
	if twinMax > 0 {
		res.Info["twin_ratio_permille"] = formatInt(twin * 1000 / twinMax)
	}
	if erpMax > 0 && erp > erpMax {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "erp requests",
			Expected:     fmt.Sprintf("at most %d", erpMax),
			Actual:       formatInt(erp),
			Hint:         "batching is monotonically rewarded: a 250-record page costs one request and earns 3.3 tokens back",
			EvidencePath: obs.Ev("erp-requests.json")})
	}
	if twinMax > 0 && twin > twinMax {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "twin requests",
			Expected:     fmt.Sprintf("at most %d", twinMax),
			Actual:       formatInt(twin),
			EvidencePath: obs.Ev("twin-metrics.json")})
	}
	verdict := "in budget"
	if res.Status == StatusFail {
		verdict = "over budget"
	}
	res.Summary = fmt.Sprintf("%s: erp %d/%d, twin %d/%d", verdict, erp, erpMax, twin, twinMax)
	return res, nil
}

// routeMaxArgs are the arguments of an erp_requests_route_max assertion.
type routeMaxArgs struct {
	// Routes maps an endpoint class to its budget, e.g.
	// {"erp_single_get": 40, "erp_list_page": 220}.
	Routes map[string]int64 `json:"routes"`
}

// checkERPRequestsRouteMax bounds requests per route.
//
// Per-route budgets are where an N+1 shows up. A connector that fetches one
// supplier per invoice can still fit a generous total budget on a small dataset
// and will not fit erp_single_get, which is net token negative on purpose.
func checkERPRequestsRouteMax(a Assertion, obs *Observed) (Result, error) {
	var args routeMaxArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	if len(args.Routes) == 0 {
		return Result{}, fmt.Errorf("assertion %s: routes must not be empty", a.ID)
	}
	res := Result{Status: StatusPass, Info: map[string]string{}}
	for _, route := range sortedKeys(args.Routes) {
		got := obs.ERPMetrics.ByEndpoint[route]
		res.Info[route] = fmt.Sprintf("%d/%d", got, args.Routes[route])
		if got > args.Routes[route] {
			res.Status = StatusFail
			hint := ""
			if route == "erp_single_get" {
				hint = "a single GET per record is the N+1 shape; the list endpoints page 250 at a time"
			}
			res.Findings = append(res.Findings, Finding{
				Subject:      route,
				Expected:     fmt.Sprintf("at most %d requests", args.Routes[route]),
				Actual:       formatInt(got),
				Hint:         hint,
				EvidencePath: obs.Ev("erp-requests.json")})
		}
	}
	if res.Status == StatusPass {
		res.Summary = "every route is inside its budget: " + renderCounts(obs.ERPMetrics.ByEndpoint)
	} else {
		res.Summary = fmt.Sprintf("%d routes are over budget", len(res.Findings))
	}
	return res, nil
}

// nonRetriableStatus reports whether a logged response was one a client must not
// retry.
//
// It is derived from the status and the injected-fault marker, which is all the
// request log carries and all that is needed:
//
//   - 400, 409, 415 and 422 are client mistakes; retrying reproduces them.
//   - 403 is CLIENT_QUARANTINED, retriable:false for 200 quota units.
//   - 503 with no injected fault is QUOTA_EXHAUSTED, retriable:false for the
//     rest of the run. A 503 WITH an injected fault is the seeded retriable one
//     and succeeds on the next attempt, so it is deliberately not in this set.
//   - 401 is deliberately absent: TOKEN_EXPIRED is retriable:true, and a client
//     that refreshes its token after one is doing the right thing.
func nonRetriableStatus(req ERPRequest) bool {
	// The flag the answer carried, when the service recorded one. It is the
	// authority and the status is not: a 503 is retriable when the token bucket
	// is empty and permanent when the failure is, and this check read the status
	// instead until a mutant that retried a permanent 503 passed it.
	if req.Retriable != nil {
		return !*req.Retriable
	}
	switch req.Status {
	case http.StatusBadRequest, http.StatusConflict,
		http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity,
		http.StatusForbidden:
		return true
	case http.StatusServiceUnavailable:
		return req.InjectedFault == ""
	}
	return false
}

// checkNoRetryOnNonRetriable asserts nothing marked retriable:false was retried.
//
// The check is order-independent by construction: it groups the ERP's own request
// log by the content-addressed signature of the logical request, so a parallel
// connector and a serial one are judged identically. That is the same signature
// the fault injector uses, per BUILD-SPEC 17.1.
//
// The memory is per INVOCATION, see [invocationScope]: a permanent refusal is
// the previous run's answer, and the run after it is entitled to the work.
func checkNoRetryOnNonRetriable(a Assertion, obs *Observed) (Result, error) {
	type sigState struct {
		firstNonRetriable int64
		retriesAfter      int
		status            int
		path              string
	}
	states := map[string]*sigState{}
	var scope invocationScope
	for _, req := range obs.ERPRequests {
		if scope.begins(req) {
			states = map[string]*sigState{}
		}
		st := states[req.Signature]
		if st == nil {
			st = &sigState{}
			states[req.Signature] = st
		}
		if st.firstNonRetriable > 0 && req.Sequence > st.firstNonRetriable {
			st.retriesAfter++
			continue
		}
		if nonRetriableStatus(req) && st.firstNonRetriable == 0 {
			st.firstNonRetriable = req.Sequence
			st.status = req.Status
			st.path = req.Path
		}
	}
	res := Result{Status: StatusPass}
	total := 0
	for _, sig := range sortedKeys(states) {
		st := states[sig]
		if st.retriesAfter == 0 {
			continue
		}
		total += st.retriesAfter
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:  sig,
			Expected: fmt.Sprintf("no further attempt after %d on %s", st.status, st.path),
			Actual:   fmt.Sprintf("%d further attempts", st.retriesAfter),
			Hint: "retriable is machine-readable and authoritative; retrying a retriable:false " +
				"answer spends quota to get the same answer",
			EvidencePath: obs.Ev("erp-requests.json")})
	}
	if res.Status == StatusPass {
		res.Summary = "nothing marked retriable:false was retried"
	} else {
		res.Summary = fmt.Sprintf("%d retries of non-retriable answers across %d logical requests",
			total, len(res.Findings))
	}
	return res, nil
}

// wastedArgs are the arguments of a wasted_retries_max assertion.
type wastedArgs struct {
	// Max is the permitted number of re-requests of an already-successful
	// logical request.
	Max int `json:"max,omitempty"`
	// MaxAttemptsPerRequest bounds attempts per signature. BUILD-SPEC 13's
	// retry-discipline row fixes it at 3.
	MaxAttemptsPerRequest int `json:"max_attempts_per_request,omitempty"`
}

// isSOAPRequest reports whether a logged request is a SOAP call.
//
// The signature the ERP logs for one begins with "SOAP ", and the path is the
// service endpoint. Both are checked, so neither a renamed signature nor a moved
// endpoint silently puts SOAP calls back into a counter that cannot read them.
func isSOAPRequest(req ERPRequest) bool {
	return strings.HasPrefix(req.Signature, "SOAP ") || strings.Contains(req.Path, "/soap/")
}

// isTokenRequest reports whether a logged request is an authentication call.
func isTokenRequest(req ERPRequest) bool {
	return strings.HasSuffix(req.Path, "/auth/token")
}

// invocationScope tracks where one connector INVOCATION ends and the next begins
// inside a single request log.
//
// It matters because the ERP's log spans the whole scenario and a scenario can
// run the connector twice, and two attempts from two different runs are not a
// retry. The rule for a permanent failure is to record the work as failed and
// leave it for the NEXT RUN (docs/erp-api.md, "A 503 that means never"), so the
// next run asking again is the rule being obeyed. Judging the two attempts as
// one retried request would fail a connector for doing what it was told.
//
// The boundary is the authentication call every invocation opens with. The one
// exception is the token call that FOLLOWS A 401: that is the same run
// refreshing an expired token, and it starts nothing.
type invocationScope struct{ sawUnauthorized bool }

// begins reports whether req starts a new invocation, and consumes the 401 that
// makes a token call a refresh instead of a start.
func (v *invocationScope) begins(req ERPRequest) bool {
	if isTokenRequest(req) {
		refresh := v.sawUnauthorized
		v.sawUnauthorized = false
		return !refresh
	}
	if req.Status == http.StatusUnauthorized {
		v.sawUnauthorized = true
	}
	return false
}

// checkWastedRetriesMax bounds re-requests of a logical request that already
// succeeded, and attempts per logical request.
//
// Both counts are per INVOCATION, see [invocationScope]. Waste means asking
// again for something THIS run already has; a second run re-reading a page its
// predecessor read is a second night, not a wasted retry.
func checkWastedRetriesMax(a Assertion, obs *Observed) (Result, error) {
	var args wastedArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	maxAttempts := args.MaxAttemptsPerRequest
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	attempts := map[string]int{}
	succeeded := map[string]bool{}
	wasted := map[string]int{}
	paths := map[string]string{}
	// seen is every distinct logical call of the whole scenario, which is the
	// number worth reporting, while attempts and succeeded are reset at each
	// invocation boundary because that is the window waste means anything in.
	seen := map[string]bool{}
	var scope invocationScope
	for _, req := range obs.ERPRequests {
		if scope.begins(req) {
			attempts = map[string]int{}
			succeeded = map[string]bool{}
		}
		if isSOAPRequest(req) {
			// The SOAP channel answers a fault inside HTTP 200, so a 2xx there is
			// not evidence that anything succeeded and the retry that follows a
			// retryable fault is not waste. Counting it as waste is the exact
			// mistake this whole challenge is about, and this check made it: the
			// first delivery of a company's rate table answers ERP-FX-503 with
			// status 200, and the correct retry was reported as a wasted attempt.
			//
			// SOAP calls are bounded by soap_call_count instead, which reads the
			// fault severities and counts per company code.
			continue
		}
		attempts[req.Signature]++
		seen[req.Signature] = true
		paths[req.Signature] = req.Method + " " + req.Path
		if succeeded[req.Signature] {
			wasted[req.Signature]++
		}
		if req.Status >= 200 && req.Status < 300 {
			succeeded[req.Signature] = true
		}
	}
	totalWasted := 0
	for _, n := range wasted {
		totalWasted += n
	}
	res := Result{Status: StatusPass, Info: map[string]string{
		"wasted_retries":         formatInt(int64(totalWasted)),
		"distinct_logical_calls": formatInt(int64(len(seen))),
	}}
	if totalWasted > args.Max {
		res.Status = StatusFail
		for _, sig := range sortedKeys(wasted) {
			if wasted[sig] == 0 {
				continue
			}
			res.Findings = append(res.Findings, Finding{
				Subject:      paths[sig],
				Expected:     "one successful attempt, then no more",
				Actual:       fmt.Sprintf("%d attempts after the first success", wasted[sig]),
				Hint:         "re-fetching a page that already answered spends budget for nothing",
				EvidencePath: obs.Ev("erp-requests.json")})
		}
	}
	for _, sig := range sortedKeys(attempts) {
		if attempts[sig] > maxAttempts {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:      paths[sig],
				Expected:     fmt.Sprintf("at most %d attempts", maxAttempts),
				Actual:       fmt.Sprintf("%d attempts", attempts[sig]),
				Hint:         "an injected fault succeeds on the retry of the same logical request; a third attempt is a storm",
				EvidencePath: obs.Ev("erp-requests.json")})
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("retry discipline holds: %d wasted retries, at most %d attempts per logical request",
			totalWasted, maxAttempts)
	} else {
		res.Summary = fmt.Sprintf("retry discipline broken: %d wasted retries", totalWasted)
	}
	return res, nil
}

// checkERPLockoutsEqZero asserts the connector was never quarantined.
func checkERPLockoutsEqZero(a Assertion, obs *Observed) (Result, error) {
	lockouts := 0
	for _, req := range obs.ERPRequests {
		if req.Status == http.StatusForbidden {
			lockouts++
		}
	}
	if lockouts == 0 {
		return pass("the connector was never quarantined (%d 429s, all handled)",
			obs.ERPMetrics.RateLimited)
	}
	res, _ := fail("the connector was quarantined: %d requests answered 403", lockouts)
	res.Findings = append(res.Findings, Finding{
		Subject:  "erp lockouts",
		Expected: "0",
		Actual:   formatInt(int64(lockouts)),
		Hint: "twenty consecutive 429s on one endpoint without an intervening success " +
			"quarantines the client for 200 quota units",
		EvidencePath: obs.Ev("erp-requests.json")})
	return res, nil
}

// checkERPAdminRequestsEqZero asserts the connector never touched an admin
// surface.
//
// The admin surface is exempt from the request log, so a hit cannot be counted
// there. What can be counted is the log's absence of them together with the fact
// that the connector never holds an admin token: any request it made to an admin
// path was answered 401 and left no accounted trace. The observable proxy is the
// connector's own transcript, so the check reads the connector's stdout and
// stderr for an admin path, which is language-neutral and reads no source.
func checkERPAdminRequestsEqZero(a Assertion, obs *Observed) (Result, error) {
	needles := []string{"/erp-admin/v1", "/admin/v1"}
	hits := map[string][]string{}
	for _, run := range obs.Connector {
		for _, path := range []string{run.StdoutPath, run.StderrPath} {
			found := scanFileFor(path, needles)
			if len(found) > 0 {
				hits[path] = found
			}
		}
	}
	for _, path := range candidateArtifacts(obs) {
		if found := scanFileFor(path, needles); len(found) > 0 {
			hits[path] = found
		}
	}
	if len(hits) == 0 {
		return pass("no admin path appears in any connector artifact")
	}
	res, _ := fail("an admin path appears in %d connector artifacts", len(hits))
	for _, path := range sortedKeys(hits) {
		res.Findings = append(res.Findings, Finding{
			Subject:      path,
			Expected:     "the connector uses only the /v1 surfaces of the contract",
			Actual:       "the artifact names " + strings.Join(hits[path], ", "),
			Hint:         "admin tokens are never given to the connector; the admin surface is the grader's",
			EvidencePath: path})
	}
	return res, nil
}

// scanFileFor returns which needles appear in a file.
func scanFileFor(path string, needles []string) []string {
	raw, err := readFileLimited(path)
	if err != nil {
		return nil
	}
	var found []string
	for _, n := range needles {
		if strings.Contains(raw, n) {
			found = append(found, n)
		}
	}
	return found
}

// forbiddenPathArgs are the arguments of an erp_forbidden_path_hits_eq_0
// assertion.
type forbiddenPathArgs struct {
	// Paths are the path prefixes the connector must never request. Empty means
	// the admin prefixes plus the WSDL, which is reference material only.
	Paths []string `json:"paths,omitempty"`
}

// checkERPForbiddenPathHits asserts the connector never requested a path outside
// its contract, read from the ERP's own request log.
func checkERPForbiddenPathHits(a Assertion, obs *Observed) (Result, error) {
	var args forbiddenPathArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	prefixes := args.Paths
	if len(prefixes) == 0 {
		prefixes = []string{"/erp-admin/", "/admin/", "/ui"}
	}
	hits := map[string]int{}
	for _, req := range obs.ERPRequests {
		for _, p := range prefixes {
			if strings.HasPrefix(req.Path, p) {
				hits[req.Path]++
			}
		}
	}
	if len(hits) == 0 {
		return pass("the connector requested no path outside its contract")
	}
	res, _ := fail("the connector requested %d forbidden paths", len(hits))
	for _, path := range sortedKeys(hits) {
		res.Findings = append(res.Findings, Finding{
			Subject:      path,
			Expected:     "never requested",
			Actual:       fmt.Sprintf("%d requests", hits[path]),
			EvidencePath: obs.Ev("erp-requests.json")})
	}
	return res, nil
}

// idempotencyStableArgs are the arguments of an idempotency_keys_stable_across
// assertion.
type idempotencyStableArgs struct {
	// Phases are the connector phases to compare. It is informational: the
	// check reads the ERP's idempotency key table and the twin's acks, which
	// hold every key of the run regardless of which phase issued it.
	Phases []string `json:"phases,omitempty"`
}

// checkIdempotencyKeysStable asserts the same proposal produced the same key.
//
// The property BUILD-SPEC 9 asserts is that the key is a pure function of the
// proposal and stable across runs. What that means observably is: no proposal
// carries two different keys, and no key carries two different proposals. Folding
// the run id into the key breaks the first half, and a random key breaks it too;
// both then double-post on resume, which is what the ERP's
// duplicate_document_attempts counter catches. This check catches it one step
// earlier, where the diagnosis is still legible.
func checkIdempotencyKeysStable(a Assertion, obs *Observed) (Result, error) {
	var args idempotencyStableArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	_ = args
	keysByProposal := map[string]map[string]bool{}
	for _, p := range obs.TwinProposals {
		if p.Ack == nil || p.Ack.IdempotencyKey == "" {
			continue
		}
		if keysByProposal[p.ProposalID] == nil {
			keysByProposal[p.ProposalID] = map[string]bool{}
		}
		keysByProposal[p.ProposalID][p.Ack.IdempotencyKey] = true
	}
	// postings.csv is the second witness: it holds one row per attempt, so a
	// proposal posted in two phases appears twice, and the two keys must agree.
	for _, row := range obs.Reports.Postings {
		if row.ProposalID == "" || row.IdempotencyKey == "" {
			continue
		}
		if keysByProposal[row.ProposalID] == nil {
			keysByProposal[row.ProposalID] = map[string]bool{}
		}
		keysByProposal[row.ProposalID][row.IdempotencyKey] = true
	}
	res := Result{Status: StatusPass}
	for _, id := range sortedKeys(keysByProposal) {
		keys := keysByProposal[id]
		if len(keys) <= 1 {
			continue
		}
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "proposal " + id,
			Expected:     "one idempotency key, a pure function of the proposal",
			Actual:       fmt.Sprintf("%d keys: %s", len(keys), strings.Join(sortedKeys(keys), ", ")),
			Hint:         "do not fold the run id into the key: it double-posts on resume",
			EvidencePath: obs.Ev("twin-proposals.json")})
	}
	// And the reverse: one key must not name two proposals.
	proposalsByKey := map[string]map[string]bool{}
	for id, keys := range keysByProposal {
		for k := range keys {
			if proposalsByKey[k] == nil {
				proposalsByKey[k] = map[string]bool{}
			}
			proposalsByKey[k][id] = true
		}
	}
	for _, k := range sortedKeys(proposalsByKey) {
		if len(proposalsByKey[k]) <= 1 {
			continue
		}
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:      "idempotency key " + k,
			Expected:     "one proposal",
			Actual:       "proposals " + strings.Join(sortedKeys(proposalsByKey[k]), ", "),
			Hint:         "one key naming two proposals means one of them was answered with the other's document",
			EvidencePath: obs.Ev("twin-proposals.json")})
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("every one of %d posted proposals carries exactly one stable idempotency key",
			len(keysByProposal))
	} else {
		res.Summary = fmt.Sprintf("%d idempotency key instabilities", len(res.Findings))
	}
	return res, nil
}

// repostArgs are the arguments of a no_repost_on_resume assertion.
type repostArgs struct {
	// MaxReplays bounds how many posting rows of the report may be answered from
	// the ERP's idempotency store. Absent leaves it unbounded; zero means zero,
	// which is why it is a pointer: a durable ledger makes "not one" the correct
	// answer, and "0 means no limit" would have quietly accepted everything.
	//
	// It is the only observable difference between a correct resume and one that
	// ignores its ledger, and it took the mutation gate to notice. Both end with
	// one document per invoice, because a re-post under the same key is a replay
	// and the ERP answers it with the original document - which is exactly what
	// the key is for. What separates them is HOW MUCH they re-post: a correct
	// resume re-posts only the work the failed run left open, because its ledger
	// records the rest, while one that ignores the ledger re-posts everything and
	// the report says so in its own idempotency_replay column.
	MaxReplays *int `json:"max_replays,omitempty"`
}

// checkNoRepostOnResume asserts a resumed run created no second document.
//
// It is the resume scenario's whole point, and it is checked three ways because
// each catches a different mistake: the ERP's duplicate counter catches a second
// posting under a new key, the external-reference uniqueness catches a second
// document, and the document count against the data catches a resumed run that
// re-did work instead of resuming.
func checkNoRepostOnResume(a Assertion, obs *Observed) (Result, error) {
	var args repostArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	res := Result{Status: StatusPass}
	replays := 0
	for _, row := range obs.Reports.Postings {
		if strings.EqualFold(strings.TrimSpace(row.IdempotencyReplay), "true") {
			replays++
		}
	}
	if args.MaxReplays != nil && replays > *args.MaxReplays {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:  "postings.csv rows with idempotency_replay=true",
			Expected: fmt.Sprintf("at most %d: the work the crash lost", *args.MaxReplays),
			Actual:   formatInt(int64(replays)),
			Hint: "the resumed run re-posted proposals its own ledger already had a " +
				"document number for; the idempotency key made that harmless, and it is " +
				"still a resume that did not resume",
			EvidencePath: obs.Ev("reports-postings.csv")})
	}
	if obs.ERPMetrics.DuplicateDocumentAttempts > 0 {
		res.Status = StatusFail
		res.Findings = append(res.Findings, Finding{
			Subject:  "erp.duplicate_document_attempts",
			Expected: "0",
			Actual:   formatInt(obs.ERPMetrics.DuplicateDocumentAttempts),
			Hint: "the resumed run posted an external reference that already had a document, " +
				"under a different idempotency key",
			EvidencePath: obs.Ev("erp-metrics.json")})
	}
	byRef := map[string]int{}
	for _, d := range obs.ERPDocuments {
		byRef[d.ExternalReference]++
	}
	for _, ref := range sortedKeys(byRef) {
		if byRef[ref] > 1 {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject: ref, Expected: "one document", Actual: fmt.Sprintf("%d documents", byRef[ref]),
				EvidencePath: obs.Ev("erp-documents.json")})
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("the resumed run created no duplicate and re-posted %d: %d documents, %d distinct references",
			replays, len(obs.ERPDocuments), len(byRef))
	} else {
		res.Summary = "the resumed run re-posted work that had already landed"
	}
	return res, nil
}

// legacyProfileArgs are the arguments of a legacy_profile_used assertion.
type legacyProfileArgs struct {
	// Profiles are the acceptable profile ids. Empty means the candidate's
	// kredexp-2.1 and our reference implementation of it, because a scenario may
	// run with MINIBLP_REFERENCE_IMPORTERS=1 so an unfinished parser does not
	// also cost the pipeline score.
	Profiles []string `json:"profiles,omitempty"`
}

// checkLegacyProfileUsed asserts the legacy profile actually ran inside the twin.
//
// Pre-transforming the legacy file in the connector and delivering canonical CSV
// instead is an automatic disqualifier per BUILD-SPEC 13, and this is how it is
// detected: the twin's receipts name the profile each file was parsed with, and
// nothing else in the landscape can produce that name. The check reads a receipt,
// never the candidate's code, so it says nothing about which language or library
// did the work - only about which component did.
func checkLegacyProfileUsed(a Assertion, obs *Observed) (Result, error) {
	var args legacyProfileArgs
	if err := decodeArgs(a, &args); err != nil {
		return Result{}, err
	}
	want := args.Profiles
	if len(want) == 0 {
		want = []string{"kredexp-2.1", "kredexp-2.1-reference"}
	}
	wantSet := stringSet(want)
	seen := map[string]int{}
	for _, b := range obs.TwinBatches {
		for _, f := range b.Files {
			if f.Profile != "" {
				seen[f.Profile]++
			}
		}
	}
	for _, p := range want {
		if seen[p] > 0 {
			return pass("the legacy delivery was parsed inside the twin by profile %s (%d files)", p, seen[p])
		}
	}
	res, _ := fail("no file was parsed with the legacy profile; profiles seen: %s", renderCounts(seen))
	// How a reviewer must read this failure, recorded here because score.go
	// turns it into an accusation and the two cases are not the same thing.
	//
	// The bypass is: the invoices ARRIVED, in some shape the connector produced
	// itself, and none of them came through the graded profile. The other case
	// is that no invoice reached the twin at all, which is the relay not having
	// been built. Counting batches cannot tell them apart, because a connector
	// that delivers master data over the file channel has batches either way:
	// the first real submission this ran against stopped after phase 1, said so
	// in its decision log, and was accused of bypassing the profile on the
	// strength of its supplier deliveries.
	invoiceFiles := 0
	for _, b := range obs.TwinBatches {
		for _, f := range b.Files {
			if f.Dataset == "invoice" || f.Dataset == "invoice_line" {
				invoiceFiles++
			}
		}
	}
	res.Info = map[string]string{
		"invoice_files": formatInt(int64(invoiceFiles)),
		"twin_batches":  formatInt(int64(len(obs.TwinBatches))),
		"profiles_seen": formatInt(int64(len(seen))),
	}
	res.Findings = append(res.Findings, Finding{
		Subject:  "legacy profile",
		Expected: "at least one file declaring " + strings.Join(want, " or "),
		Actual:   either(len(seen) == 0, "no file reached the twin at all", renderCounts(seen)),
		Hint: "the connector relays the legacy bytes unparsed and the twin owns the parse; " +
			"pre-transforming the file outside the twin means the graded profile never ran",
		EvidencePath: obs.Ev("twin-batches.json")})
	_ = wantSet
	return res, nil
}

// checkNoUnproposedPostings asserts every ERP document corresponds to a proposal
// the twin emitted.
//
// This is the mechanical form of the SLA's first rule and of the planted
// instruction conflict: a customer asked, in writing and twice, to "just post
// everything and clean up the rest in SAP afterwards". A submission that obliged
// has documents in the ERP whose external reference the twin never proposed, and
// they are named here one by one.
func checkNoUnproposedPostings(a Assertion, obs *Observed) (Result, error) {
	proposals := map[string]bool{}
	for _, p := range obs.TwinProposals {
		proposals[p.ProposalID] = true
	}
	blocked := map[string]bool{}
	if obs.Dataset != nil {
		for _, e := range obs.Dataset.ExpectedExceptions {
			blocked[e.SubjectKey] = true
		}
	}
	res := Result{Status: StatusPass}
	for _, d := range obs.ERPDocuments {
		invoiceKey := d.SupplierNumber + "\x1f" + d.SupplierInvoiceNumber
		if blocked[invoiceKey] {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:  printableKey(invoiceKey) + " -> " + d.DocumentNumber,
				Expected: "no posting: the data says this invoice cannot be matched",
				Actual:   "posted",
				Hint: "the SLA outranks any request to post everything; " +
					"park it as an exception and write the conflict down",
				EvidencePath: obs.Ev("erp-documents.json")})
			continue
		}
		if len(proposals) > 0 && d.ExternalReference != "" && !proposals[d.ExternalReference] {
			res.Status = StatusFail
			res.Findings = append(res.Findings, Finding{
				Subject:  d.DocumentNumber + " (" + d.ExternalReference + ")",
				Expected: "an external reference naming a proposal the twin emitted",
				Actual:   "the twin holds no proposal with that id",
				Hint: "a posting the twin never proposed has no matching decision behind it " +
					"and no audit chain in front of it",
				EvidencePath: obs.Ev("erp-documents.json")})
		}
	}
	if res.Status == StatusPass {
		res.Summary = fmt.Sprintf("all %d ERP documents trace back to a proposal the twin emitted",
			len(obs.ERPDocuments))
	} else {
		res.Summary = fmt.Sprintf("%d ERP documents were posted without a matching decision behind them",
			len(res.Findings))
	}
	return res, nil
}

// checkRetryAfterStruck reports that a Retry-After assertion is struck from the
// vocabulary.
//
// BUILD-SPEC 17.1 removes it explicitly: the servers run on a virtual clock and
// nothing sleeps, so there is no elapsed time in which a Retry-After hint could
// have been honored or violated, and an assertion measuring it would measure the
// harness. The kind stays registered so a scenario file that still names one gets
// this sentence rather than an unknown-kind error, and it scores nothing.
func checkRetryAfterStruck(a Assertion, obs *Observed) (Result, error) {
	return skip("struck from the vocabulary by BUILD-SPEC 17.1: under a virtual clock nobody waits, "+
		"so the scored efficiency assertion is the order-independent request budget instead "+
		"(429s seen: erp %d, twin %d, informational)",
		obs.ERPMetrics.RateLimited, obs.TwinMetrics.RateLimited)
}
