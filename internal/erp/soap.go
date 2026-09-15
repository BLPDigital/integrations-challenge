package erp

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/template"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// Namespace URIs of the SOAP channel. A client must match on (namespace URI,
// local name): the prefixes in the response deliberately differ from the ones in
// the documentation, because a parser coupled to prefix strings is the single
// most common way a first SOAP integration breaks in production.
const (
	// NSSoapEnv is the SOAP 1.1 envelope namespace. SOAP 1.2 is not served.
	NSSoapEnv = "http://schemas.xmlsoap.org/soap/envelope/"
	// NSFinRef is the service namespace, documented with the prefix fin: and
	// served as the DEFAULT namespace of the payload.
	NSFinRef = "urn:blp-erp:finref:1.0"
	// NSCommon is the common types namespace, documented with the prefix cmn:
	// and served as ns2:, declared mid-document on individual children.
	NSCommon = "urn:blp-erp:common:1.0"
	// NSWSSE is the WS-Security secext namespace of the UsernameToken.
	NSWSSE = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	// NSXSI is the XML Schema instance namespace, which carries xsi:nil.
	NSXSI = "http://www.w3.org/2001/XMLSchema-instance"
)

// SOAPActionHeader and SOAPActionQuoted are the required action header. The
// value carries the double quotes, exactly as SOAP 1.1 specifies and exactly as
// the transcript in the documentation shows.
const (
	SOAPActionHeader = "SOAPAction"
	SOAPActionQuoted = `"urn:blp-erp:finref:1.0/GetExchangeRateTable"`
	// SOAPOperation is the local name of the one operation this service serves.
	SOAPOperation = "GetExchangeRateTable"
)

// SOAPContentType is the media type the channel requires on a request and the
// media type it answers with. The response carries NO charset parameter and
// declares ISO-8859-1 in the XML prolog instead, which is how a great many real
// ERP endpoints behave and why an XML parser must be fed bytes rather than a
// pre-decoded string.
const SOAPContentType = "text/xml"

// Fault codes of the SOAP channel.
//
// The first five are the published set of section 7.3. The last three are
// additions this implementation needs and documents: the specification fixes the
// HTTP status of a missing and of a wrong SOAPAction without naming a code, and
// an unparseable envelope has to say something.
const (
	// FaultNoRateTable is PERMANENT: the company code has no rate table
	// configured. CompanyCode CH20 always faults with it, and retrying a
	// permanent fault is a graded mistake.
	FaultNoRateTable = "ERP-FX-014"
	// FaultTemporary is RETRYABLE and carries RetryAfterSeconds. The first
	// delivery of a company code's rate table request faults with it and the
	// retry succeeds.
	FaultTemporary = "ERP-FX-503"
	// FaultMissingElement reports a mandatory element that is absent or empty,
	// naming the element in the fault detail. The published case is a missing
	// cmn:CorrelationId in fin:RequestContext.
	FaultMissingElement = "ERP-FX-400"
	// FaultUnauthorized reports a missing or wrong wsse:UsernameToken.
	FaultUnauthorized = "ERP-FX-401"
	// FaultMustUnderstand reports a header this service does not understand
	// that carried soap:mustUnderstand="1".
	FaultMustUnderstand = "ERP-FX-402"
	// FaultBadAction reports a SOAPAction that is present but wrong or
	// unquoted. HTTP 200 with a fault, per section 7.3.
	FaultBadAction = "ERP-FX-403"
	// FaultBadEnvelope reports an envelope that is not SOAP 1.1, not well
	// formed, or carries no operation this service serves.
	FaultBadEnvelope = "ERP-FX-410"
	// FaultActionMissing reports an absent SOAPAction header. HTTP 500 with a
	// fault, per section 7.3: an absent action is a transport-level defect and
	// the channel says so with a server fault.
	FaultActionMissing = "ERP-FX-500"
)

// Fault severities. They are machine readable and authoritative in exactly the
// way retriable is on the REST surface: retrying a PERMANENT fault is a mistake
// the grader scores.
const (
	// SeverityPermanent means the request will fault the same way forever.
	SeverityPermanent = "PERMANENT"
	// SeverityRetryable means the same request will succeed on a later attempt.
	SeverityRetryable = "RETRYABLE"
)

