package erp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// soapRequestOptions builds a SOAP request envelope. Every field defaults to a
// correct value, so a test changes exactly the one thing it is about.
type soapRequestOptions struct {
	username      string
	password      string
	omitSecurity  bool
	correlationID string
	omitContext   bool
	companyCode   string
	omitCompany   bool
	extraHeader   string
	contentType   string
	action        string
	omitAction    bool
}

// soapRequest renders the envelope and the request that carries it.
func (h *harness) soapRequest(opts soapRequestOptions) *http.Request {
	h.t.Helper()
	if opts.username == "" {
		opts.username = h.creds.SOAPUsername
	}
	if opts.password == "" {
		opts.password = h.creds.SOAPPassword
	}
	if opts.correlationID == "" {
		opts.correlationID = "run_7f3c1a"
	}
	if opts.companyCode == "" {
		opts.companyCode = model.CompanyCodeCH10
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<soap:Envelope xmlns:soap="` + NSSoapEnv + `"` +
		` xmlns:fin="` + NSFinRef + `" xmlns:cmn="` + NSCommon + `"` +
		` xmlns:wsse="` + NSWSSE + `">` + "\n")
	b.WriteString("  <soap:Header>\n")
	if !opts.omitSecurity {
		b.WriteString(`    <wsse:Security soap:mustUnderstand="1">` + "\n")
		b.WriteString("      <wsse:UsernameToken>\n")
		b.WriteString("        <wsse:Username>" + xmlEscape(opts.username) + "</wsse:Username>\n")
		b.WriteString(`        <wsse:Password Type="PasswordText">` +
			xmlEscape(opts.password) + "</wsse:Password>\n")
		b.WriteString("      </wsse:UsernameToken>\n")
		b.WriteString("    </wsse:Security>\n")
	}
	if !opts.omitContext {
		b.WriteString(`    <fin:RequestContext soap:mustUnderstand="1">` + "\n")
		b.WriteString("      <cmn:CorrelationId>" + xmlEscape(opts.correlationID) + "</cmn:CorrelationId>\n")
		b.WriteString("    </fin:RequestContext>\n")
	}
	if opts.extraHeader != "" {
		b.WriteString("    " + opts.extraHeader + "\n")
	}
	b.WriteString("  </soap:Header>\n")
	b.WriteString("  <soap:Body>\n    <fin:GetExchangeRateTable>\n")
	if !opts.omitCompany {
		b.WriteString("      <fin:CompanyCode>" + xmlEscape(opts.companyCode) + "</fin:CompanyCode>\n")
	}
	// RateType, ValidFrom and ValidTo are always sent and always ignored.
	b.WriteString("      <fin:RateType>DAILY</fin:RateType>\n")
	b.WriteString("      <fin:ValidFrom>2026-03-01</fin:ValidFrom>\n")
	b.WriteString("      <fin:ValidTo>2026-03-31</fin:ValidTo>\n")
	b.WriteString("    </fin:GetExchangeRateTable>\n  </soap:Body>\n</soap:Envelope>\n")

	req := httptest.NewRequest(http.MethodPost, RouteSOAP, strings.NewReader(b.String()))
	ct := opts.contentType
	if ct == "" {
		ct = "text/xml; charset=utf-8"
	}
	req.Header.Set("Content-Type", ct)
	if !opts.omitAction {
		action := opts.action
		if action == "" {
			action = SOAPActionQuoted
		}
		req.Header.Set(SOAPActionHeader, action)
	}
	return req
}

// soapFault is the decoded fault, matched on (namespace URI, local name) so the
// test parses the document the way a client must.
type soapFault struct {
	FaultCode     string `xml:"faultcode"`
	FaultString   string `xml:"faultstring"`
	Code          string `xml:"detail>FinRefFault>Code"`
	Severity      string `xml:"detail>FinRefFault>Severity"`
	RetryAfter    int    `xml:"detail>FinRefFault>RetryAfterSeconds"`
	Element       string `xml:"detail>FinRefFault>Element"`
	CorrelationID string `xml:"detail>FinRefFault>CorrelationId"`
}

// decodeFault decodes a fault envelope. It sets a CharsetReader, because the
// document declares ISO-8859-1 in its prolog and carries no HTTP charset: that is
// exactly the handling the challenge asks a client for.
func decodeFault(t *testing.T, body []byte) soapFault {
	t.Helper()
	var env struct {
		Fault soapFault `xml:"Body>Fault"`
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = latin1Reader
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode fault: %v (body %s)", err, string(body))
	}
	return env.Fault
}

// latin1Reader decodes ISO-8859-1 request bytes for encoding/xml.
func latin1Reader(charset string, input io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(charset) {
	case "iso-8859-1", "windows-1252":
		return strings.NewReader(seed.DecodeCP1252(raw)), nil
	}
	return bytes.NewReader(raw), nil
}

// TestSOAPFaultMatrix walks every fault the channel can produce, with its exact
// HTTP status, fault code, ERP-FX code and severity.
//
// The statuses are the point. A business refusal is HTTP 200 carrying a fault, so
// a client that only reads status codes treats a refusal as a success; an absent
// SOAPAction is a 500, because it never reached the operation; and the SOAP 1.2
// media type is a bare 415 with no fault at all.
func TestSOAPFaultMatrix(t *testing.T) {
	tests := []struct {
		name       string
		opts       soapRequestOptions
		wantStatus int
		wantFault  string
		wantCode   string
		wantSever  string
		wantNoBody bool
	}{
		{
			name:       "SOAP 1.2 media type is a bare 415",
			opts:       soapRequestOptions{contentType: "application/soap+xml; charset=utf-8"},
			wantStatus: http.StatusUnsupportedMediaType,
			wantNoBody: true,
		},
		{
			name:       "a non-UTF-8 charset is a bare 415",
			opts:       soapRequestOptions{contentType: "text/xml; charset=iso-8859-1"},
			wantStatus: http.StatusUnsupportedMediaType,
			wantNoBody: true,
		},
		{
			name:       "an absent SOAPAction is a server fault with HTTP 500",
			opts:       soapRequestOptions{omitAction: true},
			wantStatus: http.StatusInternalServerError,
			wantFault:  "S:Server",
			wantCode:   FaultActionMissing,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "an unquoted SOAPAction is a client fault with HTTP 200",
			opts:       soapRequestOptions{action: "urn:blp-erp:finref:1.0/GetExchangeRateTable"},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultBadAction,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "a wrong SOAPAction is a client fault with HTTP 200",
			opts:       soapRequestOptions{action: `"urn:blp-erp:finref:1.0/GetSomethingElse"`},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultBadAction,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "an unknown mustUnderstand header is soap:MustUnderstand",
			opts:       soapRequestOptions{extraHeader: `<x:Tracing xmlns:x="urn:example:tracing" soap:mustUnderstand="1">on</x:Tracing>`},
			wantStatus: http.StatusOK,
			wantFault:  "S:MustUnderstand",
			wantCode:   FaultMustUnderstand,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "a wrong password is ERP-FX-401",
			opts:       soapRequestOptions{password: "not-the-password"},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultUnauthorized,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "an absent UsernameToken is ERP-FX-401",
			opts:       soapRequestOptions{omitSecurity: true},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultUnauthorized,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "an absent RequestContext is ERP-FX-400",
			opts:       soapRequestOptions{omitContext: true},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultMissingElement,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "an empty CorrelationId is ERP-FX-400",
			opts:       soapRequestOptions{correlationID: "   "},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultMissingElement,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "an absent CompanyCode is ERP-FX-400",
			opts:       soapRequestOptions{omitCompany: true},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultMissingElement,
			wantSever:  SeverityPermanent,
		},
		{
			name:       "CH20 has no rate table and always faults permanently",
			opts:       soapRequestOptions{companyCode: model.CompanyCodeCH20},
			wantStatus: http.StatusOK,
			wantFault:  "S:Client",
			wantCode:   FaultNoRateTable,
			wantSever:  SeverityPermanent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			rec := h.do(h.soapRequest(tc.opts))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %.200s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantNoBody {
				if rec.Body.Len() != 0 {
					t.Fatalf("a bare 415 must carry no body, got %.200s", rec.Body.String())
				}
				return
			}
			if got := rec.Header().Get("Content-Type"); got != SOAPContentType {
				t.Fatalf("Content-Type %q, want %q with no charset parameter", got, SOAPContentType)
			}
			fault := decodeFault(t, rec.Body.Bytes())
			if fault.FaultCode != tc.wantFault {
				t.Fatalf("faultcode %q, want %q", fault.FaultCode, tc.wantFault)
			}
			if fault.Code != tc.wantCode {
				t.Fatalf("fault code %q, want %q", fault.Code, tc.wantCode)
			}
			if fault.Severity != tc.wantSever {
				t.Fatalf("severity %q, want %q", fault.Severity, tc.wantSever)
			}
			if fault.RetryAfter != 0 {
				t.Fatalf("a permanent fault must not advertise a retry, got %d", fault.RetryAfter)
			}
		})
	}
}

// TestSOAPCH20FaultIsPermanentOnEveryAttempt asserts retrying the CH20 fault never
// helps. A candidate who retries it is making the graded mistake, and the mock must
// not reward it by eventually answering.
func TestSOAPCH20FaultIsPermanentOnEveryAttempt(t *testing.T) {
	h := newHarness(t, nil)
	for attempt := 1; attempt <= 4; attempt++ {
		rec := h.do(h.soapRequest(soapRequestOptions{companyCode: model.CompanyCodeCH20}))
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status %d, want 200", attempt, rec.Code)
		}
		fault := decodeFault(t, rec.Body.Bytes())
		if fault.Code != FaultNoRateTable || fault.Severity != SeverityPermanent {
			t.Fatalf("attempt %d: %s/%s, want %s/%s",
				attempt, fault.Code, fault.Severity, FaultNoRateTable, SeverityPermanent)
		}
	}
}

// TestSOAPRetryableFaultOnFirstDeliveryThenSuccess asserts the retryable fault and
// its content addressing: the first delivery of the CH10 signature faults
// ERP-FX-503 with RetryAfterSeconds, the retry of the same logical request
// succeeds, and the CH20 signature is a different logical request with its own
// first delivery.
func TestSOAPRetryableFaultOnFirstDeliveryThenSuccess(t *testing.T) {
	h := newHarness(t, nil)
	first := h.do(h.soapRequest(soapRequestOptions{}))
	if first.Code != http.StatusOK {
		t.Fatalf("first call: status %d, want 200", first.Code)
	}
	fault := decodeFault(t, first.Body.Bytes())
	if fault.Code != FaultTemporary {
		t.Fatalf("first call: fault %q, want %q", fault.Code, FaultTemporary)
	}
	if fault.Severity != SeverityRetryable {
		t.Fatalf("first call: severity %q, want %q", fault.Severity, SeverityRetryable)
	}
	if fault.RetryAfter != RetryAfterSeconds {
		t.Fatalf("first call: RetryAfterSeconds %d, want %d", fault.RetryAfter, RetryAfterSeconds)
	}
	if fault.CorrelationID != "run_7f3c1a" {
		t.Fatalf("first call: correlation id %q, want the request's own", fault.CorrelationID)
	}

	second := h.do(h.soapRequest(soapRequestOptions{}))
	if second.Code != http.StatusOK {
		t.Fatalf("retry: status %d, want 200", second.Code)
	}
	if bytes.Contains(second.Body.Bytes(), []byte("Fault")) {
		t.Fatalf("the retry of the same logical request must succeed, got %.300s", second.Body.String())
	}

	// A different correlation id is the same logical request: the signature is
	// the operation and the company code, and nothing else.
	third := h.do(h.soapRequest(soapRequestOptions{correlationID: "run_other"}))
	if bytes.Contains(third.Body.Bytes(), []byte("Fault")) {
		t.Fatal("the signature must not depend on the correlation id")
	}
}

// TestSOAPResponseHeaderByteShape asserts the exact bytes of the response header
// block. It is a byte assertion on purpose: the prefixes, the mid-document
// namespace declarations and the member order are the contract a client parses,
// and a refactor that "tidied" them would silently change what candidates see.
func TestSOAPResponseHeaderByteShape(t *testing.T) {
	h := newHarness(t, nil)
	h.do(h.soapRequest(soapRequestOptions{})) // spend the retryable first delivery
	rec := h.do(h.soapRequest(soapRequestOptions{}))
	body := rec.Body.String()

	wantPrologue := `<?xml version="1.0" encoding="ISO-8859-1"?>` + "\n" +
		`<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">` + "\n"
	if !strings.HasPrefix(body, wantPrologue) {
		t.Fatalf("prologue is\n%.120q\nwant\n%.120q", body, wantPrologue)
	}

	rows, _ := h.srv.soapRows()
	wantHeader := "  <S:Header>\n" +
		`    <ResponseContext xmlns="urn:blp-erp:finref:1.0">` + "\n" +
		`      <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0">run_7f3c1a</ns2:CorrelationId>` + "\n" +
		`      <ns2:SnapshotToken xmlns:ns2="urn:blp-erp:common:1.0">` +
		snapshotToken(seed.ScenarioS0, testSeed, model.CompanyCodeCH10, len(rows)) + "</ns2:SnapshotToken>\n" +
		fmt.Sprintf("      <RowCount>%d</RowCount>\n", len(rows)) +
		"      <Truncated>false</Truncated>\n" +
		"    </ResponseContext>\n" +
		"  </S:Header>\n"
	if !strings.Contains(body, wantHeader) {
		t.Fatalf("response header block is not the documented bytes.\nwant:\n%s\ngot:\n%.900s", wantHeader, body)
	}
	if got := rec.Header().Get("Content-Type"); got != SOAPContentType {
		t.Fatalf("Content-Type %q, want %q: the encoding is declared in the prolog and nowhere else",
			got, SOAPContentType)
	}
}

// TestSOAPResponseIsLatin1 asserts the provider name really is Latin-1 bytes and
// not UTF-8: the byte 0xFC stands alone, and the two byte UTF-8 sequence for the
// same character is absent.
func TestSOAPResponseIsLatin1(t *testing.T) {
	h := newHarness(t, nil)
	h.do(h.soapRequest(soapRequestOptions{}))
	rec := h.do(h.soapRequest(soapRequestOptions{}))
	body := rec.Body.Bytes()

	if !bytes.Contains(body, []byte{'Z', 0xFC, 'r', 'c', 'h', 'e', 'r'}) {
		t.Fatal("RateProvider must carry Zürcher in ISO-8859-1 bytes")
	}
	if bytes.Contains(body, []byte("Zürcher")) {
		t.Fatal("RateProvider is UTF-8: the channel promises ISO-8859-1")
	}
	if decoded := seed.DecodeCP1252(body); !strings.Contains(decoded, seed.RateProvider) {
		t.Fatalf("the decoded document does not carry %q", seed.RateProvider)
	}
}

// namespacedRow is one rate row parsed by (namespace URI, local name) only. No
// prefix appears anywhere in these tags, which is the whole point: the server's
// prefixes are its own business.
type namespacedRow struct {
	Sequence     int    `xml:"Sequence,attr"`
	CurrencyFrom string `xml:"urn:blp-erp:common:1.0 CurrencyFrom"`
	CurrencyTo   string `xml:"urn:blp-erp:common:1.0 CurrencyTo"`
	RateType     string `xml:"urn:blp-erp:finref:1.0 RateType"`
	Rate         string `xml:"urn:blp-erp:finref:1.0 Rate"`
	RateFactor   int64  `xml:"urn:blp-erp:finref:1.0 RateFactorFrom"`
	ValidFrom    string `xml:"urn:blp-erp:finref:1.0 ValidFrom"`
	ValidTo      struct {
		Nil   string `xml:"http://www.w3.org/2001/XMLSchema-instance nil,attr"`
		Value string `xml:",chardata"`
	} `xml:"urn:blp-erp:finref:1.0 ValidTo"`
	Status  string `xml:"urn:blp-erp:finref:1.0 Status"`
	Comment struct {
		Nil   string `xml:"http://www.w3.org/2001/XMLSchema-instance nil,attr"`
		Value string `xml:",chardata"`
	} `xml:"urn:blp-erp:finref:1.0 Comment"`
}

// namespacedResponse is the whole response parsed by namespace URI.
type namespacedResponse struct {
	CorrelationID string          `xml:"Header>ResponseContext>CorrelationId"`
	SnapshotToken string          `xml:"Header>ResponseContext>SnapshotToken"`
	RowCount      int             `xml:"Header>ResponseContext>RowCount"`
	Truncated     bool            `xml:"Header>ResponseContext>Truncated"`
	CompanyCode   string          `xml:"Body>GetExchangeRateTableResponse>CompanyCode"`
	RateProvider  string          `xml:"Body>GetExchangeRateTableResponse>RateProvider"`
	Rows          []namespacedRow `xml:"Body>GetExchangeRateTableResponse>ExchangeRateTable>ExchangeRate"`
}

// TestSOAPNamespaceParseFindsEveryRow asserts a parse that matches on namespace
// URIs finds every row and every graded property of it: the host locale rate, the
// per-unit factor, the xsi:nil ValidTo, the xsi:nil Comment, the DELETED rows, the
// MONTHLY_AVG rows and the Sequence attribute.
func TestSOAPNamespaceParseFindsEveryRow(t *testing.T) {
	h := newHarness(t, nil)
	h.do(h.soapRequest(soapRequestOptions{}))
	rec := h.do(h.soapRequest(soapRequestOptions{}))

	var resp namespacedResponse
	dec := xml.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.CharsetReader = latin1Reader
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("namespace-based parse failed: %v", err)
	}
	want, truncated := h.srv.soapRows()
	if len(resp.Rows) != len(want) {
		t.Fatalf("parsed %d rows, want %d", len(resp.Rows), len(want))
	}
	if resp.RowCount != len(want) || resp.Truncated != truncated {
		t.Fatalf("header says RowCount=%d Truncated=%v, want %d and %v",
			resp.RowCount, resp.Truncated, len(want), truncated)
	}
	if resp.CorrelationID != "run_7f3c1a" || resp.CompanyCode != model.CompanyCodeCH10 {
		t.Fatalf("header echoes %q / %q", resp.CorrelationID, resp.CompanyCode)
	}
	if resp.RateProvider != seed.RateProvider {
		t.Fatalf("RateProvider %q, want %q", resp.RateProvider, seed.RateProvider)
	}

	sawNilValidTo, sawNilComment, sawDeleted, sawMonthly, sawFactor100, sawPretty := false, false, false, false, false, false
	for i, got := range resp.Rows {
		src := want[i]
		if got.Sequence != src.Sequence {
			t.Fatalf("row %d: Sequence %d, want %d", i, got.Sequence, src.Sequence)
		}
		if got.CurrencyFrom != src.Base || got.CurrencyTo != src.Quote {
			t.Fatalf("row %d: %s/%s, want %s/%s", i, got.CurrencyFrom, got.CurrencyTo, src.Base, src.Quote)
		}
		if strings.TrimSpace(got.Rate) != src.RateLiteral {
			t.Fatalf("row %d: rate %q, want the host locale literal %q", i, got.Rate, src.RateLiteral)
		}
		if got.Rate != src.RateLiteral {
			sawPretty = true
		}
		if got.RateFactor != src.RateFactor {
			t.Fatalf("row %d: RateFactorFrom %d, want %d", i, got.RateFactor, src.RateFactor)
		}
		if got.RateFactor == 100 {
			sawFactor100 = true
		}
		if src.ValidToNil {
			if got.ValidTo.Nil != "true" {
				t.Fatalf("row %d: an open-ended row must carry xsi:nil, got %q", i, got.ValidTo.Nil)
			}
			sawNilValidTo = true
		} else if got.ValidTo.Value != src.ValidTo {
			t.Fatalf("row %d: ValidTo %q, want %q", i, got.ValidTo.Value, src.ValidTo)
		}
		if src.CommentNil {
			if got.Comment.Nil != "true" {
				t.Fatalf("row %d: a nil comment must carry xsi:nil", i)
			}
			sawNilComment = true
		}
		if got.Status == model.FxStatusDeleted {
			sawDeleted = true
		}
		if got.RateType == model.RateTypeMonthlyAvg {
			sawMonthly = true
		}
	}
	for name, saw := range map[string]bool{
		"an xsi:nil ValidTo":      sawNilValidTo,
		"an xsi:nil Comment":      sawNilComment,
		"a DELETED row":           sawDeleted,
		"a MONTHLY_AVG row":       sawMonthly,
		"a RateFactorFrom of 100": sawFactor100,
		"a pretty-printed value":  sawPretty,
	} {
		if !saw {
			t.Fatalf("the served table is missing %s, which is a graded property of this channel", name)
		}
	}
}

