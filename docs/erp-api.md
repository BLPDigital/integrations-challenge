# ERP REST surface

Reference. Real captured requests and responses for every route are in `docs/transcripts.md`.
The machine-readable contract is `docs/erp-openapi.yaml`; **read it once end to end**, because one
route form is documented only there.

Base: `http://127.0.0.1:8082` for humans; the grader passes the real address in `--erp-base-url`
and the servers accept `--listen 127.0.0.1:0`, so never hard-code a port.

## Routes

| Method | Path | Cost | Notes |
|---|---|---|---|
| POST | `/erp/v1/auth/token` | 10 vms | client credentials; JSON body or form-encoded |
| GET | `/erp/v1/suppliers` | 40 + 0.5/rec | paged |
| GET | `/erp/v1/suppliers/{supplier_number}` | 25 vms | single. Net token negative on purpose |
| GET | `/erp/v1/purchase-orders` | 40 + 0.5/rec | paged; also the bulk `ids=` form, see the OpenAPI file |
| GET | `/erp/v1/purchase-order-lines` | 40 + 0.5/rec | paged |
| GET | `/erp/v1/uom-conversions` | 40 + 0.5/rec | one page, no paging, `max_change_seq: 0`, `has_more: false` |
| POST | `/erp/v1/ap/documents` | 60 vms | single posting |
| POST | `/erp/v1/ap/documents:batch` | 60 + 8/item | max 200 items |
| POST | `/soap/FinancialReferenceDataService` | 120 + 0.2/row | see `docs/soap.md` |
| GET | `/soap/FinancialReferenceDataService?wsdl` | 0 vms, 1 quota unit | reference material only |
| GET | `/healthz` | free | liveness |
| GET | `/ui/` | free | read-only web UI, no auth |

`Authorization: Bearer <access_token>` is required on everything under `/erp/v1` except the token
endpoint. The SOAP endpoint has no bearer token at all: it authenticates with a WS-Security
UsernameToken inside its own envelope. One landscape with two auth models is the point, not an
oversight.

## Auth

```
POST /erp/v1/auth/token
Content-Type: application/json

{"client_id":"$ERP_CLIENT_ID","client_secret":"$ERP_CLIENT_SECRET"}
```

```json
{"access_token":"tok_236b1fce...","expires_after_requests":250,"expires_after_virtual_ms":900000}
```

There is no `expires_in`, because there is no wall clock. The token dies after 250 authenticated
requests or 900'000 virtual milliseconds, whichever comes first; then every request answers
`401 TOKEN_EXPIRED`, `retriable:true`, and you refresh. A refresh costs one quota unit.

Wrong credentials are `401 TOKEN_INVALID`, `retriable:false`. A missing header is
`401 TOKEN_MISSING`. The 401 is charged: `Guard` wraps `Bearer`, so a client that never refreshes
still pays for the requests it wastes.

## The list envelope

Identical on every list endpoint, so one parser covers the surface.

```json
{"records":[...],"next_cursor":"eyJk...","max_change_seq":146329,"returned":250,"has_more":true}
```

- `records` is never `null`.
- `next_cursor` is empty exactly when `has_more` is `false`.
- **`max_change_seq` is the highest change sequence in the whole collection, not in this page.** It
  is therefore only a valid watermark once `has_more` is `false`: persisting it after the first page
  of a multi-page pull skips everything you have not read yet. This is the single most useful line
  in this document.
- Order is `(change_seq, natural_key)` ascending, stable across runs.

### Query parameters

| Parameter | Default | Behavior |
|---|---|---|
| `limit` | 100 | max 250. A larger value is **clamped silently** to 250 and the response carries `X-Limit-Clamped: 250`. `limit < 1` or non-numeric is `400 MALFORMED_BODY` |
| `cursor` | none | opaque, signed, tamper-evident. A hand-edited or foreign cursor is `400 INVALID_CURSOR`. A cursor issued for another dataset is also `400` |
| `changed_since` | 0 | keeps records with `change_seq > value`. `0` returns everything, which is a cold load. Negative or non-numeric is `400` |

