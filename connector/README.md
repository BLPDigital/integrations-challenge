# `connector/` is yours

Write the connector here, in **any language**, with **any third-party libraries** that language
offers. Our standard-library-only rule binds our Go code, not yours. Nothing in this directory is
read by the grader except `run.sh`, `setup.sh` and `Dockerfile`: we drive your connector through the
CLI below and then assert the state of the two servers, so the language, the libraries and the
architecture are your call and cost you nothing either way.

Bring your own tests. We do not score them directly, and reviewers read them.

## The CLI contract

Fixed, so grading is language-agnostic. The grader invokes `connector/run.sh` with exactly these
arguments:

```
connector/run.sh run --run-id ID
                     --erp-base-url URL --twin-base-url URL
                     --erp-export-dir PATH --twin-drop-dir PATH
                     --state-dir PATH --report-dir PATH
                     [--config FILE] [--no-chaos]
connector/run.sh --version
```

| Argument | What it is |
|---|---|
| `--run-id` | ours, never invented by you. A scenario can invoke you twice: a resumed run gets the same id, the next night's run gets a fresh one, and neither is permission to post anything twice. Put it in the manifest `run_id`, in the SOAP `CorrelationId` and in the reports. Never in an idempotency key |
| `--erp-base-url`, `--twin-base-url` | for example `http://127.0.0.1:41235`. Ports are allocated per run, so never hard-code 8081 or 8082 |
| `--erp-export-dir` | where the ERP drops `KRED_<Mandant>_<YYYYMMDD>_<NNN>.txt` plus an empty `.ok` sentinel written last. Read-only for you, and never read a data file whose `.ok` sibling is absent |
| `--twin-drop-dir` | the twin's inbox ROOT. You write batches into its `incoming/` subdirectory, which is the only twin-owned path you may write, and you read your receipts from the sibling `receipts/`. Used by the file channel and ignored by the REST channel |
| `--state-dir` | yours, persistent between runs. Watermarks, cursors, whatever you need to resume |
| `--report-dir` | where the three report files go |
| `--config` | optional, yours to define. The grader passes it only if you document it |
| `--no-chaos` | informational: fault injection is off on the servers. Change nothing about your behavior |

`--version` prints one line and exits 0. It is called before the series and its output goes into
`run.json` as `connector_version`.

## Environment

Credentials arrive as environment variables. Nothing else is guaranteed to be set.

```
ERP_CLIENT_ID       ERP_CLIENT_SECRET        # POST /erp/v1/auth/token
TWIN_CLIENT_ID      TWIN_CLIENT_SECRET       # POST /v1/auth/token
SOAP_USERNAME       SOAP_PASSWORD            # the wsse:UsernameToken of the SOAP call
```

Admin tokens are never given to a connector: `/admin/v1` and `/erp-admin/v1` are ours, and a
connector that needs them has the wrong design. None of these six values, and no token issued from
them, may appear in stdout, a log, a report or a state file.

Locally you can read the seeded values from `GET /erp-admin/v1/credentials` with the ERP admin token
(see the repository README).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | clean run, nothing needed a human |
| 2 | completed with business exceptions. The **normal** outcome in an exception-driven product, and explicitly not a failure |
| 3 | hard failure: the run did not complete |

Any other exit code fails the scenario. Exiting 1 because an invoice did not match is a graded
mistake, and so is exiting 0 with exceptions in the queue.

## Report files, written to `--report-dir`

### `run.json`

One object. Every key below is expected; the grader cross-checks the counters against the servers'
own metrics and reports a disagreement of more than 2 as a finding.

```json
{"run_id":"","connector_version":"","exit_code":0,
 "channel_used":"file","formats_used":["csv"],
 "erp_requests":0,"erp_429s":0,"erp_5xx_retried":0,
 "twin_requests":0,"twin_429s":0,"soap_calls":0,
 "batches_written":0,
 "records_read":0,"records_ingested":0,"records_rejected":0,"records_skipped_unchanged":0,
 "proposals_read":0,"proposals_posted":0,"proposals_rejected":0,"proposals_pending":0,
 "duplicate_document_attempts":0,
 "watermark_before":{},"watermark_after":{},"full_load":false,
 "fx_snapshot_token":"","fx_correlation_id":"","fx_truncated":false}
```