// TestSOAPTruncatedTable asserts the truncation knob: the table is cut, the header
// says so, and the meaning lives in the header rather than in the row count.
func TestSOAPTruncatedTable(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.SOAPTruncateAt = 25 })
	h.do(h.soapRequest(soapRequestOptions{}))
	rec := h.do(h.soapRequest(soapRequestOptions{}))
	var resp namespacedResponse
	dec := xml.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.CharsetReader = latin1Reader
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !resp.Truncated {
		t.Fatal("a cut table must report Truncated=true")
	}
	if resp.RowCount != 25 || len(resp.Rows) != 25 {
		t.Fatalf("RowCount=%d rows=%d, want 25", resp.RowCount, len(resp.Rows))
	}
}

// TestSOAPIgnoresRateTypeValidFromValidTo asserts the documented v1.0 backward
// compatibility: the three filter elements are accepted and ignored, and the full
// rolling window comes back whatever they say.
func TestSOAPIgnoresRateTypeValidFromValidTo(t *testing.T) {
	h := newHarness(t, nil)
	h.do(h.soapRequest(soapRequestOptions{}))
	rec := h.do(h.soapRequest(soapRequestOptions{}))
	rows, _ := h.srv.soapRows()
	var resp namespacedResponse
	dec := xml.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.CharsetReader = latin1Reader
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Rows) != len(rows) {
		t.Fatalf("a request asking for DAILY rows in March returned %d of %d rows: the filters are "+
			"documented as ignored", len(resp.Rows), len(rows))
	}
}

