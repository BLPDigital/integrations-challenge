package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// Path prefixes that are free of charge: they cost no virtual time, spend no
// quota, count in no metric, meet no injected fault and produce no log line, so
// a reviewer refreshing a UI or a grader reading metrics can never perturb a
// transcript.
const (
	// AdminPathTwin is the twin's admin surface.
	AdminPathTwin = "/admin/v1"
	// AdminPathERP is the ERP's admin surface.
	AdminPathERP = "/erp-admin/v1"
)

// IsAdminPath reports whether path is on an admin surface.
func IsAdminPath(path string) bool {
	return strings.HasPrefix(path, AdminPathTwin) || strings.HasPrefix(path, AdminPathERP)
}

// ServiceForPath returns the svc label of a path: "erp" for the ERP's REST,
// admin and SOAP prefixes, "miniblp" for everything else. It is the default
// when GuardOptions.Service is empty, and it is unambiguous because the two
// servers' accounted surfaces are disjoint by prefix.
func ServiceForPath(path string) string {
	if strings.HasPrefix(path, "/erp/") || strings.HasPrefix(path, AdminPathERP) ||
		strings.HasPrefix(path, "/soap/") || path == "/erp" {
		return "erp"
	}
	return "miniblp"
}

// WithDecision returns a context carrying the Governor's admission decision.
func WithDecision(ctx context.Context, d simclock.Decision) context.Context {
	return context.WithValue(ctx, ctxDecision, d)
}

// DecisionFrom returns the admission decision Guard stamped onto ctx.
func DecisionFrom(ctx context.Context) (simclock.Decision, bool) {
	d, ok := ctx.Value(ctxDecision).(simclock.Decision)
	return d, ok
}

// RequestSeq returns the request sequence number Guard assigned, or 0 when the
// request did not pass a Guard. It is the request_seq of a provenance entry and
// the chunk selector of the applied-then-500 injection.
func RequestSeq(ctx context.Context) int64 {
	if d, ok := DecisionFrom(ctx); ok {
		return d.Seq
	}
	return 0
}

// GuardOptions configures the accounting middleware. Guard is the mandated
// two-argument form; GuardWith exists because the structured log line carries a
// svc field and a test needs somewhere other than stdout to write it.
type GuardOptions struct {
	// Governor is the server's Governor. Required.
	Governor *simclock.Governor
	// Classify returns the endpoint label, the fault-injection signature and
	// the record count of a request.
	//
	// The label must be one of the simclock.EndpointClass values: it is both
	// the metrics and quarantine label and the row of the virtual-cost table
	// the request is charged with. An unknown label costs nothing, which shows
	// up as a suspiciously free endpoint rather than as a panic.
	//
	// The signature identifies the LOGICAL request for content-addressed fault
	// injection: METHOD, route template and the canonicalized identifying
	// parameters (a decoded cursor position, not the opaque cursor string; the
	// item keys of a posting; the company code of a SOAP call). It must not
	// contain the request sequence, a timestamp, a connection or goroutine
	// identity, or anything else that varies with the client's concurrency, or
	// two runs of one correct client meet different faults. An empty signature
	// falls back to METHOD + " " + path, which is order independent but does
	// not distinguish two pages of the same list.
	//
	// A nil Classify charges nothing and labels by method and path.
	Classify func(*http.Request) (endpoint string, signature string, records int)
	// Service is the svc field of the log line. Empty means ServiceForPath.
	Service string
	// Log is where the one-line JSON log goes. Nil means os.Stdout.
	Log io.Writer
}

// Guard returns the middleware that wires the Governor into a handler chain. It
// is the only place a request is accounted for.
//
// Per non-admin request, in this order:
//
//  1. classify the request into an endpoint label and a record count,
//  2. compute the virtual cost from the published cost table,
//  3. call Governor.Admit exactly once, which assigns the request sequence,
//     advances the virtual clock and spends the quota,
//  4. on rejection write the canonical error body and stop,
//  5. otherwise ask the Governor for a seed-keyed injected fault and, if one
//     applies, write it instead of calling the handler,
//  6. otherwise call the handler,
//  7. write one JSON log line to stdout, with no timestamp in it.
//
// Admin paths bypass steps 1 to 6 and 7 entirely: no cost, no quota, no
// injection, no metrics, no log line.
//
// An injected 429 additionally charges the rate-limit penalty on top of the
// request's own cost, matching the "any 429 response" row of the cost table. An
// injected 503 charges nothing extra.
//
// Middleware order is part of the contract, because the build spec counts every
// non-admin request including 429s and auth refreshes: Guard must be the
// outermost accounted middleware, with [Bearer] inside it. Wrapping the other way
// round makes a 401 free, so a client that never refreshes its token pays no
// quota for the requests it wastes.
//
//	mux = httpx.Guard(gov, classify)(httpx.Bearer(gov)(routes))
func Guard(gov *simclock.Governor, classify func(*http.Request) (endpoint string, signature string, records int)) func(http.Handler) http.Handler {
	return GuardWith(GuardOptions{Governor: gov, Classify: classify})
}