### `postings.csv`

One line per document, carrying the whole audit chain. Header fixed, in this order:

```
proposal_id,invoice_twin_id,supplier_number,supplier_invoice_number,source_batch_id,
source_file_or_chunk,source_line_or_ordinal,idempotency_key,erp_document_number,erp_status,
http_status,attempts,idempotency_replay
```

This is the artifact an integration engineer hands a customer's finance team. Producing it is part of
the task, not extra credit.

### `exceptions.csv`

Header fixed, in this order:

```
subject_key,subject_type,stage,code,field,message,source_batch_id,source_file_or_chunk,
source_line_or_ordinal
```

Graded on the blocking set only. Extra rows with `stage="info"` (importer warnings, for example) are
explicitly allowed and ignored.

### The reconciliation we run on them

Per run, so they hold on a delta run as well:

1. `records_read == records_ingested + records_rejected + records_skipped_unchanged`
2. `proposals_read == proposals_posted + proposals_rejected + proposals_pending`
3. the sum of posted amounts in `postings.csv` equals the sum the twin holds for the proposals
   acknowledged in this run, to the cent
4. every `postings.csv` row resolves to exactly one twin invoice and exactly one ERP document number
5. every blocking twin exception opened in this run appears in `exceptions.csv`, and the reverse

## Idempotency

Stated as a property, because the recipe is yours:

- **Property, asserted:** the key is a pure function of the proposal and stable across runs. Posting
  the same pending proposal in two runs produces exactly one ERP document.
- **A conforming recipe:** `blp:{tenant}:{proposal_id}:{proposal_content_hash[0:16]}`.
- **Do not fold `--run-id` into the key.** It double-posts on resume, and the ERP's
  `duplicate_document_attempts` counter catches it.

## Writing into the twin's file channel

If you choose the file channel: one batch is one directory containing exactly one `manifest.json`
plus its data files. Publication is atomic, and the twin's scanner is watching:

```
write everything into <twin-drop-dir>/incoming/.staging-<batch_id>/
fsync
rename it in one operation to <twin-drop-dir>/incoming/<batch_id>
```

Names beginning with `.` and names ending in `.tmp` or `.part` are ignored, which is what makes the
staging directory safe. `incoming/` is the only twin-owned directory you may write: creating anything
in `processing/`, `processed/`, `rejected/`, `receipts/` or `outbox/` is `FOREIGN_WRITE_DETECTED` and
fails the run. `record_count` and `sha256` are mandatory per file and are verified before a single
record is applied.

## `run.sh` and `setup.sh`

Both ship as executable stubs in this directory. Replace them.

- `run.sh` is invoked once per scenario, with no network access beyond the two local servers. It must
  pass the arguments through and exit with 0, 2 or 3. As shipped it prints a message and exits 3.
- `setup.sh` is invoked **once** before the scenario series, with network access allowed. Compile,
  `pip install`, `npm ci`, `go build`, whatever your language needs. It must be idempotent and must
  exit non-zero if the build fails.

## A container, if you want one

Supported, and no better or worse for your score, but the harness knows nothing about containers: it
runs `setup.sh` once and `run.sh` per scenario, and that is the whole contract. So a container is
something YOUR two scripts do:

- `setup.sh` builds the image (it has network access, and it runs once),
- `run.sh` is a thin `docker run` wrapper that passes the arguments through and returns the
  container's exit code unchanged.

Two things to get right if you go that way. Both services listen on **127.0.0.1** of the host, so the
container needs host networking (`--network host`) or an equivalent, and the URLs you are handed
already point at loopback. And the four directories you are given are host paths, so bind-mount them
and write them as the invoking uid and gid, or your reports land nowhere the grader looks.

Whatever you choose: a clean checkout plus one `setup.sh` has to be enough.
