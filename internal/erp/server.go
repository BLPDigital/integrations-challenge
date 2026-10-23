package erp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// A Server is the mock ERP. It is an http.Handler and owns everything that makes
// one run deterministic: the loaded dataset, the Governor (virtual clock, token
// bucket, quota, content-addressed fault injection), the created documents, the
// idempotency store, the SOAP call log and the request log.
//
// One Server is one run. POST /erp-admin/v1/reset replaces the run state with a
// fresh one, and POST /erp-admin/v1/seed replaces the dataset too; both rebuild
// the middleware chain, because the Governor is captured by the accounting
// middleware and a reset Governor must be a new Governor.
type Server struct {
	cfg Config
	log *requestLog

	// rest and soap carry the two accounted surfaces, kept apart because they
	// authenticate differently: the REST surface takes a bearer token, the SOAP
	// channel takes a WS-Security UsernameToken, and one landscape with two auth
	// models is the point rather than an oversight. free carries the liveness
	// probe, the admin surface and the optional web UI, which cost nothing and
	// count nothing.
	rest *http.ServeMux
	soap *http.ServeMux
	free *http.ServeMux

	// chain is the accounted surface wrapped in Guard and Bearer. It is an
	// atomic value because a reset replaces the Governor underneath it.
	chain atomic.Value

	mu   sync.Mutex
	data *data
	st   *runState
}

// New loads the scenario's dataset and returns a ready Server. It writes the
// legacy file export drop, because seeding is when the sender's SFTP directory
// fills up.
func New(cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	d, err := loadData(cfg.Scenario, cfg.Seed)
	if err != nil {
		return nil, err
	}
	logOut := cfg.Log
	if logOut == nil {
		logOut = os.Stdout
	}
	s := &Server{
		cfg:  cfg,
		log:  newRequestLog(logOut, cfg.RequestLogLimit),
		rest: http.NewServeMux(),
		soap: http.NewServeMux(),
		free: http.NewServeMux(),
		data: d,
	}
	s.st = newRunState(cfg)
	s.routes()
	s.rebuildChain()
	if err := s.writeExport(); err != nil {
		return nil, err
	}
	return s, nil
}

// runState is everything that is per run rather than per dataset. A reset
// replaces the whole value, so no counter can survive a reset by accident.
type runState struct {
	gov *simclock.Governor
	// pageRequests counts page requests per dataset and emptyPageServed records
	// which datasets have had their one empty page, for Config.EmptyPageAt. Both
	// are guarded by mu.
	pageRequests    map[string]int
	emptyPageServed map[string]bool
	idem            *httpx.IdempotencyStore

	// cursorSecret signs pagination cursors. It is derived from the seed, not
	// drawn, so two servers on the same seed emit byte-identical cursors and a
	// transcript stays diffable.
	cursorSecret []byte

	// documents are the created AP documents in creation order, docByRef the
	// duplicate index over (tenant, external_reference), and docSeq the
	// document number counter.
	documents []Document
	docByRef  map[string]int
	docSeq    int

	// rejections counts business rejections by code, for the admin metrics.
	rejections map[string]int64

	// soapCalls is the SOAP call log; soapAttempts is the per-signature
	// attempt counter behind the retryable ERP-FX-503, which is content
	// addressed and never a count of SOAP requests.
	soapCalls    []SOAPCall
	soapAttempts map[string]int

	// exportFiles are the file names written to the export drop, in write
	// order, the .ok sentinel of each delivery last.
	exportFiles []string
}

// newRunState returns the run state of a fresh run: a new Governor with the
// published limits, an empty idempotency store and empty counters.
func newRunState(cfg Config) *runState {
	sum := sha256.Sum256([]byte("erp/cursor-secret|" + strconv.FormatInt(cfg.Seed, 10)))
	return &runState{
		gov: simclock.New(simclock.Config{
			Seed:               cfg.Seed,
			QuotaLimit:         cfg.Quota,
			BucketCapacity:     BucketCapacity,
			RefillIntervalVms:  BucketRefillIntervalVms,
			Chaos:              cfg.Chaos,
			PermanentFaultAt:   cfg.PermanentFaultAt,
			PermanentFaultPath: cfg.PermanentFaultPath,
			TokenRequestTTL:    TokenRequestTTL,
			TokenVirtualTTLVms: TokenVirtualTTLVms,
		}),
		idem:         httpx.NewIdempotencyStore(0),
		cursorSecret: sum[:],
		docByRef:     map[string]int{},
		rejections:   map[string]int64{},
		soapAttempts: map[string]int{},
	}
}

