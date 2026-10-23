# Challenge specification

Authoritative. Everything the machine grades is in this file or in `docs/rules.md`. What the
reviewers score in the decision log is in `docs/templates/DECISIONS.md`. Nothing else is graded.

Companion documents, all reference material you look things up in rather than read front to back:
`docs/erp-api.md` and `docs/erp-openapi.yaml` (the ERP surface), `docs/twin-api.md` (the twin's file
inbox, matching, the exception queue, the outbox and the acks), `docs/transcripts.md` (real requests
and responses, captured from the running servers rather than written by hand: the endpoints your
connector needs, and the failures worth meeting before you meet them, including the replay and the
duplicate). Read the whole OpenAPI file at least once: one route form is documented only there.

---

## 1. The two systems and what each owns

| | `erp` (:8082) | `miniblp`, the twin (:8081) |
|---|---|---|
| Role | the customer's system of record | BLP's digital twin |
| Owns | suppliers, purchase orders, purchase order lines, UoM conversions, AP documents | the append-only revision store, provenance, the captured invoices, matching, exceptions, proposals, receipts |
| Read surface | REST `/erp/v1` | `GET /v1/exceptions`, `GET /v1/outbox/proposals`, mirrored outbox files |
| Write surface | `POST /erp/v1/ap/documents`, `POST /erp/v1/ap/documents:batch` | file drop `inbox/incoming/`, `POST /v1/match`, `POST /v1/outbox/acks` |
| Money on the wire | decimal string `"1234.56"`, dates `YYYY-MM-DD` | decimal string, dates `YYYY-MM-DD` |
| Auth | bearer token from `POST /erp/v1/auth/token` | bearer token from `POST /v1/auth/token` |

You write **the connector between them**, in any language. Both services are ours and are not to be
modified; the grader rebuilds them from a pristine tree with only your allowlisted files overlaid.

Four things are already in the twin when your connector starts, loaded by `POST /admin/v1/seed`:

- the **1'400 cost centers** and the **15 UoM conversion rows**, standing in for "another
  integration loaded them last year",
- the group's **exchange rates**, loaded by another integration as well,
- the night's **supplier invoices**, which BLP captured itself from the subsidiary's nightly export.
  They sit in the twin as batches named `capture-KRED_<mandant>_<date>_<run>` with source system
  `blp-capture`, and their matching waits until you call `POST /v1/match`.

You do not pull or deliver any of those. The master data the matching needs, you put there.

The twin converts foreign currency itself: the half-open interval match on the posting date, the
conversion, and the one rounding. A proposal carries the converted amount, the rate, the factor and
the source amount, and you post them as they are.

---

## 2. One run, in order

One invocation of your connector does all of this, in this order.

1. **Master data.** Pull suppliers, purchase orders and purchase order lines from the ERP's
   paginated REST lists. Use the watermark you persisted (`changed_since`) so a second night pulls
   only what changed.
2. **Deliver.** Write them into the twin's file inbox as canonical records (datasets `supplier`,
   `purchase_order`, `purchase_order_line`), in any of the four wire formats, and wait for the
   receipt's `DONE`. Persist a watermark per dataset from `max_change_seq` once the receipt
   confirms the batch.
3. **Match.** `POST /v1/match` with your run id. The twin matches every captured invoice still
   waiting, against the master data it holds now, in capture order, opens an exception for each one
   that fails a rule and emits a proposal for each one that passes. Call it after the receipt says
   `DONE`: called before the master data arrives, it matches against an empty master and every
   invoice becomes an exception. A second call matches nothing new and answers zeros.
4. **Exceptions.** Read `GET /v1/exceptions` to the end of its cursor. It is what a human must look
   at, and it is the content of `exceptions.csv`.
5. **Post.** Read `GET /v1/outbox/proposals?status=pending` and post each proposal to the ERP with a
   stable `Idempotency-Key`. Batch: up to 200 items per request. Record every outcome.
6. **Acknowledge.** Send the outcome of every posting back to the twin with `POST /v1/outbox/acks`
   (or as an `outbox_ack` data file in a batch), so the audit chain closes on the ERP document
   number. A posting you acknowledge as `rejected` opens an `E_ACK_REJECTED` exception, which
   belongs in `exceptions.csv` as well.
7. **Report.** Write `run.json`, `postings.csv` and `exceptions.csv` into `--report-dir`, and exit
   with the right code.

