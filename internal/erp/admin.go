package erp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// Admin surface paths. The whole surface is admin-token gated, free of charge,
// counted in no metric and never injected into: a reviewer refreshing a page or a
// grader reading counters must not be able to perturb the transcript it is
// reading.
const (
	// AdminPathMetrics serves the request accounting of the run.
	AdminPathMetrics = httpx.AdminPathERP + "/metrics"
	// AdminPathDocuments serves the AP documents the run has booked.
	AdminPathDocuments = httpx.AdminPathERP + "/documents"
	// AdminPathIdempotencyKeys serves the retained idempotency keys.
	AdminPathIdempotencyKeys = httpx.AdminPathERP + "/idempotency-keys"
	// AdminPathRequests serves the request log.
	AdminPathRequests = httpx.AdminPathERP + "/requests"
	// AdminPathSOAPCalls serves the SOAP call log.
	AdminPathSOAPCalls = httpx.AdminPathERP + "/soap-calls"
	// AdminPathReset restarts the run, keeping the dataset.
	AdminPathReset = httpx.AdminPathERP + "/reset"
	// AdminPathSeed loads a scenario at a seed and restarts the run.
	AdminPathSeed = httpx.AdminPathERP + "/seed"
	// AdminPathAdvance moves the master data on to a later scenario without
	// discarding the documents already posted. It is what makes a two-phase
	// delta scenario possible: see [Server.Advance].
	AdminPathAdvance = httpx.AdminPathERP + "/advance"
	// AdminPathStateDigest serves the content digest of everything the ERP holds.
	AdminPathStateDigest = httpx.AdminPathERP + "/state/digest"
	// AdminPathCredentials serves the seed-derived credentials.
	AdminPathCredentials = httpx.AdminPathERP + "/credentials"
)

// adminRoutes registers the admin surface on the free mux.
func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+AdminPathMetrics, s.admin(s.handleAdminMetrics))
	mux.Handle("GET "+AdminPathDocuments, s.admin(s.handleAdminDocuments))
	mux.Handle("GET "+AdminPathIdempotencyKeys, s.admin(s.handleAdminIdempotencyKeys))
	mux.Handle("GET "+AdminPathRequests, s.admin(s.handleAdminRequests))
	mux.Handle("GET "+AdminPathSOAPCalls, s.admin(s.handleAdminSOAPCalls))
	mux.Handle("POST "+AdminPathReset, s.admin(s.handleAdminReset))
	mux.Handle("POST "+AdminPathSeed, s.admin(s.handleAdminSeed))
	mux.Handle("POST "+AdminPathAdvance, s.admin(s.handleAdminAdvance))
	mux.Handle("GET "+AdminPathStateDigest, s.admin(s.handleAdminStateDigest))
	mux.Handle("GET "+AdminPathCredentials, s.admin(s.handleAdminCredentials))
	mux.Handle(httpx.AdminPathERP+"/", s.admin(s.handleAdminNotFound))
}

// admin wraps an admin handler in the token check. An absent and a wrong token
// get the same answer, so the surface leaks nothing to a connector token.
func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpx.RequireAdminToken(w, r, s.Credentials().AdminToken) {
			return
		}
		h(w, r)
	})
}

// handleAdminNotFound answers an unknown admin path.
func (s *Server) handleAdminNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, &httpx.Error{
		Code:      "NOT_FOUND",
		Message:   "no such admin endpoint",
		Retriable: false,
		Status:    http.StatusNotFound,
	})
}

