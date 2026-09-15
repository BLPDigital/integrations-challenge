package httpx

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// DefaultIdempotencyRetention is the documented floor of the idempotency
// store's retention: 4096 keys, or the whole run, whichever is larger. A store
// built with a non-positive cap retains the whole run and never evicts, which is
// the larger of the two for every scenario in this project; a store built with a
// smaller positive cap is raised to this floor.
const DefaultIdempotencyRetention = 4096

// An IdempotencyKey identifies a stored response. Tenant, Method and Path are
// part of the key so the same client-chosen key on two endpoints, or from two
// tenants, can never collide: an idempotency key is a promise about one
// operation, not a global name.
type IdempotencyKey struct {
	Tenant string
	Method string
	Path   string
	Key    string
}

// String renders the key for logs and error details. The parts are joined with
// a unit separator so no part can forge a boundary.
func (k IdempotencyKey) String() string {
	return strings.Join([]string{k.Tenant, k.Method, k.Path, k.Key}, "\x1f")
}

// An IdempotencyRecord is the stored response of a completed request, plus the
// hash of the request body that produced it.
type IdempotencyRecord struct {
	// Status is the HTTP status of the original response.
	Status int
	// Header is a copy of the response headers worth replaying. Hop-by-hop
	// and length headers are dropped by Put.
	Header http.Header
	// Body is the original response body, replayed verbatim.
	Body []byte
	// BodyHash is the hash of the original request body, from BodyHash.
	BodyHash string
}

// clone deep-copies a record so a caller can neither see nor cause a mutation
// of stored state.
func (r IdempotencyRecord) clone() IdempotencyRecord {
	out := r
	out.Header = make(http.Header, len(r.Header))
	for k, v := range r.Header {
		vv := make([]string, len(v))
		copy(vv, v)
		out.Header[k] = vv
	}
	out.Body = append([]byte(nil), r.Body...)
	return out
}

// An IdempotencyOutcome is the verdict of IdempotencyStore.Check.
type IdempotencyOutcome int

// The idempotency verdicts.
const (
	// IdempotencyNew means the key is unknown: do the work, then Put.
	IdempotencyNew IdempotencyOutcome = iota
	// IdempotencyReplay means the key is known with the same request body:
	// replay the stored response verbatim with WriteReplay and do no work.
	IdempotencyReplay
	// IdempotencyConflict means the key is known with a different request
	// body: answer 409 IDEMPOTENCY_KEY_REUSED and do no work.
	IdempotencyConflict
)

// String returns the lowercase name of the outcome, for logs.
func (o IdempotencyOutcome) String() string {
	switch o {
	case IdempotencyNew:
		return "new"
	case IdempotencyReplay:
		return "replay"
	case IdempotencyConflict:
		return "conflict"
	}
	return "unknown"
}

// An IdempotencyStore maps (tenant, method, path, key) to the response the
// server sent the first time. It is in memory and per run: idempotency is a
// property of one run of one scenario, and a stored response from a previous
// process would make a resume scenario undecidable.
//
// Eviction is by insertion order, never by map order, so two runs that overflow
// the retention cap evict the same keys in the same sequence. Re-Putting an
// existing key keeps its original position, so a replayed request cannot push
// an older key's eviction further out.
//
// All methods are safe for concurrent use.
type IdempotencyStore struct {
	mu        sync.Mutex
	retention int
	recs      map[string]IdempotencyRecord
	order     []string
}

// NewIdempotencyStore returns a store retaining retention keys. A non-positive
// retention retains the whole run and never evicts; a positive retention below
// DefaultIdempotencyRetention is raised to it.
func NewIdempotencyStore(retention int) *IdempotencyStore {
	if retention > 0 && retention < DefaultIdempotencyRetention {
		retention = DefaultIdempotencyRetention
	}
	return &IdempotencyStore{
		retention: retention,
		recs:      make(map[string]IdempotencyRecord),
	}
}