`cursor` and `changed_since` compose: the page starts at the later of the two positions, so a
resumed delta pull loses nothing. The cursor is base64url of a small signed JSON object; treat it as
opaque and pass it back verbatim.

## Posting

`Content-Type: application/json` (or absent) only; anything else is `415 UNSUPPORTED_CONTENT_TYPE`.
`Idempotency-Key` is **required**; absent or blank is `400 IDEMPOTENCY_KEY_REQUIRED`.

The single endpoint takes one item object. The batch endpoint takes `{"items":[...]}`, and a bare
JSON array as a convenience. Unknown members are ignored. Item:

```json
{"item_key":"prp_0000001","external_reference":"prp_0000001","company_code":"CH10",
 "supplier_number":"0000417","supplier_invoice_number":"0004711","document_type":"RE",
 "document_date":"2026-03-29","posting_date":"2026-03-29","po_number":"","cost_center":"CC-2200",
 "currency":"CHF","gross_amount":"1252.78","vat_amount":"0.00",
 "source_currency":"EUR","source_gross_amount":"1345.63","fx_rate":"0.931000","fx_rate_factor":1}
```

Mandatory: `external_reference`, `company_code`, `supplier_number`, `supplier_invoice_number`,
`document_type` (`RE` or `GU`), `document_date`, `currency`, `gross_amount`. `item_key` defaults to
`external_reference`; `posting_date` defaults to `document_date`. Amounts are decimal **strings**; a
JSON number is `400 MALFORMED_BODY` with reason `number_format`. The four `source_*` / `fx_*` fields
document the conversion you performed: the ERP stores them and never recomputes them, because an ERP
that answered with its own conversion would be handing out the answer.

### Per-item result

```json
{"item_key":"prp_0000001","status":"posted","document_number":"AP-2026-0000001","fiscal_year":2026,
 "posting_date":"2026-03-29","erp_revision":1,"idempotency_replay":false}
```

Document numbers are `AP-<fiscal year>-<7 digits>`, assigned in request order under one lock.

The batch response wraps the results and adds counts:

```json
{"results":[...],"posted":2,"rejected":2,"duplicates":0}
```

HTTP status: **`207 Multi-Status`** when a batch produced more than one distinct outcome. A batch in
which everything succeeded, or everything was rejected, is `200`. The single endpoint is always
`200`. `results` keeps the request's item order and is the authority; the counts are a convenience.

### Business rejections

A business rejection is **not** a transport error. It arrives as a per-item result inside a `200` or
`207`, always with `"retriable": false`, and retrying one is a graded mistake.

```json
{"item_key":"prp_0000003","status":"rejected","idempotency_replay":false,
 "code":"ERP_PERIOD_CLOSED","message":"posting_date: the fiscal period of this posting date is closed",
 "retriable":false}
```

Checked in this fixed order, first failure wins, exactly one code returned:

| Code | Trigger |
|---|---|
| `ERP_SUPPLIER_UNKNOWN` | no creditor carries this `supplier_number` |
| `ERP_SUPPLIER_BLOCKED` | the creditor carries a payment block |
| `ERP_PO_NOT_FOUND` | `po_number` non-empty and no order carries it |
| `ERP_PO_CLOSED` | the referenced order is not `OPEN` |
| `ERP_PERIOD_CLOSED` | the effective posting date falls in a closed fiscal period (seeded: before 2026-02-01) |
| `ERP_COST_CENTER_UNKNOWN` | `cost_center` non-empty and not configured for this `company_code` |
| `ERP_AMOUNT_MISMATCH` | the posting exceeds the referenced order by more than **5 percent** |

An **empty `cost_center` is accepted** on purpose. The customer's own format leaves the position cost
center undefined; a correct client surfaces that gap instead of inventing a value, and refusing the
posting for it would punish exactly that behavior.

The tolerance comparison is net of VAT, one-sided (only over-billing is refused) and skipped when the
posting currency differs from the order's, because the mock will not form an FX opinion that could
contradict yours.

