package miniblp

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The twin's published rate limiting and token lifetime, from BUILD-SPEC 3.2
// and 4. They are constants rather than flags because a connector is graded
// against them: a scenario may change the hard quota, never the bucket.
const (
	// BucketCapacity is the token bucket's capacity and its initial fill.
	BucketCapacity = 30
	// RefillIntervalVms is the virtual-millisecond interval that earns one
	// token. It is also the retry_after_hint_ms of a 429.
	RefillIntervalVms = 40
	// TokenRequestTTL is how many authenticated requests an access token
	// survives.
	TokenRequestTTL = 250
	// TokenVirtualTTLVms is how much virtual time an access token survives.
	TokenVirtualTTLVms = 900_000
)

// Wire limits of the ingest and outbox surface, from BUILD-SPEC 8.1 and 8.4.
const (
	// MaxChunkRecords is the largest number of records one ingest chunk may
	// carry. The byte limit is httpx.MaxRequestBodyBytes, the same 8 MiB.
	MaxChunkRecords = 1000
	// MaxAcks is the largest number of acks one POST /v1/outbox/acks may carry.
	MaxAcks = 1000
	// MaxOutboxPage is the largest, and the default, outbox page size.
	MaxOutboxPage = 500
)

// Codes the twin adds to the shared httpx vocabulary. They are the batch- and
// record-level findings of the ingest surface; the record-level data codes come
// from internal/model and internal/importer, and the blocking match verdicts are
// the published EXC_ set in match.go.
const (
	// CodeUnknownProfile reports a manifest profile no importer is registered
	// for. It is a whole-batch reject: a file whose format nobody can read
	// cannot be partially applied.
	CodeUnknownProfile = "UNKNOWN_PROFILE"
	// CodeManifestMissing reports a batch directory seen on two consecutive
	// scans with no manifest.json in it. Two scans rather than one, because the
	// first sighting may be a publication that is still in flight.
	CodeManifestMissing = "MANIFEST_MISSING"
	// CodeManifestInvalid reports a manifest that is not readable, not the
	// declared version, or internally inconsistent.
	CodeManifestInvalid = "MANIFEST_INVALID"
	// CodeBatchIDConflict reports a batch id delivered twice with a different
	// manifest. The first delivery stands: a silent overwrite would make the
	// twin's history depend on delivery order.
	CodeBatchIDConflict = "BATCH_ID_CONFLICT"
	// CodeChecksumMismatch reports a file whose bytes do not hash to the sha256
	// the manifest declares. It is the truncated-transfer guard and is checked
	// before a single record is applied.
	CodeChecksumMismatch = "CHECKSUM_MISMATCH"
	// CodeRecordCountMismatch reports a file that parsed to a different number
	// of records than the manifest declares. Also checked before any record is
	// applied.
	CodeRecordCountMismatch = "RECORD_COUNT_MISMATCH"
	// CodeFileUnreadable reports a file the manifest names that could not be
	// read at all.
	CodeFileUnreadable = "FILE_UNREADABLE"
	// CodeFileNotAccepted reports a file the importer refused: a fatal
	// diagnostic, or a rejected record under on_error abort_batch.
	CodeFileNotAccepted = "FILE_NOT_ACCEPTED"
	// CodeForeignWriteDetected reports the connector writing into a directory
	// the twin owns. Only incoming/ is the connector's; everything else is
	// state the twin is responsible for, and a foreign write there means the
	// run's bookkeeping cannot be trusted.
	CodeForeignWriteDetected = "FOREIGN_WRITE_DETECTED"
	// CodeBatchTooLarge reports a chunk above MaxChunkRecords records or above
	// the 8 MiB body limit.
	CodeBatchTooLarge = "BATCH_TOO_LARGE"
	// CodeBatchNotFound reports a batch ref no open batch has.
	CodeBatchNotFound = "BATCH_NOT_FOUND"
	// CodeBatchClosed reports a chunk or a commit on a batch that is already
	// committed or aborted.
	CodeBatchClosed = "BATCH_CLOSED"
	// CodeDatasetUnknown reports a dataset outside model.Datasets.
	CodeDatasetUnknown = "DATASET_UNKNOWN"
	// CodeVersionConflict reports a failed if_version, If-Match or
	// If-None-Match precondition. It is a record-level code on a chunk and a
	// 412 on the single-record PUT.
	CodeVersionConflict = "E_VERSION_CONFLICT"
	// CodeClosureViolation reports a receipt whose outcome counts do not add up
	// to the number of records seen. It is an internal error, never a warning:
	// the whole point of the receipt is that nothing was dropped silently.
	CodeClosureViolation = "CLOSURE_VIOLATION"
	// CodeAuthFailed reports client credentials the scenario does not know.
	CodeAuthFailed = "AUTH_FAILED"
	// CodeNotFound reports an admin lookup that resolved to nothing.
	CodeNotFound = "NOT_FOUND"
)