// RetryAfterSeconds is the delay the retryable fault advertises.
const RetryAfterSeconds = 2

// A SOAPCall is one entry of the SOAP call log, served by
// GET /erp-admin/v1/soap-calls. It is how a debrief shows that a candidate
// retried a permanent fault, or never retried a retryable one.
type SOAPCall struct {
	// Sequence counts SOAP requests in arrival order. It is a log ordinal and
	// nothing depends on it: the retryable fault is addressed by signature, not
	// by sequence.
	Sequence int `json:"sequence"`
	// Signature is the content-addressed signature of the logical call.
	Signature string `json:"signature"`
	// CompanyCode is the requested company code, empty when the request never
	// got far enough to name one.
	CompanyCode string `json:"company_code"`
	// CorrelationID is the echoed correlation id.
	CorrelationID string `json:"correlation_id"`
	// HTTPStatus is the status the client saw: 200 for a successful table and
	// for a business fault alike, 415 for the wrong media type, 500 for an
	// absent SOAPAction.
	HTTPStatus int `json:"http_status"`
	// FaultCode is the ERP-FX code when this call faulted, empty on success.
	FaultCode string `json:"fault_code"`
	// Severity is the fault severity, empty on success.
	Severity string `json:"severity"`
	// RowCount is the number of rate rows served, and Truncated whether the
	// table was cut.
	RowCount  int  `json:"row_count"`
	Truncated bool `json:"truncated"`
}

// soapSignature is the content-addressed signature of a rate table call:
// the operation and the company code, and nothing else. RateType, ValidFrom and
// ValidTo are accepted and ignored, so they are not part of the logical identity
// of the request either.
func soapSignature(companyCode string) string {
	return "SOAP " + SOAPOperation + "|" + companyCode
}

// soapCompanyCode returns the company code of a SOAP request body, or an empty
// string when the body cannot be parsed. It buffers the body so the handler can
// still read it, and it is called by the classifier before the Governor sees the
// request.
func soapCompanyCode(r *http.Request) string {
	body, err := httpx.ReadBody(r)
	if err != nil {
		return ""
	}
	env, err := parseSOAPRequest(body)
	if err != nil || env.Body == nil || env.Body.Request == nil {
		return ""
	}
	return strings.TrimSpace(env.Body.Request.CompanyCode)
}

// soapRowCount is the number of rows a company code's rate table would carry. It
// feeds the per-row component of the SOAP virtual cost and records_returned.
//
// A company code with no rate table costs the base only. The retryable first
// delivery is charged as though it had succeeded, because the classifier must not
// consume the attempt it is trying to describe.
func (s *Server) soapRowCount(companyCode string) int {
	if companyCode != model.CompanyCodeCH10 {
		return 0
	}
	rows, _ := s.soapRows()
	return len(rows)
}

// soapRows returns the rate rows to serve and whether the table was truncated.
// The rows keep the generator's document order, which is deliberately not
// validity order: a superseded row sits after its winner for one currency and
// before it for another, so neither first-wins nor last-wins is right.
func (s *Server) soapRows() ([]seed.FxRow, bool) {
	d, _ := s.state()
	rows := d.set.FxRows
	cut := s.cfg.SOAPTruncateAt
	if cut > 0 && cut < len(rows) {
		return rows[:cut], true
	}
	return rows, false
}