// TestSOAPCallLog asserts the admin call log records every call with its outcome,
// which is the evidence a debrief is argued from.
func TestSOAPCallLog(t *testing.T) {
	h := newHarness(t, nil)
	h.do(h.soapRequest(soapRequestOptions{}))
	h.do(h.soapRequest(soapRequestOptions{}))
	h.do(h.soapRequest(soapRequestOptions{companyCode: model.CompanyCodeCH20}))
	h.do(h.soapRequest(soapRequestOptions{contentType: "application/soap+xml"}))

	rec := h.adminGet(AdminPathSOAPCalls)
	var log AdminSOAPCalls
	if err := decodeJSON(rec.Body.Bytes(), &log); err != nil {
		t.Fatalf("decode call log: %v", err)
	}
	if log.Count != 4 {
		t.Fatalf("%d calls logged, want 4", log.Count)
	}
	want := []struct {
		status int
		code   string
	}{
		{http.StatusOK, FaultTemporary},
		{http.StatusOK, ""},
		{http.StatusOK, FaultNoRateTable},
		{http.StatusUnsupportedMediaType, ""},
	}
	for i, w := range want {
		got := log.Calls[i]
		if got.Sequence != i+1 || got.HTTPStatus != w.status || got.FaultCode != w.code {
			t.Fatalf("call %d is %+v, want status %d and fault %q", i+1, got, w.status, w.code)
		}
	}
	if log.Calls[1].RowCount == 0 {
		t.Fatal("the successful call must record its row count")
	}
	m := h.metrics()
	if m.SOAPCalls != 4 {
		t.Fatalf("metrics soap_calls %d, want 4", m.SOAPCalls)
	}
	if m.SOAPFaults[FaultNoRateTable] != 1 || m.SOAPFaults[FaultTemporary] != 1 {
		t.Fatalf("metrics soap_faults %v", m.SOAPFaults)
	}
}