// Record outcomes of a receipt, from BUILD-SPEC 8.2. Exactly one of them
// applies to every record seen, which is what makes the closure invariant an
// invariant rather than a hope.
const (
	// OutcomeAccepted is a record that was applied with no findings.
	OutcomeAccepted = "accepted"
	// OutcomeAcceptedWithWarning is a record that was applied and carries at
	// least one warning, such as a documented ambiguity.
	OutcomeAcceptedWithWarning = "accepted_with_warning"
	// OutcomeRejected is a record that was not applied because of a finding in
	// the data.
	OutcomeRejected = "rejected"
	// OutcomeSkippedUnchanged is a record whose content the twin already held,
	// so no new version was created; a provenance entry was still appended.
	OutcomeSkippedUnchanged = "skipped_unchanged"
	// OutcomeReplayed is a record of a batch that is a byte-identical
	// re-delivery of a batch already processed. Nothing was applied.
	OutcomeReplayed = "replayed"
	// OutcomeQuarantined is a record that parsed and validated but could not be
	// applied for a reason that is neither the sender's fault nor a permanent
	// one. It is held for an operator instead of being dropped.
	OutcomeQuarantined = "quarantined"
)

// Batch statuses reported on a receipt and by GET /admin/v1/batches.
const (
	// BatchOpen is a REST batch that has been opened and not yet committed.
	BatchOpen = "open"
	// BatchAccepted is a batch every record of which was applied.
	BatchAccepted = "accepted"
	// BatchPartiallyAccepted is a batch some records of which were rejected.
	BatchPartiallyAccepted = "partially_accepted"
	// BatchRejected is a wholesale-rejected batch: nothing in it was applied.
	BatchRejected = "rejected"
	// BatchReplayed is a byte-identical re-delivery of a processed batch.
	BatchReplayed = "replayed"
	// BatchAborted is a REST batch the client abandoned.
	BatchAborted = "aborted"
)

// Ingest channels, as they appear in provenance and on a receipt.
const (
	// ChannelFile is the inbox drop directory.
	ChannelFile = store.ChannelFile
	// ChannelREST is the HTTP ingest surface.
	ChannelREST = store.ChannelREST
)

