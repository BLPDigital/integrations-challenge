package erp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scriptStep is one request of the determinism script.
type scriptStep struct {
	name string
	run  func(h *harness) *httptest.ResponseRecorder
}

// scriptResult is what one step produced, reduced to what a client can observe.
type scriptResult struct {
	name    string
	status  int
	headers string
	body    string
}

// determinismScript is a fixed request script covering every surface of the
// service: authentication, all three paginated lists with a cursor walk, the bulk
// form, the single GET, the conversion table, the SOAP channel through its
// retryable fault into a successful table, a CH20 permanent fault, single and
// batched postings, a replay, a duplicate, a business rejection and two transport
// errors.
//
// It is deliberately written as data rather than as a sequence of assertions: the
// point is not what each answer is, but that two servers with the same seed
// produce the same bytes for all of it.
func determinismScript() []scriptStep {
	return []scriptStep{
		{"auth token", func(h *harness) *httptest.ResponseRecorder {
			return h.post(RouteAuthToken, map[string]string{
				"client_id": h.creds.ClientID, "client_secret": h.creds.ClientSecret,
			}, "")
		}},
		{"suppliers page 1", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteSuppliers + "?limit=25")
		}},
		{"suppliers page 2 by cursor", func(h *harness) *httptest.ResponseRecorder {
			_, env := h.list(RouteSuppliers + "?limit=25")
			return h.get(RouteSuppliers + "?limit=25&cursor=" + env.NextCursor)
		}},
		{"suppliers clamped", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteSuppliers + "?limit=9999")
		}},
		{"suppliers delta", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteSuppliers + "?changed_since=100500&limit=50")
		}},
		{"purchase orders", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RoutePurchaseOrders + "?limit=40")
		}},
		{"purchase order bulk", func(h *harness) *httptest.ResponseRecorder {
			view := h.dataset().purchaseOrders
			ids := view.items[0].PONumber + "," + view.items[2].PONumber
			return h.get(RoutePurchaseOrders + "?ids=" + queryEscape(ids))
		}},
		{"purchase order lines", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RoutePurchaseOrderLines + "?limit=60")
		}},
		{"single supplier", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteSuppliers + "/0000000401")
		}},
		{"uom conversions", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteUoMConversions)
		}},
		{"soap first delivery", func(h *harness) *httptest.ResponseRecorder {
			return h.do(h.soapRequest(soapRequestOptions{}))
		}},
		{"soap retry", func(h *harness) *httptest.ResponseRecorder {
			return h.do(h.soapRequest(soapRequestOptions{}))
		}},
		{"soap CH20", func(h *harness) *httptest.ResponseRecorder {
			return h.do(h.soapRequest(soapRequestOptions{companyCode: "CH20"}))
		}},
		{"post single", func(h *harness) *httptest.ResponseRecorder {
			return h.post(RouteDocuments, h.goodItem("prp_det_1"), "key-det-1")
		}},
		{"post replay", func(h *harness) *httptest.ResponseRecorder {
			return h.post(RouteDocuments, h.goodItem("prp_det_1"), "key-det-1")
		}},
		{"post duplicate under another key", func(h *harness) *httptest.ResponseRecorder {
			return h.post(RouteDocuments, h.goodItem("prp_det_1"), "key-det-1b")
		}},
		{"post batch mixed", func(h *harness) *httptest.ResponseRecorder {
			good := h.goodItem("prp_det_2")
			bad := h.goodItem("prp_det_3")
			bad.SupplierNumber = h.blockedSupplier()
			return h.post(RouteDocumentsBatch,
				map[string]any{"items": []PostingItem{good, bad}}, "key-det-batch")
		}},
		{"post without a key", func(h *harness) *httptest.ResponseRecorder {
			return h.post(RouteDocuments, h.goodItem("prp_det_4"), "")
		}},
		{"forged cursor", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteSuppliers + "?cursor=eyJhIjoxfQ")
		}},
		{"wsdl", func(h *harness) *httptest.ResponseRecorder {
			return h.get(RouteSOAP + "?wsdl")
		}},
	}
}

