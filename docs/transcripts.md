# Worked transcripts

Every exchange below was captured from the two services on scenario S0, seed
20260416, by `tools/capture-transcripts.sh`. Nothing here is hand-written, and
nothing here is a substitute for `docs/erp-api.md`, `docs/twin-api.md` and
`docs/soap.md`: the documents are the contract, and this file is what the
contract looks like on the wire when you have not written any code yet.

Tokens are shown as `<token>`. Every other byte is real.

## The ERP

### Authenticate

Client credentials as JSON. The two services take a form body as well, because half the HTTP clients in the world post credentials that way.

```http
POST /erp/v1/auth/token HTTP/1.1
Host: 127.0.0.1:8082
Content-Type: application/json

{
 "client_id": "blp-connector",
 "client_secret": "<erp-client-secret>"
}

HTTP/1.1 200
Content-Type: application/json

{
 "access_token": "<token>",
 "expires_after_requests": 250,
 "expires_after_virtual_ms": 900000
}
```

### One page of creditors

`returned`, `has_more`, `next_cursor` and `max_change_seq` are the whole pagination contract. `max_change_seq` is the collection's highest sequence and is a usable watermark only once `has_more` is false.

```http
GET /erp/v1/suppliers?limit=2 HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json

{
 "records": [
  {
   "supplier_number": "0000000401",
   "name": "Rüegg & Söhne AG",
   "country": "DE",
   "currency": "EUR",
   "iban": "CH6400762000003175519",
   "vat_number": "CHE-205.487.979 MWST",
   "payment_terms_days": 14,
   "blocked": false,
   "change_seq": 100001,
   "legacy_id": 401
  },
  {
   "supplier_number": "0000000402",
   "name": "Weiß Hydraulik AG",
   "country": "CH",
   "currency": "CHF",
   "iban": "CH4900762000003183438",
   "vat_number": "CHE-802.781.899 MWST",
   "payment_terms_days": 60,
   "blocked": false,
   "change_seq": 100002,
   "legacy_id": 402
  }
 ],
 "next_cursor": "eyJjaGFuZ2Vfc2VxX2hpZ2giOjEwMDAwMiwiZGF0YXNldCI6InN1cHBsaWVyIiwibGFzdF9rZXkiOiIwMDAwMDAwNDAyIiwic2lnIjoiYWM0YWQyN2MwMjAxYmViNyJ9",
 "max_change_seq": 100918,
 "returned": 2,
 "has_more": true
}
```

### The next page, by cursor

The cursor is opaque and signed. Editing it, or building your own, is `400 CURSOR_INVALID`: a cursor is the server's statement about where you were.

```http
GET /erp/v1/suppliers?limit=2&cursor=eyJjaGFuZ2Vfc2VxX2hpZ2giOjEwMDAwMiwiZGF0YXNldCI6InN1cHBsaWVyIiwibGFzdF9rZXkiOiIwMDAwMDAwNDAyIiwic2lnIjoiYWM0YWQyN2MwMjAxYmViNyJ9 HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json

{
 "records": [
  {
   "supplier_number": "0000000403",
   "name": "Schürch Maschinenbau GmbH",
   "country": "DE",
   "currency": "EUR",
   "iban": "CH3200762000003191357",
   "vat_number": "CHE-159.310.623 MWST",
   "payment_terms_days": 30,
   "blocked": false,
   "change_seq": 100003,
   "legacy_id": 403
  },
  {
   "supplier_number": "0000000404",
   "name": "Größle Werkzeuge AG",
   "country": "DE",
   "currency": "USD",
   "iban": "CH2100762000003199276",
   "vat_number": "CHE-874.618.247 MWST",
   "payment_terms_days": 30,
   "blocked": false,
   "change_seq": 100004,
   "legacy_id": 404
  }
 ],
 "next_cursor": "eyJjaGFuZ2Vfc2VxX2hpZ2giOjEwMDAwNCwiZGF0YXNldCI6InN1cHBsaWVyIiwibGFzdF9rZXkiOiIwMDAwMDAwNDA0Iiwic2lnIjoiNWE0MzZkOTI0MDQyMDFjMyJ9",
 "max_change_seq": 100918,
 "returned": 2,
 "has_more": true
}
```

### A page size above the published maximum

Note `X-Limit-Clamped`. The request is not refused and it is not silently honored either: the clamp is applied and announced, so a client that asks for too much can notice.