// routes registers both muxes. The accounted surface is registered with method
// and route patterns, so the route template a signature carries is the pattern
// itself and can never drift from what is served.
func (s *Server) routes() {
	s.rest.HandleFunc("POST "+RouteAuthToken, s.handleAuthToken)
	s.rest.HandleFunc("GET "+RouteSuppliers, s.handleSuppliers)
	s.rest.HandleFunc("GET "+RouteSupplier, s.handleSupplier)
	s.rest.HandleFunc("GET "+RoutePurchaseOrders, s.handlePurchaseOrders)
	s.rest.HandleFunc("GET "+RoutePurchaseOrderLines, s.handlePurchaseOrderLines)
	s.rest.HandleFunc("GET "+RouteUoMConversions, s.handleUoMConversions)
	s.rest.HandleFunc("POST "+RouteDocuments, s.handlePostDocument)
	s.rest.HandleFunc("POST "+RouteDocumentsBatch, s.handlePostDocumentBatch)
	s.rest.HandleFunc("/", s.handleNotFound)

	s.soap.HandleFunc("POST "+RouteSOAP, s.handleSOAP)
	s.soap.HandleFunc("GET "+RouteSOAP, s.handleWSDL)
	s.soap.HandleFunc("/", s.handleNotFound)

	s.free.HandleFunc("GET "+RouteHealthz, s.handleHealthz)
	s.adminRoutes(s.free)
	if s.cfg.UI != nil {
		s.free.Handle("/ui/", s.cfg.UI)
		s.free.Handle("/ui", s.cfg.UI)
	}
}

// rebuildChain wraps the accounted surface in the current Governor's middleware.
//
// The order is part of the contract: Guard outermost, Bearer inside it. Wrapping
// the other way round would make a 401 free, so a client that never refreshes
// its token would pay no quota for the requests it wastes.
//
// Bearer wraps the REST surface only. The SOAP channel authenticates with a
// WS-Security UsernameToken inside its own envelope and has no bearer token at
// all, which is the documented difference between the two channels and not an
// oversight; it is still inside Guard, so it is accounted like everything else.
func (s *Server) rebuildChain() {
	gov := s.governor()
	paid := http.NewServeMux()
	paid.Handle("/erp/v1/", httpx.Bearer(gov)(s.rest))
	paid.Handle("/soap/", s.soap)
	paid.HandleFunc("/", s.handleNotFound)
	s.chain.Store(httpx.GuardWith(httpx.GuardOptions{
		Governor: gov,
		Classify: s.classify,
		Service:  "erp",
		Log:      s.log,
	})(paid))
}

// ServeHTTP routes a request to the free surface or to the accounted chain.
//
// The liveness probe is free because the grader polls it before every run at
// 20 ms intervals, and a probe that spent quota would make the request budget
// depend on how fast the process started. The admin surface is free because a
// reviewer reading metrics must not be able to perturb a transcript.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == RouteHealthz || httpx.IsAdminPath(path) ||
		path == "/ui" || strings.HasPrefix(path, "/ui/") {
		s.free.ServeHTTP(w, r)
		return
	}
	s.chain.Load().(http.Handler).ServeHTTP(w, r)
}

// governor returns the current Governor.
func (s *Server) governor() *simclock.Governor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.gov
}

// state returns the current dataset and run state together, so a handler reads
// one consistent pair rather than two halves of two different runs.
func (s *Server) state() (*data, *runState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, s.st
}

// Credentials returns the credential set of this server. The grader hands these
// to the connector; they are derived from the seed, which is why the repository
// carries no credential fixture.
//
// It takes the lock because a reseed replaces the credentials along with the
// dataset, and a request that read half of an old set and half of a new one would
// be a race with no upside.
func (s *Server) Credentials() Credentials {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Creds
}

// Scenario returns the loaded scenario identifier.
func (s *Server) Scenario() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Scenario
}

// Seed returns the scenario seed.
func (s *Server) Seed() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Seed
}

