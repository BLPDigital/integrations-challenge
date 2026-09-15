package simclock

// Metrics is the per-run request accounting of one server, served verbatim from
// GET /erp-admin/v1/metrics and GET /admin/v1/metrics. The JSON field names are
// the contract; the grader reads these numbers and cross-checks the candidate's
// self-reported run.json against them.
//
// A Metrics value returned by Governor.Metrics is a deep copy and is safe to
// marshal or mutate. ByEndpoint is never nil, so it marshals as {} rather than
// null, and encoding/json sorts its keys, so the JSON bytes are independent of
// map iteration order.
type Metrics struct {
	// RequestsTotal counts every non-admin request that reached
	// Governor.Admit, including the ones it rejected with 429, 403 or 503.
	RequestsTotal int64 `json:"requests_total"`
	// ByEndpoint counts the same requests split by the endpoint label passed
	// to Governor.Admit.
	ByEndpoint map[string]int64 `json:"by_endpoint"`
	// RateLimited counts 429 responses, both token-bucket and injected.
	RateLimited int64 `json:"429s"`
	// FiveXXInjected counts injected 5xx responses: the seed-keyed 503s and
	// the single applied-then-500 chunk.
	FiveXXInjected int64 `json:"5xx_injected"`
	// FiveXXRetried counts injected 5xx responses a client retried, reported
	// by the handler through Governor.Inc5xxRetried.
	FiveXXRetried int64 `json:"5xx_retried"`
	// PermanentInjected counts the injected permanent transport errors: the
	// 503s that say retriable:false. It is at most one per run and it is
	// reported separately from FiveXXInjected, because the two are opposite
	// instructions to a client and lumping them together would hide which one
	// a run actually met.
	PermanentInjected int64 `json:"permanent_injected"`
	// QuotaUsed counts quota units consumed; it never exceeds QuotaLimit.
	QuotaUsed int64 `json:"quota_used"`
	// QuotaLimit is the configured hard quota for the run.
	QuotaLimit int64 `json:"quota_limit"`
	// RecordsReturned sums the record counts of admitted requests.
	RecordsReturned int64 `json:"records_returned"`
	// VirtualClockMs is the virtual clock in whole virtual milliseconds.
	VirtualClockMs int64 `json:"virtual_clock_ms"`
	// DuplicateDocumentAttempts counts repeated ERP document postings under a
	// known idempotency key, reported through
	// Governor.IncDuplicateDocumentAttempts.
	DuplicateDocumentAttempts int64 `json:"duplicate_document_attempts"`
	// DuplicateApplyAttempts counts repeated twin chunk applications under a
	// known idempotency key, reported through
	// Governor.IncDuplicateApplyAttempts.
	DuplicateApplyAttempts int64 `json:"duplicate_apply_attempts"`
}

// clone returns a deep copy of m.
func (m Metrics) clone() Metrics {
	out := m
	out.ByEndpoint = make(map[string]int64, len(m.ByEndpoint))
	for k, v := range m.ByEndpoint {
		out.ByEndpoint[k] = v
	}
	return out
}