```http
GET /erp/v1/suppliers?limit=5000 HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json
X-Limit-Clamped: 250
Transfer-Encoding: chunked

{
 "records": [
  {
   "supplier_number": "0000000401",
   "name": "Rüegg & Söhne AG",
   "country": "DE",
   "currency": "EUR",
   "iban": "CH6400762000003175519",
   "vat_number": "CHE-205.487.979 MWST",
   "payment_terms_days": 14,
   "blocked": false,
   "change_seq": 100001,
   "legacy_id": 401
  },
  {
   "supplier_number": "0000000402",
   "name": "Weiß Hydraulik AG",
   "country": "CH",
   "currency": "CHF",
   "iban": "CH4900762000003183438",
   "vat_number": "CHE-802.781.899 MWST",
   "payment_terms_days": 60,
   "blocked": false,
   "change_seq": 100002,
   "legacy_id": 402
  },
  {
   "supplier_number": "0000000403",
   "name": "Schürch Maschinenbau GmbH",
   "country": "DE",
   "currency": "EUR",
   "iban": "CH3200762000003191357",
   "vat_number": "CHE-159.310.623 MWST",
   "payment_terms_days": 30,
   "blocked": false,
   "change_seq": 100003,
   "legacy_id": 403
  },
  {
   "supplier_number": "0000000404",
   "name": "Größle Werkzeuge AG",
   "country": "DE",
   "currency": "USD",
   "iban": "CH2100762000003199276",
   "vat_number": "CHE-874.618.247 MWST",
   "payment_terms_days": 30,
   "blocked": false,
   "change_seq": 100004,
   "legacy_id": 404
  },
  {
   "supplier_number": "0000000405",
   "name": "Rüegg Hydraulik AG",
   "country": "DE",
   "currency": "CHF",
   "iban": "CH8100762000003207195",
   "vat_number": "CHE-518.378.755 MWST",
   "payment_terms_days": 60,
   "blocked": false,
   "change_seq": 100005,
   "legacy_id": 405
  },
  {
   "supplier_number": "0000000406",
   "name": "Rüegg Zerspanung GmbH",
   "country": "DE",
   "currency": "USD",
 
... truncated
```

### A purchase order line

`unit_price` carries four fraction digits and `quantity` three. A unit price is a rate, not a booked amount, so it is allowed more digits than the currency's minor unit. Read both as strings.

```http
GET /erp/v1/purchase-order-lines?limit=1 HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json

{
 "records": [
  {
   "po_number": " 4500002042 ",
   "line_no": "00010",
   "material": "MAT-100294",
   "description": "Position 00010",
   "quantity": "16.000",
   "uom": "STK",
   "unit_price": "10.6864",
   "currency": "CHF",
   "gl_account": "0006460",
   "cost_center": "0020019",
   "change_seq": 100399
  }
 ],
 "next_cursor": "eyJjaGFuZ2Vfc2VxX2hpZ2giOjEwMDM5OSwiZGF0YXNldCI6InB1cmNoYXNlX29yZGVyX2xpbmUiLCJsYXN0X2tleSI6IjQ1MDAwMDIwNDJcdTAwMWYwMDAxMCIsInNpZyI6IjJmYWJmYjlhYmE4Mzg4ODgifQ",
 "max_change_seq": 100920,
 "returned": 1,
 "has_more": true
}
```

### The unit conversion table

One page, no paging, `max_change_seq: 0`. It is customizing rather than master data: it changes when somebody decides it does, not when a record moves.

```http
GET /erp/v1/uom-conversions HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json

{
 "records": [
  {
   "material": "",
   "alt_uom": "CTN",
   "numerator": 12,
   "denominator": 1,
   "base_uom": "EA"
  },
  {
   "material": "",
   "alt_uom": "EA",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "EA"
  },
  {
   "material": "",
   "alt_uom": "G",
   "numerator": 1,
   "denominator": 1000,
   "base_uom": "KG"
  },
  {
   "material": "",
   "alt_uom": "H",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "STD"
  },
  {
   "material": "",
   "alt_uom": "KG",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "KG"
  },
  {
   "material": "",
   "alt_uom": "L",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "L"
  },
  {
   "material": "",
   "alt_uom": "M",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "M"
  },
  {
   "material": "",
   "alt_uom": "M2",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "M2"
  },
  {
   "material": "",
   "alt_uom": "PAU",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "PAU"
  },
  {
   "material": "",
   "alt_uom": "PCE",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "EA"
  },
  {
   "material": "",
   "alt_uom": "STD",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "STD"
  },
  {
   "material": "",
   "alt_uom": "STK",
   "numerator": 1,
   "denominator": 1,
   "base_uom": "EA"
  },
  {
   "material": "",
   "alt_uom": "TON",
   "numerator": 1000,
   "denominator": 1,
   "base_uom": "KG"
  },
  {
   "material": "MAT-1000-3",
   "alt_uom": "CTN",
   "numerator": 1000,
   "denominator": 3,
   "base_uom": "EA"
  },
  {
   "material": "MAT-EIGHTH",
   "alt_uom": "
... truncated
```

## The SOAP channel

### The SOAPAction header is part of the contract

A wrong or unquoted SOAPAction answers **HTTP 200** carrying a `soap:Fault`. On this channel the status line is never your success signal, and this is the cheapest way to learn it.