// handleSOAP serves POST /soap/FinancialReferenceDataService.
//
// The checks are in a fixed order, and the order is the contract:
//
//  1. Content-Type must be text/xml. application/soap+xml is a bare 415 with no
//     fault at all, because a SOAP 1.2 media type never reached the SOAP 1.1
//     stack that would have produced one.
//  2. SOAPAction: absent is HTTP 500 with a server fault, present but wrong or
//     unquoted is HTTP 200 with a client fault.
//  3. the envelope must be SOAP 1.1 and well formed,
//  4. a header this service does not understand carrying mustUnderstand="1" is
//     soap:MustUnderstand, which is what the SOAP specification requires before
//     the body is looked at,
//  5. the wsse:UsernameToken must match,
//  6. fin:RequestContext must carry a non-empty cmn:CorrelationId,
//  7. the operation must be GetExchangeRateTable and must name a company code,
//  8. CompanyCode is honored; RateType, ValidFrom and ValidTo are accepted and
//     ignored, documented as v1.0 backward compatibility, and the full rolling
//     window always comes back.
func (s *Server) handleSOAP(w http.ResponseWriter, r *http.Request) {
	if !soapContentTypeOK(r.Header.Get("Content-Type")) {
		// A bare 415: no fault body, no diagnostics. This is what the real
		// endpoint does and the reason the challenge documents the media type.
		s.logSOAP(SOAPCall{HTTPStatus: http.StatusUnsupportedMediaType})
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if _, present := r.Header[http.CanonicalHeaderKey(SOAPActionHeader)]; !present {
		s.writeFault(w, r, http.StatusInternalServerError, "S:Server",
			"SOAPAction header missing", FaultActionMissing, SeverityPermanent, "", "", "")
		return
	}
	if r.Header.Get(SOAPActionHeader) != SOAPActionQuoted {
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"SOAPAction not supported by this endpoint; the action must be quoted exactly as documented",
			FaultBadAction, SeverityPermanent, "", "", "")
		return
	}

	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	env, perr := parseSOAPRequest(body)
	if perr != nil {
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"request envelope is not a SOAP 1.1 envelope this service understands",
			FaultBadEnvelope, SeverityPermanent, "", "", "")
		return
	}
	if name := env.unknownMustUnderstand(); name != "" {
		s.writeFault(w, r, http.StatusOK, "S:MustUnderstand",
			"header "+name+" carries mustUnderstand but is not understood by this service",
			FaultMustUnderstand, SeverityPermanent, name, "", "")
		return
	}

	creds := s.Credentials()
	if !env.credentialsMatch(creds) {
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"wsse:UsernameToken missing or not recognized",
			FaultUnauthorized, SeverityPermanent, "", "", "")
		return
	}

	correlation := env.correlationID()
	switch {
	case correlation == "":
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"fin:RequestContext must carry a non-empty cmn:CorrelationId",
			FaultMissingElement, SeverityPermanent, "cmn:CorrelationId", "", "")
		return
	case !isLatin1Printable(correlation):
		// The response is ISO-8859-1 and the correlation id is echoed into it,
		// so an id this channel cannot represent is refused rather than mangled.
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"cmn:CorrelationId carries characters this ISO-8859-1 channel cannot represent",
			FaultMissingElement, SeverityPermanent, "cmn:CorrelationId", "", "")
		return
	}

	if env.Body == nil || env.Body.Request == nil {
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"the request body carries no operation this service serves",
			FaultBadEnvelope, SeverityPermanent, "", "", correlation)
		return
	}
	company := strings.TrimSpace(env.Body.Request.CompanyCode)
	if company == "" {
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"CompanyCode is mandatory", FaultMissingElement, SeverityPermanent,
			"CompanyCode", "", correlation)
		return
	}

	signature := soapSignature(company)
	if company != model.CompanyCodeCH10 {
		// PERMANENT, always, for every other company code: that subsidiary has
		// no rate table configured, and no number of retries will configure one.
		s.writeFault(w, r, http.StatusOK, "S:Client",
			"no exchange rate table is configured for this company code",
			FaultNoRateTable, SeverityPermanent, "CompanyCode", company, correlation)
		return
	}
	if s.soapFirstDelivery(signature) {
		// RETRYABLE, once per logical request. The trigger is the signature's
		// first delivery, exactly like the Governor's content-addressed
		// injection: it is not a count of SOAP requests, so a client that issues
		// its calls in another order, or in parallel, meets the same fault.
		s.writeFault(w, r, http.StatusOK, "S:Server",
			"the exchange rate snapshot is being rebuilt; repeat the request",
			FaultTemporary, SeverityRetryable, "", company, correlation)
		return
	}

	rows, truncated := s.soapRows()
	payload, err := s.renderRateTable(company, correlation, rows, truncated)
	if err != nil {
		httpx.Fail(w, httpx.Internal("rate table could not be serialized"))
		return
	}
	s.logSOAP(SOAPCall{
		Signature:     signature,
		CompanyCode:   company,
		CorrelationID: correlation,
		HTTPStatus:    http.StatusOK,
		RowCount:      len(rows),
		Truncated:     truncated,
	})
	writeSOAP(w, http.StatusOK, payload)
}