The grader drives `POST /admin/v1/inbox/scan` on the twin while your connector runs. There is no
timer and no watcher: a batch you drop into `incoming/` is picked up on the next scan, never sooner,
and the receipt's `DONE` sentinel is your signal that it was.

---

## 3. The virtual clock. Read this before you write a retry loop

**There is no wall clock on either server.** Neither one calls `time.Now()`. Time is a monotone
integer, `virtual_ms`, that moves only when a request declares a cost, and it moves by exactly that
cost. Nothing on either server sleeps, polls or expires in real time.

The token bucket refills **from that same virtual clock**. So:

- a 250-record ERP list page costs 1 token, advances `40 + 0.5 * 250 = 165` vms, and earns
  `165 / 50 = 3.3` tokens back. **Net positive.**
- a single-record ERP GET costs 1 token, advances 25 vms, earns 0.5 tokens back. **Net negative.**

Batching is therefore monotonically rewarded, and pulling a 12'000-record master one record at a
time runs the bucket dry no matter how patiently you wait.

**Sleeping buys nothing.** Real seconds do not refill the bucket, do not expire a token and do not
advance a fiscal period. `Retry-After: 1` and `retry_after_hint_ms` are advisory shapes of a real
API; honoring them costs you nothing and gains you nothing, and no assertion measures whether you
did. The only way to get a token back is to make a request that earns more than it costs, or to
make fewer requests.

Virtual costs, published verbatim. Fractional costs accumulate in hundredths of a vms, so no float
appears anywhere.

| Endpoint class | Cost |
|---|---|
| ERP list page (`erp_list_page`) | 40 vms + 0.5 vms per record returned |
| ERP single GET (`erp_single_get`) | 25 vms |
| ERP single document POST | 60 vms |
| ERP batch document POST | 60 vms + 8 vms per item |
| ERP auth token | 10 vms |
| twin match (`twin_match`) | 20 vms |
| twin exception queue page (`twin_exceptions_list`) | 30 vms + 0.1 vms per record |
| twin outbox page | 30 vms + 0.1 vms per record |
| twin acks | 20 vms + 0.05 vms per ack |
| twin auth token | 10 vms |
| any 429 or quarantine 403 | one refill interval of that server |

Admin surfaces (`/admin/v1`, `/erp-admin/v1`), `/healthz` and both UIs cost nothing and count
nothing. A batch you drop into the twin's inbox is a file, not a request, so it costs nothing
either. Everything else counts, including 429s, 401s and token refreshes.

---

## 4. Every number you are graded against

### 4.1 Rate limiting, quota, tokens

| | ERP | twin |
|---|---|---|
| Bucket capacity (also its initial fill) | 40 | 30 |
| Refill | 1 token per 50 vms | 1 token per 40 vms |
| `retry_after_hint_ms` on a 429 | 50 | 40 |
| Access token lifetime | 250 authenticated requests **or** 900'000 vms, whichever first | same |
| Consecutive 429s on one endpoint that arm the quarantine | 20 | 20 |
| Quarantine length once armed | 200 quota units | 200 quota units |

Empty bucket is `429 RATE_LIMITED`, `retriable:true`, `Retry-After: 1`. An expired token is
`401 TOKEN_EXPIRED`, `retriable:true`: refresh it. A wrong secret is `401 TOKEN_INVALID`,
`retriable:false`. Exceeding the hard quota is `503 QUOTA_EXHAUSTED`, `retriable:false`, for the
rest of the run; that is the one 503 you must never retry.

### 4.2 Caps

| Cap | Value | Behavior above it |
|---|---|---|
| ERP list `limit` | default 100, **max 250** | clamped silently to 250, response carries `X-Limit-Clamped: 250`. Not an error |
| ERP batch posting items | 200 | `400 MALFORMED_BODY`, reason `too_many_items` |
| Twin acks per request | 1'000 | `413 BATCH_TOO_LARGE` |
| Twin outbox `limit` | default and max 500 | clamped silently, no header |
| Twin exception queue `limit` | default and max 500 | clamped silently, no header |
| Importer diagnostics per file | 1'000 | the rest are replaced by one `diagnostics_truncated` warning |

### 4.3 Hard request quota per scenario

Counted per server, over every non-admin request including 429s and token refreshes.