```http
POST /soap/FinancialReferenceDataService HTTP/1.1
Host: 127.0.0.1:8082
Content-Type: text/xml; charset=utf-8
SOAPAction: not-the-right-value

<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:fin="urn:blp-erp:finref:1.0"
                  xmlns:cmn="urn:blp-erp:common:1.0"
                  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soapenv:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" soapenv:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>FINREF_SVC</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText"><soap-password></wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soapenv:mustUnderstand="1">
      <cmn:CorrelationId>transcripts</cmn:CorrelationId>
      <cmn:ConsumerSystem>MINI-BLP</cmn:ConsumerSystem>
    </fin:RequestContext>
  </soapenv:Header>
  <soapenv:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>CH10</fin:CompanyCode>
      <fin:RateType>DAILY</fin:RateType>
      <fin:ValidTo xsi:nil="true"/>
    </fin:GetExchangeRateTable>
  </soapenv:Body>
</soapenv:Envelope>


HTTP/1.1 200
Content-Type: text/xml

<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">
  <S:Body>
    <S:Fault>
      <faultcode>S:Client</faultcode>
      <faultstring>SOAPAction not supported by this endpoint; the action must be quoted exactly as documented</faultstring>
      <detail>
        <FinRefFault xmlns="urn:blp-erp:finref:1.0">
          <ns2:Code xmlns:ns2="urn:blp-erp:common:1.0">ERP-FX-403</ns2:Code>
          <Severity>PERMANENT</Severity>
          <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0"></ns2:CorrelationId>
        </FinRefFault>
      </detail>
    </S:Fault>
  </S:Body>
</S:Envelope>

```

### The same request with the quotes

The quotes are part of the value. Three things to notice in the answer: the prolog declares **ISO-8859-1** and no HTTP header repeats it, the prefixes are `S:` and `ns2:` rather than the documentation's, and the rates carry a decimal **comma**. The first delivery of a company's table may answer a RETRYABLE fault; retry the identical request and it succeeds.

```http
POST /soap/FinancialReferenceDataService HTTP/1.1
Host: 127.0.0.1:8082
Content-Type: text/xml; charset=utf-8
SOAPAction: "urn:blp-erp:finref:1.0/GetExchangeRateTable"

<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:fin="urn:blp-erp:finref:1.0"
                  xmlns:cmn="urn:blp-erp:common:1.0"
                  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soapenv:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" soapenv:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>FINREF_SVC</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText"><soap-password></wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soapenv:mustUnderstand="1">
      <cmn:CorrelationId>transcripts</cmn:CorrelationId>
      <cmn:ConsumerSystem>MINI-BLP</cmn:ConsumerSystem>
    </fin:RequestContext>
  </soapenv:Header>
  <soapenv:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>CH10</fin:CompanyCode>
      <fin:RateType>DAILY</fin:RateType>
      <fin:ValidTo xsi:nil="true"/>
    </fin:GetExchangeRateTable>
  </soapenv:Body>
</soapenv:Envelope>


HTTP/1.1 200
Content-Type: text/xml

<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">
  <S:Body>
    <S:Fault>
      <faultcode>S:Server</faultcode>
      <faultstring>the exchange rate snapshot is being rebuilt; repeat the request</faultstring>
      <detail>
        <FinRefFault xmlns="urn:blp-erp:finref:1.0">
          <ns2:Code xmlns:ns2="urn:blp-erp:common:1.0">ERP-FX-503</ns2:Code>
          <Severity>RETRYABLE</Severity>
          <RetryAfterSeconds>2</RetryAfterSeconds>
          <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0">transcripts</ns2:CorrelationId>
        </FinRefFault>
      </detail>
    </S:Fault>
  </S:Body>
</S:Envelope>

```

### A permanent fault

`Severity` is machine-readable and authoritative. PERMANENT means never retry: no number of attempts configures a rate table for a company code that has none. Its invoices belong in the exception queue.

