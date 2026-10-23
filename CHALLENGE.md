# The challenge

For a senior engineer who has never seen this repository. Read [README.md](README.md) first for how
to start both services, then this brief, then [docs/SLA.md](docs/SLA.md) and
[docs/customer/CSM-ticket-4412.md](docs/customer/CSM-ticket-4412.md). The CLI, environment, exit
codes and report schemas are in [connector/README.md](connector/README.md).

## Scenario: Steinbach Industrie AG

Steinbach Industrie AG (fictional), the Swiss subsidiary of the Steinbach Group in Wil SG, uses BLP
to automate incoming supplier invoices. BLP already captures the invoices, and they are waiting in
BLP to be matched. What is missing is the ERP side: the group ERP holds the master data the matching
needs, and it must receive the matched invoices as AP postings. You build that connector.

| Topic | Detail |
|---|---|
| Group ERP | Central system of record. REST facade with paginated lists, token auth, a hard request quota per night, injected 429 and 503 responses |
| BLP side | A local copy of BLP (the "twin"). The invoices are already in it. Cost centers, unit conversions and exchange rates were loaded by other integrations |
| Master data | About 12'000 suppliers and 8'000 open purchase orders |
| Invoice volume | About 5'000 invoices in the first run, then about 1'100 per night |
| Company codes | CH10 is live. CH20 is not onboarded yet; its invoices "will come later" |
| Operations | One run per night. It must be safe to run again if it dies, because the ERP maintenance window overlaps on Sundays |
| Rules | [docs/SLA.md](docs/SLA.md), one page, shown to the customer's auditors |
| People | Head of Shared Services (sponsor), AP team of 3 FTE losing one this quarter, group IT mailbox with no individual contact |

From the kickoff call, said twice by the Head of Shared Services:

> "Bucht einfach alles, den Rest räumen wir nachher im SAP auf. Hauptsache die Rechnungen sind im
> System, sonst zahlen wir wieder zu spät."
>
> ("Just post everything, we will clean up the rest in SAP afterwards. The main thing is that the
> invoices are in the system, otherwise we will be paying late again.")

**The brief is incomplete on purpose.** Where you lack an answer, decide, write the assumption into
`DECISIONS.md`, and move on.

## Task 1: Master data into BLP

Pull suppliers, purchase orders and purchase order lines from the ERP's paginated REST lists and
deliver them into the twin's file inbox as canonical records. The wire format is your choice: `csv`,
`xml`, `json` or `ndjson`. Every format reaches the same score.

Accepted when:

- the twin's state digest equals ours for the scenario,
- the run stays inside the published request quota,
- the second night pulls only what changed (`changed_since` from a watermark you persisted,
  `full_load` false).

Answer in `DECISIONS.md`:

1. Which format did you pick, and what did it cost you?
2. What do you persist between runs, and when exactly do you write it?

## Task 2: Post and close

Once the master data is in, ask the twin to match the invoices (`POST /v1/match`). It puts a posting
proposal into its outbox for every invoice that matches and an exception into its queue for every
one that does not. Post the proposals to the ERP, report each ERP document number back to the twin,
and write the three reports.

Accepted when:

- no proposal is left pending,
- no document is posted twice, across retries, repeated runs and a crash in the middle of a run,
- the set of ERP documents is exactly the expected one,
- every posted document traces back to its invoice in `postings.csv`, and `exceptions.csv` lists
  what a human must look at,
- the run exits with 2 when exceptions remain. That is the normal outcome, not a failure.

Answer in `DECISIONS.md`:

1. How is your idempotency key built, and what must never be in it?
2. What does your next run do if this one is killed after the ERP accepted a posting but before you
   reported it back?

## Task 3: The customer's request

The Head of Shared Services asked twice to "just post everything". The SLA says the opposite.

Answer in `DECISIONS.md`:

1. What happens to the unmatched invoices tonight?
2. What do you tell her, in two or three sentences she would accept?
3. What would you change so she does not get "a report with hundreds of rows" again?

`DECISIONS.md` has four headings: my choices (format, state and the idempotency key), the
customer's request and my answer, where I stopped, what I would do next. One page. The template is
[docs/templates/DECISIONS.md](docs/templates/DECISIONS.md).

**The strongest single signal in this case is finding something we got wrong.** The documents and
the services contain real defects. Write down what you find.

## One run, in order

One invocation of your connector is one night. The endpoints are in [docs/spec.md](docs/spec.md),
[docs/erp-api.md](docs/erp-api.md) and [docs/twin-api.md](docs/twin-api.md).

1. Pull suppliers, purchase orders and purchase order lines from the ERP, from your watermark.
2. Deliver them into the twin's file inbox (`<twin-drop-dir>/incoming/`) and wait for the batch
   receipt's `DONE`.
3. Call `POST /v1/match` with your run id. The twin matches every waiting invoice against the
   master data it holds now. Called before the master data is in, it matches against an empty master
   and every invoice becomes an exception.
4. Read the open exception queue, `GET /v1/exceptions`, for `exceptions.csv`.
5. Read the pending proposals, `GET /v1/outbox/proposals`, and post them to the ERP with a stable
   `Idempotency-Key`.
6. Acknowledge every outcome with `POST /v1/outbox/acks`.
7. Write `run.json`, `postings.csv` and `exceptions.csv`, and exit 0, 2 or 3.

The twin converts foreign currency itself and rounds once, half away from zero. A proposal arrives
with the converted amount, the rate and the factor already on it: post them as they are.

## Rules that are graded and easy to miss

1. **Nothing unmatched is posted.** An invoice without a matching supplier, purchase order or cost
   center becomes a visible exception.
2. **No duplicate is ever posted**, however often a request is retried or a run repeated. Do not put
   the run id into the idempotency key.
3. **Every run is safe to run again after any exit code.** Recovery is never "delete the state
   directory".
4. **No IBAN, BIC, secret or token in any output**: stdout, logs, state files, reports. Masking
   (`CH93****2957`) is preferred over omission.
5. **The ERP is read-only outside the posting endpoints.**
6. **Identity is exact string equality** after trimming. Leading zeros are never added or removed.
   Two suppliers differ only in padding. Purchase order numbers are the one documented exception,
   see [docs/rules.md](docs/rules.md) section 2.
7. **Retry discipline.** `retriable` in an error body decides, not the HTTP status. Never retry
   `retriable:false`, at most 3 attempts on anything.

## Time box

About 3 hours, hard stop. Suggested order: get the smoke scenario `S0` green end to end, then Task 2
correctness, then the delta run, then `DECISIONS.md`. A connector that posts 40 invoices correctly
and parks the rest with the right reason beats one that posts all of them wrong.

When the time is up, commit what you have, even mid-refactor, and fill in "Where I stopped" and
"What I would do next". The stop line carries points. An honest one still reaches an interview; a
submission that hides where it ran out of time does not.

## How to self-check

```
make check        # gofmt, go vet, go test, import audit, determinism grep, protected tree
make selfcheck    # every public scenario, per-assertion pass/fail with readable diffs
make scenarios    # the point allocation of the public scenarios, assertion by assertion
```

Without the `Makefile` wrappers, the same work is:

```
gofmt -l . && go vet ./... && go test ./...          # most of make check
go run ./cmd/grade selfcheck                         # make selfcheck
go run ./cmd/grade run --scenario S0 --no-chaos      # one scenario, verbose
```

`make selfcheck` runs the same assertions we score with, prints them as readable expectations rather
than a digest mismatch, and is the single most useful thing in the repository. Run it early and
often, and read the failures rather than guessing.

## How it is scored

100 points, everything published: 80 machine-checked, 20 for the decision log, which people score.

| Block | Points | Detail |
|---|---|---|
| Public scenarios | 40 | S1 and S2 each: state digest 8, exception set 5, nothing pending 3, exact ERP document set 2, inside budget 2 |
| Hidden scenarios | 15 | five, 3 each: the same night runs twice; rejected and conflicting batches; the master data's edges (an empty page with `has_more` true, two creditors differing only in padding, a `PO-` prefix and spaces, a unit with no conversion); the acknowledgment never lands; a transport error that never succeeds. They re-combine documented mechanisms and introduce none |
| Robustness | 15 | S1: receipt closure 1, every record in one terminal state 1, no admin path 1, no credential in any artifact 2, no IBAN in any artifact 2. S2: watermark advanced and `full_load` false 2, no duplicate document 4, retry discipline 1, never quarantined 1 |
| Traceability | 10 | audit chain of 25 sampled invoices 6, reports reconcile 2, one document per external reference 2 |
| Decision log | 20 | dimensions engaged, 2 each, at most 10 (five of six: idempotency, failure taxonomy, efficiency, identifiers and data, state and recovery, format choice); the customer's request 5; an honest stop line 5 |

The rows sum to 100. The machine-checked numbers are the `points` fields in the scenario files, and
`make scenarios` prints them, so you can check this table against the harness rather than trust it.

Published request quotas, counted per server per run, including 429s and token refreshes. Exceeding
one is `503 QUOTA_EXHAUSTED`, `retriable:false`, for the rest of the run.

| Scenario | ERP quota | Twin quota |
|---|---|---|
| S0 smoke gate (not scored) | 200 | 120 |
| S1 cold load | 1'200 | 500 |
| S2 delta with chaos, two invocations | 1'400 | 640 |

Bands: 82 and above, with at least 12 in the decision log, reads as staff or strong senior; 65 to 81
solid senior; 50 to 64 mid; 38 to 49 with an honest stop line is still an interview. The onsite
threshold is 50 with no disqualifier, both duplicate counters at 0 (or an explicit written
acknowledgment), and at least 10 in the decision log.

Automatic disqualifiers, whatever the total: posting an unmatched invoice because the customer asked
for it, and a write-up that contradicts the servers' own counters.

## AI assistants

Allowed and expected. Use whatever you use at work, including agents that write whole files. We care
about the result and about whether you own it.

Two consequences worth knowing in advance. The review conversation is built from your own diff and
your own `score.md`: we will ask you to defend specific lines, hand-trace your commit ordering
through a crash, change one line and say what breaks and which scenario catches it, and show us
something you decided **not** to post. Code you cannot explain is worth less than half as much code
you can. And non-disclosure of AI use is a process observation about how you work, never a code
finding: say what you used, it costs you nothing.

## What we do with your submission

1. The automated scenarios run against your connector through the fixed CLI, three times each, plus
   the hidden scenarios. Byte-identical scores across runs, or it is our bug.
2. Two reviewers score the decision log independently before they confer.
3. A 60 minute conversation, in English or German, built from your own submission: your walkthrough,
   questions on your own code, a round with new information, and your questions. It is scored
   separately and never summed into the 100.

If you would rather see the wire than read about it, [docs/transcripts.md](docs/transcripts.md) has
the exchanges you need, captured from the running services: a replay, a duplicate, a 207 with one
rejection in it.

What to hand back, exactly: [docs/DELIVERABLES.md](docs/DELIVERABLES.md).