// soapContentTypeOK reports whether a request media type is acceptable: text/xml
// with either no charset or a UTF-8 charset. application/soap+xml is SOAP 1.2's
// media type and is refused with a bare 415.
func soapContentTypeOK(contentType string) bool {
	mt := contentType
	params := ""
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		mt, params = contentType[:i], contentType[i+1:]
	}
	if !strings.EqualFold(strings.TrimSpace(mt), SOAPContentType) {
		return false
	}
	for _, part := range strings.Split(params, ";") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 || !strings.EqualFold(strings.TrimSpace(part[:eq]), "charset") {
			continue
		}
		cs := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
		if !strings.EqualFold(cs, "utf-8") && !strings.EqualFold(cs, "utf8") {
			return false
		}
	}
	return true
}

// soapFirstDelivery reports whether sig is being delivered for the first time in
// this run and records the delivery.
//
// The counter is per signature, never per request: that is what makes the
// retryable fault content addressed. A retry of the same logical call is the
// signature's second delivery and succeeds, which is the fairness guarantee
// behind "an injected fault always succeeds on the retry".
func (s *Server) soapFirstDelivery(sig string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.st.soapAttempts[sig]
	s.st.soapAttempts[sig] = attempt + 1
	return attempt == 0
}

// logSOAP appends one entry to the SOAP call log.
func (s *Server) logSOAP(call SOAPCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call.Sequence = len(s.st.soapCalls) + 1
	s.st.soapCalls = append(s.st.soapCalls, call)
}

// writeSOAP writes an already encoded SOAP document.
//
// Content-Type is text/xml with no charset parameter: the encoding is declared in
// the XML prolog and nowhere else, which is exactly the situation a client must
// handle by handing its parser bytes.
func writeSOAP(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", SOAPContentType)
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// writeFault renders and writes a SOAP fault and logs the call.
//
// A business fault is HTTP 200 carrying soap:Fault. That is not a quirk: in SOAP
// 1.1 the fault IS the answer, and a client that only looks at the status code
// will happily treat a refusal as a success.
func (s *Server) writeFault(w http.ResponseWriter, r *http.Request, status int,
	faultCode, faultString, code, severity, element, company, correlation string) {
	data := soapFaultData{
		FaultCode:     xmlEscape(faultCode),
		FaultString:   xmlEscape(faultString),
		Code:          xmlEscape(code),
		Severity:      xmlEscape(severity),
		Element:       xmlEscape(element),
		CorrelationID: xmlEscape(correlation),
	}
	if severity == SeverityRetryable {
		data.RetryAfterSeconds = RetryAfterSeconds
	}
	payload, err := renderTemplate(faultTemplate, data)
	if err != nil {
		httpx.Fail(w, httpx.Internal("fault could not be serialized"))
		return
	}
	sig := ""
	if company != "" {
		sig = soapSignature(company)
	}
	s.logSOAP(SOAPCall{
		Signature:     sig,
		CompanyCode:   company,
		CorrelationID: correlation,
		HTTPStatus:    status,
		FaultCode:     code,
		Severity:      severity,
	})
	writeSOAP(w, status, payload)
}

// renderRateTable renders the successful response.
func (s *Server) renderRateTable(company, correlation string, rows []seed.FxRow, truncated bool) ([]byte, error) {
	d, _ := s.state()
	data := soapResponseData{
		CorrelationID: xmlEscape(correlation),
		SnapshotToken: snapshotToken(d.set.Scenario, d.set.Seed, company, len(rows)),
		RowCount:      len(rows),
		Truncated:     strconv.FormatBool(truncated),
		CompanyCode:   xmlEscape(company),
		RateProvider:  xmlEscape(seed.RateProvider),
		Rows:          make([]soapRowData, 0, len(rows)),
	}
	for _, row := range rows {
		data.Rows = append(data.Rows, soapRow(row))
	}
	return renderTemplate(rateTableTemplate, data)
}

// soapRow renders one rate row into its pre-encoded template shape.
//
// Everything a client could get wrong is deliberate here: the rate carries the
// ERP host locale (decimal comma), RateFactorFrom is per unit so JPY is quoted
// per 100 units, an open-ended ValidTo is xsi:nil rather than absent, one row's
// Comment is xsi:nil so a blanket nil-is-an-error rule fails on a perfectly good
// row, and one row's value is surrounded by pretty-printer whitespace so a
// client that does not trim fails on a correct number.
func soapRow(row seed.FxRow) soapRowData {
	out := soapRowData{
		Sequence:   row.Sequence,
		Base:       xmlEscape(row.Base),
		Quote:      xmlEscape(row.Quote),
		RateType:   xmlEscape(row.RateType),
		RateFactor: row.RateFactor,
		ValidFrom:  xmlEscape(row.ValidFrom),
		Status:     xmlEscape(row.Status),
	}
	rate := xmlEscape(row.RateLiteral)
	if row.PrettyPrinted {
		rate = "\n            " + rate + "\n          "
	}
	out.RateCell = rate
	if row.ValidToNil {
		out.ValidToElem = `<ValidTo xsi:nil="true"/>`
	} else {
		out.ValidToElem = "<ValidTo>" + xmlEscape(row.ValidTo) + "</ValidTo>"
	}
	if row.CommentNil {
		out.CommentElem = `<Comment xsi:nil="true"/>`
	} else {
		out.CommentElem = "<Comment>" + xmlEscape(row.Comment) + "</Comment>"
	}
	return out
}

// snapshotToken is the opaque snapshot identifier of a rate table response. It is
// derived from the scenario, the seed, the company code and the row count, so it
// is stable across runs and changes when the snapshot does. No clock, no counter.
func snapshotToken(scenario string, seedValue int64, company string, rows int) string {
	sum := sha256.Sum256([]byte("erp/soap-snapshot|" + scenario + "|" +
		strconv.FormatInt(seedValue, 10) + "|" + company + "|" + strconv.Itoa(rows)))
	return "snap-" + hex.EncodeToString(sum[:])[:16]
}

// isLatin1Printable reports whether every rune of s is printable in ISO-8859-1.
// The C1 range is excluded on purpose: those bytes are control characters in
// ISO-8859-1 and printable characters in Windows-1252, and a value whose meaning
// depends on which of the two a reader assumes has no place in an identifier.
func isLatin1Printable(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x20 && r <= 0x7e:
		case r >= 0xa0 && r <= 0xff:
		default:
			return false
		}
	}
	return true
}

