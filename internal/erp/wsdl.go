package erp

import (
	"net/http"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// handleWSDL serves GET /soap/FinancialReferenceDataService?wsdl.
//
// It is reference material only: nothing in the challenge requires code
// generation, and a client that hand-writes its envelope from the documented
// transcript is doing the normal thing. The document deliberately uses the
// prefixes the documentation uses (soap:, fin:, cmn:), which are NOT the prefixes
// the response uses; that mismatch is the whole point of matching on namespace
// URIs.
func (s *Server) handleWSDL(w http.ResponseWriter, r *http.Request) {
	if _, ok := r.URL.Query()["wsdl"]; !ok {
		httpx.WriteError(w, http.StatusNotFound, &httpx.Error{
			Code:      "NOT_FOUND",
			Message:   "the SOAP endpoint answers POST; append ?wsdl for the service description",
			Retriable: false,
			Status:    http.StatusNotFound,
		})
		return
	}
	// The WSDL is pure ASCII, so it is served as UTF-8. The rate table response
	// is the document that declares ISO-8859-1, and it does so in its prolog.
	w.Header().Set("Content-Type", SOAPContentType+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(wsdlDocument))
}

// wsdlDocument is the service description. It is a constant: there is nothing in
// it that depends on the scenario, the seed or the request.
const wsdlDocument = `<?xml version="1.0" encoding="UTF-8"?>
<definitions name="FinancialReferenceDataService"
             targetNamespace="` + NSFinRef + `"
             xmlns="http://schemas.xmlsoap.org/wsdl/"
             xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/"
             xmlns:soapenv="` + NSSoapEnv + `"
             xmlns:fin="` + NSFinRef + `"
             xmlns:cmn="` + NSCommon + `"
             xmlns:xsd="http://www.w3.org/2001/XMLSchema">

  <!-- Financial reference data, interface version 1.0.
       One operation: GetExchangeRateTable.
       Style: document/literal. Transport: HTTP. SOAP version: 1.1.
       Security: WS-Security UsernameToken (PasswordText) in the SOAP header.
       SOAPAction must be sent, quoted, exactly as declared below.

       Compatibility note carried over from 1.0: the RateType, ValidFrom and
       ValidTo elements of the request are accepted and IGNORED. The service
       always returns the full rolling window. Filter on the client side.

       Encoding note: responses declare ISO-8859-1 in the XML prolog and carry
       no charset parameter in the HTTP Content-Type. Feed your parser bytes.
       The response prefixes are chosen by the server and are not the prefixes
       used in this document. Match elements on (namespace URI, local name). -->

  <types>
    <xsd:schema targetNamespace="` + NSCommon + `" elementFormDefault="qualified">
      <xsd:element name="CorrelationId" type="xsd:string"/>
      <xsd:element name="SnapshotToken" type="xsd:string"/>
      <xsd:element name="CurrencyFrom" type="xsd:string"/>
      <xsd:element name="CurrencyTo" type="xsd:string"/>
      <xsd:element name="Code" type="xsd:string"/>
    </xsd:schema>

    <xsd:schema targetNamespace="` + NSFinRef + `" elementFormDefault="qualified"
                xmlns:cmn="` + NSCommon + `">
      <xsd:import namespace="` + NSCommon + `"/>

      <xsd:element name="RequestContext">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element ref="cmn:CorrelationId"/>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>

      <xsd:element name="ResponseContext">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element ref="cmn:CorrelationId"/>
            <xsd:element ref="cmn:SnapshotToken"/>
            <xsd:element name="RowCount" type="xsd:int"/>
            <xsd:element name="Truncated" type="xsd:boolean"/>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>

      <xsd:element name="GetExchangeRateTable">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element name="CompanyCode" type="xsd:string"/>
            <xsd:element name="RateType" type="xsd:string" minOccurs="0"/>
            <xsd:element name="ValidFrom" type="xsd:string" minOccurs="0"/>
            <xsd:element name="ValidTo" type="xsd:string" minOccurs="0"/>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>

      <xsd:complexType name="ExchangeRate">
        <xsd:sequence>
          <xsd:element ref="cmn:CurrencyFrom"/>
          <xsd:element ref="cmn:CurrencyTo"/>
          <xsd:element name="RateType" type="xsd:string"/>
          <!-- Host locale: decimal comma, six fraction digits, dot as the
               thousands separator. Whitespace around the value is possible. -->
          <xsd:element name="Rate" type="xsd:string"/>
          <!-- Per-unit factor. 100 means the rate is quoted per 100 units. -->
          <xsd:element name="RateFactorFrom" type="xsd:long"/>
          <xsd:element name="ValidFrom" type="xsd:string"/>
          <!-- xsi:nil="true" means open ended. -->
          <xsd:element name="ValidTo" type="xsd:string" nillable="true"/>
          <!-- ACTIVE or DELETED. DELETED is a soft delete: drop the row. -->
          <xsd:element name="Status" type="xsd:string"/>
          <xsd:element name="Comment" type="xsd:string" nillable="true"/>
        </xsd:sequence>
        <!-- Supersession: for an otherwise identical key the highest Sequence
             wins, whatever the document order. -->
        <xsd:attribute name="Sequence" type="xsd:int" use="required"/>
      </xsd:complexType>

      <xsd:element name="GetExchangeRateTableResponse">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element name="CompanyCode" type="xsd:string"/>
            <xsd:element name="RateProvider" type="xsd:string"/>
            <xsd:element name="ExchangeRateTable">
              <xsd:complexType>
                <xsd:sequence>
                  <xsd:element name="ExchangeRate" type="fin:ExchangeRate"
                               minOccurs="0" maxOccurs="unbounded"/>
                </xsd:sequence>
              </xsd:complexType>
            </xsd:element>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>

      <xsd:element name="FinRefFault">
        <xsd:complexType>
          <xsd:sequence>
            <xsd:element ref="cmn:Code"/>
            <!-- PERMANENT or RETRYABLE. Retrying a PERMANENT fault is wrong. -->
            <xsd:element name="Severity" type="xsd:string"/>
            <xsd:element name="RetryAfterSeconds" type="xsd:int" minOccurs="0"/>
            <xsd:element name="Element" type="xsd:string" minOccurs="0"/>
            <xsd:element ref="cmn:CorrelationId" minOccurs="0"/>
          </xsd:sequence>
        </xsd:complexType>
      </xsd:element>
    </xsd:schema>
  </types>

  <message name="GetExchangeRateTableRequest">
    <part name="parameters" element="fin:GetExchangeRateTable"/>
    <part name="context" element="fin:RequestContext"/>
  </message>
  <message name="GetExchangeRateTableResponse">
    <part name="parameters" element="fin:GetExchangeRateTableResponse"/>
    <part name="context" element="fin:ResponseContext"/>
  </message>
  <message name="FinRefFault">
    <part name="fault" element="fin:FinRefFault"/>
  </message>

  <portType name="FinancialReferenceDataPortType">
    <operation name="GetExchangeRateTable">
      <input message="fin:GetExchangeRateTableRequest"/>
      <output message="fin:GetExchangeRateTableResponse"/>
      <fault name="FinRefFault" message="fin:FinRefFault"/>
    </operation>
  </portType>

  <binding name="FinancialReferenceDataBinding" type="fin:FinancialReferenceDataPortType">
    <soap:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <operation name="GetExchangeRateTable">
      <soap:operation soapAction="urn:blp-erp:finref:1.0/GetExchangeRateTable" style="document"/>
      <input>
        <soap:body use="literal" parts="parameters"/>
        <soap:header message="fin:GetExchangeRateTableRequest" part="context" use="literal"/>
      </input>
      <output>
        <soap:body use="literal" parts="parameters"/>
        <soap:header message="fin:GetExchangeRateTableResponse" part="context" use="literal"/>
      </output>
      <fault name="FinRefFault">
        <soap:fault name="FinRefFault" use="literal"/>
      </fault>
    </operation>
  </binding>

  <service name="FinancialReferenceDataService">
    <port name="FinancialReferenceDataPort" binding="fin:FinancialReferenceDataBinding">
      <soap:address location="http://127.0.0.1:8082/soap/FinancialReferenceDataService"/>
    </port>
  </service>
</definitions>
`
