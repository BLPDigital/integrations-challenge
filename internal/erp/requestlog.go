package erp

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// A RequestLogEntry is one line of the ERP's request log, served by
// GET /erp-admin/v1/requests. It is the evidence a debrief is argued from: what
// the client asked for, what it was charged, what it got back and which injected
// fault it met.
//
// Admin requests never appear here. They cost nothing, count nothing and are
// never injected into, so a reviewer reading metrics cannot perturb a transcript.
type RequestLogEntry struct {
	// Sequence is the Governor's request sequence, counted over every non-admin
	// request whether it was admitted or rejected.
	Sequence int64 `json:"sequence"`
	// Endpoint is the endpoint class the request was charged as, which is the
	// row of the published virtual-cost table that applied.
	Endpoint string `json:"endpoint"`
	// Signature is the content-addressed signature of the logical request: the
	// method, the route template and the canonicalized identifying parameters.
	// It is in the log so a reviewer can see why a fault fired where it did.
	Signature string `json:"signature"`
	// Method and Path are the request line, verbatim.
	Method string `json:"method"`
	Path   string `json:"path"`
	// Status is the status the client actually saw.
	Status int `json:"status"`
	// VirtualCostVms is the virtual time this request advanced the clock by,
	// i.e. the difference between VirtualClockMs and the previous entry's. For
	// a client that issues its requests one at a time, which is what the
	// published cost table is written for, it is exactly the request's own
	// cost; the quota 503 costs nothing because a spent quota cannot be spent
	// again.
	VirtualCostVms int64 `json:"virtual_cost_vms"`
	// VirtualClockMs is the virtual clock after the request.
	VirtualClockMs int64 `json:"virtual_clock_ms"`
	// QuotaUsed is the quota consumed after the request.
	QuotaUsed int64 `json:"quota_used"`
	// InjectedFault names the fault the Governor injected instead of calling
	// the handler, empty when the handler produced the response itself.
	InjectedFault string `json:"injected_fault"`
	// Retriable is the flag the error body carried, absent when the accounting
	// middleware did not produce the body. A status code is not the answer: a
	// 503 is retriable when the bucket is empty and permanent when the failure
	// is, and the flag is what says which.
	Retriable *bool `json:"retriable,omitempty"`
}

// requestLog is the accounting middleware's log sink: an io.Writer that parses
// the one-line JSON records httpx.Guard emits, keeps them for the admin surface
// and forwards the bytes verbatim to the service's real log destination.
//
// It is written this way because Guard owns the request sequence, the status the
// client saw and the injected fault, and its log line is the one place all three
// are true at once. The endpoint class comes from the classifier through
// classBySig: the class is a pure function of the logical request, so keying it
// by signature is exact and needs no per-request correlation.
type requestLog struct {
	mu         sync.Mutex
	out        io.Writer
	limit      int
	entries    []RequestLogEntry
	dropped    int64
	classBySig map[string]string
	prevVms    int64
}

// newRequestLog returns a log retaining limit entries and forwarding to out.
func newRequestLog(out io.Writer, limit int) *requestLog {
	return &requestLog{out: out, limit: limit, classBySig: map[string]string{}}
}

// noteClass records the endpoint class of a signature. The classifier calls it
// once per request, before the Governor is asked to admit it.
func (l *requestLog) noteClass(sig, class string) {
	if sig == "" {
		return
	}
	l.mu.Lock()
	l.classBySig[sig] = class
	l.mu.Unlock()
}

// guardLine is the subset of httpx.Guard's log line the request log reads. The
// field names are Guard's contract.
type guardLine struct {
	Seq       int64  `json:"seq"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Vms       int64  `json:"vms"`
	QuotaUsed int64  `json:"quota_used"`
	Sig       string `json:"sig"`
	Injected  string `json:"injected"`
	Retriable *bool  `json:"retriable"`
}

// Write forwards p to the service log and appends one entry per JSON line it
// contains. A line that is not a Guard log line is forwarded and ignored, so a
// caller may share the writer.
func (l *requestLog) Write(p []byte) (int, error) {
	if l.out != nil {
		if _, err := l.out.Write(p); err != nil {
			return 0, err
		}
	}
	for _, line := range bytes.Split(p, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var g guardLine
		if err := json.Unmarshal(line, &g); err != nil || g.Method == "" {
			continue
		}
		l.append(g)
	}
	return len(p), nil
}

// append records one parsed line.
func (l *requestLog) append(g guardLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := RequestLogEntry{
		Sequence:       g.Seq,
		Endpoint:       l.classBySig[g.Sig],
		Signature:      g.Sig,
		Method:         g.Method,
		Path:           g.Path,
		Status:         g.Status,
		VirtualCostVms: g.Vms - l.prevVms,
		VirtualClockMs: g.Vms,
		QuotaUsed:      g.QuotaUsed,
		InjectedFault:  g.Injected,
		Retriable:      g.Retriable,
	}
	if e.VirtualCostVms < 0 {
		e.VirtualCostVms = 0
	}
	l.prevVms = g.Vms
	if l.limit > 0 && len(l.entries) >= l.limit {
		l.dropped++
		return
	}
	l.entries = append(l.entries, e)
}

// snapshot returns a copy of the retained entries and the number dropped past
// the retention cap.
func (l *requestLog) snapshot() ([]RequestLogEntry, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]RequestLogEntry, len(l.entries))
	copy(out, l.entries)
	return out, l.dropped
}

// count returns the number of retained entries.
func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// reset clears the entries, the virtual-clock carry and the class index. It is
// called when the run is reset, because a request log that outlived its
// Governor would report costs against a clock that no longer exists.
func (l *requestLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
	l.dropped = 0
	l.prevVms = 0
	l.classBySig = map[string]string{}
}