// xmlEscape escapes text for an XML text node or attribute value. The templates
// are text templates, which escape nothing, so every value passed into one goes
// through here first.
func xmlEscape(s string) string {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}

// renderTemplate executes a template and encodes the result as the ISO-8859-1
// bytes the channel promises in its prolog.
//
// encoding/xml cannot produce this document: it cannot emit the S: envelope
// prefix with a default-namespace payload and ns2: declarations placed
// mid-document on individual children, and it cannot write anything but UTF-8.
// The document is therefore written by hand over pre-encoded values, which is
// also how the real endpoint we are imitating does it.
func renderTemplate(t *template.Template, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	out, err := seed.EncodeCP1252(buf.String())
	if err != nil {
		return nil, fmt.Errorf("erp: SOAP response is not representable in ISO-8859-1: %w", err)
	}
	return out, nil
}

// soapResponseData is the template shape of a successful response. Every string
// member is already XML escaped.
type soapResponseData struct {
	CorrelationID string
	SnapshotToken string
	RowCount      int
	Truncated     string
	CompanyCode   string
	RateProvider  string
	Rows          []soapRowData
}

// soapRowData is the template shape of one rate row. ValidToElem and CommentElem
// are whole pre-rendered elements, because the nil form is an empty element with
// an attribute and not a text value.
type soapRowData struct {
	Sequence    int
	Base        string
	Quote       string
	RateType    string
	RateCell    string
	RateFactor  int64
	ValidFrom   string
	ValidToElem string
	Status      string
	CommentElem string
}

// soapFaultData is the template shape of a fault.
type soapFaultData struct {
	FaultCode         string
	FaultString       string
	Code              string
	Severity          string
	Element           string
	RetryAfterSeconds int
	CorrelationID     string
}