// TestSOAPRejectsANonLatin1CorrelationID asserts the channel refuses an id it
// would have to mangle, rather than echoing a broken byte sequence into an
// ISO-8859-1 document.
func TestSOAPRejectsANonLatin1CorrelationID(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.do(h.soapRequest(soapRequestOptions{correlationID: "run_中文"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	fault := decodeFault(t, rec.Body.Bytes())
	if fault.Code != FaultMissingElement {
		t.Fatalf("fault %q, want %q", fault.Code, FaultMissingElement)
	}
}

// TestSOAPRejectsSOAP12Envelope asserts SOAP 1.1 is the contract: a SOAP 1.2
// envelope sent with the right media type is a fault, not a silent success.
func TestSOAPRejectsSOAP12Envelope(t *testing.T) {
	h := newHarness(t, nil)
	body := `<?xml version="1.0" encoding="UTF-8"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope">
  <env:Body><GetExchangeRateTable xmlns="urn:blp-erp:finref:1.0">
    <CompanyCode>CH10</CompanyCode></GetExchangeRateTable></env:Body>
</env:Envelope>`
	req := httptest.NewRequest(http.MethodPost, RouteSOAP, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set(SOAPActionHeader, SOAPActionQuoted)
	rec := h.do(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := decodeFault(t, rec.Body.Bytes()).Code; got != FaultBadEnvelope {
		t.Fatalf("fault %q, want %q", got, FaultBadEnvelope)
	}
}

// TestWSDL asserts the service description is served, is well formed and declares
// the quoted SOAPAction a client has to send.
func TestWSDL(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.get(RouteSOAP + "?wsdl")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var doc struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the WSDL is not well formed: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`soapAction="urn:blp-erp:finref:1.0/GetExchangeRateTable"`,
		`targetNamespace="` + NSFinRef + `"`,
		NSCommon,
		"RateFactorFrom",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the WSDL does not mention %q", want)
		}
	}
	if plain := h.get(RouteSOAP); plain.Code != http.StatusNotFound {
		t.Fatalf("a GET without ?wsdl: status %d, want 404", plain.Code)
	}
}