```http
POST /soap/FinancialReferenceDataService HTTP/1.1
Host: 127.0.0.1:8082
Content-Type: text/xml; charset=utf-8
SOAPAction: "urn:blp-erp:finref:1.0/GetExchangeRateTable"

<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:fin="urn:blp-erp:finref:1.0"
                  xmlns:cmn="urn:blp-erp:common:1.0"
                  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soapenv:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" soapenv:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>FINREF_SVC</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText"><soap-password></wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soapenv:mustUnderstand="1">
      <cmn:CorrelationId>transcripts</cmn:CorrelationId>
      <cmn:ConsumerSystem>MINI-BLP</cmn:ConsumerSystem>
    </fin:RequestContext>
  </soapenv:Header>
  <soapenv:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>CH20</fin:CompanyCode>
      <fin:RateType>DAILY</fin:RateType>
      <fin:ValidTo xsi:nil="true"/>
    </fin:GetExchangeRateTable>
  </soapenv:Body>
</soapenv:Envelope>


HTTP/1.1 200
Content-Type: text/xml

<?xml version="1.0" encoding="ISO-8859-1"?>
<S:Envelope xmlns:S="http://schemas.xmlsoap.org/soap/envelope/">
  <S:Body>
    <S:Fault>
      <faultcode>S:Client</faultcode>
      <faultstring>no exchange rate table is configured for this company code</faultstring>
      <detail>
        <FinRefFault xmlns="urn:blp-erp:finref:1.0">
          <ns2:Code xmlns:ns2="urn:blp-erp:common:1.0">ERP-FX-014</ns2:Code>
          <Severity>PERMANENT</Severity>
          <Element>CompanyCode</Element>
          <ns2:CorrelationId xmlns:ns2="urn:blp-erp:common:1.0">transcripts</ns2:CorrelationId>
        </FinRefFault>
      </detail>
    </S:Fault>
  </S:Body>
</S:Envelope>

```

## Posting to the ERP

### One document

Amounts are decimal strings; a JSON number is `400 MALFORMED_BODY` with reason `number_format`. The four source/fx fields document the conversion YOU performed: the ERP stores them and never recomputes them, because an ERP that answered with its own conversion would be handing out the answer.

```http
POST /erp/v1/ap/documents HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>
Idempotency-Key: blp:acme-ch:transcripts-1:aaaaaaaaaaaaaaaa
Content-Type: application/json

{
 "item_key": "transcripts-1",
 "external_reference": "transcripts-1",
 "company_code": "CH10",
 "supplier_number": "0000000417",
 "supplier_invoice_number": "0004711",
 "document_type": "RE",
 "document_date": "2026-03-29",
 "posting_date": "2026-03-29",
 "po_number": "",
 "cost_center": "0012340",
 "currency": "CHF",
 "gross_amount": "1252.78",
 "vat_amount": "0.00",
 "source_currency": "EUR",
 "source_gross_amount": "1345.63",
 "fx_rate": "0.931000",
 "fx_rate_factor": 1
}

HTTP/1.1 200
Content-Type: application/json

{
 "item_key": "transcripts-1",
 "status": "posted",
 "document_number": "AP-2026-0000001",
 "fiscal_year": 2026,
 "posting_date": "2026-03-29",
 "erp_revision": 1,
 "idempotency_replay": false
}
```

### The same request with the same key

The original response replayed verbatim, down to the document number, with `idempotency_replay: true` on every item and the `Idempotent-Replay` header. This is what a correct retry of the applied-then-500 posting looks like.

```http
POST /erp/v1/ap/documents HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>
Idempotency-Key: blp:acme-ch:transcripts-1:aaaaaaaaaaaaaaaa
Content-Type: application/json

{
 "item_key": "transcripts-1",
 "external_reference": "transcripts-1",
 "company_code": "CH10",
 "supplier_number": "0000000417",
 "supplier_invoice_number": "0004711",
 "document_type": "RE",
 "document_date": "2026-03-29",
 "posting_date": "2026-03-29",
 "po_number": "",
 "cost_center": "0012340",
 "currency": "CHF",
 "gross_amount": "1252.78",
 "vat_amount": "0.00",
 "source_currency": "EUR",
 "source_gross_amount": "1345.63",
 "fx_rate": "0.931000",
 "fx_rate_factor": 1
}

HTTP/1.1 200
Content-Type: application/json
Idempotent-Replay: true

{
 "item_key": "transcripts-1",
 "status": "posted",
 "document_number": "AP-2026-0000001",
 "fiscal_year": 2026,
 "posting_date": "2026-03-29",
 "erp_revision": 1,
 "idempotency_replay": true
}
```

### The same document under a FRESH key

The same `external_reference` again. You get the ORIGINAL document number back, so nothing is double-booked, `duplicate: true` says what happened, and `duplicate_document_attempts` moves. Grading asserts that counter is zero, so this answer is a failed assertion wearing a 200.

```http
POST /erp/v1/ap/documents HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>
Idempotency-Key: blp:acme-ch:transcripts-1:bbbbbbbbbbbbbbbb
Content-Type: application/json

{
 "item_key": "transcripts-1",
 "external_reference": "transcripts-1",
 "company_code": "CH10",
 "supplier_number": "0000000417",
 "supplier_invoice_number": "0004711",
 "document_type": "RE",
 "document_date": "2026-03-29",
 "posting_date": "2026-03-29",
 "po_number": "",
 "cost_center": "0012340",
 "currency": "CHF",
 "gross_amount": "1252.78",
 "vat_amount": "0.00",
 "source_currency": "EUR",
 "source_gross_amount": "1345.63",
 "fx_rate": "0.931000",
 "fx_rate_factor": 1
}

HTTP/1.1 200
Content-Type: application/json

{
 "item_key": "transcripts-1",
 "status": "posted",
 "document_number": "AP-2026-0000001",
 "fiscal_year": 2026,
 "posting_date": "2026-03-29",
 "erp_revision": 1,
 "idempotency_replay": false,
 "duplicate": true
}
```

