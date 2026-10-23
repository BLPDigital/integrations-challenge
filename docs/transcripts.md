# Worked transcripts

Every exchange below was captured from the two services on scenario S0, seed
20260416, by `tools/capture-transcripts.sh`. Nothing here is hand-written, and
nothing here is a substitute for `docs/erp-api.md` and `docs/twin-api.md`:
the documents are the contract, and this file is what the
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

The master data was delivered as one file batch before the calls below, by the
drop protocol of `docs/twin-api.md`. The invoices were already in the twin: BLP
captured them before the run.

### Match the captured invoices

Once per night, after the master data receipt says DONE. Every captured invoice still waiting is matched against the master data the twin holds now: a proposal for each one that matches, one blocking exception for each one that does not. A second call matches nothing new and answers zeros.

```http
POST /v1/match HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>
Content-Type: application/json

{
 "run_id": "transcripts"
}

HTTP/1.1 200
Content-Type: application/json

{
 "invoices_matched": 100,
 "proposals_emitted": 87,
 "exceptions_opened": 13
}
```

### Two rows of the open exception queue

Paged like the outbox. Every row carries the code and where the invoice came from, which is what exceptions.csv is built from.

```http
GET /v1/exceptions?limit=2 HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json
Vary: Accept

{
 "has_more": true,
 "next_cursor": "eyJjaGFuZ2Vfc2VxX2hpZ2giOjAsImRhdGFzZXQiOiJleGNlcHRpb24iLCJsYXN0X2tleSI6IjAwMDAwMDA0MTdcdTAwMWYwMDA0NzcwXHUwMDFmRVhDX1VPTV9VTk1BUFBBQkxFIiwic2lnIjoiZTM2MjJlMTRkZDA1MWJiNSJ9",
 "records": [
  {
   "subject_key": "0000000413\u001f0004716",
   "subject_type": "invoice",
   "stage": "match",
   "code": "EXC_PO_NOT_FOUND",
   "field": "po_number",
   "message": "the referenced purchase order does not exist",
   "source_batch_id": "capture-KRED_0100_20260416_001",
   "source_file_or_chunk": "KRED_0100_20260416_001.txt",
   "source_line_or_ordinal": "22"
  },
  {
   "subject_key": "0000000417\u001f0004770",
   "subject_type": "invoice",
   "stage": "match",
   "code": "EXC_UOM_UNMAPPABLE",
   "field": "uom",
   "message": "unit of measure PAL has no conversion to a base unit",
   "source_batch_id": "capture-KRED_0100_20260416_001",
   "source_file_or_chunk": "KRED_0100_20260416_001.txt",
   "source_line_or_ordinal": "203"
  }
 ],
 "returned": 2
}
```

### One proposal from the outbox

A proposal is the twin's request for a posting: it carries the converted amount, the rate and factor it was converted with, the source amount it came from, and where the invoice was captured. Money never travels as a JSON number.

```http
GET /v1/outbox/proposals?limit=1&status=pending HTTP/1.1
Host: 127.0.0.1:8081
Authorization: Bearer <token>

HTTP/1.1 200
Content-Type: application/json
Vary: Accept

{
 "has_more": true,
 "next_cursor": "eyJjaGFuZ2Vfc2VxX2hpZ2giOjEsImRhdGFzZXQiOiJwcm9wb3NhbCIsImxhc3Rfa2V5IjoicHJwXzAwMDAwMDEiLCJzaWciOiI4NDVlNjNhNjdiZGE5N2Q5In0",
 "records": [
  {
   "proposal_id": "prp_0000001",
   "invoice_key": "0000000401\u001f0004803",
   "supplier_number": "0000000401",
   "supplier_invoice_number": "0004803",
   "invoice_version": 1,
   "proposal_content_hash": "7c048638190f9b7db14723d0dc759decde3767bb602d57fbeb5cda3f631256d1",
   "status": "pending",
   "company_code": "CH10",
   "document_type": "RE",
   "document_date": "2026-04-14",
   "posting_date": "2026-04-14",
   "matched_po": "",
   "matched_cost_center": "CC-2215",
   "currency": "CHF",
   "gross_amount": "73.60",
   "gross_amount_minor": 7360,
   "vat_amount": "0.00",
   "source_currency": "GBP",
   "source_gross_amount": "68.01",
   "fx_rate": "1.082250",
   "fx_rate_factor": 1,
   "vat_code": "V0",
   "payment_terms_days": 45,
   "discount_raw": "",
   "created_in_run": "transcripts",
   "created_seq": 1,
   "attempts": 0,
   "warnings": "ambiguous_field_semantics",
   "erp_document_number": "",
   "source_batch_id": "capture-KRED_0100_20260416_001",
   "source_file_or_chunk": "KRED_0100_20260416_001.txt",
   "source_line_or_ordinal": "315"
  }
 ],
 "returned": 1
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
  "cost_center": 60,
  "fx_rate": 97,
  "invoice": 100,
  "purchase_order": 160,
  "purchase_order_line": 520,
  "supplier": 240
 },
 "digest": "7e93b7b958817768086cfb5bd1ad1aa6e3b1b8cebca56f92908eab774a582e40",
 "high_seq": 1292
}
```