// queryEscape percent-encodes a query parameter value.
func queryEscape(s string) string {
	out := make([]byte, 0, len(s)*3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == ',':
			out = append(out, c)
		default:
			out = append(out, '%', "0123456789ABCDEF"[c>>4], "0123456789ABCDEF"[c&0xf])
		}
	}
	return string(out)
}

// runDeterminismScript runs the script and reduces every answer to a comparable
// record.
func runDeterminismScript(t *testing.T, h *harness) []scriptResult {
	t.Helper()
	var out []scriptResult
	for _, step := range determinismScript() {
		rec := step.run(h)
		headers := ""
		for _, name := range []string{
			"Content-Type", HeaderLimitClamped, "Retry-After", "Idempotent-Replay",
		} {
			if v := rec.Header().Get(name); v != "" {
				headers += name + ": " + v + "\n"
			}
		}
		out = append(out, scriptResult{
			name: step.name, status: rec.Code, headers: headers, body: rec.Body.String(),
		})
	}
	return out
}

// TestDeterminismTwoServers is the contract the whole service exists to keep: two
// fresh servers with the same seed, driven through the same request script,
// produce byte-identical responses, byte-identical metrics, an identical request
// log and the same state digest.
//
// Chaos is ON, because injected faults are part of what has to be reproducible:
// they are content addressed, so the same logical requests meet the same faults.
func TestDeterminismTwoServers(t *testing.T) {
	first := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	second := newHarness(t, func(cfg *Config) { cfg.Chaos = true })

	a := runDeterminismScript(t, first)
	b := runDeterminismScript(t, second)
	if len(a) != len(b) {
		t.Fatalf("%d results against %d", len(a), len(b))
	}
	for i := range a {
		if a[i].name != b[i].name {
			t.Fatalf("step %d: %q against %q", i, a[i].name, b[i].name)
		}
		if a[i].status != b[i].status {
			t.Fatalf("step %q: status %d against %d", a[i].name, a[i].status, b[i].status)
		}
		if a[i].headers != b[i].headers {
			t.Fatalf("step %q: headers\n%s\nagainst\n%s", a[i].name, a[i].headers, b[i].headers)
		}
		if a[i].body != b[i].body {
			t.Fatalf("step %q: bodies differ\n%.400s\n---\n%.400s", a[i].name, a[i].body, b[i].body)
		}
	}

	// Metrics, verbatim bytes: the counters, the by-endpoint split, the virtual
	// clock and the quota all have to land in the same place.
	metricsA := first.adminGet(AdminPathMetrics).Body.String()
	metricsB := second.adminGet(AdminPathMetrics).Body.String()
	if metricsA != metricsB {
		t.Fatalf("metrics differ:\n%s\n%s", metricsA, metricsB)
	}
	if requestsA, requestsB := first.adminGet(AdminPathRequests).Body.String(),
		second.adminGet(AdminPathRequests).Body.String(); requestsA != requestsB {
		t.Fatalf("request logs differ:\n%.800s\n%.800s", requestsA, requestsB)
	}
	if callsA, callsB := first.adminGet(AdminPathSOAPCalls).Body.String(),
		second.adminGet(AdminPathSOAPCalls).Body.String(); callsA != callsB {
		t.Fatalf("SOAP call logs differ:\n%s\n%s", callsA, callsB)
	}
	if docsA, docsB := first.adminGet(AdminPathDocuments).Body.String(),
		second.adminGet(AdminPathDocuments).Body.String(); docsA != docsB {
		t.Fatalf("documents differ:\n%s\n%s", docsA, docsB)
	}
	digestA, err := first.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	digestB, err := second.srv.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if digestA.Digest != digestB.Digest {
		t.Fatalf("state digests differ:\n%s\n%s", digestA.Digest, digestB.Digest)
	}

	// The script must actually have exercised the surface, or this test would
	// pass on two servers that answered nothing.
	m := first.metrics()
	if m.RequestsTotal < int64(len(a)) {
		t.Fatalf("only %d accounted requests for %d steps", m.RequestsTotal, len(a))
	}
	if m.DocumentsCreated == 0 || m.SOAPCalls == 0 || m.DuplicateDocumentAttempts == 0 {
		t.Fatalf("the script did not reach the interesting paths: %+v", m)
	}
}