### Shape failures

A shape failure is a transport error, `400 MALFORMED_BODY`, with `details.reason` from this closed
vocabulary and `details.field` / `details.item_key` naming the offender:

```
body_empty  json_invalid  json_not_an_object  items_required  too_many_items
field_required  enum_unknown  date_format  money_scale  number_format
supplier_number_format
```

No error body ever carries the value you were supposed to compute. A rejection says what is wrong
and why, never what the right answer is.

### Idempotency and duplicates

| Situation | Answer |
|---|---|
| new key | the request is processed |
| same key, **same** body | the original response replayed verbatim, `idempotency_replay:true` on every item, plus the response header `Idempotent-Replay: true`. Down to the document numbers |
| same key, **different** body | `409 IDEMPOTENCY_KEY_REUSED` |
| an `external_reference` that already produced a document, under a **different** key | `200`, `status:"posted"`, the **original** `document_number`, `duplicate:true`, `idempotency_replay:false`, and `duplicate_document_attempts` is incremented |

Grading asserts `duplicate_document_attempts == 0`. No second document is ever created; the counter
is the evidence.

### The posting that was booked and then failed

With fault injection on, one posting request per run is **applied and then answered `500 INTERNAL`,
`retriable: true`**. The documents are booked, their numbers are assigned, and the response you were
supposed to read is gone. This is the only reason an idempotency key exists on this endpoint, and it
is the difference between the two behaviors above:

- retry the identical request with **the same key**, and you replay the stored response, document
  numbers and all. One document. Nothing to reconcile.
- retry it with a **fresh key**, and you post the same `external_reference` again. You get the
  original document number back, so nothing is double-booked, but `duplicate_document_attempts`
  moves and the assertion above fails.

The fault fires on the first attempt of a given business request and never on a retry, so a client
that retries correctly always gets through. It is keyed on what the request is about and not on your
key, so choosing a new key does not buy you a fresh roll of the dice.

### A 503 that means never

One scenario answers a single request with **`503` and `"retriable": false`**, code
`PERMANENT_TRANSPORT_FAILURE`. The status line looks like something to retry and the flag says it is
not, and **the flag is the contract**: every error body on both services carries it precisely so no
client ever has to infer retriability from a status code. A 503 is retriable when the token bucket is
empty and permanent when the failure is.

What to do with it: spend no second attempt, record the affected work as **failed** and not as
rejected - the ERP never answered, so nothing was decided about those invoices - leave them pending
in the twin for the next run, and exit 2. Retrying it spends attempts, and on a bad day quota, on a
request that cannot succeed.

### An empty page is not the end of a collection

One scenario answers one page of every paginated collection with `"returned": 0`, `"records": []`,
**`"has_more": true`** and a usable `next_cursor` - your own cursor, echoed back, so the next request
returns the page this one withheld. Real APIs do this when the records behind a cursor were filtered
after it was issued.

`has_more` is the authority on whether a collection continues. A client that treats an empty page as
a terminator loses every record after it, silently, and the loss looks like missing master data
rather than like a paging bug.

## Error body

Identical on both servers, on every error:

```json
{"code":"RATE_LIMITED","message":"token bucket empty","retriable":true,
 "retry_after_hint_ms":50,"details":{}}
```

`retriable` is machine-readable and **authoritative**. Retrying a `retriable:false` response is a
graded mistake; not retrying a `retriable:true` one wastes work you were meant to recover.
`retry_after_hint_ms` appears on 429s only and is the exact refill interval.

| Status | Code | `retriable` |
|---|---|---|
| 400 | `MALFORMED_BODY`, `INVALID_CURSOR`, `IDEMPOTENCY_KEY_REQUIRED` | false |
| 401 | `TOKEN_MISSING`, `TOKEN_INVALID` | false |
| 401 | `TOKEN_EXPIRED` | true |
| 403 | `CLIENT_QUARANTINED` | false |
| 404 | `NOT_FOUND`, `SUPPLIER_NOT_FOUND` | false |
| 409 | `IDEMPOTENCY_KEY_REUSED` | false |
| 415 | `UNSUPPORTED_CONTENT_TYPE` | false |
| 429 | `RATE_LIMITED` | true |
| 500 | `INTERNAL` | true |
| 503 | `SERVICE_UNAVAILABLE` (injected) | **true** |
| 503 | `QUOTA_EXHAUSTED` | **false**, for the rest of the run |