// rateTableTemplate is the successful response, byte for byte.
//
// Read the prefixes: S: for the envelope where the documentation writes soap:,
// the payload in a default namespace where the documentation writes fin:, and
// ns2: for the common types, declared on each individual child rather than once
// at the top. A client that matches on prefixes parses nothing here; a client
// that matches on (namespace URI, local name) parses all of it.
var rateTableTemplate = template.Must(template.New("ratetable").Parse(
	`<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="` + NSSoapEnv + `">
  <S:Header>
    <ResponseContext xmlns="` + NSFinRef + `">
      <ns2:CorrelationId xmlns:ns2="` + NSCommon + `">{{.CorrelationID}}</ns2:CorrelationId>
      <ns2:SnapshotToken xmlns:ns2="` + NSCommon + `">{{.SnapshotToken}}</ns2:SnapshotToken>
      <RowCount>{{.RowCount}}</RowCount>
      <Truncated>{{.Truncated}}</Truncated>
    </ResponseContext>
  </S:Header>
  <S:Body>
    <GetExchangeRateTableResponse xmlns="` + NSFinRef + `" xmlns:xsi="` + NSXSI + `">
      <CompanyCode>{{.CompanyCode}}</CompanyCode>
      <RateProvider>{{.RateProvider}}</RateProvider>
      <ExchangeRateTable>
{{range .Rows}}        <ExchangeRate Sequence="{{.Sequence}}">
          <ns2:CurrencyFrom xmlns:ns2="` + NSCommon + `">{{.Base}}</ns2:CurrencyFrom>
          <ns2:CurrencyTo xmlns:ns2="` + NSCommon + `">{{.Quote}}</ns2:CurrencyTo>
          <RateType>{{.RateType}}</RateType>
          <Rate>{{.RateCell}}</Rate>
          <RateFactorFrom>{{.RateFactor}}</RateFactorFrom>
          <ValidFrom>{{.ValidFrom}}</ValidFrom>
          {{.ValidToElem}}
          <Status>{{.Status}}</Status>
          {{.CommentElem}}
        </ExchangeRate>
{{end}}      </ExchangeRateTable>
    </GetExchangeRateTableResponse>
  </S:Body>
</S:Envelope>
`))

// faultTemplate is the fault document. faultcode, faultstring and detail are
// unqualified, as SOAP 1.1 requires; the machine-readable code, the severity and
// the retry advice live inside detail, where a client can find them without
// parsing prose.
var faultTemplate = template.Must(template.New("fault").Parse(
	`<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="` + NSSoapEnv + `">
  <S:Body>
    <S:Fault>
      <faultcode>{{.FaultCode}}</faultcode>
      <faultstring>{{.FaultString}}</faultstring>
      <detail>
        <FinRefFault xmlns="` + NSFinRef + `">
          <ns2:Code xmlns:ns2="` + NSCommon + `">{{.Code}}</ns2:Code>
          <Severity>{{.Severity}}</Severity>
{{if .RetryAfterSeconds}}          <RetryAfterSeconds>{{.RetryAfterSeconds}}</RetryAfterSeconds>
{{end}}{{if .Element}}          <Element>{{.Element}}</Element>
{{end}}          <ns2:CorrelationId xmlns:ns2="` + NSCommon + `">{{.CorrelationID}}</ns2:CorrelationId>
        </FinRefFault>
      </detail>
    </S:Fault>
  </S:Body>
</S:Envelope>
`))

// soapEnvelope is the request envelope.
//
// The request parser is deliberately LENIENT where the response is strict: child
// elements are matched on local name in any namespace, so a client that writes
// its own prefixes, or omits a namespace, is understood. We ask candidates to
// parse by namespace URI; punishing them for how they write their request would
// be a different lesson and not the one we mean.
type soapEnvelope struct {
	XMLName xml.Name       `xml:"Envelope"`
	Header  *soapReqHeader `xml:"Header"`
	Body    *soapReqBody   `xml:"Body"`
}

// soapReqHeader is the SOAP header block: the two headers this service
// understands, plus everything else so mustUnderstand can be honored.
type soapReqHeader struct {
	Security       *wsseSecurity      `xml:"Security"`
	RequestContext *finRequestContext `xml:"RequestContext"`
	Other          []soapAnyElement   `xml:",any"`
}

// wsseSecurity is the WS-Security header.
type wsseSecurity struct {
	UsernameToken *wsseUsernameToken `xml:"UsernameToken"`
}