// AdminMetrics is the body of GET /erp-admin/v1/metrics: the Governor's request
// accounting verbatim, plus what only the ERP knows.
//
// The embedded simclock.Metrics is inlined, so requests_total, by_endpoint,
// 429s, 5xx_injected, 5xx_retried, quota_used, quota_limit, records_returned,
// virtual_clock_ms and duplicate_document_attempts are top-level members with
// exactly the names the build specification fixes. The grader reads these numbers
// and cross-checks the connector's self-reported run.json against them.
type AdminMetrics struct {
	simclock.Metrics

	Svc      string `json:"svc"`
	Scenario string `json:"scenario"`
	Seed     int64  `json:"seed"`
	Chaos    bool   `json:"chaos"`

	// DocumentsCreated counts AP documents that exist, which is never more than
	// the number of distinct external references posted.
	DocumentsCreated int `json:"documents_created"`
	// BusinessRejections counts per-item rejections by code. encoding/json sorts
	// the keys, so the bytes do not depend on map iteration order.
	BusinessRejections map[string]int64 `json:"business_rejections"`
	// IdempotencyKeys is the number of retained keys.
	IdempotencyKeys int `json:"idempotency_keys"`
	// SOAPCalls counts SOAP requests that reached the handler, and SOAPFaults
	// counts them by fault code.
	SOAPCalls  int              `json:"soap_calls"`
	SOAPFaults map[string]int64 `json:"soap_faults"`
	// RequestLogEntries and RequestLogDropped describe the request log.
	RequestLogEntries int   `json:"request_log_entries"`
	RequestLogDropped int64 `json:"request_log_dropped"`
	// ExportFiles are the legacy delivery file names in write order.
	ExportFiles []string `json:"export_files"`
}

// handleAdminMetrics serves the request accounting.
func (s *Server) handleAdminMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st, cfg := s.st, s.cfg
	rejections := make(map[string]int64, len(st.rejections))
	for code, n := range st.rejections {
		rejections[code] = n
	}
	faults := map[string]int64{}
	for _, call := range st.soapCalls {
		if call.FaultCode != "" {
			faults[call.FaultCode]++
		}
	}
	m := AdminMetrics{
		Metrics:            st.gov.Metrics(),
		Svc:                "erp",
		Scenario:           cfg.Scenario,
		Seed:               cfg.Seed,
		Chaos:              cfg.Chaos,
		DocumentsCreated:   len(st.documents),
		BusinessRejections: rejections,
		IdempotencyKeys:    st.idem.Len(),
		SOAPCalls:          len(st.soapCalls),
		SOAPFaults:         faults,
		ExportFiles:        append([]string(nil), st.exportFiles...),
	}
	s.mu.Unlock()
	m.RequestLogEntries = s.log.count()
	_, m.RequestLogDropped = s.log.snapshot()
	if m.ExportFiles == nil {
		m.ExportFiles = []string{}
	}
	writeJSON(w, http.StatusOK, m)
}

// AdminDocuments is the body of GET /erp-admin/v1/documents.
type AdminDocuments struct {
	Documents []Document `json:"documents"`
	Count     int        `json:"count"`
}

// handleAdminDocuments serves the created documents in creation order, which is
// document number order because the numbers are handed out in sequence.
func (s *Server) handleAdminDocuments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	docs := append([]Document(nil), s.st.documents...)
	s.mu.Unlock()
	if docs == nil {
		docs = []Document{}
	}
	writeJSON(w, http.StatusOK, AdminDocuments{Documents: docs, Count: len(docs)})
}

// AdminIdempotencyKeys is the body of GET /erp-admin/v1/idempotency-keys. The
// keys are in insertion order, which is also the eviction order, and never a map
// order.
type AdminIdempotencyKeys struct {
	Keys  []string `json:"keys"`
	Count int      `json:"count"`
}

// handleAdminIdempotencyKeys serves the retained idempotency keys.
func (s *Server) handleAdminIdempotencyKeys(w http.ResponseWriter, r *http.Request) {
	_, st := s.state()
	keys := st.idem.Keys()
	if keys == nil {
		keys = []string{}
	}
	writeJSON(w, http.StatusOK, AdminIdempotencyKeys{Keys: keys, Count: len(keys)})
}

// AdminRequests is the body of GET /erp-admin/v1/requests.
type AdminRequests struct {
	Requests []RequestLogEntry `json:"requests"`
	Count    int               `json:"count"`
	// Dropped counts entries past the retention cap. It is zero in every
	// published scenario, because the cap is far above every published quota.
	Dropped int64 `json:"dropped"`
}

// handleAdminRequests serves the request log in arrival order.
func (s *Server) handleAdminRequests(w http.ResponseWriter, r *http.Request) {
	entries, dropped := s.log.snapshot()
	if entries == nil {
		entries = []RequestLogEntry{}
	}
	writeJSON(w, http.StatusOK, AdminRequests{Requests: entries, Count: len(entries), Dropped: dropped})
}