| Scenario | ERP quota | Twin quota |
|---|---|---|
| S0 | 200 | 120 |
| S1 | 1'200 | 500 |
| S2 | 1'400 | 640 |
| H1 to H5 | 400 each | 240 each |

S2 runs the connector **twice**, so its budget covers both invocations: a cold load of the S2 base
in the first (the reference connector spends about 220 ERP requests on it with chaos on, reading 250
records a page) and a delta in the second (about 20). The quota is a ceiling on chattiness, not a
watermark detector: a record-at-a-time client runs out of it long before it finishes, and a
250-a-page client has room to spare. Whether you persisted a watermark is graded directly instead,
by `full_load == false` and the advanced watermark in the robustness block, because a quota loose
enough for an honest second cold load cannot also prove you avoided one.

### 4.4 Dataset sizes

| Dataset | S0 | S1 | S2 |
|---|---|---|---|
| suppliers | 240 | 12'000 | 12'000 base, 900 re-issued with a higher `change_seq` |
| cost centers (already in the twin) | 60 | 1'400 | 1'400 |
| purchase orders | 160 | 8'000 | 8'000 base, 400 changed |
| purchase order lines | 520 | 26'400 | 26'400 base, 1'300 changed |
| exchange rate rows (already in the twin) | 97 | 488 | 488 + 48 new |
| invoices BLP captured (already in the twin) | 100 (+300 lines) | 5'000 (+14'800 lines) | + 1'100 new (+3'300 lines), 40 re-sent unchanged, 12 re-sent with a changed gross amount |
| UoM conversion rows (already in the twin) | 15 | 15 | 15 |

Roughly 14 percent of invoices are built to land in the exception queue. BLP captures the two
company codes as separate batches: in S1 the CH10 capture carries 4'537 invoices and 13'434 lines,
the CH20 capture 463 and 1'366.

### 4.5 Scenarios

| Id | Visibility | What it is |
|---|---|---|
| S0 | public | smoke gate: tiny dataset, no chaos, zero points. Get green here first |
| S1 | public, scored | cold load: full master pull, the captured invoices matched, proposals posted, acks closed |
| S2 | public, scored | delta with chaos: watermark-only pull, injected 429 and 503, one applied-then-500 **posting**, and a second captured delivery with re-sent and amended invoices |
| H1 to H5 | hidden | the same night runs twice; a wholesale-rejected batch and `BATCH_ID_CONFLICT`; the master data's edges; an acknowledgment refused permanently after every posting succeeded, and the night after under a fresh run id; a 503 that means never. They re-combine documented mechanisms only |

---

## 5. Determinism, and what "chaos" means

Everything both servers do is a pure function of `(scenario, seed, the logical requests you make)`.
There is no randomness, no clock, no map-iteration order and no filesystem-order dependence.

Fault injection is **content-addressed**, not sequence-keyed:

```
sig     = METHOD + " " + routeTemplate + "|" + canonicalSalientParams
attempt = attemptCounter[sig]++                       // per signature, per run
h       = fnv64a(seed, sig, attempt)
inject429 = (attempt == 0) and h % 23 == 0
inject503 = (attempt == 0) and h % 37 == 0            // always retriable
applyThenFail = (attempt == 0) and h % 17 == 0        // one posting request per run
```

`canonicalSalientParams` is the parameter subset that identifies the logical request: for a list
page the dataset plus `changed_since` plus the **decoded** cursor position, never the opaque cursor
string; for a posting the sorted set of the items' `external_reference`. Never the wall clock,
never the request sequence, never a goroutine or connection identity.

Three consequences you can rely on:

1. **An injected fault always succeeds on the retry of the same logical request.** A fault fires
   only on a signature's first delivery.
2. A parallel connector and a serial connector meet exactly the same fault set.
3. Running the same submission five times produces byte-identical scores.

The token bucket is a real limiter and is not subject to the first-delivery rule: an empty bucket
429 repeats until the bucket refills. `--no-chaos` disables injection and changes nothing else, so
the state digest is identical with and without it.

The **state digest** (`GET /admin/v1/state/digest`) is the primary grading assertion. It hashes only
`{natural key, payload}` per record, per dataset, in sorted order. Deliberately excluded: version
numbers, sequence numbers, provenance, batch ids, format and every counter. Also excluded whole:
`outbox_ack`, the outbound acknowledgment journal, whose payload records what one run did: your run
id, how many attempts it took, the status the ERP answered with, and the document number the ERP
assigned in arrival order. The same logical data delivered in any of the four formats therefore
produces a byte-identical digest, and so do two correct connectors built on different concurrency,
with different retry policies, run under different ids. That is what makes grading format-blind and
architecture-blind.