// Config configures a Server. Every field is optional except DataDir, InboxDir
// and OutboxDir; the zero value of the rest is the documented local default, so
// a test needs three temporary directories and nothing else.
type Config struct {
	// DataDir holds the revision store, under DataDir/store.
	DataDir string
	// InboxDir is the file channel's root: it holds incoming, processing,
	// processed, rejected and receipts.
	InboxDir string
	// OutboxDir is where mirrored proposal exports are written, one
	// subdirectory per run id.
	OutboxDir string
	// Scenario and Seed identify the run. The seed keys fault injection, token
	// derivation and the cursor signature, so two runs of one scenario are
	// byte-comparable.
	Scenario string
	Seed     int64
	// Quota is the hard per-run request quota. Non-positive means unlimited.
	Quota int
	// Chaos enables the seed-keyed fault injection of BUILD-SPEC 17.1. It
	// changes nothing else, so the state digest is identical either way.
	Chaos bool

	// PermanentFaultAt answers the nth request whose path contains
	// PermanentFaultPath with a permanent, non-retriable transport error: a 503
	// whose retriable flag is false. Zero disables it.
	//
	// It exists so a scenario can state that one specific exchange never
	// succeeded, which is the only way to reach the state where the twin still
	// offers a proposal the connector has already posted: the acknowledgment did
	// not land. Independent of Chaos, because it is a property of the scenario
	// rather than of the noise.
	PermanentFaultAt   int
	PermanentFaultPath string
	// AdminToken gates /admin/v1. An empty token closes the admin surface
	// completely, which is httpx.CheckAdminToken's fail-closed contract.
	AdminToken string
	// ClientID and ClientSecret are the connector's credentials for
	// POST /v1/auth/token. When both are empty the endpoint accepts any
	// non-empty pair, so `make up` works without a fixture; grading always sets
	// them.
	ClientID     string
	ClientSecret string
	// RunID is the run the twin attributes its own writes to, and the outbox
	// subdirectory used when a manifest declares no run id of its own.
	RunID string
	// Log is where the structured request log goes. Nil means os.Stdout.
	Log io.Writer
	// UI is an optional read-only web interface, mounted at /ui outside the
	// Guard and outside the admin token check: it is a local development tool
	// that must cost no quota and must never be able to perturb the transcript
	// it renders. Nil leaves /ui unmounted.
	UI http.Handler
}

// A Server is one running digital twin: a revision store, a request Governor,
// the two ingest channels, the matching engine, the outbox and the admin
// surface.
//
// One mutex serializes every mutation of the twin's own state - batches,
// receipts, the scan counter, the proposal counter, run reports - so that two
// concurrent chunk requests produce the same state as the same two requests
// serialized. The store has its own lock and is safe on its own; the server's
// lock exists for the bookkeeping around it. Determinism is worth more here
// than concurrency: a scenario is graded on its bytes.
type Server struct {
	cfg Config

	// storeDir is DataDir/store.
	storeDir string

	// handlerMu guards handler, which POST /admin/v1/reset replaces. It is not
	// mu: a request holds it only while it looks the handler up.
	handlerMu sync.RWMutex
	handler   http.Handler

	mu    sync.Mutex
	st    *store.Store
	gov   *simclock.Governor
	idem  *httpx.IdempotencyStore
	seedS string

	// cursorSecret signs outbox cursors. It is derived from the seed, so the
	// same logical position encodes to the same bytes on every run.
	cursorSecret []byte

	// batches holds every batch this process has seen, keyed by batch id, and
	// batchOrder is the order they were first seen, which is what
	// GET /admin/v1/batches reports.
	batches    map[string]*batchState
	batchOrder []string

	// scanCount is the monotone inbox scan counter. It is never a timestamp.
	scanCount int64
	// noManifest counts consecutive scans that saw a batch directory without a
	// manifest, keyed by directory name.
	noManifest map[string]int64
	// owned records the entries the twin itself created in the directories it
	// owns, so anything else found there is a foreign write.
	owned map[string]bool

	// proposalSeq is the created_seq of the next proposal. It is restored from
	// the store on open, so ids stay unique and ascending across restarts.
	proposalSeq int64

	// runs holds the per-run report, keyed by run id, and lastRun names the
	// most recent one.
	runs    map[string]*runReport
	runList []string
	lastRun string
}

// New opens or creates a twin at cfg's directories and returns it ready to
// serve. The directory tree of the file channel is created if absent, because a
// twin with no inbox cannot be scanned and failing at the first scan instead is
// a worse diagnostic.
func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("miniblp: DataDir is required")
	}
	if strings.TrimSpace(cfg.InboxDir) == "" {
		return nil, errors.New("miniblp: InboxDir is required")
	}
	if strings.TrimSpace(cfg.OutboxDir) == "" {
		return nil, errors.New("miniblp: OutboxDir is required")
	}
	if cfg.RunID == "" {
		cfg.RunID = "run_twin"
	}
	s := &Server{
		cfg:      cfg,
		storeDir: filepath.Join(cfg.DataDir, "store"),
	}
	if err := s.open(); err != nil {
		return nil, err
	}
	return s, nil
}