The two 503s are the one place where reading `retriable` instead of the status code matters.

## Record shapes

`supplier`, plus `legacy_id` which the canonical record does not carry:

```json
{"supplier_number":"0000417","name":"Widmer Hydraulik AG","country":"LI","currency":"CHF",
 "iban":"CH3400762000003302223","vat_number":"CHE-961.843.398 MWST","payment_terms_days":30,
 "blocked":false,"change_seq":111979,"legacy_id":417}
```

`purchase_order` (note the space-padded `po_number`, which is planted, not a bug):

```json
{"po_number":" 4500002042 ","supplier_number":"0000003474","company_code":"CH10","currency":"CHF",
 "status":"OPEN","order_date":"2026-02-15","cost_center":"CC-3283","change_seq":111980}
```

`purchase_order_line` (`unit_price` at 4 fraction digits, `quantity` at 3, `uom` from
`EA PCE STK CTN KG TON L M`):

```json
{"po_number":" 4500002042 ","line_no":"00010","material":"MAT-100294","description":"Position 00010",
 "quantity":"5.000","uom":"STK","unit_price":"7.1128","currency":"CHF","gl_account":"0006460",
 "cost_center":"CC-3283","change_seq":119961}
```

See "Known inconsistencies" in `docs/twin-api.md` before you relay purchase order lines.

`uom_conversion`: `{"material":"","alt_uom":"CTN","numerator":12,"denominator":1,"base_uom":"EA"}`.

## The file export drop

The ERP writes the subsidiary's nightly deliveries into `--erp-export-dir`. Per delivery: the data
file first, then an **empty `.ok` sentinel written last**. The sentinel means "these bytes are
complete", so a data file without its sibling must not be read.

```
KRED_0100_20260416_001.txt   KRED_0100_20260416_001.ok
KRED_0200_20260416_001.txt   KRED_0200_20260416_001.ok
```

`KRED_<mandant>_<YYYYMMDD>_<NNN>.txt`; mandant `0100` is company code CH10, `0200` is CH20. The
bytes are CP1252, CRLF, semicolon-separated: relay them **unparsed** under
`profile: kredexp-2.1`. A reseed removes only files whose names this service produces, so your own
state file in the same directory survives.

## The admin surface is ours, not yours

`/erp-admin/v1` requires the header `X-Admin-Token`, costs nothing, counts nothing, and **your
connector is never given the token.** It is listed here so you can read it yourself while
developing, and so you know what the grader reads. The grader asserts your connector made zero
requests to it.

| Path | What |
|---|---|
| `GET /erp-admin/v1/metrics` | the full request accounting of the run, including `duplicate_document_attempts` and `permanent_injected` |
| `GET /erp-admin/v1/documents` | every AP document booked |
| `GET /erp-admin/v1/idempotency-keys` | the retained keys |
| `GET /erp-admin/v1/requests` | the request log: sequence, endpoint, signature, status, virtual cost, quota used, injected fault marker, and the `retriable` flag the answer carried |
| `GET /erp-admin/v1/soap-calls` | the SOAP call log with faults and severities |
| `GET /erp-admin/v1/state/digest` | the content digest of everything the ERP holds, per component |
| `GET /erp-admin/v1/credentials` | the seed-derived credentials, which is why no credential fixture exists in the tree |
| `POST /erp-admin/v1/reset` | new run state, same dataset, export drop rewritten |
| `POST /erp-admin/v1/seed` | load a scenario at a seed and restart the run |

A missing or wrong admin token is `403 ADMIN_TOKEN_REQUIRED`. The request log is the single fastest
way to see an N+1 pull or a retry storm in your own connector.