// AdminSOAPCalls is the body of GET /erp-admin/v1/soap-calls.
type AdminSOAPCalls struct {
	Calls []SOAPCall `json:"calls"`
	Count int        `json:"count"`
}

// handleAdminSOAPCalls serves the SOAP call log in arrival order.
func (s *Server) handleAdminSOAPCalls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	calls := append([]SOAPCall(nil), s.st.soapCalls...)
	s.mu.Unlock()
	if calls == nil {
		calls = []SOAPCall{}
	}
	writeJSON(w, http.StatusOK, AdminSOAPCalls{Calls: calls, Count: len(calls)})
}

// AdminState is the body of a reset or a seed: what the server holds now.
type AdminState struct {
	Svc      string `json:"svc"`
	Scenario string `json:"scenario"`
	Seed     int64  `json:"seed"`
	Chaos    bool   `json:"chaos"`
	Quota    int    `json:"quota"`
	// Counts are the record counts of the loaded dataset.
	Counts map[string]int `json:"counts"`
	// MaxChangeSeq is the highest change sequence in the dataset: the watermark
	// a connector should hold after a complete pull.
	MaxChangeSeq int64 `json:"max_change_seq"`
	// ExportFiles are the legacy delivery file names in write order, each data
	// file immediately followed by its sentinel.
	ExportFiles []string `json:"export_files"`
}

// adminState builds the current state summary.
func (s *Server) adminState() AdminState {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := append([]string(nil), s.st.exportFiles...)
	if files == nil {
		files = []string{}
	}
	return AdminState{
		Svc:          "erp",
		Scenario:     s.cfg.Scenario,
		Seed:         s.cfg.Seed,
		Chaos:        s.cfg.Chaos,
		Quota:        s.cfg.Quota,
		Counts:       s.data.set.Counts(),
		MaxChangeSeq: s.data.set.MaxChangeSeq,
		ExportFiles:  files,
	}
}

// handleAdminReset restarts the run and rewrites the export drop.
func (s *Server) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	if err := s.Reset(); err != nil {
		httpx.Fail(w, httpx.Internal("reset failed: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s.adminState())
}

// adminSeedRequest is the body of POST /erp-admin/v1/seed.
type adminSeedRequest struct {
	Scenario string `json:"scenario"`
	Seed     int64  `json:"seed"`
}

// handleAdminSeed loads a scenario at a seed and restarts the run.
//
// The credentials follow the seed, because they are derived from it; the admin
// token does not, so the caller that issued the reseed can still read what it
// created.
func (s *Server) handleAdminSeed(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var req adminSeedRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httpx.Fail(w, httpx.MalformedBody("json_invalid",
				"the seed request must be a JSON object with scenario and seed"))
			return
		}
	}
	if err := s.Reseed(strings.TrimSpace(req.Scenario), req.Seed); err != nil {
		httpx.Fail(w, httpx.MalformedBody("scenario_unknown", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s.adminState())
}

// handleAdminAdvance moves the master data on to a later scenario, keeping every
// document, idempotency key and counter of the run so far.
func (s *Server) handleAdminAdvance(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var req adminSeedRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httpx.Fail(w, httpx.MalformedBody("json_invalid",
				"the advance request must be a JSON object with scenario and seed"))
			return
		}
	}
	seedValue := req.Seed
	if seedValue == 0 {
		seedValue = s.Seed()
	}
	if err := s.Advance(strings.TrimSpace(req.Scenario), seedValue); err != nil {
		httpx.Fail(w, httpx.MalformedBody("scenario_unknown", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s.adminState())
}

// handleAdminStateDigest serves the state digest.
func (s *Server) handleAdminStateDigest(w http.ResponseWriter, r *http.Request) {
	digest, err := s.Digest()
	if err != nil {
		httpx.Fail(w, httpx.Internal("digest failed: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, digest)
}

// handleAdminCredentials serves the seed-derived credentials.
//
// It exists so a harness can hand a connector its credentials without a fixture
// file in the tree. It is admin-token gated: a connector token can never reach
// it, and the secrets it returns are the ones the connector is already expected
// to hold.
func (s *Server) handleAdminCredentials(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Credentials())
}