// TestDeterminismAcrossAReset asserts a reset really does restart the run: the
// same script after a reset produces the same bytes as it did on a fresh server.
func TestDeterminismAcrossAReset(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	before := runDeterminismScript(t, h)
	beforeMetrics := h.adminGet(AdminPathMetrics).Body.String()

	if err := h.srv.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	h.token = h.authToken()
	// The token request of the harness is one accounted request on both sides of
	// the reset, so the scripts start from the same place.
	fresh := newHarness(t, func(cfg *Config) { cfg.Chaos = true })
	after := runDeterminismScript(t, h)
	_ = runDeterminismScript(t, fresh)

	for i := range before {
		if before[i].body != after[i].body || before[i].status != after[i].status {
			t.Fatalf("step %q differs across a reset:\n%.300s\n---\n%.300s",
				before[i].name, before[i].body, after[i].body)
		}
	}
	if got := h.adminGet(AdminPathMetrics).Body.String(); got != beforeMetrics {
		t.Fatalf("metrics differ across a reset:\n%s\n%s", beforeMetrics, got)
	}
}

// TestSignaturesAreStableAndOrderIndependent asserts the classifier's signatures
// are what section 17.1 requires: a function of the logical request only. The
// opaque cursor string never appears in one, two encodings of the same position
// produce the same signature, and a posting's signature is the sorted set of its
// external references.
func TestSignaturesAreStableAndOrderIndependent(t *testing.T) {
	h := newHarness(t, nil)

	_, env := h.list(RouteSuppliers + "?limit=10")
	req := httptest.NewRequest(http.MethodGet, RouteSuppliers+"?limit=10&cursor="+env.NextCursor, nil)
	_, sig, _ := h.srv.classifyRequest(req)
	if want := "GET " + RouteSuppliers + "|changed_since=0|after=0000000410"; sig != want {
		t.Fatalf("list signature %q, want %q", sig, want)
	}
	if strings.Contains(sig, env.NextCursor) {
		t.Fatal("the signature must never carry the opaque cursor string")
	}

	// Same page, different page size: the same logical request.
	bigger := httptest.NewRequest(http.MethodGet, RouteSuppliers+"?limit=250&cursor="+env.NextCursor, nil)
	_, sigBigger, _ := h.srv.classifyRequest(bigger)
	if sigBigger != sig {
		t.Fatalf("limit changed the signature: %q against %q", sigBigger, sig)
	}

	// A posting's signature is the sorted external reference set, so two clients
	// that batch the same items in different orders meet the same faults.
	one := h.post(RouteDocumentsBatch, map[string]any{"items": []PostingItem{
		h.goodItem("prp_b"), h.goodItem("prp_a"),
	}}, "key-sig-1")
	if one.Code != http.StatusOK {
		t.Fatalf("batch: status %d", one.Code)
	}
	forward := postingSignature(t, h, []string{"prp_a", "prp_b"})
	backward := postingSignature(t, h, []string{"prp_b", "prp_a"})
	if forward != backward {
		t.Fatalf("item order changed the signature: %q against %q", forward, backward)
	}
	if want := "POST " + RouteDocumentsBatch + "|prp_a,prp_b"; forward != want {
		t.Fatalf("posting signature %q, want %q", forward, want)
	}

	// The SOAP signature is the operation and the company code, and nothing else.
	soapReq := h.soapRequest(soapRequestOptions{correlationID: "run_x"})
	_, soapSig, _ := h.srv.classifyRequest(soapReq)
	if want := "SOAP GetExchangeRateTable|CH10"; soapSig != want {
		t.Fatalf("SOAP signature %q, want %q", soapSig, want)
	}
}

// postingSignature classifies a batch of the named references and returns its
// signature.
func postingSignature(t *testing.T, h *harness, refs []string) string {
	t.Helper()
	items := make([]PostingItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, h.goodItem(ref))
	}
	body, err := marshalJSON(map[string]any{"items": items})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, RouteDocumentsBatch, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	_, sig, _ := h.srv.classifyRequest(req)
	return sig
}