// Reset restarts the run: a new Governor with a zeroed clock, quota and metrics,
// an empty idempotency store, no documents, no SOAP calls and an empty request
// log. The dataset is untouched, and the export drop is rewritten because a
// reset run must find the sender's directory in the state a fresh run finds it.
func (s *Server) Reset() error {
	s.mu.Lock()
	s.st = newRunState(s.cfg)
	s.mu.Unlock()
	s.log.reset()
	s.rebuildChain()
	return s.writeExport()
}

// Reseed loads a scenario at a seed and resets the run: a new dataset, a new
// Governor, no documents and a rewritten export drop. An empty scenario keeps
// the current one, which is how a caller reseeds the same scenario with a new
// seed. The admin token is deliberately preserved across a reseed, so the caller
// that issued the reseed can still read the metrics of what it just created.
func (s *Server) Reseed(scenario string, seedValue int64) error {
	if scenario == "" {
		scenario = s.Scenario()
	}
	d, err := loadData(scenario, seedValue)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg.Scenario = scenario
	s.cfg.Seed = seedValue
	s.cfg.Creds = Config{Seed: seedValue, Creds: Credentials{AdminToken: s.cfg.Creds.AdminToken}}.withDefaults().Creds
	s.data = d
	s.st = newRunState(s.cfg)
	s.mu.Unlock()
	s.log.reset()
	s.rebuildChain()
	return s.writeExport()
}

// Advance moves the ERP to a later scenario's master data WITHOUT discarding
// anything the run already produced.
//
// It exists because a delta scenario has to be two connector invocations against
// ONE ERP: a watermark that no second process ever reads back is not a watermark.
// Reseeding cannot do that, because it drops the documents already posted and the
// idempotency keys that prove they were posted once, and a connector's second
// night would then look like a first night to the ERP.
//
// So this replaces exactly the master data, the rate table and the export drop,
// and preserves the posted documents, the idempotency store, the request log and
// every counter. Change sequences come from the new dataset, which is cumulative
// by construction, so a connector that kept a watermark reads only what moved.
func (s *Server) Advance(scenario string, seedValue int64) error {
	if scenario == "" {
		return fmt.Errorf("erp: advance needs a scenario")
	}
	d, err := loadData(scenario, seedValue)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg.Scenario = scenario
	s.data = d
	s.mu.Unlock()
	// The chain index is derived from the dataset, so it is rebuilt; the run
	// state, the log and the counters are deliberately left alone.
	s.rebuildChain()
	return s.writeExport()
}

// handleHealthz answers the liveness probe.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	scenario, seedValue := s.cfg.Scenario, s.cfg.Seed
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, struct {
		Status   string `json:"status"`
		Svc      string `json:"svc"`
		Scenario string `json:"scenario"`
		Seed     int64  `json:"seed"`
	}{"ok", "erp", scenario, seedValue})
}

// handleNotFound answers an unknown path on the accounted surface with the
// canonical error body rather than net/http's plain text, so a client only ever
// has to parse one error shape.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotFound, &httpx.Error{
		Code:      "NOT_FOUND",
		Message:   "no such endpoint on this service",
		Retriable: false,
		Status:    http.StatusNotFound,
	})
}

// classify is the Guard's classifier: it returns the endpoint class of a
// request, the content-addressed signature of the logical request and the record
// count that feeds both the virtual cost and records_returned.
//
// The signature rules matter for validity, and they are these:
//
//   - a list page is "GET <routeTemplate>|changed_since=<v>|after=<decoded
//     cursor key>", using the DECODED cursor position, never the opaque cursor
//     string, so two clients that page the same data meet the same faults
//     whatever their cursors look like; the bulk ids= form adds the sorted
//     canonical id set, because a bulk read of three orders is a different
//     logical request from a bulk read of four,
//   - a single GET is the entity key,
//   - a posting is the sorted set of the items' external references,
//   - the SOAP operation is "SOAP GetExchangeRateTable|<CompanyCode>".
//
// Never the request sequence, never a timestamp, never a connection or goroutine
// identity: with any of those, a parallel connector and a serial connector would
// meet different fault sets and two runs of one correct submission would score
// differently.
func (s *Server) classify(r *http.Request) (endpoint string, signature string, records int) {
	endpoint, signature, records = s.classifyRequest(r)
	s.log.noteClass(signature, endpoint)
	return endpoint, signature, records
}