### The same key with a different body

`409 IDEMPOTENCY_KEY_REUSED`. A key is a promise about a body: reusing one for different content is the one case where the ERP would have to guess which of the two you meant, and it refuses instead.

```http
POST /erp/v1/ap/documents HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>
Idempotency-Key: blp:acme-ch:transcripts-1:aaaaaaaaaaaaaaaa
Content-Type: application/json

{
 "item_key": "transcripts-1",
 "external_reference": "transcripts-1",
 "company_code": "CH10",
 "supplier_number": "0000000417",
 "supplier_invoice_number": "0004711",
 "document_type": "RE",
 "document_date": "2026-03-29",
 "posting_date": "2026-03-29",
 "po_number": "",
 "cost_center": "0012340",
 "currency": "CHF",
 "gross_amount": "9999.99",
 "vat_amount": "0.00",
 "source_currency": "EUR",
 "source_gross_amount": "1345.63",
 "fx_rate": "0.931000",
 "fx_rate_factor": 1
}

HTTP/1.1 409
Content-Type: application/json

{
 "code": "IDEMPOTENCY_KEY_REUSED",
 "message": "idempotency key already used with a different request body",
 "retriable": false,
 "details": {}
}
```

### A batch where one item is rejected

**`207 Multi-Status`**, because the outcomes differ. `results` keeps your item order and is the authority; the counts are a convenience. A business rejection is a per-item result with `retriable: false`, never a transport error, and retrying one is a graded mistake.

```http
POST /erp/v1/ap/documents:batch HTTP/1.1
Host: 127.0.0.1:8082
Authorization: Bearer <token>
Idempotency-Key: blp:acme-ch:transcripts-batch:cccccccccccccccc
Content-Type: application/json

{
 "items": [
  {
   "item_key": "transcripts-2",
   "external_reference": "transcripts-2",
   "company_code": "CH10",
   "supplier_number": "0000000417",
   "supplier_invoice_number": "0004712",
   "document_type": "RE",
   "document_date": "2026-03-29",
   "posting_date": "2026-03-29",
   "po_number": "",
   "cost_center": "0012340",
   "currency": "CHF",
   "gross_amount": "1252.78",
   "vat_amount": "0.00",
   "source_currency": "EUR",
   "source_gross_amount": "1345.63",
   "fx_rate": "0.931000",
   "fx_rate_factor": 1
  },
  {
   "item_key": "transcripts-3",
   "external_reference": "transcripts-3",
   "company_code": "CH10",
   "supplier_number": "0000009999",
   "supplier_invoice_number": "0004713",
   "document_type": "RE",
   "document_date": "2026-03-29",
   "posting_date": "2026-03-29",
   "po_number": "",
   "cost_center": "0012340",
   "currency": "CHF",
   "gross_amount": "1252.78",
   "vat_amount": "0.00",
   "source_currency": "EUR",
   "source_gross_amount": "1345.63",
   "fx_rate": "0.931000",
   "fx_rate_factor": 1
  }
 ]
}

HTTP/1.1 207
Content-Type: application/json

{
 "results": [
  {
   "item_key": "transcripts-2",
   "status": "posted",
   "document_number": "AP-2026-0000002",
   "fiscal_year": 2026,
   "posting_date": "2026-03-29",
   "erp_revision": 1,
   "idempotency_replay": false
  },
  {
   "item_key": "transcripts-3",
   "status": "rejected",
   "idempotency_replay": false,
   "code": "ERP_SUPPLIER_UNKNOWN",
   "message": "supplier_number: no creditor carries this supplier number",
   "retriable": false
  }
 ],
 "posted": 1,
 "rejected": 1,
 "duplicates": 0
}
```

## The digital twin

### Authenticate

The same grant on both services, and the same token lifetime: 250 requests or 900'000 virtual milliseconds, whichever comes first. A 401 TOKEN_EXPIRED is not a failure; it is a refresh.

```http
POST /v1/auth/token HTTP/1.1
Host: 127.0.0.1:8081
Content-Type: application/json

{
 "client_id": "blp-connector",
 "client_secret": "<twin-client-secret>"
}

HTTP/1.1 200
Content-Type: application/json

{
 "access_token": "<token>",
 "expires_after_requests": 250,
 "expires_after_virtual_ms": 900000,
 "token_type": "Bearer"
}
```

### Open a batch

The open body is the file-channel manifest minus the three per-file facts only a file has: `path`, `record_count` and `sha256`. Note that `batch_ref` IS the `batch_id`: one name, one value, nothing for a resuming client to persist.