// open creates the directory tree, opens the store and builds the request
// pipeline. It is also the second half of POST /admin/v1/reset.
func (s *Server) open() error {
	for _, dir := range s.dirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("miniblp: creating %s: %w", dir, err)
		}
	}
	st, err := store.Open(s.storeDir)
	if err != nil {
		return err
	}
	s.st = st
	s.gov = simclock.New(simclock.Config{
		Seed:               s.cfg.Seed,
		QuotaLimit:         s.cfg.Quota,
		BucketCapacity:     BucketCapacity,
		RefillIntervalVms:  RefillIntervalVms,
		Chaos:              s.cfg.Chaos,
		PermanentFaultAt:   s.cfg.PermanentFaultAt,
		PermanentFaultPath: s.cfg.PermanentFaultPath,
		TokenRequestTTL:    TokenRequestTTL,
		TokenVirtualTTLVms: TokenVirtualTTLVms,
	})
	s.idem = httpx.NewIdempotencyStore(0)
	s.cursorSecret = deriveSecret("miniblp/cursor", s.cfg.Seed)
	s.batches = map[string]*batchState{}
	s.batchOrder = nil
	s.scanCount = 0
	s.noManifest = map[string]int64{}
	s.owned = map[string]bool{}
	s.runs = map[string]*runReport{}
	s.runList = nil
	s.lastRun = ""
	if err := s.restoreProposalSeq(); err != nil {
		return err
	}
	s.adoptOwned()
	s.handlerMu.Lock()
	s.handler = s.buildHandler()
	s.handlerMu.Unlock()
	return nil
}

// dirs returns every directory the twin owns, in creation order.
func (s *Server) dirs() []string {
	return []string{
		s.cfg.DataDir, s.storeDir,
		s.cfg.InboxDir,
		filepath.Join(s.cfg.InboxDir, dirIncoming),
		filepath.Join(s.cfg.InboxDir, dirProcessing),
		filepath.Join(s.cfg.InboxDir, dirProcessed),
		filepath.Join(s.cfg.InboxDir, dirRejected),
		filepath.Join(s.cfg.InboxDir, dirReceipts),
		s.cfg.OutboxDir,
	}
}

// restoreProposalSeq sets the next proposal sequence from the store, so a
// restart continues the id series instead of colliding with it.
func (s *Server) restoreProposalSeq() error {
	high := int64(0)
	err := s.st.ScanProposals(func(p store.Proposal) bool {
		if p.CreatedSeq > high {
			high = p.CreatedSeq
		}
		return true
	})
	if err != nil {
		return err
	}
	s.proposalSeq = high + 1
	return nil
}

// deriveSecret returns a deterministic secret for a purpose and a seed. Nothing
// in this landscape is random: two runs of the same scenario have to sign their
// cursors identically or a transcript stops being diffable.
func deriveSecret(purpose string, seed int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(seed))
	sum := sha256.Sum256(append([]byte(purpose+"\x00"), b[:]...))
	return sum[:]
}

// Close closes the store. It is idempotent.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil {
		return nil
	}
	return s.st.Close()
}

// Store returns the revision store. It exists for the UI and for tests; the
// store is safe for concurrent use.
func (s *Server) Store() *store.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// Governor returns the request Governor, whose Metrics the admin surface and
// the UI report.
func (s *Server) Governor() *simclock.Governor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gov
}

// Handler returns the twin's HTTP handler. The returned handler is stable for
// the life of the Server: it looks the current pipeline up per request, so
// POST /admin/v1/reset can replace the Governor - and with it every counter -
// without invalidating a handler a caller already installed.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			s.healthz(w, r)
			return
		}
		if s.cfg.UI != nil && (r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/")) {
			s.cfg.UI.ServeHTTP(w, r)
			return
		}
		s.handlerMu.RLock()
		h := s.handler
		s.handlerMu.RUnlock()
		h.ServeHTTP(w, r)
	})
}

