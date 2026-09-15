# The challenge

For a senior engineer who has never seen this repository. Read [README.md](README.md) first for how
to start the two services, then this file, then the two customer documents it points at.

## The situation

Steinbach Industrie AG, a Swiss subsidiary in Wil SG, captures supplier invoices in a local
accounting system and exports them nightly to SFTP as a `KRED-EXP` file, and today an accountant
retypes every one of them into the group ERP by hand. Customer Success has agreed phase 1 with them:
pull the master data the matching needs out of the group ERP, ingest the nightly delivery into our
digital twin, match invoices against purchase orders and cost centers, convert foreign currency with
the group's own rate table, and post the matched invoices back so the AP team only touches the ones
that need a human. The business context, the volumes and the customer's own words are in
[docs/customer/CSM-ticket-4412.md](docs/customer/CSM-ticket-4412.md); the interface description for
the legacy file is [docs/customer/KRED-EXP-2.1.md](docs/customer/KRED-EXP-2.1.md), version 2.1 from
2019, and the customer confirms it is still accurate "apart from the open points at the end". The
rules this integration is held to are one page, [docs/SLA.md](docs/SLA.md), and they were shown to
the customer's auditors. The Head of Shared Services asked for something the SLA forbids, twice, on
the record, which is part of the exercise and not an accident.

## What to build

A connector that runs the nightly flow once per invocation, in `connector/`, in any language. Four
phases, each with the outcome we assert.

**Phase 1, master data.** Read suppliers, purchase orders and purchase order lines from the ERP's
paginated REST surface and deliver them into the twin. The cost centers and the 15 UoM conversion
rows are already in the twin, loaded by another integration, and you do not pull them: the twin's
dataset vocabulary has no name for a UoM conversion and would refuse one.
*Accepted when the twin's `GET /admin/v1/state/digest` equals ours for the scenario, the run stays
inside the published request quota, and the second run pulls a delta (`changed_since` from a
persisted watermark, `full_load` false) instead of reloading everything.*

**Phase 2, the legacy delivery.** Take each `KRED_*.txt` from the ERP's export directory, but only
once its `.ok` sentinel exists, and relay the bytes to the twin unparsed, declaring
`profile: "kredexp-2.1"` on the manifest file entry.
*Accepted when every record of the delivery is accounted for in the twin's receipt
(`seen == accepted + accepted_with_warning + rejected + skipped_unchanged + quarantined`), and a
byte-identical re-delivery is a no-op with no second outbox emission.*

**Phase 3, exchange rates.** Call `GetExchangeRateTable` over SOAP once per company code, drop
`Status=DELETED` rows, resolve `Sequence` supersession (highest wins, and the loser is placed after
the winner in document order), normalize the host locale decimals, carry `Rate` and `RateFactorFrom`
separately, and deliver the surviving rows to the twin.
*Accepted when the proposal amounts convert exactly, currencies whose only row is DELETED land in
exceptions, a `PERMANENT` fault is not retried, a `RETRYABLE` one is, and `Truncated=true` makes the
run refuse to post.*

**Phase 4, post and close.** Read pending proposals from the twin's outbox, post them to the ERP with
a stable `Idempotency-Key`, feed the returned document number back with
`POST /v1/outbox/acks`, and write the three report files.
*Accepted when `proposals_pending == 0`, the ERP's `duplicate_document_attempts` and the twin's
`duplicate_apply_attempts` are both 0, the set of ERP documents is exactly the expected one, and
`run.json`, `postings.csv` and `exceptions.csv` reconcile against the servers' own counters.*

Business exceptions are the normal outcome, not a failure: exit code 2 says "done, with items a
human needs to see". The exact CLI, environment, exit codes and report schemas are in
[connector/README.md](connector/README.md).

## The two choices you have to make and justify

Both axes are free and independent, **for the master data**. The nightly legacy delivery is not part
of this choice: the `kredexp-2.1` profile runs only on a batch that arrives through the file drop,
because the twin parses the file's own bytes and verifies the `sha256` and the `record_count` you
declared for them. The REST ingest surface takes canonical records only. So phase 2 means the
manifest, the two declarations and the atomic staging-plus-rename, whichever channel you pick for
phase 1.