// wsseUsernameToken is the UsernameToken. The Password element's Type attribute
// is accepted and ignored: this channel only ever sees PasswordText.
type wsseUsernameToken struct {
	Username string `xml:"Username"`
	Password string `xml:"Password"`
}

// finRequestContext is the service's own request context header.
type finRequestContext struct {
	CorrelationID string `xml:"CorrelationId"`
}

// soapReqBody is the SOAP body block.
type soapReqBody struct {
	Request *getRateTableRequest `xml:"GetExchangeRateTable"`
	Other   []soapAnyElement     `xml:",any"`
}

// getRateTableRequest is the operation's request. RateType, ValidFrom and ValidTo
// are accepted and IGNORED, documented as v1.0 backward compatibility: the full
// rolling window always comes back, and a client that believes it filtered
// server-side will post rates it never asked for.
type getRateTableRequest struct {
	CompanyCode string `xml:"CompanyCode"`
	RateType    string `xml:"RateType"`
	ValidFrom   string `xml:"ValidFrom"`
	ValidTo     string `xml:"ValidTo"`
}

// soapAnyElement is any element this service does not model, with its attributes,
// so a mustUnderstand can be detected on a header we do not understand.
type soapAnyElement struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
}

// parseSOAPRequest decodes a request envelope and insists it is SOAP 1.1.
//
// The decoder accepts ISO-8859-1 and Windows-1252 request bodies as well as
// UTF-8, because a client that talks to an ISO-8859-1 endpoint often answers in
// kind and refusing that would be pedantry rather than a lesson.
func parseSOAPRequest(body []byte) (*soapEnvelope, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(strings.TrimSpace(charset)) {
		case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
			raw, err := io.ReadAll(input)
			if err != nil {
				return nil, err
			}
			return strings.NewReader(seed.DecodeCP1252(raw)), nil
		case "", "utf-8", "utf8", "us-ascii", "ascii":
			return input, nil
		}
		return nil, fmt.Errorf("erp: unsupported request charset %q", charset)
	}
	var env soapEnvelope
	if err := dec.Decode(&env); err != nil {
		return nil, err
	}
	if env.XMLName.Space != NSSoapEnv {
		return nil, fmt.Errorf("erp: envelope namespace %q is not SOAP 1.1", env.XMLName.Space)
	}
	if env.Body == nil {
		return nil, fmt.Errorf("erp: envelope carries no Body")
	}
	return &env, nil
}

// unknownMustUnderstand returns the qualified name of the first header element
// this service does not understand that carries mustUnderstand, or an empty
// string when there is none.
func (env *soapEnvelope) unknownMustUnderstand() string {
	if env.Header == nil {
		return ""
	}
	for _, h := range env.Header.Other {
		for _, attr := range h.Attrs {
			if attr.Name.Local != "mustUnderstand" {
				continue
			}
			if attr.Name.Space != "" && attr.Name.Space != NSSoapEnv {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(attr.Value)) {
			case "1", "true":
				if h.XMLName.Space == "" {
					return h.XMLName.Local
				}
				return "{" + h.XMLName.Space + "}" + h.XMLName.Local
			}
		}
	}
	return ""
}

// credentialsMatch reports whether the envelope carries the expected
// UsernameToken. The comparison is constant time and the fault never echoes what
// was sent.
func (env *soapEnvelope) credentialsMatch(creds Credentials) bool {
	if env.Header == nil || env.Header.Security == nil || env.Header.Security.UsernameToken == nil {
		return false
	}
	token := env.Header.Security.UsernameToken
	userOK := subtle.ConstantTimeCompare(
		[]byte(strings.TrimSpace(token.Username)), []byte(creds.SOAPUsername)) == 1
	passOK := subtle.ConstantTimeCompare(
		[]byte(strings.TrimSpace(token.Password)), []byte(creds.SOAPPassword)) == 1
	return userOK && passOK
}

// correlationID returns the trimmed correlation id of the request context.
func (env *soapEnvelope) correlationID() string {
	if env.Header == nil || env.Header.RequestContext == nil {
		return ""
	}
	return strings.TrimSpace(env.Header.RequestContext.CorrelationID)
}
