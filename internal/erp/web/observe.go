package web

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// An IdempotencyStat is one row of the idempotency key view: what a client did
// with one key and what the ERP answered.
//
// Method, Path and Key come from the composite key the ERP's idempotency store
// retains; FirstStatus, Replays and Conflicts come from the observer, because the
// store publishes the keys it holds and not the traffic that produced them. A
// row read from the store alone therefore has Observed false and leaves the three
// counted columns blank rather than guessing at them.
type IdempotencyStat struct {
	// Tenant, Method, Path and Key are the four parts of the store's composite
	// key. One key is a promise about one operation, so the method and the path
	// are part of its identity and the same client-chosen key on two endpoints
	// is two keys.
	Tenant string
	Method string
	Path   string
	Key    string

	// FirstStatus is the status of the first response under this key.
	FirstStatus int
	// Replays counts responses the ERP served from the store, i.e. the ones
	// carrying Idempotent-Replay: true. A replay is correct client behavior
	// after a timeout; a large count next to a small document count is a client
	// retrying something that already succeeded.
	Replays int
	// Conflicts counts 409 IDEMPOTENCY_KEY_REUSED answers, i.e. the same key
	// offered with a different body. Every one of them is a client bug: it means
	// the key was derived from something other than the content it is supposed
	// to protect.
	Conflicts int
	// Attempts counts every request seen under this key, replays and conflicts
	// included.
	Attempts int
	// Observed reports whether the counted columns are real. It is false for a
	// key the observer never saw, which happens when the UI is not wired in
	// front of the server.
	Observed bool
}

// An Observer records the idempotency traffic passing through it. It is the one
// piece of the UI that is not a pure reader, and it exists because the ERP's
// admin surface publishes the idempotency KEYS it retains
// (GET /erp-admin/v1/idempotency-keys) but not the first status, the replay count
// or the conflict count of each, and those three are exactly what tells a
// reviewer whether a client is retrying safely or hammering a key it derived
// from a fresh UUID every time.
//
// It reads only what the wire already says: the Idempotency-Key request header,
// the response status, and the Idempotent-Replay response header the ERP sets on
// a served replay. It never buffers a body and it never changes a response.
//
// An Observer is safe for concurrent use.
type Observer struct {
	mu    sync.Mutex
	stats map[string]*IdempotencyStat
	order []string
}

// newObserver returns an empty Observer.
func newObserver() *Observer {
	return &Observer{stats: map[string]*IdempotencyStat{}}
}

// Middleware returns next wrapped in the Observer. Requests without an
// Idempotency-Key pass through untouched and unrecorded.
func (o *Observer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get(httpx.HeaderIdempotencyKey))
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		rec := &observedWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		o.record(r.Method, r.URL.Path, key, rec.statusCode(), rec.replayed())
	})
}

// record folds one observed response into the statistics.
func (o *Observer) record(method, path, key string, status int, replay bool) {
	id := strings.Join([]string{method, path, key}, "\x1f")
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.stats[id]
	if !ok {
		st = &IdempotencyStat{Method: method, Path: path, Key: key, Observed: true}
		o.stats[id] = st
		// Insertion order is kept in a slice, never read out of the map, so two
		// runs of the same traffic render the same table in the same order.
		o.order = append(o.order, id)
	}
	st.Attempts++
	if st.FirstStatus == 0 {
		st.FirstStatus = status
	}
	switch {
	case replay:
		st.Replays++
	case status == http.StatusConflict:
		st.Conflicts++
	}
}

// stat returns the observed statistics for one key, if any.
func (o *Observer) stat(method, path, key string) (IdempotencyStat, bool) {
	id := strings.Join([]string{method, path, key}, "\x1f")
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.stats[id]
	if !ok {
		return IdempotencyStat{}, false
	}
	return *st, true
}

// snapshot returns the observed statistics in first-seen order.
func (o *Observer) snapshot() []IdempotencyStat {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]IdempotencyStat, 0, len(o.order))
	for _, id := range o.order {
		out = append(out, *o.stats[id])
	}
	return out
}

// An observedWriter records the status a handler wrote and whether the handler
// marked the response as an idempotent replay.
type observedWriter struct {
	http.ResponseWriter
	status int
	replay bool
}

// WriteHeader records the first status and reads the replay marker off the
// headers the handler set, which is the last moment they are guaranteed to be
// complete.
func (o *observedWriter) WriteHeader(status int) {
	if o.status == 0 {
		o.status = status
		o.replay = strings.EqualFold(
			strings.TrimSpace(o.Header().Get(httpx.HeaderIdempotentReplay)), "true")
	}
	o.ResponseWriter.WriteHeader(status)
}

// Write records an implicit 200 on the first body byte.
func (o *observedWriter) Write(p []byte) (int, error) {
	if o.status == 0 {
		o.WriteHeader(http.StatusOK)
	}
	return o.ResponseWriter.Write(p)
}

// Flush forwards a flush when the underlying writer supports one, so wrapping
// does not disable streaming.
func (o *observedWriter) Flush() {
	if f, ok := o.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// statusCode returns the recorded status, defaulting to 200 for a handler that
// wrote nothing at all.
func (o *observedWriter) statusCode() int {
	if o.status == 0 {
		return http.StatusOK
	}
	return o.status
}

// replayed reports whether the observed response was served from the
// idempotency store.
func (o *observedWriter) replayed() bool { return o.replay }

// parseStoreKey splits one composite key of the idempotency store into its four
// parts. The store joins them with a unit separator so that no part can forge a
// boundary; a value that is not in that shape is returned with everything in Key,
// because a UI that dropped a row it could not parse would be hiding evidence.
func parseStoreKey(composite string) IdempotencyStat {
	parts := strings.Split(composite, "\x1f")
	if len(parts) != 4 {
		return IdempotencyStat{Key: composite}
	}
	return IdempotencyStat{Tenant: parts[0], Method: parts[1], Path: parts[2], Key: parts[3]}
}

// mergeIdempotency joins the keys the ERP retains with what the observer saw.
//
// The store's insertion order is the eviction order and is the primary order of
// the table. Keys the observer saw but the store does not retain (a 400 for a
// missing body, a 409 that stored nothing) are appended afterwards, sorted, so
// they are visible without disturbing the store's order.
func mergeIdempotency(stored []string, observed []IdempotencyStat) []IdempotencyStat {
	out := make([]IdempotencyStat, 0, len(stored)+len(observed))
	seen := make(map[string]bool, len(stored))
	byID := make(map[string]IdempotencyStat, len(observed))
	for _, st := range observed {
		byID[strings.Join([]string{st.Method, st.Path, st.Key}, "\x1f")] = st
	}
	for _, composite := range stored {
		row := parseStoreKey(composite)
		id := strings.Join([]string{row.Method, row.Path, row.Key}, "\x1f")
		seen[id] = true
		if obs, ok := byID[id]; ok {
			row.FirstStatus, row.Replays, row.Conflicts = obs.FirstStatus, obs.Replays, obs.Conflicts
			row.Attempts, row.Observed = obs.Attempts, true
		}
		out = append(out, row)
	}
	extra := make([]IdempotencyStat, 0, len(observed))
	for _, st := range observed {
		if !seen[strings.Join([]string{st.Method, st.Path, st.Key}, "\x1f")] {
			extra = append(extra, st)
		}
	}
	sort.Slice(extra, func(i, j int) bool {
		if extra[i].Path != extra[j].Path {
			return extra[i].Path < extra[j].Path
		}
		return extra[i].Key < extra[j].Key
	})
	return append(out, extra...)
}