// buildHandler wires the pipeline: the accounting Guard outermost, the bearer
// token check inside it, the routes innermost. The order is the one httpx.Guard
// documents and is part of the contract: a 401 has to cost its quota unit like
// any other request, or a client that never refreshes its token pays nothing for
// the requests it wastes.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	guard := httpx.GuardWith(httpx.GuardOptions{
		Governor: s.gov,
		Classify: s.classify,
		Service:  "miniblp",
		Log:      s.cfg.Log,
	})
	return guard(httpx.Bearer(s.gov)(mux))
}

// routes registers every route of both surfaces.
func (s *Server) routes(mux *http.ServeMux) {
	// Versioned connector surface.
	mux.HandleFunc("POST /v1/auth/token", s.handleAuthToken)
	mux.HandleFunc("POST /v1/ingest/batches", s.handleOpenBatch)
	mux.HandleFunc("POST /v1/ingest/batches/{ref}/records", s.handleChunk)
	mux.HandleFunc("POST /v1/ingest/batches/{ref}/commit", s.handleCommit)
	mux.HandleFunc("POST /v1/ingest/batches/{ref}/abort", s.handleAbort)
	mux.HandleFunc("PUT /v1/ingest/records/{dataset}/{key}", s.handlePutRecord)
	mux.HandleFunc("GET /v1/outbox/proposals", s.handleOutboxProposals)
	mux.HandleFunc("POST /v1/outbox/acks", s.handleAcks)

	// Admin surface. Free of quota, free of virtual time, admin token gated.
	mux.HandleFunc("GET /admin/v1/records/raw/{sha256}", s.adminRawRecord)
	mux.HandleFunc("GET /admin/v1/records/{dataset}/{key}", s.adminRecord)
	mux.HandleFunc("GET /admin/v1/batches", s.adminBatches)
	mux.HandleFunc("GET /admin/v1/batches/{id}/records", s.adminBatchRecords)
	mux.HandleFunc("GET /admin/v1/runs/last", s.adminRunLast)
	mux.HandleFunc("GET /admin/v1/runs/{run_id}", s.adminRun)
	mux.HandleFunc("GET /admin/v1/exceptions", s.adminExceptions)
	mux.HandleFunc("GET /admin/v1/proposals", s.adminProposals)
	mux.HandleFunc("GET /admin/v1/audit/{key}", s.adminAudit)
	mux.HandleFunc("GET /admin/v1/state/digest", s.adminDigest)
	mux.HandleFunc("GET /admin/v1/metrics", s.adminMetrics)
	mux.HandleFunc("POST /admin/v1/inbox/scan", s.adminScan)
	mux.HandleFunc("POST /admin/v1/reset", s.adminReset)
	mux.HandleFunc("POST /admin/v1/seed", s.adminSeed)
}

// healthz answers the readiness probe. It is outside the Guard and outside the
// admin token check: the grader polls it before the run starts, so it may cost
// nothing and may not require a credential.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpx.Fail(w, &httpx.Error{Code: "METHOD_NOT_ALLOWED", Message: "GET /healthz",
			Status: http.StatusMethodNotAllowed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"svc":      "miniblp",
		"scenario": s.cfg.Scenario,
	})
}