| Axis | Options |
|---|---|
| Ingest **channel** into the twin | the file importer (`var/miniblp/inbox/incoming/<batch_id>/`) or the REST ingest surface (`POST /v1/ingest/batches` then `.../records` then `.../commit`) |
| Wire **format** of the records you deliver | `csv`, `xml`, `json` or `ndjson` |

Every legitimate combination scores identically. Grading asserts outcome state through a digest that
covers only the canonical `{key, payload}` of every record, in key order: channel, format, encoding,
provenance, batch ids, version numbers, sequence numbers and request counts are all excluded by
construction, so the same logical data delivered any legitimate way produces a byte-identical
digest. The two channels have genuinely different properties (the file channel verifies
`record_count` and `sha256` before it applies a single record; the REST channel streams and gives you
per-record results in the response), and the formats differ in what they can express (a CSV cell
cannot hold the invoice line list, so a CSV sender delivers `invoice_line` as its own file). Pick
one of each, for reasons, and write the reasons down.

**Implementing a second channel or a second format is worth zero points.** Nobody should spend two
hours buying nothing. If you want to show range, say in `DECISIONS.md` what the other choice would
have cost and bought.

## The Go task

Implement the legacy importer `kredexp-2.1` inside the twin: package
`internal/importer/kredexp`, satisfying `importer.Format`, against the customer's own interface
description [docs/customer/KRED-EXP-2.1.md](docs/customer/KRED-EXP-2.1.md), which is the contract.
The format is CP1252 without a BOM, semicolon separated, CRLF terminated, with doubled quotes and
embedded line breaks inside quoted fields, record types `VORLAUF` / `KOPF` / `POS` / `NACHLAUF`,
decimal points with optional apostrophe grouping, trailing minus for negatives, `TTMMJJ` dates with
a pivot at 70, significant leading zeros everywhere, and a trailer with record counts and control
totals the receiver has to verify. **The twin owns this parse. Your connector relays the legacy bytes
unparsed and declares the profile in the manifest**, so the format is parsed exactly once, in Go, and
you never write it twice in two languages. Standard library only in here, and nothing outside this
package changes except one line: uncomment the blank import in `internal/importer/all/all.go`. The
harness contract, the closed set of diagnostic codes and the helpers that already exist (CP1252
decoding, the accepted numeric shapes, the diagnostic builder) are in
[internal/importer/kredexp/README.md](internal/importer/kredexp/README.md). Read section 9 of the
customer document, "Open points and known deviations", before you write a line of code.

The parser and the pipeline are scored **independently**. The four public fixtures in
`testdata/kredexp/` plus three hidden ones score the parser on its own, so an unfinished pipeline
does not cost you the parser, and an unfinished parser does not cost you the pipeline: the twin
rejects a `kredexp-2.1` batch it has no importer for with a whole-batch `UNKNOWN_PROFILE` that
applies nothing and leaves everything else in the run intact. To exercise phases 1, 3 and 4 before
your parser works, deliver master data with the `blp-canonical-v1` profile, which is fully
implemented. The twin also honors `MINIBLP_REFERENCE_IMPORTERS=1`, which prefers our own reference
implementation of the legacy format when one is linked into the binary; our grading build links it,
and the build in your bundle does not, so do not plan your afternoon around it.

## Time budget

About six hours for the core, and a **hard stop at six**. This is deliberate: we are measuring
judgment under a budget, not stamina. When the six hours are up, commit what you have, even if it is
mid-refactor, and fill in the "Where I stopped" and "What I would do next" sections of
`DECISIONS.md`. Those two sections carry points. An honest stop line with no silent guesses still
reaches an interview; a submission that hides where it ran out of time does not.

Stretch goals (crash and resume, scale under a memory cap) are worth **zero points** and are
recorded as a tie-break note only. Nothing about peak memory or wall-clock runtime is scored.

