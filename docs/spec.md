# Challenge specification

Authoritative. Everything you are graded on is in this file, in `docs/rules.md`, in `docs/soap.md`
or in `docs/customer/KRED-EXP-2.1.md`. Nothing else is graded.

Companion documents, all reference material you look things up in rather than read front to back:
`docs/erp-api.md` and `docs/erp-openapi.yaml` (the ERP surface), `docs/twin-api.md` (the twin's two
ingest channels, the outbox and the acks), `docs/soap.md` (the one SOAP operation),
`docs/transcripts.md` (real requests and responses, captured from the running servers rather than
written by hand: every endpoint your connector needs, and every failure worth meeting before you meet
it, including the SOAPAction quotes, the chunk gap, the replay and the duplicate). Read the whole OpenAPI file at least once: one route form is documented only there.

---

## 1. The two systems and what each owns

| | `erp` (:8082) | `miniblp`, the twin (:8081) |
|---|---|---|
| Role | the customer's system of record | BLP's digital twin |
| Owns | suppliers, purchase orders, purchase order lines, UoM conversions, FX rates, AP documents | the append-only revision store, provenance, matching, exceptions, proposals, receipts |
| Read surface | REST `/erp/v1`, one SOAP operation, a legacy file export drop | `GET /v1/outbox/proposals`, mirrored outbox files |
| Write surface | `POST /erp/v1/ap/documents`, `POST /erp/v1/ap/documents:batch` | file drop `inbox/incoming/`, REST `POST /v1/ingest/...`, `POST /v1/outbox/acks` |
| Money on the wire | decimal string `"1234.56"`, dates `YYYY-MM-DD` | decimal string, dates `YYYY-MM-DD` |
| Auth | bearer token from `POST /erp/v1/auth/token`; the SOAP channel uses a WS-Security UsernameToken instead | bearer token from `POST /v1/auth/token` |

You write **the connector between them**, in any language, plus **one importer inside the twin**
(`internal/importer/kredexp/`, Go, standard library only). Both services are ours and are not to be
modified; the grader rebuilds them from a pristine tree with only your allowlisted files overlaid.

Two things are already loaded in the twin by `POST /admin/v1/seed`, standing in for "another
integration loaded them last year": the **1'400 cost centers** and the **15 UoM conversion rows**.
You do not pull those. Everything else the twin knows, you put there.

Ownership of the FX table is split on purpose. **The connector** reads the SOAP table, drops
`DELETED` rows, resolves `Sequence` supersession, normalizes the host-locale decimals and rejects
unusable rows, carrying `rate` and `rate_factor` as two separate fields so nothing is lost to
premature division. **The twin** does the half-open interval match on the posting date and performs
the conversion. Both halves are graded.

---

## 2. The four phases of one run

One invocation of your connector does all four, in this order.

**Phase 1, master data.** Pull suppliers, purchase orders and purchase order lines from the ERP's
paginated REST lists, and the exchange rate table from the SOAP operation. Deliver them to the twin
as canonical records (dataset `supplier`, `purchase_order`, `purchase_order_line`, `fx_rate`) over
either ingest channel, in any of the four wire formats. Persist a watermark per dataset from
`max_change_seq` so the next run pulls only what changed.

**Phase 2, the legacy delivery.** The ERP has written the subsidiary's nightly export into
`--erp-export-dir`: `KRED_<mandant>_<YYYYMMDD>_<NNN>.txt` plus an empty `.ok` sentinel written
**last**. A data file without its `.ok` sibling is incomplete and must not be read. Relay those
bytes to the twin **unparsed**, declaring `profile: kredexp-2.1` and dataset `invoice`. The twin
owns the parse: do not parse the legacy format in your connector. There are two deliveries per
seed, mandant `0100` (company code CH10) and `0200` (CH20).

**Phase 3, posting.** Read `GET /v1/outbox/proposals?status=pending` and post each proposal to the
ERP with a stable `Idempotency-Key`. Batch: up to 200 items per request. Record every outcome.