---

## 6. The connector contract

Fixed so grading is language-agnostic. The grader invokes `connector/run.sh` with exactly these
arguments; `run.sh` may be a thin wrapper around a compiled binary, an interpreter or a Docker
image.

```
<connector> run --run-id ID
                --erp-base-url URL --twin-base-url URL
                --twin-drop-dir PATH
                --state-dir PATH --report-dir PATH
                [--config FILE] [--no-chaos]
<connector> --version
```

`--run-id` is supplied by us and must **never** be invented by the connector. A scenario can invoke
your connector more than once, and both shapes occur: a run that resumes an interrupted one is given
**the same** run id, and the next night's run is given **a fresh** one, because a schedule does not
know that last night ended badly. Neither is permission to post anything twice. What a second
invocation may re-post is decided by your own record of what you booked and by the key recipe below,
never by the run id.

Credentials arrive as environment variables, never on the command line:

```
ERP_CLIENT_ID   ERP_CLIENT_SECRET
TWIN_CLIENT_ID  TWIN_CLIENT_SECRET
```

Admin tokens are never given to the connector. `/erp-admin/v1` and `/admin/v1` are ours: reading
them from your connector is a graded mistake, and the grader asserts zero connector traffic there.

Exit codes:

| Code | Meaning |
|---|---|
| `0` | clean run |
| `2` | completed with business exceptions. **This is the normal outcome** in an exception-driven product and is explicitly not a failure |
| `3` | hard failure |
| anything else | fails the scenario |

`connector/setup.sh` runs **once** before the scenario series, with network access allowed, so you
may install dependencies there. `connector/run.sh` runs once per scenario with no network beyond
the two local servers. Those two scripts are the entire contract: the harness builds nothing else
and knows nothing about containers, so if you want one, build the image in `setup.sh` and make
`run.sh` the `docker run` wrapper. See `connector/README.md` for the two details that catch people
(loopback networking and the bind-mounted directories).

---

## 7. The three report files

Written to `--report-dir` on every run, including a run that ended in exceptions.

### `run.json`

One object, these keys:

```
run_id, connector_version, exit_code, channel_used, formats_used[],
erp_requests, erp_429s, erp_5xx_retried, twin_requests, twin_429s,
batches_written, records_read, records_ingested, records_rejected,
records_skipped_unchanged, proposals_read, proposals_posted, proposals_rejected,
proposals_pending, duplicate_document_attempts,
watermark_before{}, watermark_after{}, full_load
```

### `postings.csv`

Fixed header, one line per document, carrying the whole audit chain:

```
proposal_id,invoice_twin_id,supplier_number,supplier_invoice_number,source_batch_id,
source_file_or_chunk,source_line_or_ordinal,idempotency_key,erp_document_number,
erp_status,http_status,attempts,idempotency_replay
```

The three `source_*` columns come down the outbox row of each proposal: the capture batch, the file
and the line the invoice came from. The audit chain check requires them non-empty.

### `exceptions.csv`

Fixed header, the same columns `GET /v1/exceptions` returns:

```
subject_key,subject_type,stage,code,field,message,source_batch_id,
source_file_or_chunk,source_line_or_ordinal
```

Graded on the **blocking** set only. Extra informational rows with `stage="info"` are explicitly
allowed and ignored by grading, so surfacing a warning here costs nothing.

### Per-run reconciliation, all six checked

These hold **per run**, so they are true on a delta run too:

1. `records_read == records_ingested + records_rejected + records_skipped_unchanged`
2. `proposals_read == proposals_posted + proposals_rejected + proposals_pending`
3. the sum of `postings.csv` amounts for `erp_status=posted` equals the sum the twin holds for the
   proposals acknowledged in this run, to the cent
4. every `postings.csv` row resolves to exactly one twin invoice and one ERP document number
5. every blocking exception open in the twin at the end of the run appears in `exceptions.csv`, and
   vice versa
6. `run.json` counters agree with the servers' own metrics within +/- 2. A disagreement is reported
   as a finding and costs no points; a write-up that **contradicts** the counters is a
   disqualifier

---

