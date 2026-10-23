# The twin: file inbox, matching, exceptions, outbox, acks

Reference. Real captured transcripts are in `docs/transcripts.md`; the two for matching and the
exception queue are illustrative.

Base: `http://127.0.0.1:8081` for humans; the grader passes `--twin-base-url` and
`--twin-drop-dir`. Auth is `POST /v1/auth/token` with `TWIN_CLIENT_ID` / `TWIN_CLIENT_SECRET`, then
`Authorization: Bearer <access_token>`; the response is the same shape as the ERP's plus
`"token_type":"Bearer"`. Bucket 30, refill 1 token per 40 vms, token lifetime 250 requests or
900'000 vms.

**The wire format is yours.** JSON, NDJSON, XML and CSV all carry records into the file inbox. The
state digest excludes format, provenance and every counter, so the same logical data delivered in
any of them hashes identically. Pick whichever you can finish; none is scored. It also excludes your
run id, your retry counts and the order you posted in, so two correct connectors built on different
concurrency reach the same digest.

## 0. What the twin holds before your run

`POST /admin/v1/seed` (the grader's seed step, `make up` locally) loads what other systems put into
the twin before your connector starts:

- the cost centers and the unit-of-measure conversions, from another integration,
- the group's exchange rates, from another integration,
- the night's supplier invoices, which BLP captured itself from the subsidiary's nightly export.
  They arrive through the same inbox as batches named `capture-KRED_<mandant>_<date>_<run>`, with
  source system `blp-capture`, so their receipts sit in `receipts/` next to yours. They are not
  yours to count in `run.json`.

The captured invoices are parsed and stored, but **not matched**: their matching waits until you
call `POST /v1/match` (section 2), so it runs against the master data you delivered.

---

## 1. The file inbox

### 1.1 Directory contract

Root is `--twin-drop-dir`:

```
incoming/     the ONLY directory your connector may write
processing/   twin-owned, in flight
processed/    twin-owned, completed batches, files preserved
rejected/     twin-owned, wholesale-rejected batches, files preserved
receipts/     twin-owned, one directory per batch
../outbox/    twin-owned, proposal exports per run id
```

Writing into any twin-owned directory is `FOREIGN_WRITE_DETECTED` and fails the run. The twin
creates all five directories at startup; do not delete them.

### 1.2 The atomic drop protocol

A batch is one directory `incoming/<batch_id>/` containing exactly one `manifest.json` plus its data
files. Publication must be atomic:

```
1. write everything into  incoming/.staging-<batch_id>/
2. fsync
3. one rename:            incoming/.staging-<batch_id>  ->  incoming/<batch_id>
```

The scanner ignores every name starting with `.` and every name ending in `.tmp` or `.part`, so if
you cannot rename atomically those suffixes are a documented second way to publish safely.

A batch id (and a run id) is 1 to 128 characters of ASCII letters, digits, dot, dash or underscore,
not starting with a dot. They become directory names, so anything else is `MANIFEST_INVALID`.

A batch directory seen on **two consecutive scans** with no `manifest.json` in it is
`MANIFEST_MISSING`. Two scans and not one, because the first sighting may be a publication still in
flight. "Later" is the twin's monotone scan counter, never a timer.

Scanning happens only on `POST /admin/v1/inbox/scan`, which the grader drives while your connector
runs. There is no watcher and no goroutine: a batch is picked up on the next scan and never sooner.
Batches are processed in ascending directory-name order.

### 1.3 The manifest

```json
{"manifest_version": "1",
 "batch_id": "b-0001",
 "sequence": 1,
 "producer": "acme-connector/1.4.2",
 "run_id": "run_7f3c1a",
 "tenant": "acme-ch",
 "source_system": "erp-prod",
 "mode": "upsert",
 "full_load": false,
 "watermark": {"supplier": "146329", "purchase_order": "146348"},
 "on_error": "continue",
 "profile": "blp-canonical-v1",
 "files": [
  {"path": "suppliers.csv", "dataset": "supplier", "format": "csv",
   "profile": "blp-canonical-v1", "encoding": "UTF-8",
   "record_count": 3, "sha256": "4a5c1730...",
   "csv": {"delimiter": ",", "quote": "\"", "escape": "double", "header": true,
           "line_ending": "CRLF", "decimal_separator": ".", "thousands_separator": "",
           "date_format": "RFC3339", "null_token": "", "trim": "both"}}]}
```

| Field | Required | Meaning |
|---|---|---|
| `manifest_version` | defaults `"1"` | `"1"` is the only version read. Anything else is `MANIFEST_INVALID` |
| `batch_id` | yes | the replay and conflict key |
| `sequence` | no | your own delivery counter. Part of the manifest hash, so changing it makes a re-delivery a conflict |
| `producer` | no | free text, echoed on the receipt |
| `run_id` | no | names the run report and the outbox subdirectory |
| `tenant` | no | echoed; namespaces the idempotency store |
| `source_system` | no | echoed into every record's provenance |
| `mode` | defaults `upsert` | `upsert` or `replace_dataset`. `replace_dataset` **requires** `full_load: true` and surfaces as `full_load` on the run report. That is the full-reload detector |
| `full_load` | defaults `false` | see above |
| `watermark` | no | dataset to watermark value, echoed on the run report. This is how `watermark_advanced` is asserted |
| `on_error` | defaults `continue` | `continue` or `abort_batch` |
| `profile` | no | a batch-level default for the files' profile |
| `files[]` | yes | one entry per data file |

Per file entry:

| Field | Required | Meaning |
|---|---|---|
| `path` | yes | a plain name inside the batch directory. No `/`, no `\`, not `.`, not `..`, not `manifest.json`, no duplicates |
| `dataset` | yes in practice | one of `supplier`, `cost_center`, `purchase_order`, `purchase_order_line`, `fx_rate`, `invoice`, `invoice_line`, `outbox_ack`. You deliver `supplier`, `purchase_order`, `purchase_order_line` and, if you acknowledge by file, `outbox_ack`. An unknown name is `MANIFEST_INVALID` |
| `format` | inferred from the extension | `csv`, `xml`, `json`, `ndjson` |
| `profile` | defaults to the batch `profile`, then `blp-canonical-v1` | `blp-canonical-v1`, the twin's own record shape. A profile the twin cannot read is `UNKNOWN_PROFILE`, a whole-batch reject |
| `encoding` | defaults `UTF-8` | `UTF-8`, `UTF-8-BOM`, `ISO-8859-1`, `windows-1252`. An XML prolog **wins over** the manifest; a disagreement is still reported as `ENCODING_CONFLICT`. Declared UTF-8 with invalid bytes is per-record `E_ENCODING` at the offending record, never a silent U+FFFD |
| `record_count` | **mandatory** | absent is `MANIFEST_INVALID`; a value that disagrees with what parsed is `RECORD_COUNT_MISMATCH`, a whole-file reject. Absent is a different fact from zero, so an empty file declares `0` |
| `sha256` | **mandatory** | 64 lowercase hex digits over the file's bytes. A mismatch is `CHECKSUM_MISMATCH`, a whole-file reject |
| `csv` | no | the dialect |

**`record_count` and `sha256` are verified before a single record is applied.** That is the
truncated-transfer guard: a half-transferred file cannot reach the ledger.

`on_error: abort_batch` is exact, because every file of a batch is verified and parsed before the
first record is applied, so an aborted batch leaves no partial state.

### 1.4 Batch replay versus BATCH_ID_CONFLICT

The manifest hash is taken after defaults are filled in, so two deliveries that differ only in an
omitted default are the same batch.

| Second delivery of a known `batch_id` | Result |
|---|---|
| **same** manifest hash | accepted as a **no-op replay**: batch status `replayed`, `replay: true`, `accepted` 0, every record counted under `replayed`, no outbox emission, an identical receipt |
| **different** manifest hash | `BATCH_ID_CONFLICT`. Batch status `rejected`, nothing applied, the batch moved to `rejected/`. **Never a silent overwrite:** the first delivery stands, because otherwise the twin's history would depend on delivery order |

### 1.5 Receipts, and DONE is the only completion signal

`receipts/<batch_id>/` gets four files:

```
receipt.json    records.csv    rejects.csv    DONE
```

`DONE` is a zero-byte sentinel created **last**, by rename. **`DONE` is the only completion signal.
Polling for `receipt.json` is a documented mistake:** it exists before it is complete.

`records.csv` has this fixed header, one row per record seen:

```
file,line,record_ordinal,dataset,natural_key,outcome,twin_id,version,code,field,message,raw_excerpt_sha256
```

`rejects.csv` has the same header and carries the rejected subset, so a finance team gets one small
file instead of a large one to filter.

Outcomes, exactly one per record seen:

| Outcome | Meaning |
|---|---|
| `accepted` | applied, no findings |
| `accepted_with_warning` | applied, at least one warning |
| `rejected` | not applied, a finding in the data |
| `skipped_unchanged` | the twin already held this exact content, so no new version was created. A provenance entry **is** still appended, so re-delivery stays auditable |
| `replayed` | a record of a byte-identical re-delivery of an already processed batch. Nothing applied |
| `quarantined` | parsed and validated, but not applicable for a reason that is neither the sender's fault nor permanent. Held for an operator rather than dropped |

### 1.6 The closure invariant

Asserted by the twin at receipt-write time and re-asserted by the grader, per file and per batch:

```
seen == accepted + accepted_with_warning + rejected + skipped_unchanged + quarantined
sum(per_file.parsed_records) == counts.seen
```

A violation is `CLOSURE_VIOLATION`, an internal error and never a warning: the whole point of a
receipt is that nothing was dropped silently. `closure_ok` on the receipt says so explicitly.

The receipt also carries `proposals_emitted[]`, `exceptions_opened[]`, the resolved `profiles[]`,
`manifest_sha256`, `received_scan`, `batch_status` and `virtual_clock_ms`.

Batch statuses: `open`, `accepted`, `partially_accepted`, `rejected`, `replayed`, `aborted`.

---

## 2. Matching: `POST /v1/match`

```
POST /v1/match         20 vms, bearer token
Content-Type: application/json

{"run_id":"run_7f3c1a"}
```

The body is optional. The call matches every captured invoice still waiting, against the master data
the twin holds **now**, batch by batch in capture order, with the rules of `docs/rules.md` section 4.
Each invoice it matches ends in exactly one place: a pending proposal in the outbox, or a blocking
exception in the open queue. An unchanged re-send is not matched again. Response `200`:

```json
{"invoices_matched":100,"proposals_emitted":87,"exceptions_opened":13}
```

- **When.** After your master data is in, which means your batch receipt says `DONE`, and before you
  read the outbox. Called before the master data arrives, it matches the invoices against an empty
  master and every one of them becomes an exception.
- **Idempotent.** A second call matches nothing new and answers zeros. Nothing is matched twice, so
  a resumed run may call it again safely.
- **`run_id`.** Send your `--run-id`. The proposals and exceptions the call creates carry it, and
  the outbox mirror goes to `outbox/<run_id>/`. A `run_id` is 1 to 128 characters of ASCII letters,
  digits, dot, dash or underscore; anything else is `400 MALFORMED_BODY`, reason `run_id_invalid`.

On the second night of a scenario, the twin holds the next captured delivery, waiting again, and
the same call matches it against the master data your delta delivered.

---

## 3. The exception queue: `GET /v1/exceptions`

```
GET /v1/exceptions?limit=500&cursor=      30 vms + 0.1/record, bearer token
```

The **open** exception queue, in key order, paged like the outbox:

```json
{"records":[...],"returned":13,"has_more":false,"next_cursor":""}
```

`limit` defaults to and is capped at **500**, clamped silently with no header; a value below 1 or
not a number is `400 MALFORMED_BODY`, reason `limit_invalid`. Follow `next_cursor` until `has_more`
is false. The response honors `Accept`: `application/json`, `application/x-ndjson`,
`application/xml`, `text/csv`.

A row carries these fields, in this order, which is the header of `exceptions.csv`:

```
subject_key,subject_type,stage,code,field,message,source_batch_id,source_file_or_chunk,
source_line_or_ordinal
```

`exceptions.csv` must contain every open blocking exception: the reconciliation compares the two.
The queue is the twin's whole open queue, so on a second night it still lists what the first night
left open. An acknowledgment you send as `rejected` opens one more exception, `E_ACK_REJECTED`
(section 7); read the queue after your acks, or add those rows from your own ledger.

---

## 4. Canonical record shapes

`profile: blp-canonical-v1`. The CSV column orders below are the fixed canonical field orders and
are the same field order the XML and JSON writers emit. All amounts and quantities are decimal
**strings**; all dates are `YYYY-MM-DD`; all keys are strings with significant leading zeros.

**supplier** (key `supplier_number`)
```
supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq
```

**cost_center** (key `code`; already in the twin, you do not deliver these)
```
code,name,company_code,valid_from,valid_to,blocked
```

**purchase_order** (key `po_number`)
```
po_number,supplier_number,company_code,currency,status,order_date,cost_center,change_seq
```

**purchase_order_line** (key `po_number` + U+001F + `line_no`)
```
po_number,line_no,material,description,quantity,uom,unit_price,currency,gl_account,cost_center,change_seq
```

**fx_rate** (key `base` + U+001F + `quote` + U+001F + `valid_from` + U+001F + `rate_type`; already in
the twin)
```
base,quote,valid_from,rate_type,valid_to,rate,rate_factor,sequence,status
```

**invoice** (key `supplier_number` + U+001F + `supplier_invoice_number`; BLP's capture loads these)
```
supplier_number,supplier_invoice_number,company_code,document_type,document_date,receipt_date,
po_number,currency,gross_amount,vat_amount,vat_code,payment_terms_days,discount_raw,discount_days,
cost_center,text,lines
```
`lines` is the embedded line list. It is a field of the JSON and XML shapes and has **no CSV
column**, because a CSV cell cannot hold a list, which is why `invoice_line` exists as a dataset of
its own.

**invoice_line** (key `supplier_number` + U+001F + `supplier_invoice_number` + U+001F + `line_no`)
```
supplier_number,supplier_invoice_number,line_no,gl_account,cost_center,quantity,uom,unit_price,
line_amount,tax_code,currency
```

**outbox_ack** (key `proposal_id`)
```
proposal_id,status,external_document_number,external_revision,external_fiscal_year,
external_posting_date,idempotency_key,run_id,posted_at,attempts,http_status,idempotency_replay,
error,reason
```

Validation, exactly as enforced: keys must be non-empty; enumerations are closed (`company_code`
`CH10`/`CH20`, `document_type` `RE`/`GU`, `status` `OPEN`/`CLOSED`/`CANCELLED`, `rate_type`
`DAILY`/`MONTHLY_AVG`, FX `status` `ACTIVE`/`DELETED`); currencies must be in the fixed table;
dates must be `YYYY-MM-DD`; `uom` must be one of `EA PCE CTN KG G L M STK STD PAU M2 H TON PAL`;
an invoice line's `line_amount` and every booked amount must be exact in the minor units of their
currency, while a purchase order line's `unit_price` is a **rate** and is allowed up to six fraction
digits (`E_MONEY_SCALE` beyond that); an FX `rate_factor` must be positive. `iban` and `vat_number` are not validated. An empty
`invoice_line.cost_center` is **valid on purpose** and produces `ambiguous_field_semantics`.

---

## 5. Known inconsistencies: two defects we found in our own build

Both were real, both were found by running the servers rather than by reading the specification, and
both are **fixed** in what you are given. They are recorded here because the fix is more instructive
than the bug, and because naming a defect in our specification is the strongest single signal in this
exercise: if you find a third one, put it in `DECISIONS.md`.

**1. The canonical `uom` set was narrower than the landscape.** Canonical validation accepted
`EA PCE CTN KG G L M`, while the ERP's purchase order lines and the subsidiary's invoices use `STK`
and `TON` heavily and the invoices also use `PAL`. The same logical line was therefore accepted when
BLP's capture read it, where its lines are embedded, and rejected `E_UOM_UNKNOWN` through the
canonical path. The two ways in did not reach the same state for the same data, which contradicts
the promise this whole document rests on: the same data reaches the same state however it
arrives.

*Fixed by widening the closed set to the units the landscape actually delivers*, listed in section 3.
`TON` and `PAL` are in it deliberately: both are well formed, so both reach the matching engine,
where `TON` converts through the published table and `PAL` has no conversion and raises
`EXC_UOM_UNMAPPABLE`. The alternative, rejecting them at ingest, would have replaced a business
exception a clerk can act on with a parse reject nobody asked for. The lesson survives the fix: the
ERP's vocabulary is still not the twin's, and mapping is still yours to do, but the failure now lands
where a human can act on it.

**2. Every purchase order line the ERP serves failed canonical validation.** `unit_price` was checked
against the currency's minor units, and every unit price the ERP serves carries four fraction digits
(`"7.1128"`). 511 of 520 lines in S0 were rejected `E_MONEY_NOT_INTEGER_MINOR`.

*Fixed by distinguishing a rate from a booked amount.* A booked amount (`line_amount`,
`gross_amount`, anything that hits a ledger) must still be exact in its currency's minor units; a
per-unit price is a rate and is allowed up to six fraction digits, beyond which it is `E_MONEY_SCALE`.
This is the same distinction the FX table already made with `rate_factor`, so the rule became
consistent rather than merely looser.

---

## 6. Outbox

```
GET /v1/outbox/proposals?status=pending&limit=500&cursor=      30 vms + 0.1/record
```

`status` defaults to `pending`; `all` disables the filter. `limit` defaults to and is capped at
**500**, clamped silently with no header. Order is ascending `proposal_id`, which is zero-padded, so
it is also ascending `created_seq`: a proposal's id never changes, so a page boundary cannot skip a
proposal written after the previous page was served. That is what "resume, not restart" needs from a
paginated outbox.

```json
{"records":[...],"returned":2,"has_more":false,"next_cursor":""}
```

The response honors `Accept`: `application/json`, `application/x-ndjson`, `application/xml`,
`text/csv`. A row is **flat and posting-ready**: everything the ERP's document POST needs is here, so
you never fetch the invoice a second time.

```
proposal_id,invoice_key,supplier_number,supplier_invoice_number,invoice_version,
proposal_content_hash,status,company_code,document_type,document_date,posting_date,matched_po,
matched_cost_center,currency,gross_amount,gross_amount_minor,vat_amount,source_currency,
source_gross_amount,fx_rate,fx_rate_factor,vat_code,payment_terms_days,discount_raw,
created_in_run,created_seq,attempts,warnings,erp_document_number,source_batch_id,
source_file_or_chunk,source_line_or_ordinal
```

`proposal_content_hash` is what the recommended idempotency-key recipe hashes. `warnings` is a
comma-joined list. `discount_raw` travels verbatim and is never interpreted. The three `source_*`
columns say where the captured invoice came from: the capture batch, the file and the line. They
feed the same columns of `postings.csv`, and the audit chain check requires them non-empty.

The same pages are mirrored to disk on every emission, under the run id you gave `POST /v1/match`,
so the outbox is readable with **zero HTTP requests**:

```
outbox/<run_id>/proposals-0001.csv
outbox/<run_id>/proposals-0001.json
outbox/<run_id>/proposals-0001.ndjson
outbox/<run_id>/proposals-0001.xml
outbox/<run_id>/DONE
```

Proposal statuses: `pending`, `acknowledged`, `rejected`, `superseded`, `needs_investigation`. A new
decision supersedes an older proposal rather than editing it, so the history says what was proposed
and when it stopped being current. `proposals_pending == 0` is the headline assertion of every
happy-path scenario.

---

## 7. Acks

```
POST /v1/outbox/acks         20 vms + 0.05 per ack, max 1'000, Idempotency-Key required
```

Or deliver the same records as a data file with `dataset: outbox_ack` in any batch. Both routes store
the ack as a record, so a run that acknowledges by file and a run that acknowledges over HTTP reach
the same state and the same digest: the double-post detector cannot depend on which route you chose.

Ack payload, one object per proposal, in any of the four formats:

```json
{"proposal_id":"prp_0000001","status":"posted",
 "external_document_number":"AP-2026-0000001","external_revision":1,
 "external_fiscal_year":2026,"external_posting_date":"2026-03-29",
 "idempotency_key":"blp:acme-ch:prp_0000001:85b70b63e1061536","run_id":"run_7f3c1a",
 "attempts":1,"http_status":207,"idempotency_replay":false,
 "error":"","reason":""}
```

### The full ack state table

| `status` | Condition | `effect` | Proposal after | Finding |
|---|---|---|---|---|
| `posted` | no ack yet | `acknowledged` | `acknowledged`, document number stored, chain closed | none |
| `posted` | same document number as the stored ack | `replay` | `acknowledged`, unchanged | warning `W_ACK_REPLAY` |
| `posted` | **different** document number | `conflict` | `needs_investigation`, the original ack kept, the conflicting one appended to `ack_conflicts` | error `E_ACK_CONFLICT`, blocking exception raised against the proposal. **This is the double-post detector** |
| `rejected` | | `rejected` | `rejected`, invoice back in the open queue | exception carrying the ERP's own code in `details` |
| `failed` | | `retry` | stays `pending`, `attempts` incremented | none. The next run must pick it up: resume, not restart |
| `skipped` | | `skipped` | stays `pending`, with a reason | none |
| unknown status | | | nothing written | `E_ACK_INVALID` |
| unknown `proposal_id` | | `rejected` | none | `E_ACK_UNKNOWN_PROPOSAL` |

HTTP status: `200` when every ack applied, **`207` when some applied and some were rejected**, and
`422` when every one was rejected. The mixed case is the one worth coding for, and it is the same
convention as the ERP's batch posting: read the per-ack `records[]`, never the status alone. The
body carries `acknowledged`, `rejected` and a per-ack `records[]` with `ordinal`, `proposal_id`,
`effect`, `proposal_status`, `erp_document_number`, `attempts`, `errors[]`, `warnings[]` and
`pointer`.

`failed` is the honest answer to a transport error you could not recover: it keeps the proposal
pending so the next run resumes it, and it does not pretend the posting happened. Never send
`posted` with a document number you did not receive.

### An acknowledgment that will never be accepted

One scenario answers the ack request with **`503` and `"retriable": false`**, code
`PERMANENT_TRANSPORT_FAILURE`. The flag is the contract and the status is not, on both services: a
503 is retriable when the token bucket is empty and permanent when the failure is.

The state it leaves is the one worth thinking about. The ERP holds the documents, your ledger holds
their numbers, and the twin still lists those proposals as **pending**, because nothing told it
otherwise. The next run therefore sees work that looks undone and is not. What it must do is answer
from its own record: acknowledge, and post nothing a second time. Posting again is not a disaster
(an idempotency key derived from the proposal makes it a replay and the ERP hands back the original
document number), but it is a run that resumed nothing, and if the key is derived from the run id
instead then the second night is a duplicate the ERP counts against you.

Do not retry it. Record the outcome, exit 3, and let the next run finish the job.

---

## 8. The admin surface is ours, not yours

`/admin/v1` requires `X-Admin-Token`, costs nothing, counts nothing, and your connector is never
given the token. Use it while developing; the grader reads it for assertions.

| Path | What |
|---|---|
| `GET /admin/v1/metrics` | request accounting, records per dataset, proposals by status, open exceptions, `duplicate_apply_attempts`, closure violations |
| `GET /admin/v1/state/digest` | the graded digest, plus per-dataset record counts. The digest covers the seven inbound datasets; `outbox_ack` is stored and reported like any other but never hashed, because its payload carries your run id, your attempt counts and the document numbers the ERP assigned in arrival order |
| `GET /admin/v1/records/{dataset}/{key}` | every revision with its full provenance. This is where you see whether an older version overwrote a newer one |
| `GET /admin/v1/records/raw/{sha256}` | the original source bytes, content-addressed |
| `GET /admin/v1/batches`, `GET /admin/v1/batches/{id}/records?outcome=` | batches and per-record outcomes |
| `GET /admin/v1/runs/{run_id}`, `GET /admin/v1/runs/last` | the run report: scans, batches, counts, `full_load`, `watermark`, proposals, exceptions |
| `GET /admin/v1/exceptions?state=open` | the graded exception queue. The response also returns the closed `codes` list |
| `GET /admin/v1/proposals` | proposals with their acks and ack conflicts |
| `GET /admin/v1/audit/{invoice key, proposal id or ERP document number}` | the whole chain: source file and line, raw bytes by sha256, revisions, proposals, acks, exceptions, in both directions |
| `POST /admin/v1/inbox/scan` | run one scan. Returns the batches finished, what is still pending, any foreign writes, and the digest after the scan |
| `POST /admin/v1/reset` | new run state |
| `POST /admin/v1/seed` | load what the twin holds before a run (section 0): the cost centers, the 15 UoM conversion rows and the exchange rates as `source_system: other-integration`, and the night's captured invoices, waiting for `POST /v1/match`. Without it the twin starts empty |
| `POST /admin/v1/advance` | the next night: the next rate rows and the next captured delivery. The grader calls it between the two nights of S2, together with the ERP's own advance |

Provenance per revision, which is what makes the audit chain work:

```json
{"batch_id":"b-0001","channel":"file","source_file":"invoices.csv","source_line":2,
 "chunk_ordinal":null,"record_ordinal":0,"source_sha256":"db3fa224...",
 "profile":"blp-canonical-v1","format":"csv","encoding":"UTF-8","source_system":"erp-prod",
 "run_id":"run_7f3c1a","received_scan":1,"request_seq":null}
```