**Phase 4, acknowledgment and reports.** Send the outcome of every posting back to the twin with
`POST /v1/outbox/acks` (or as an `outbox_ack` data file in a batch), so the audit chain closes on
the ERP document number. Write `run.json`, `postings.csv` and `exceptions.csv` into
`--report-dir`. Exit with the right code.

Between phases the grader drives `POST /admin/v1/inbox/scan` on the twin. There is no timer and no
watcher: a batch you drop into `incoming/` is picked up on the next scan, never sooner.

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
| ERP SOAP `GetExchangeRateTable` | 120 vms + 0.2 vms per row |
| ERP single document POST | 60 vms |
| ERP batch document POST | 60 vms + 8 vms per item |
| ERP auth token | 10 vms |
| twin open batch | 30 vms |
| twin records chunk | 20 vms + 0.15 vms per record |
| twin commit / abort | 20 vms |
| twin single PUT | 20 vms |
| twin outbox page | 30 vms + 0.1 vms per record |
| twin acks | 20 vms + 0.05 vms per ack |
| twin auth token | 10 vms |
| any 429 or quarantine 403 | one refill interval of that server |
| ERP `GET ...?wsdl` | 0 vms, but one quota unit |

Admin surfaces (`/admin/v1`, `/erp-admin/v1`), `/healthz` and both UIs cost nothing and count
nothing. Everything else counts, including 429s, 401s and token refreshes.

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
| Twin records chunk | 1'000 records **or** 8 MiB (8'388'608 bytes) | `413 BATCH_TOO_LARGE` |
| Twin acks per request | 1'000 | `413 BATCH_TOO_LARGE` |
| Twin outbox `limit` | default and max 500 | clamped silently, no header |
| Importer diagnostics per file | 1'000 | the rest are replaced by one `diagnostics_truncated` warning |

### 4.3 Hard request quota per scenario

Counted per server, over every non-admin request including 429s and token refreshes.

| Scenario | ERP quota | Twin quota |
|---|---|---|
| S0 | 200 | 120 |
| S1 | 1'200 | 500 |
| S2 | 1'400 | 640 |
| H1 to H7 | 400 each | 240 each |
| X1 (stretch) | 1'350 summed across both invocations | 500 |
| X2 (stretch) | 2'400 | 900 |

S2 runs the connector **twice**, so its budget covers both invocations: a cold load of the S2 base
in the first (the reference connector spends 213 requests on it, reading 250 records a page) and a
delta in the second (about 30). The quota is a ceiling on chattiness, not a watermark detector: a
record-at-a-time client runs out of it long before it finishes, and a 250-a-page client has room to
spare. Whether you persisted a watermark is graded directly instead, by `full_load == false` and the
advanced watermark in the robustness block, because a quota loose enough for an honest second cold
load cannot also prove you avoided one.

### 4.4 Dataset sizes

| Dataset | S0 | S1 | S2 |
|---|---|---|---|
| suppliers | 240 | 12'000 | 12'000 base, 900 re-issued with a higher `change_seq` |
| cost centers (pre-seeded in the twin) | 60 | 1'400 | 1'400 |
| purchase orders | 160 | 8'000 | 8'000 base, 400 changed |
| purchase order lines | 520 | 26'400 | 26'400 base, 1'300 changed |
| FX rows served by SOAP | 120 (weekly intervals) | 600 (daily) | 600 + 60 new |
| invoices in the legacy delivery | 100 (+300 lines) | 5'000 (+14'800 lines) | + 1'100 new (+3'300 lines), 40 re-sent byte-identically, 12 re-sent with a changed gross amount |
| UoM conversion rows (pre-seeded) | 15 | 15 | 15 |

Roughly 14 percent of invoices are built to land in the exception queue. The two seeded deliveries
split those invoices by mandant: in S1 the `0100` file carries 4'537 invoices and 13'434 positions,
the `0200` file 463 and 1'366.