## 8. Idempotency keys

**The property, which is what is asserted:** the key is a pure function of the proposal and stable
across runs. Posting the same pending proposal in two runs yields exactly one ERP document.

**A conforming recipe, optional:**

```
blp:{tenant}:{proposal_id}:{proposal_content_hash[0:16]}
```

`proposal_content_hash` comes down the outbox row, so the recipe needs no state of your own.

**Stated explicitly: do not fold `run_id` into the key.** Doing so double-posts on resume. The ERP's
`duplicate_document_attempts` counter catches it, and grading asserts that counter is `0`, together
with the twin's own `duplicate_apply_attempts`. A UUID per attempt fails for the same reason.

The ERP also indexes `(tenant, external_reference)` independently of your key. A second posting of
an `external_reference` that already produced a document, arriving under a *different* key, is
answered with the **original** document number, `duplicate:true`, `idempotency_replay:false`, and
increments the counter. No second document is created, because we refuse to build a corrupting
mock, but the counter is the evidence.

---

## 9. What is scored, and what is not

Scored, 100 points:

| Block | Points |
|---|---|
| Public scenarios, S1 and S2 each: state digest 8, exception set 5, `proposals_pending == 0` 3, the exact ERP document set 2, in budget 2 | 40 |
| Hidden scenarios H1 to H5, 3 each | 15 |
| Robustness. S1: receipt closure 1, every record in one terminal state 1, no admin path 1, no credential in any artifact 2, no IBAN in any artifact 2. S2: watermark advanced and `full_load == false` 2, no duplicate document 4, retry discipline 1, never quarantined 1 | 15 |
| Traceability: the audit chain of 25 sampled invoices, forwards and backwards 6, the reports reconcile 2, one document per external reference 2 | 10 |
| Decision log: dimensions engaged, 2 each, at most 10 (five of six); the customer's request 5; an honest stop line 5 | 20 |

The rows sum to 100. Every block except the decision log is machine-checked; the decision log is
scored by two reviewers against `docs/templates/DECISIONS.md`.

**Not scored, so stop optimizing for it:**

- **No wall-clock timeout.** A safety timeout per invocation exists only so a hung grading run does
  not block the queue. It is sized per scenario, 5 minutes on the smoke gate and 15 on S1 and S2,
  and every public scenario's own value is in its file under `timeout_s`. Hitting it is reported as
  a harness event, not a score.
- **No memory ceiling.** A JVM or .NET baseline never costs a point.
- **429s are informational.** They are counted and shown, never scored. The scored efficiency
  assertion is the order-independent request budget, total and per route.
- **Third-party libraries in your connector are fine**, in whatever language you choose. The
  standard-library-only rule binds our Go code, not your connector.
- **Which wire format you use is not scored.** JSON, NDJSON, XML and CSV all reach the same digest.
  Pick what you can finish.
- Exit code `2` is not a failure.

Automatic disqualifiers, regardless of total: a write-up that contradicts the server counters, and
posting unmatched invoices because a customer email asked you to.

On that last one: `docs/customer/CSM-ticket-4412.md` quotes the customer saying "Bucht einfach
alles, den Rest räumen wir nachher im SAP auf" ("just post everything, we will clean up the rest in
SAP afterwards"). `docs/SLA.md` says the opposite. **Obey the SLA**, leave the unmatched items as
exceptions, and name the conflict plus a proposed resolution in your decision log. Being asked to do
the wrong thing by a well-meaning customer is a weekly event in this job.

---

## 10. Hard stop

**Plan for about 3 hours. Stop there.** Then commit, and fill in the "Where I stopped" and "What I
would do next" sections of `DECISIONS.md`.

The stop line carries real points. An honest stop line keeps a 38-to-49 point submission in the
interview pool; a submission that hides an unfinished half does not. Naming the thing you did not
get to, and how you would have done it, is worth more than a rushed half-implementation of it.

Suggested order if you are short on time: get S0 green end to end first, then the correctness of
posting and acknowledging, then the delta and the watermark, then `DECISIONS.md`. A connector that
posts 40 invoices correctly and parks the rest with the right reason scores far better than one that
posts all of them wrong.

The single strongest signal in this whole exercise: **finding something we got wrong.** There is at
least one such thing in the shipped landscape. `docs/twin-api.md` section "Known inconsistencies"
names the ones we already know about; if you find another, write it down.