```http
POST /v1/ingest/batches HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Content-Type: application/json

{
 "manifest_version": "1",
 "batch_id": "transcripts-0001",
 "producer": "docs/transcripts",
 "run_id": "transcripts",
 "tenant": "acme-ch",
 "source_system": "erp-prod",
 "mode": "upsert",
 "full_load": false,
 "on_error": "continue",
 "files": [
  {
   "dataset": "supplier",
   "format": "json",
   "profile": "blp-canonical-v1",
   "encoding": "UTF-8"
  }
 ]
}

HTTP/1.1 201
Content-Type: application/json

{
 "batch_id": "transcripts-0001",
 "batch_ref": "transcripts-0001",
 "manifest_sha256": "303bb6e988da1687054f27520e5d6bce6e6accbee08d74143fbae849f2168bff",
 "max_body_bytes": 8388608,
 "max_records": 1000,
 "records_endpoint": "/v1/ingest/batches/transcripts-0001/records",
 "replay": false,
 "status": "open"
}
```

### One chunk, one good record and one bad one

`207` because the outcomes differ. The body is JSON whatever format the request used, because a per-record result carries nested `errors[]` and a CSV cell cannot hold a list. Every error names the field AND the location in the bytes you sent.

```http
POST /v1/ingest/batches/transcripts-0001/records?dataset=supplier HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Idempotency-Key: ing:transcripts:supplier:1
X-Chunk-Ordinal: 1
Content-Type: application/json

[
 {
  "supplier_number": "0000000417",
  "name": "Meier Pr\u00e4zision AG",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.789 MWST",
  "payment_terms_days": 30,
  "blocked": false,
  "change_seq": 100417
 },
 {
  "supplier_number": "0000000418",
  "name": "Amount as a JSON number",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.780 MWST",
  "payment_terms_days": "thirty",
  "blocked": false,
  "change_seq": 100418
 }
]

HTTP/1.1 207
Content-Type: application/json

{
 "batch_ref": "transcripts-0001",
 "chunk_ordinal": 1,
 "counts": {
  "seen": 2,
  "accepted": 1,
  "accepted_with_warning": 0,
  "rejected": 1,
  "skipped_unchanged": 0,
  "quarantined": 0,
  "replayed": 0
 },
 "dataset": "supplier",
 "records": [
  {
   "ordinal": 1,
   "dataset": "supplier",
   "natural_key": "0000000417",
   "outcome": "accepted",
   "twin_id": "twn_2915577e0c1c7c26",
   "version": 1,
   "content_hash": "b92209279a24746f1ba6733340e0b8a609ff404eb1ed0dce619ced637e5b4a02",
   "errors": [],
   "warnings": [],
   "pointer": "/0",
   "file": "chunk-supplier-0001",
   "line": 1,
   "chunk_ordinal": 1,
   "raw_excerpt_sha256": "b92209279a24746f1ba6733340e0b8a609ff404eb1ed0dce619ced637e5b4a02"
  },
  {
   "ordinal": 2,
   "dataset": "supplier",
   "natural_key": "",
   "outcome": "rejected",
   "errors": [
    {
     "code": "E_NUMBER_FORMAT",
     "field": "payment_terms_days",
     "message": "expected a whole number with no grouping and no fraction",
     "pointer": "/1/payment_terms_days"
    }
   ],
   "warnings": [],
   "pointer": "/1",
   "file": "chunk-supplier-0001",
   "chunk_ordinal": 1
  }
 ]
}
```

### The same chunk with the same key

The stored response, replayed verbatim. This is what makes the retry of an applied-then-500 chunk exact, and it is the reason the key has to be a function of the payload rather than of the attempt.

```http
POST /v1/ingest/batches/transcripts-0001/records?dataset=supplier HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Idempotency-Key: ing:transcripts:supplier:1
X-Chunk-Ordinal: 1
Content-Type: application/json

[
 {
  "supplier_number": "0000000417",
  "name": "Meier Pr\u00e4zision AG",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.789 MWST",
  "payment_terms_days": 30,
  "blocked": false,
  "change_seq": 100417
 },
 {
  "supplier_number": "0000000418",
  "name": "Amount as a JSON number",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.780 MWST",
  "payment_terms_days": "thirty",
  "blocked": false,
  "change_seq": 100418
 }
]

HTTP/1.1 207
Content-Type: application/json
Idempotent-Replay: true

{
 "batch_ref": "transcripts-0001",
 "chunk_ordinal": 1,
 "counts": {
  "seen": 2,
  "accepted": 1,
  "accepted_with_warning": 0,
  "rejected": 1,
  "skipped_unchanged": 0,
  "quarantined": 0,
  "replayed": 0
 },
 "dataset": "supplier",
 "records": [
  {
   "ordinal": 1,
   "dataset": "supplier",
   "natural_key": "0000000417",
   "outcome": "accepted",
   "twin_id": "twn_2915577e0c1c7c26",
   "version": 1,
   "content_hash": "b92209279a24746f1ba6733340e0b8a609ff404eb1ed0dce619ced637e5b4a02",
   "errors": [],
   "warnings": [],
   "pointer": "/0",
   "file": "chunk-supplier-0001",
   "line": 1,
   "chunk_ordinal": 1,
   "raw_excerpt_sha256": "b92209279a24746f1ba6733340e0b8a609ff404eb1ed0dce619ced637e5b4a02"
  },
  {
   "ordinal": 2,
   "dataset": "supplier",
   "natural_key": "",
   "outcome": "rejected",
   "errors": [
    {
     "code": "E_NUMBER_FORMAT",
     "field": "payment_terms_days",
     "message": "expected a whole number with no grouping and no fraction",
     "pointer": "/1/payment_terms_days"
    }
   ],
   "warnings": [],
   "pointer": "/1",
   "file": "chunk-supplier-0001",
   "chunk_ordinal": 1
  }
 ]
}
```