// handleAuthToken issues an access token for client credentials.
//
// The credentials come from the scenario fixture through Config. When the
// fixture set none, any non-empty pair is accepted, so a local `make up` works;
// the response is otherwise identical, which keeps the local and the graded
// transcript the same shape.
//
// A JSON body and a form-encoded body are both read. The versioned surface is
// JSON, but half the HTTP clients in the world post client credentials as a form
// and the token endpoint is the first request a connector ever makes: refusing
// the form spelling would spend a candidate's afternoon on a 400 that says
// nothing about the integration.
func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		GrantType    string `json:"grant_type"`
	}
	switch {
	case strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded"):
		form, err := url.ParseQuery(string(body))
		if err != nil {
			httpx.Fail(w, httpx.MalformedBody("form_invalid",
				"the token request carries client_id and client_secret"))
			return
		}
		req.ClientID = form.Get("client_id")
		req.ClientSecret = form.Get("client_secret")
		req.GrantType = form.Get("grant_type")
	case len(body) > 0:
		if err := json.Unmarshal(body, &req); err != nil {
			httpx.Fail(w, httpx.MalformedBody("json_invalid",
				"the token request is a JSON object with client_id and client_secret"))
			return
		}
	}
	if !s.credentialsOK(req.ClientID, req.ClientSecret) {
		httpx.Fail(w, &httpx.Error{Code: CodeAuthFailed, Message: "unknown client credentials",
			Retriable: false, Status: http.StatusUnauthorized})
		return
	}
	s.mu.Lock()
	token := s.gov.IssueToken()
	requests, virtual := s.gov.TokenTTL()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":             token,
		"token_type":               "Bearer",
		"expires_after_requests":   requests,
		"expires_after_virtual_ms": virtual,
	})
}

// credentialsOK compares client credentials in constant time. An unconfigured
// twin accepts any non-empty pair; a configured one accepts exactly its own.
func (s *Server) credentialsOK(id, secret string) bool {
	if s.cfg.ClientID == "" && s.cfg.ClientSecret == "" {
		return id != "" && secret != ""
	}
	okID := subtle.ConstantTimeCompare([]byte(id), []byte(s.cfg.ClientID)) == 1
	okSecret := subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.ClientSecret)) == 1
	return okID && okSecret
}

// classify is the Guard's classifier: it names the endpoint class that prices
// the request, the content-addressed signature that keys fault injection, and
// the record count that both the cost and the metrics need.
//
// The signature is a function of the LOGICAL request and of nothing else. Per
// BUILD-SPEC 17.1 it carries the method, the route template and the
// canonicalized identifying parameters, and never the request sequence, a
// clock, a connection or a goroutine, so a parallel connector and a serial one
// meet exactly the same fault set:
//
//   - an ingest chunk: "POST /v1/ingest/records|<batch_ref>|<dataset>|<ordinal>";
//   - an outbox page: the status filter and the DECODED cursor position, never
//     the opaque cursor string;
//   - an ack submission: the sorted proposal ids it acknowledges;
//   - a batch open: the manifest's batch id;
//   - a single-record PUT: its dataset and key.
func (s *Server) classify(r *http.Request) (string, string, int) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/v1/auth/token":
		return string(simclock.TwinAuthToken), "POST /v1/auth/token", 0

	case r.Method == http.MethodPost && path == "/v1/ingest/batches":
		id := ""
		var m manifest
		if json.Unmarshal(classifyBody(r), &m) == nil {
			id = m.BatchID
		}
		return string(simclock.TwinOpenBatch), "POST /v1/ingest/batches|" + id, 0

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/records") &&
		strings.HasPrefix(path, "/v1/ingest/batches/"):
		ref, _, _ := strings.Cut(strings.TrimPrefix(path, "/v1/ingest/batches/"), "/")
		dataset := r.URL.Query().Get("dataset")
		ordinal := r.Header.Get(HeaderChunkOrdinal)
		n, _ := s.countRecords(r, dataset)
		sig := "POST /v1/ingest/records|" + ref + "|" + dataset + "|" + ordinal
		return string(simclock.TwinRecordsChunk), sig, n

	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/ingest/batches/"):
		rest := strings.TrimPrefix(path, "/v1/ingest/batches/")
		ref, action, _ := strings.Cut(rest, "/")
		return string(simclock.TwinCommit), "POST /v1/ingest/batches/" + action + "|" + ref, 0

	case r.Method == http.MethodPut && strings.HasPrefix(path, "/v1/ingest/records/"):
		rest := strings.TrimPrefix(path, "/v1/ingest/records/")
		ds, key, _ := strings.Cut(rest, "/")
		return string(simclock.TwinSinglePut), "PUT /v1/ingest/records|" + ds + "|" + key, 1

	case r.Method == http.MethodGet && path == "/v1/outbox/proposals":
		status, limit, cur, err := s.outboxQuery(r)
		pos := ""
		if err == nil {
			pos = cur.LastKey + ":" + strconv.FormatInt(cur.ChangeSeqHigh, 10)
		}
		return string(simclock.TwinOutboxList),
			"GET /v1/outbox/proposals|" + status + "|" + pos, s.outboxPageSize(status, limit)

	case r.Method == http.MethodPost && path == "/v1/outbox/acks":
		ids, n := s.ackSignature(r)
		return string(simclock.TwinAcks), "POST /v1/outbox/acks|" + ids, n
	}
	return "", "", 0
}