### 4.5 Scenarios

| Id | Visibility | What it is |
|---|---|---|
| S0 | public | smoke gate: tiny dataset, no chaos, zero points. Get green here first |
| S1 | public, scored | cold load: full master pull, one legacy batch, proposals posted, acks closed |
| S2 | public, scored | delta with chaos: watermark-only pull, injected 429 and 503, one applied-then-500 ingest chunk, one applied-then-500 **posting**, re-delivered and amended invoices |
| H1 to H7 | hidden | replay no-op; a wholesale-rejected batch and `BATCH_ID_CONFLICT`; the rate table's edges; the legacy format's edges; a rate table that declares itself truncated; an acknowledgment refused permanently after every posting succeeded, and the night after under a fresh run id; a 503 that means never. They re-combine documented mechanisms only |
| X1, X2 | stretch, zero points | X1 kills your connector with SIGKILL once the ERP holds a document and invokes it again with the same run id: it checks only that you converge, never how much you redid. X2 is the same night with three times the data inside twice S1's request quota, and it reports your peak resident set and your wall time. Both are in `grading/scenarios/`, both are tie-break notes, and `grade selfcheck --stretch` runs them |

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
applyThenFail = (attempt == 0) and h % 17 == 0        // one twin chunk per run
```

`canonicalSalientParams` is the parameter subset that identifies the logical request: for a list
page the dataset plus `changed_since` plus the **decoded** cursor position, never the opaque cursor
string; for a posting the sorted set of the items' `external_reference`; for a twin chunk
`(batch_ref, dataset, chunk_ordinal)`; for the SOAP call the `CompanyCode`. Never the wall clock,
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
numbers, sequence numbers, provenance, batch ids, channel, format and every counter. Also excluded
whole: `outbox_ack`, the outbound acknowledgment journal, whose payload records what one run did -
your run id, how many attempts it took, the status the ERP answered with, and the document number the
ERP assigned in arrival order. The same logical data delivered through any legitimate channel and
format combination therefore produces a byte-identical digest, and so do two correct connectors built
on different concurrency, with different retry policies, run under different ids. That is what makes
grading channel-blind, format-blind and architecture-blind.

---

## 6. The connector contract

Fixed so grading is language-agnostic. The grader invokes `connector/run.sh` with exactly these
arguments; `run.sh` may be a thin wrapper around a compiled binary, an interpreter or a Docker
image.

```
<connector> run --run-id ID
                --erp-base-url URL --twin-base-url URL
                --erp-export-dir PATH --twin-drop-dir PATH
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
SOAP_USERNAME   SOAP_PASSWORD
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
erp_requests, erp_429s, erp_5xx_retried, twin_requests, twin_429s, soap_calls,
batches_written, records_read, records_ingested, records_rejected,
records_skipped_unchanged, proposals_read, proposals_posted, proposals_rejected,
proposals_pending, duplicate_document_attempts,
watermark_before{}, watermark_after{}, full_load,
fx_snapshot_token, fx_correlation_id, fx_truncated
```

### `postings.csv`

Fixed header, one line per document, carrying the whole audit chain:

```
proposal_id,invoice_twin_id,supplier_number,supplier_invoice_number,source_batch_id,
source_file_or_chunk,source_line_or_ordinal,idempotency_key,erp_document_number,
erp_status,http_status,attempts,idempotency_replay
```

### `exceptions.csv`

Fixed header:

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
5. every blocking twin exception opened in this run appears in `exceptions.csv`, and vice versa
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
`duplicate_document_attempts` counter catches it, grading asserts that counter is `0`, and the twin's
`duplicate_apply_attempts` does the same for ingest chunks. A UUID per attempt fails for the same
reason.

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
| Public scenarios S1 and S2: state digest 12, exception set 8, `proposals_pending == 0` plus the exact ERP document set 6, in budget 4 (binary) | 30 |
| Hidden scenarios H1 to H7, 20 in total | 20 |
| The Go task: 4 public fixtures 8, 3 hidden fixtures 8, ambiguity write-up 4 | 20 |
| Robustness: both duplicate counters at 0 (4), receipt closure and no vanished record (3), retry discipline and never quarantined (2), SOAP discipline (1), no admin path (1), no credential and no IBAN in any artifact (2), legacy profile used (1), watermark advanced and `full_load == false` in S2 (1) | 15 |
| Traceability: 25 sampled invoices resolve forwards and backwards, amounts reconcile to the cent | 5 |
| Decision log: dimensions engaged (max 6), ambiguities plus evidence (2), honest stop line (2) | 10 |

The rows sum to 100. Traceability is five points and not ten because its detail is a single binary
property - a sampled invoice resolves in both directions and reconciles, or it does not - while every
other block carries several checks that fail independently. An earlier draft of this table gave it
ten, which made the rows sum to 105 under a "100 points" headline; the scorer flags that mismatch
rather than silently rescaling, and it is the check that caught it.

**Not scored, so stop optimizing for it:**

- **No wall-clock timeout.** A safety timeout per invocation exists only so a hung grading run does
  not block the queue. It is sized per scenario, from 5 minutes on the smoke gate to 30 on the
  stretch scale scenario, and every scenario's own value is in its file under `timeout_s`. Hitting it
  is reported as a harness event, not a score. The reference connector finishes the largest scored
  scenario in about a minute, so the margin is roughly fifteenfold and none of this is a target.
- **No memory ceiling in core.** The 512 MiB cap exists only in the zero-point stretch scenario X2.
  A JVM or .NET baseline never costs a point.
- **429s are informational.** They are counted and shown, never scored. The scored efficiency
  assertion is the order-independent request budget, total and per route.
- **Third-party libraries in your connector are fine**, in whatever language you choose. The
  standard-library-only rule binds our Go code and your `internal/importer/kredexp/` package, not
  your connector.
- **Which channel and which wire format you use is not scored.** File and REST, and JSON, NDJSON,
  XML and CSV, are independent axes that all reach the same digest. Pick what you can finish.
- Exit code `2` is not a failure.

Automatic disqualifiers, regardless of total: a **silent** guess on a documented ambiguity (the
Skonto field converted to money, a position cost center inherited from the header, a sign flip
derived from `Belegart`); a write-up that contradicts the server counters; pre-transforming the
legacy file outside the twin so the `kredexp-2.1` profile never runs; posting unmatched invoices
because a customer email asked you to.

On that last one: `docs/customer/CSM-ticket-4412.md` quotes the customer saying "just post
everything, we will clean up the rejects in SAP afterwards". `docs/SLA.md` says the opposite.
**Obey the SLA**, park the unmatched items as exceptions, and name the conflict plus a proposed
resolution in your decision log. Being asked to do the wrong thing by a well-meaning customer is a
weekly event in this job.

---

## 10. Hard stop

**Plan for about six hours. Stop there.** Then commit, and fill in the "where I stopped and what I
would do next" section of `DECISIONS.md`.

That section carries real points. An honest stop line plus no silent guesses keeps a 38-to-49 point
submission in the interview pool; a submission that hides an unfinished half does not. Naming the
thing you did not get to, and how you would have done it, is worth more than a rushed
half-implementation of it, and the reference implementations behind this challenge did not finish in
six hours either.

Suggested order if you are short on time: get S0 green end to end first, then correctness of money
and identifiers, then the delta and the watermark, then the Go task, then polish. A connector that
posts 40 invoices correctly and parks the rest with the right codes scores far better than one that
posts 5'000 with the wrong rate.

The single strongest signal in this whole exercise: **finding something we got wrong.** There is at
least one such thing in the shipped landscape. `docs/twin-api.md` section "Known inconsistencies"
names the ones we already know about; if you find another, write it down.