// Get returns the stored record for k. The record is a deep copy.
func (s *IdempotencyStore) Get(k IdempotencyKey) (IdempotencyRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[k.String()]
	if !ok {
		return IdempotencyRecord{}, false
	}
	return rec.clone(), true
}

// Put stores the response for k. It is called after the work has been done and
// the response has been rendered, with the same request body hash that Check
// saw. Hop-by-hop, length and replay headers are not stored: they belong to the
// individual response, not to the operation.
func (s *IdempotencyStore) Put(k IdempotencyKey, rec IdempotencyRecord) {
	stored := rec.clone()
	for _, drop := range []string{"Content-Length", "Connection", "Transfer-Encoding", HeaderIdempotentReplay, "Date"} {
		stored.Header.Del(drop)
	}
	id := k.String()

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.recs[id]; !exists {
		s.order = append(s.order, id)
	}
	s.recs[id] = stored
	s.evict()
}

// evict drops the oldest keys until the store is within its retention cap. The
// caller holds the mutex.
func (s *IdempotencyStore) evict() {
	if s.retention <= 0 {
		return
	}
	for len(s.order) > s.retention {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.recs, oldest)
	}
}

// Len returns the number of retained keys.
func (s *IdempotencyStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

// Keys returns the retained keys in insertion order. It exists for the admin
// surface (GET /erp-admin/v1/idempotency-keys) and for tests; the order is the
// eviction order and never a map order.
func (s *IdempotencyStore) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// Check resolves k against the store for a request whose body hashes to
// bodyHash. On IdempotencyReplay the returned record is the response to replay;
// on the other two outcomes it is the zero record.
func (s *IdempotencyStore) Check(k IdempotencyKey, bodyHash string) (IdempotencyRecord, IdempotencyOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[k.String()]
	if !ok {
		return IdempotencyRecord{}, IdempotencyNew
	}
	if rec.BodyHash != bodyHash {
		return IdempotencyRecord{}, IdempotencyConflict
	}
	return rec.clone(), IdempotencyReplay
}

// WriteReplay writes a stored response verbatim and marks it as a replay with
// Idempotent-Replay: true. The stored status, headers and body are reproduced
// exactly, because a client comparing two responses to the same key must see
// the same document number, the same per-item results and the same ordering.
func WriteReplay(w http.ResponseWriter, rec IdempotencyRecord) {
	h := w.Header()
	for name, values := range rec.Header {
		for _, v := range values {
			h.Add(name, v)
		}
	}
	h.Set(HeaderIdempotentReplay, "true")
	status := rec.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(rec.Body)
}

// BodyHash returns the lowercase hex sha256 of b, the request-body hash the
// store compares. An absent body and an empty body hash the same, which is
// correct: they are the same request.
func BodyHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RequiresIdempotencyKey reports whether r must carry an Idempotency-Key: a
// mutating method on a versioned path. Admin paths are exempt because they are
// operator tooling, and the token endpoints are exempt because a token request
// has no state to make idempotent and its response is the token itself.
func RequiresIdempotencyKey(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	path := r.URL.Path
	if IsAdminPath(path) || !isVersionedPath(path) {
		return false
	}
	return !strings.HasSuffix(path, "/auth/token")
}

// isVersionedPath reports whether path has a /v1 segment: /v1/... on the twin,
// /erp/v1/... on the ERP.
func isVersionedPath(path string) bool {
	return strings.Contains(path, "/v1/") || strings.HasSuffix(path, "/v1")
}

// RequireIdempotencyKey returns the request's Idempotency-Key, or
// 400 IDEMPOTENCY_KEY_REQUIRED when a mutating versioned request has none. A
// whitespace-only header counts as none: a key that is not a key is worse than
// no key, because it looks like idempotency without being it.
func RequireIdempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get(HeaderIdempotencyKey))
	if key == "" && RequiresIdempotencyKey(r) {
		return "", IdempotencyKeyRequired()
	}
	return key, nil
}