// classifyRequest is classify without the bookkeeping, so it can be tested as a
// pure function.
func (s *Server) classifyRequest(r *http.Request) (string, string, int) {
	d, _ := s.state()
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == RouteAuthToken:
		return string(simclock.ERPAuthToken), "POST " + RouteAuthToken, 0

	case r.Method == http.MethodGet && path == RouteSuppliers:
		return s.classifyList(r, d.suppliers)
	case r.Method == http.MethodGet && path == RoutePurchaseOrders:
		return s.classifyList(r, s.viewForPORequest(r, d))
	case r.Method == http.MethodGet && path == RoutePurchaseOrderLines:
		return s.classifyList(r, d.poLines)
	case r.Method == http.MethodGet && path == RouteUoMConversions:
		return string(simclock.ERPListPage), "GET " + RouteUoMConversions, len(d.uom)

	case r.Method == http.MethodGet && strings.HasPrefix(path, RouteSuppliers+"/"):
		key := strings.TrimPrefix(path, RouteSuppliers+"/")
		return string(simclock.ERPSingleGet), "GET " + RouteSupplier + "|" + key, 1

	case r.Method == http.MethodPost && path == RouteDocuments:
		refs, n := postingRefs(r, false)
		return string(simclock.ERPDocumentPost), "POST " + RouteDocuments + "|" + refs, n
	case r.Method == http.MethodPost && path == RouteDocumentsBatch:
		refs, n := postingRefs(r, true)
		return string(simclock.ERPBatchDocumentPost), "POST " + RouteDocumentsBatch + "|" + refs, n

	case r.Method == http.MethodPost && path == RouteSOAP:
		company := soapCompanyCode(r)
		return string(simclock.ERPSoapRateTable), soapSignature(company), s.soapRowCount(company)
	case r.Method == http.MethodGet && path == RouteSOAP:
		// The WSDL is reference material with no row of its own in the cost
		// table, so it advances the clock by nothing. It still spends one quota
		// unit, like every other non-admin request.
		return "erp_soap_wsdl", "GET " + RouteSOAP + "?wsdl", 0
	}
	// An unknown path. The endpoint label is a constant rather than the path, so
	// a client hitting random URLs cannot inflate the metrics map with one label
	// per URL; the signature keeps the path, because that is what identifies the
	// logical request.
	return "erp_unknown", r.Method + " " + path, 0
}

// classifyList classifies a list request. A request whose cursor or parameters
// are unusable is classified as a zero-record page: the handler will answer 400,
// and a rejected request must not be charged for records it never returned.
func (s *Server) classifyList(r *http.Request, v view) (string, string, int) {
	lr, err := s.parseListRequest(r, v)
	if err != nil {
		return string(simclock.ERPListPage), "GET " + v.route() + "|invalid", 0
	}
	page := lr.page()
	return string(simclock.ERPListPage), lr.signature(), page.count
}

// writeJSON writes payload as JSON with the canonical error-free happy path:
// no HTML escaping, one trailing newline, Content-Type application/json.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	buf, err := marshalJSON(payload)
	if err != nil {
		httpx.Fail(w, httpx.Internal("response body not serializable"))
		return
	}
	w.Header().Set("Content-Type", httpx.MediaJSON)
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// marshalJSON renders payload with HTML escaping off and a trailing newline. It
// is the only JSON encoder in the package, so every response body is built the
// same way and the bytes are a pure function of the value.
func marshalJSON(payload any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// errBadQuery is 400 MALFORMED_BODY for an unusable query parameter. The reason
// vocabulary is httpx's; the message names the parameter and the rule and never
// the value the client should have sent.
func errBadQuery(param, reason string) error {
	e := httpx.MalformedBody(reason, "query parameter "+param+" is not usable")
	return e.WithDetail("parameter", param)
}

// intQuery parses a non-negative integer query parameter. An absent or empty
// parameter yields def.
func intQuery(r *http.Request, name string, def int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errBadQuery(name, "not_an_integer")
	}
	if v < 0 {
		return 0, errBadQuery(name, "negative")
	}
	return v, nil
}