They are real scenarios and they ship with the exercise: `make selfcheck STRETCH=1`, or
`go run ./cmd/grade selfcheck --stretch`. X1 kills your connector with SIGKILL once the ERP holds a
document and then invokes it again with the same run id; it checks that you converge on the same
state a clean run reaches, and deliberately does not check how much work the resume redid. X2 is the
same night with three times the data inside twice S1's request quota, and it prints your peak
resident set next to a 512 MiB reference figure. Neither can cost you a point. Both are worth
running once, because a connector that survives them is telling a reviewer something a green
scorecard does not.

## How to self-check

```
make check                # gofmt, go vet, go test, import audit, determinism grep
make selfcheck            # every public scenario, per-assertion pass/fail with readable diffs
make selfcheck STRETCH=1  # the same, plus the two stretch scenarios (zero points, slower)
make golden-diff          # the Go task, per document and per finding
```

Without the `Makefile` wrappers, the same work is:

```
gofmt -l . && go vet ./... && go test ./...          # make check
go test ./internal/importer/ -run Golden -v          # make golden-diff
go run ./cmd/grade selfcheck                         # make selfcheck
go run ./cmd/grade selfcheck --stretch               # make selfcheck STRETCH=1
go run ./cmd/grade run --scenario S0 --no-chaos      # one scenario, verbose
go run ./cmd/grade golden-diff --fixture testdata/kredexp/03_quoted_fields_ok.txt
```

`make selfcheck` runs the same assertions we score you with, prints them as readable expectations
rather than a digest mismatch, and is the single most useful thing in the repository. Run it early,
run it often, and read the failures rather than guessing.

## Rules that are graded and easy to miss

1. **Nothing unmatched is posted.** An invoice missing a supplier, a purchase order it references, a
   valid cost center or a rate valid on its document date becomes a visible exception, not a
   posting.
2. **No duplicate is ever posted**, no matter how often a delivery is re-sent, a request retried or a
   run repeated. The idempotency key is a pure function of the proposal and stable across runs. Do
   not fold the run id into it: that double-posts on resume, and the ERP counts it.
3. **Every run is re-runnable after any exit code**, without redoing what already landed and without
   losing what was in flight. Recovery is never "delete the state directory".
4. **Bank data and secrets never reach an output.** No IBAN, BIC, client secret or bearer token in
   stdout, logs, state files or any of the three reports. Masking (`CH93****2957`) is preferred over
   omission, and the scan matches full values, so a masked record passes.
5. **The ERP is read-only outside the posting endpoints.** No master data writes, and no change
   inside the customer's ERP is required to make this work.
6. **An ambiguity in the customer's document is surfaced, never guessed.** Where a field's meaning is
   genuinely undecided, carry the value verbatim, emit the documented warning, and write down the
   reading you would propose. Silently converting a discount field to money, silently inheriting a
   cost center, or deriving a sign flip from a document type are each an automatic disqualifier: they
   change what a supplier gets paid.
7. **An instruction that conflicts with the SLA loses, and the conflict gets written down.** There is
   one such instruction in the ticket, quoted verbatim from the customer. Posting unmatched invoices
   because a customer email said so is a red flag, and an automatic disqualifier when it moves money.
8. **Identity is exact string equality** after trimming, everywhere. Nothing is normalized by adding
   or stripping leading zeros. The master deliberately carries two different creditors whose numbers
   differ only in padding. Purchase order numbers are the one documented exception: trim, drop a
   leading `PO-`, then compare exactly.
9. **Retry discipline.** `retriable` in an error body is authoritative. Never retry a
   `retriable:false` response or a `PERMANENT` SOAP fault; at most 3 attempts on anything.
10. **Nothing in an error body is an oracle.** A rejection tells you what is wrong and why, never
    what the right value was.

## How it is scored

100 machine-checked points, everything published.

