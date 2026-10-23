package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// A Source is the read-only view of one ERP that the UI renders. It is an
// interface with one method per view rather than a single fat snapshot, so a page
// reads exactly what it displays: the overview costs three reads, the SOAP call
// log costs one.
//
// Every method returns a value the caller owns. No implementation may hand out a
// slice or a map the ERP is still writing to, because a template ranging over
// live server state is a data race with a rendered page as its symptom.
type Source interface {
	// Metrics returns the run's request accounting.
	Metrics() (erp.AdminMetrics, error)
	// State returns the loaded scenario, seed, quota and dataset counts.
	State() (erp.AdminState, error)
	// Documents returns the created AP documents in creation order.
	Documents() ([]erp.Document, error)
	// Requests returns the request log in arrival order, oldest first. The UI
	// reverses it where it displays newest first.
	Requests() ([]erp.RequestLogEntry, error)
	// SOAPCalls returns the SOAP call log in arrival order.
	SOAPCalls() ([]erp.SOAPCall, error)
	// IdempotencyKeys returns the retained idempotency keys in insertion order,
	// in the store's own composite form (tenant, method, path, key joined by
	// unit separators).
	IdempotencyKeys() ([]string, error)
	// Dataset returns the loaded master data. The value is shared and must be
	// treated as immutable by the caller: a dataset is regenerated only when the
	// scenario or the seed changes, and every reader gets the same pointer.
	Dataset() (*seed.Dataset, error)
}

// An ERP is the part of *[erp.Server] the UI reads: its handler, so the admin
// surface can be called in process, and the three accessors that say which
// landscape is loaded.
//
// It is an interface so this package depends on what it uses rather than on a
// concrete server, which is also what lets a test drive the UI against a real
// [erp.Server] without any indirection of its own.
type ERP interface {
	http.Handler
	// Credentials returns the seed-derived credential set. The UI uses the
	// admin token from it and nothing else; it never renders a credential.
	Credentials() erp.Credentials
	// Scenario returns the loaded scenario identifier.
	Scenario() string
	// Seed returns the scenario seed.
	Seed() int64
}

// An AdminSource reads an ERP through its own admin surface, in process: it
// builds a GET, hands it to the server's handler with the admin token and decodes
// the JSON body. No socket, no network, no second copy of the ERP's state.
//
// This is the intended implementation of [Source], and it is deliberately not a
// set of extra accessors on the server: the admin surface is already the ERP's
// published read model, it is already free of charge, counted in no metric and
// never injected into, so a reviewer refreshing a page cannot perturb the very
// transcript the page is showing. The one thing the admin surface does not
// publish is the master data itself, and that needs no accessor either, because
// [seed.Generate] is a pure function of (scenario, seed) and the ERP loaded its
// dataset from exactly that pair.
type AdminSource struct {
	erp ERP

	// mu guards the dataset cache only. Everything else is a fresh read.
	mu sync.Mutex
	// set, setScenario and setSeed are the memoized dataset and the pair it was
	// generated from. Generating the S1 dataset is 12'000 suppliers and a
	// 5'000-invoice legacy file, which is far too much work to repeat per page
	// view, and it is also completely deterministic, so caching it changes
	// nothing but the cost.
	set          *seed.Dataset
	setScenario  string
	setSeed      int64
	setGenerated bool
}

// NewAdminSource returns a Source reading e's admin surface in process.
func NewAdminSource(e ERP) *AdminSource {
	return &AdminSource{erp: e}
}

// get issues an in-process admin GET for path and decodes the response body into
// out. A non-200 answer is an error naming the status and the body, because a
// UI that silently rendered an empty table when the admin surface said 403 would
// be worse than a UI that said so.
func (s *AdminSource) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("web: admin request %s: %w", path, err)
	}
	req.Header.Set(httpx.HeaderAdminToken, s.erp.Credentials().AdminToken)
	rec := &capture{header: http.Header{}}
	s.erp.ServeHTTP(rec, req)
	if rec.status != http.StatusOK && rec.status != 0 {
		return fmt.Errorf("web: admin GET %s: status %d: %s",
			path, rec.status, strings.TrimSpace(rec.body.String()))
	}
	if err := json.Unmarshal(rec.body.Bytes(), out); err != nil {
		return fmt.Errorf("web: admin GET %s: decode: %w", path, err)
	}
	return nil
}

// Metrics returns the run's request accounting.
func (s *AdminSource) Metrics() (erp.AdminMetrics, error) {
	var m erp.AdminMetrics
	err := s.get(erp.AdminPathMetrics, &m)
	return m, err
}

// State returns the loaded scenario, seed, quota and dataset counts.
func (s *AdminSource) State() (erp.AdminState, error) {
	// There is no GET for the state summary: the ERP builds it as the response
	// to a reset or a seed, both of which are writes and both of which the UI
	// must never perform. The counts come from the dataset, which is the same
	// value the server itself counted, and the scalars from the server's own
	// accessors.
	set, err := s.Dataset()
	if err != nil {
		return erp.AdminState{}, err
	}
	m, err := s.Metrics()
	if err != nil {
		return erp.AdminState{}, err
	}
	return erp.AdminState{
		Svc:          "erp",
		Scenario:     s.erp.Scenario(),
		Seed:         s.erp.Seed(),
		Chaos:        m.Chaos,
		Quota:        int(m.QuotaLimit),
		Counts:       set.Counts(),
		MaxChangeSeq: set.MaxChangeSeq,
		ExportFiles:  append([]string(nil), m.ExportFiles...),
	}, nil
}

// Documents returns the created AP documents in creation order.
func (s *AdminSource) Documents() ([]erp.Document, error) {
	var body erp.AdminDocuments
	if err := s.get(erp.AdminPathDocuments, &body); err != nil {
		return nil, err
	}
	return body.Documents, nil
}

// Requests returns the request log in arrival order.
func (s *AdminSource) Requests() ([]erp.RequestLogEntry, error) {
	var body erp.AdminRequests
	if err := s.get(erp.AdminPathRequests, &body); err != nil {
		return nil, err
	}
	return body.Requests, nil
}

// SOAPCalls returns the SOAP call log in arrival order.
func (s *AdminSource) SOAPCalls() ([]erp.SOAPCall, error) {
	var body erp.AdminSOAPCalls
	if err := s.get(erp.AdminPathSOAPCalls, &body); err != nil {
		return nil, err
	}
	return body.Calls, nil
}

// IdempotencyKeys returns the retained idempotency keys in insertion order.
func (s *AdminSource) IdempotencyKeys() ([]string, error) {
	var body erp.AdminIdempotencyKeys
	if err := s.get(erp.AdminPathIdempotencyKeys, &body); err != nil {
		return nil, err
	}
	return body.Keys, nil
}

// Dataset returns the loaded master data, regenerating it when the server's
// scenario or seed has changed since the last read.
//
// Regeneration is safe to do here because it is a pure function: the server
// loaded its own dataset from the same (scenario, seed) pair through the same
// generator, so this is the same landscape and not a second, parallel one.
func (s *AdminSource) Dataset() (*seed.Dataset, error) {
	scenario, seedValue := s.erp.Scenario(), s.erp.Seed()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setGenerated && s.setScenario == scenario && s.setSeed == seedValue {
		return s.set, nil
	}
	set, err := seed.Generate(scenario, seedValue)
	if err != nil {
		return nil, fmt.Errorf("web: generate dataset %s/%d: %w", scenario, seedValue, err)
	}
	s.set, s.setScenario, s.setSeed, s.setGenerated = set, scenario, seedValue, true
	return set, nil
}