### The same chunk under a NEW key

Applied again - the store is content-addressed, so no record changes - and `duplicate_apply_attempts` moves. Grading asserts that counter is zero: it is the evidence that your key is derived from your payload.

```http
POST /v1/ingest/batches/transcripts-0001/records?dataset=supplier HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Idempotency-Key: ing:transcripts:supplier:1-again
X-Chunk-Ordinal: 1
Content-Type: application/json

[
 {
  "supplier_number": "0000000417",
  "name": "Meier Pr\u00e4zision AG",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.789 MWST",
  "payment_terms_days": 30,
  "blocked": false,
  "change_seq": 100417
 },
 {
  "supplier_number": "0000000418",
  "name": "Amount as a JSON number",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.780 MWST",
  "payment_terms_days": "thirty",
  "blocked": false,
  "change_seq": 100418
 }
]

HTTP/1.1 207
Content-Type: application/json

{
 "batch_ref": "transcripts-0001",
 "chunk_ordinal": 1,
 "counts": {
  "seen": 2,
  "accepted": 0,
  "accepted_with_warning": 0,
  "rejected": 1,
  "skipped_unchanged": 1,
  "quarantined": 0,
  "replayed": 0
 },
 "dataset": "supplier",
 "records": [
  {
   "ordinal": 1,
   "dataset": "supplier",
   "natural_key": "0000000417",
   "outcome": "skipped_unchanged",
   "twin_id": "twn_2915577e0c1c7c26",
   "version": 1,
   "content_hash": "b92209279a24746f1ba6733340e0b8a609ff404eb1ed0dce619ced637e5b4a02",
   "errors": [],
   "warnings": [],
   "pointer": "/0",
   "file": "chunk-supplier-0001",
   "line": 1,
   "chunk_ordinal": 1,
   "raw_excerpt_sha256": "b92209279a24746f1ba6733340e0b8a609ff404eb1ed0dce619ced637e5b4a02"
  },
  {
   "ordinal": 2,
   "dataset": "supplier",
   "natural_key": "",
   "outcome": "rejected",
   "errors": [
    {
     "code": "E_NUMBER_FORMAT",
     "field": "payment_terms_days",
     "message": "expected a whole number with no grouping and no fraction",
     "pointer": "/1/payment_terms_days"
    }
   ],
   "warnings": [],
   "pointer": "/1",
   "file": "chunk-supplier-0001",
   "chunk_ordinal": 1
  }
 ]
}
```

### A skipped ordinal

`CHUNK_GAP`, with what was expected and what arrived. Ordinals are 1-based and strictly increasing per (batch, dataset): a gap means a chunk is missing, and guessing which one is not the twin's job.

```http
POST /v1/ingest/batches/transcripts-0001/records?dataset=supplier HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Idempotency-Key: ing:transcripts:supplier:9
X-Chunk-Ordinal: 9
Content-Type: application/json

[
 {
  "supplier_number": "0000000417",
  "name": "Meier Pr\u00e4zision AG",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.789 MWST",
  "payment_terms_days": 30,
  "blocked": false,
  "change_seq": 100417
 },
 {
  "supplier_number": "0000000418",
  "name": "Amount as a JSON number",
  "country": "CH",
  "currency": "CHF",
  "iban": "CH9300762011623852957",
  "vat_number": "CHE-123.456.780 MWST",
  "payment_terms_days": "thirty",
  "blocked": false,
  "change_seq": 100418
 }
]

HTTP/1.1 409
Content-Type: application/json

{
 "code": "CHUNK_GAP",
 "message": "chunk ordinal out of sequence",
 "retriable": false,
 "details": {
  "expected": 2,
  "seen": 9
 }
}
```

### Commit

The receipt object, identical in shape to the one the file channel writes to `receipts/<batch_id>/receipt.json`. One contract, two ways in.