| Block | Points | Detail |
|---|---|---|
| Public scenarios | 30 | state digest 12 (6 per scenario), exception set 8, `proposals_pending==0` plus the exact ERP document set 6, inside budget 4 (binary) |
| Hidden scenarios | 20 | seven hidden scenarios. They re-combine documented mechanisms and introduce none |
| Go task | 20 | 4 public fixtures 8, 3 hidden fixtures 8, the ambiguity write-up 4 |
| Robustness invariants | 15 | both duplicate counters at 0 → 4; receipt closure and no vanished record → 3; retry discipline and never quarantined → 2; SOAP call discipline → 1; no admin path touched → 1; no credential and no IBAN in any artifact → 2; the legacy delivery parsed under its profile → 1; watermark advanced and `full_load==false` on the delta run → 1 |
| Traceability | 5 | 25 sampled invoices resolve forwards and backwards, amounts reconcile to the cent |
| Decision log | 10 | dimensions engaged (max 6) plus ambiguities with evidence (2) plus an honest stop line (2) |
| Stretch | 0 | tie-break note only |

The rows sum to 100, and every number in them is the sum of the `points` fields in the scenario
files: `make scenarios` prints the whole allocation, assertion by assertion, so you can check this
table against the harness rather than trusting it.

Published request quotas, counted per server per run, including 429s and token refreshes. Exceeding
one is `503 QUOTA_EXHAUSTED`, `retriable:false`, for the rest of the run.

| Scenario | ERP quota | Twin quota |
|---|---|---|
| S0 smoke gate (not scored) | 200 | 120 |
| S1 cold load | 1'200 | 500 |
| S2 delta with chaos, two invocations | 1'400 | 640 |

Amounts we assert exactly, so you can check your money path before the grader does. Rounding happens
in exactly one place, the FX conversion, and it is half away from zero.

| Case | Gross | Rate | Factor | Result | A wrong path gives |
|---|---|---|---|---|---|
| GBP invoice | 100.00 | 1.082250 | 1 | **108.23** | 108.22 with banker's rounding or float64 |
| JPY invoice | 250000 | 0.556300 | 100 | **1390.75** | 139'075.00 when the rate factor is ignored |
| EUR on the DST switch, 2026-03-29 | 1'345.63 | 0.931000 | 1 | **1252.78** | 1244.03 when the FX date is truncated to UTC |
| Credit note | -100.00 | 1.082250 | 1 | **-108.23** | -108.22 when rounding is not away from zero |

An input value is never rounded: more fraction digits than the field allows is an error, not a
rounded value.

Bands: 82 and above with a green Go task and at least two named ambiguities reads as staff or strong
senior; 65 to 81 solid senior; 50 to 64 mid; 38 to 49 with an honest stop line and no silent guesses
is still an interview. The onsite threshold is 50 with no disqualifier, both duplicate counters at 0
or an explicit written acknowledgment, and at least six decision-log dimensions engaged.

The strongest single signal in this challenge: **finding something we got wrong.** The documents
contain real defects. Tell us.

## AI assistants

Allowed and expected. Use whatever you use at work, including agents that write whole files. We care
about the result and about whether you own it.

Two consequences worth knowing in advance. The review conversation is built from your own diff and
your own `score.md`: we will ask you to defend specific lines, hand-trace your commit ordering
through a SIGKILL, change one line and say what breaks and which scenario catches it, and show us
something you decided **not** to post. Code you cannot explain is worth less than half as much code
you can. And non-disclosure of AI use is a process observation about how you work, never a code
finding: say what you used, it costs you nothing.

## What we do with your submission

1. The automated scenarios run against your connector through the fixed CLI, three times each,
   plus the hidden scenarios and the golden fixtures for the Go task. Byte-identical scores across
   runs, or it is our bug.
2. A rubric with seven dimensions, scored by two reviewers independently before they confer:
   idempotency and delivery semantics; failure taxonomy and exception-driven behavior; efficiency and
   API citizenship; money, encoding, identifiers and units; state, incrementality and recovery; spec
   fidelity on `kredexp-2.1` and Go craft; judgment, communication and ownership.
3. A 45 to 60 minute conversation built entirely from your own submission. It is scored separately
   and never summed into the 100.

If you would rather see the wire than read about it, [docs/transcripts.md](docs/transcripts.md) is
every exchange you need, captured from the running services: the SOAPAction quotes, a chunk gap, a
replay, a duplicate, a 207 with one rejection in it.

What to hand back, exactly: [docs/DELIVERABLES.md](docs/DELIVERABLES.md).