// GuardWith is Guard with an explicit service label and log destination.
func GuardWith(opts GuardOptions) func(http.Handler) http.Handler {
	logw := opts.Log
	if logw == nil {
		logw = os.Stdout
	}
	var mu sync.Mutex // serializes log lines, so no two interleave
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsAdminPath(r.URL.Path) || opts.Governor == nil {
				next.ServeHTTP(w, r)
				return
			}

			endpoint, sig, records := "", "", 0
			if opts.Classify != nil {
				endpoint, sig, records = opts.Classify(r)
			}
			if endpoint == "" {
				endpoint = r.Method + " " + r.URL.Path
			}
			if sig == "" {
				sig = r.Method + " " + r.URL.Path
			}
			cost := simclock.Cost(simclock.EndpointClass(endpoint), records)

			gov := opts.Governor
			dec := gov.Admit(endpoint, cost, records)
			line := logLine{
				Svc:    opts.Service,
				Sig:    sig,
				Seq:    dec.Seq,
				Event:  "request",
				Method: r.Method,
				Path:   r.URL.Path,
				Vms:    dec.Vms,
			}
			if line.Svc == "" {
				line.Svc = ServiceForPath(r.URL.Path)
			}

			switch {
			case !dec.Allow:
				line.Status = dec.Status
				line.Retriable = &dec.Retriable
				WriteError(w, dec.Status, &Error{
					Code:             dec.Code,
					Message:          admissionMessage(dec.Code),
					Retriable:        dec.Retriable,
					RetryAfterHintMs: dec.RetryAfterHintMs,
					Status:           dec.Status,
				})
			default:
				// The permanent transport error first, because it is a property
				// of the scenario rather than of the noise and it must not be
				// masked by a retriable injection landing on the same request.
				if pstatus, pcode, ok := gov.InjectPermanent(r.URL.Path); ok {
					never := false
					line.Status = pstatus
					line.Injected = pcode
					line.Retriable = &never
					WriteError(w, pstatus, &Error{
						Code:      pcode,
						Message:   injectedMessage(pcode),
						Retriable: false,
						Status:    pstatus,
					})
					// break, not return: the request still has to reach the log
					// line at the bottom like every other one.
					break
				}
				status, code, retriable, ok := gov.InjectFault(sig)
				if ok {
					if status == http.StatusTooManyRequests {
						line.Vms = gov.Advance(gov.RateLimitCostVms100())
					}
					line.Status = status
					line.Injected = code
					line.Retriable = &retriable
					e := &Error{
						Code:      code,
						Message:   injectedMessage(code),
						Retriable: retriable,
						Status:    status,
					}
					if status == http.StatusTooManyRequests {
						e.RetryAfterHintMs = dec.RetryAfterHintMs
						if e.RetryAfterHintMs == 0 {
							e.RetryAfterHintMs = gov.RateLimitCostVms100() / simclock.Vms100PerVms
						}
					}
					WriteError(w, status, e)
					break
				}
				rec := &statusRecorder{ResponseWriter: w}
				next.ServeHTTP(rec, r.WithContext(WithDecision(r.Context(), dec)))
				line.Status = rec.statusCode()
			}

			line.QuotaUsed = gov.Metrics().QuotaUsed
			mu.Lock()
			writeLogLine(logw, line)
			mu.Unlock()
		})
	}
}

// logLine is the structured one-line log record. The field order is the field
// order of the build spec's example and encoding/json preserves it, so two runs
// of the same scenario produce byte-identical stdout. There is deliberately no
// timestamp: a clock would make the transcript undiffable.
type logLine struct {
	Svc       string `json:"svc"`
	Seq       int64  `json:"seq"`
	Event     string `json:"event"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Vms       int64  `json:"vms"`
	QuotaUsed int64  `json:"quota_used"`
	// Sig is the fault-injection signature of the logical request. It is in the
	// log so a reviewer can see why a fault fired where it did.
	Sig string `json:"sig,omitempty"`
	// Injected names the injected fault this response carries, and is absent on
	// the responses the handler itself produced.
	Injected string `json:"injected,omitempty"`
	// Retriable is the flag the error body carried, absent when this middleware
	// did not produce the body.
	//
	// It is in the log because a status code does not carry the answer: a 503 is
	// retriable when the bucket is empty and not retriable when the failure is
	// permanent, and every error body in this landscape says which precisely so
	// nobody has to guess. A grader that guessed from the status was the first
	// thing this field caught.
	Retriable *bool `json:"retriable,omitempty"`
}

// writeLogLine marshals one log line and writes it with its newline in a single
// Write, so concurrent handlers cannot interleave halves of a line.
func writeLogLine(w io.Writer, line logLine) {
	buf, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = w.Write(append(buf, '\n'))
}

// admissionMessage returns the human-readable message of an admission
// rejection. The Governor owns the codes; the prose lives here so both servers
// say the same thing.
func admissionMessage(code string) string {
	switch code {
	case simclock.CodeRateLimited:
		return "token bucket empty"
	case simclock.CodeQuarantined:
		return "client quarantined after consecutive rate limits"
	case simclock.CodeQuotaExhausted:
		return "request quota exhausted for this run"
	}
	return "request rejected"
}

// injectedMessage returns the message of an injected fault.
func injectedMessage(code string) string {
	switch code {
	case simclock.CodeRateLimited:
		return "rate limited"
	case simclock.CodeUnavailable:
		return "service temporarily unavailable"
	case simclock.CodePermanentTransport:
		return "this request cannot succeed; retriable is false and it means it"
	}
	return "request failed"
}

// statusRecorder captures the status a handler wrote, so the log line reports
// what the client actually saw rather than what the handler intended.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the first status written and passes it through.
func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write records an implicit 200 on the first body byte.
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush forwards a flush when the underlying writer supports one, so wrapping
// does not disable streaming.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// statusCode returns the recorded status, defaulting to 200 for a handler that
// wrote nothing at all.
func (s *statusRecorder) statusCode() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}