// classifyBody reads the request body for the classifier and always puts it
// back, whatever happened.
//
// Putting it back unconditionally is the whole point. The classifier runs before
// the handler on the same request, so a read it does not restore is a body the
// handler never sees - and the handler is the only place that can answer with the
// right status. A body over the published cap is returned as it was read, one
// byte past the cap, so the handler's own httpx.ReadBody reaches the same verdict
// the classifier did.
func classifyBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	buf, _ := io.ReadAll(io.LimitReader(r.Body, httpx.MaxRequestBodyBytes+1))
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf
}

// countRecords returns how many records a chunk body carries, which is what the
// per-record half of the chunk's virtual cost is charged on. It decodes the body
// a second time rather than caching it: the cost of a request has to be a pure
// function of the request, and one extra decode is a cheaper price for that than
// a cache that could disagree with the handler.
func (s *Server) countRecords(r *http.Request, dataset string) (int, error) {
	body := classifyBody(r)
	if len(body) == 0 || len(body) > httpx.MaxRequestBodyBytes {
		return 0, nil
	}
	probe := r.Clone(r.Context())
	probe.Body = io.NopCloser(bytes.NewReader(body))
	recs, err := httpx.DecodeRecords(probe, dataset)
	if err != nil {
		return 0, err
	}
	return len(recs), nil
}

// ackSignature returns the sorted proposal ids an ack submission names, joined
// with commas, and how many acks it carries. Sorted, because two connectors that
// acknowledge the same proposals in a different order are making the same
// logical request and must meet the same faults.
func (s *Server) ackSignature(r *http.Request) (string, int) {
	body := classifyBody(r)
	if len(body) == 0 || len(body) > httpx.MaxRequestBodyBytes {
		return "", 0
	}
	probe := r.Clone(r.Context())
	probe.Body = io.NopCloser(bytes.NewReader(body))
	recs, err := httpx.DecodeRecords(probe, model.DatasetOutboxAck.String())
	if err != nil {
		return "", 0
	}
	ids := make([]string, 0, len(recs))
	for _, raw := range recs {
		var probe struct {
			ProposalID string `json:"proposal_id"`
		}
		if json.Unmarshal(raw, &probe) == nil {
			ids = append(ids, probe.ProposalID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ","), len(recs)
}

// twinID returns the twin's surrogate id for a record: a content address of its
// dataset and natural key, so it is stable across runs, carries no counter and
// can be recomputed from a receipt line alone.
func twinID(dataset, key string) string {
	sum := sha256.Sum256([]byte("miniblp/twin-id\x00" + dataset + model.KeySeparator + key))
	return "twn_" + hex.EncodeToString(sum[:8])
}

// writeJSON writes a value as JSON with the given status. Errors are the
// canonical error body and go through httpx.Fail; this is for the successful
// shapes the admin surface serves.
func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		httpx.Fail(w, httpx.Internal("the response could not be encoded"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(buf, '\n'))
}

// statusForResults returns the status of a per-record response: 200 when every
// record was accepted, 422 when every record was rejected, 207 in between. An
// empty set is 200: a chunk with no records is a no-op, not a failure.
func statusForResults(res []RecordResult) int {
	rejected, total := 0, len(res)
	for _, r := range res {
		if r.Outcome == OutcomeRejected || r.Outcome == OutcomeQuarantined {
			rejected++
		}
	}
	switch {
	case total == 0 || rejected == 0:
		return http.StatusOK
	case rejected == total:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusMultiStatus
	}
}