```http
POST /v1/ingest/batches/transcripts-0001/commit HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Content-Type: application/json

{}

HTTP/1.1 200
Content-Type: application/json

{
 "batch_id": "transcripts-0001",
 "batch_ref": "transcripts-0001",
 "channel": "rest",
 "run_id": "transcripts",
 "tenant": "acme-ch",
 "source_system": "erp-prod",
 "producer": "docs/transcripts",
 "sequence": 0,
 "mode": "upsert",
 "full_load": false,
 "on_error": "continue",
 "profiles": [
  "blp-canonical-v1"
 ],
 "manifest_sha256": "303bb6e988da1687054f27520e5d6bce6e6accbee08d74143fbae849f2168bff",
 "batch_status": "partially_accepted",
 "replay": false,
 "codes": [],
 "counts": {
  "seen": 4,
  "accepted": 1,
  "accepted_with_warning": 0,
  "rejected": 2,
  "skipped_unchanged": 1,
  "quarantined": 0,
  "replayed": 0
 },
 "files": [
  {
   "path": "chunk-supplier-0001",
   "dataset": "supplier",
   "format": "json",
   "profile": "blp-canonical-v1",
   "encoding": "UTF-8",
   "declared_records": -1,
   "parsed_records": 2,
   "sha256": "2125b0db7c00d264404fc2979c2c35c2d1b182a7ae0298238225ae4ef6230d35",
   "status": "partially_accepted",
   "codes": [],
   "counts": {
    "seen": 2,
    "accepted": 1,
    "accepted_with_warning": 0,
    "rejected": 1,
    "skipped_unchanged": 0,
    "quarantined": 0,
    "replayed": 0
   },
   "chunk_ordinal": 1
  },
  {
   "path": "chunk-supplier-0001",
   "dataset": "supplier",
   "format": "json",
   "profile": "blp-canonical-v1",
   "encoding": "UTF-8",
   "declared_records": -1,
   "parsed_records": 2,
   "sha256": "2125b0db7c00d264404fc2979c2c35c2d1b182a7ae0298238225ae4ef6230d35",
   "status": "partially_accepted",
   "codes": [],
   "counts": {
    "seen": 2,
    "accepted": 0,
    "accepted_with_warning": 0,
    "rejected": 1,
... truncated
```

### One proposal from the outbox

A proposal is the twin's request for a posting: it carries the converted amount, the rate and factor it was converted with, and the source amount it came from. Money is an integer in minor units and it never travels as a JSON number.

```http
GET /v1/outbox/proposals?limit=1&status=pending HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json
Vary: Accept

{
 "has_more": false,
 "next_cursor": "",
 "records": [],
 "returned": 0
}
```

### Acknowledge one posting

The ack is what closes the audit chain: without it the twin's proposal stays pending and nothing can be traced from an ERP document number back to a line in a file. `proposals_pending` is graded, and it is the counter that catches a connector which posted and never told anyone.

```http
POST /v1/outbox/acks HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Idempotency-Key: ack:transcripts:0123456789abcdef
Content-Type: application/json

{
 "run_id": "transcripts",
 "acks": [
  {
   "proposal_id": "prp_0000001",
   "status": "posted",
   "external_document_number": "AP-2026-0000001",
   "external_fiscal_year": 2026,
   "external_posting_date": "2026-04-14",
   "external_revision": 1,
   "http_status": 207,
   "attempts": 1,
   "idempotency_replay": false,
   "idempotency_key": "blp:acme-ch:prp_0000001:0123456789abcdef"
  }
 ]
}

HTTP/1.1 422
Content-Type: application/json

{
 "acknowledged": 0,
 "records": [
  {
   "ordinal": 1,
   "proposal_id": "",
   "effect": "rejected",
   "proposal_status": "",
   "attempts": 0,
   "errors": [
    {
     "code": "E_KEY_MISSING",
     "field": "proposal_id",
     "message": "an ack names the proposal it acknowledges",
     "pointer": "/proposal_id"
    },
    {
     "code": "E_FIELD_REQUIRED",
     "field": "status",
     "message": "an ack carries a status: posted, rejected, failed or skipped",
     "pointer": "/status"
    }
   ],
   "warnings": []
  }
 ],
 "rejected": 1
}
```

### The state digest (admin, ours, not yours)

The graded digest and the per-dataset record counts. Your connector is never given the admin token; use it while developing, because it is the fastest way to see whether what you sent is what landed.

```http
GET /admin/v1/state/digest HTTP/1.1
Host: 127.0.0.1:8081
X-Admin-Token: dev-twin-admin

HTTP/1.1 200
Content-Type: application/json

{
 "datasets": {
  "supplier": 1
 },
 "digest": "59a2c529d88bb2e82ece5925eccedb5340304a4ead477a52a6a5030cb3d839c1",
 "high_seq": 2
}
```
