# BLP Digital integrations challenge

## Start here

**If you were given access to this repository on GitHub:**

1. **Clone it.**

   ```
   git clone https://github.com/BLPDigital/integrations-challenge.git blp-challenge
   cd blp-challenge
   ```

2. **Create an empty private repository of your own** on GitHub, with no README and no
   `.gitignore`, and point this clone at it:

   ```
   git remote set-url origin https://github.com/YOUR-ACCOUNT/YOUR-REPO.git
   git push -u origin HEAD
   ```

   **Keep the commit you cloned as the first one.** It is what we diff your work against. A
   history that starts from scratch costs you the part of the review that reads how the work
   went, and there is no way for us to recover it afterwards.

3. **Work there.** Commit as you go, in whatever rhythm suits you. We read the history, not to
   police it but because the order things happened in says more about how somebody works than a
   finished diff does.

4. **When you are done, add `fatjonblp` as a collaborator with read access** to your repository
   (Settings, then Collaborators, then Add people) and send us the link. Please leave the
   repository in place until we have talked it through with you.

5. **Keep your repository private**, do not open a pull request against this one, and please do
   not copy the exercise into a public repository.

**If you were given a tarball instead:** unpack it, `git init` if you like having a history, and send
the result back as an archive. Everything else below is identical.

**The time box is about three hours, with a hard stop**, and what you did not finish belongs in
`DECISIONS.md` rather than in an apology. Stopping on time with an honest account of where you stopped
is worth more here than a rushed everything. The three things you hand back are in
[docs/DELIVERABLES.md](docs/DELIVERABLES.md).

---

This repository is a small, self-contained integration landscape plus one hiring task. Two services
are provided by us and run locally with no network and no dependencies outside the Go standard
library. You build the integration between them.

The brief is [CHALLENGE.md](CHALLENGE.md). Read that next.

## The two systems

**`erp` (port 8082, `cmd/erp`) is the customer's system of record.** It owns suppliers, purchase
orders, purchase order lines and unit-of-measure conversions on a paginated REST surface under
`/erp/v1`, and receives AP postings at `/erp/v1/ap/documents` and `/erp/v1/ap/documents:batch`. It
is deliberately old-fashioned: a token bucket, a hard request quota, injected 429 and 503 faults.

**`miniblp` (port 8081, `cmd/miniblp`) is BLP's digital twin.** It already holds what other systems
loaded before your run: the cost centers, the unit conversions, the group's exchange rates, and the
night's supplier invoices, which BLP captured itself from the subsidiary's nightly export. It takes
your master data through a file drop (`var/miniblp/inbox/incoming/`) in JSON, NDJSON, XML or CSV,
and stores every record as an append-only revision with full provenance. On `POST /v1/match` it
matches the waiting invoices against suppliers, purchase orders, cost centers and rates, opens an
exception for anything that does not match (`GET /v1/exceptions`), and emits a posting proposal for
anything that does. Proposals leave through `GET /v1/outbox/proposals` and are closed by
`POST /v1/outbox/acks`, which is what turns a posted ERP document number into a complete audit
chain.

## Start it

```
go run ./cmd/erp     --listen 127.0.0.1:8082 --scenario S0 --seed 20260416 --no-chaos
go run ./cmd/miniblp --listen 127.0.0.1:8081 --scenario S0 --seed 20260416 \
                     --data-dir var/miniblp --no-chaos
curl -X POST -H 'X-Admin-Token: miniblp-admin-token' \
     -d '{"scenario":"S0","seed":20260416}' http://127.0.0.1:8081/admin/v1/seed
```

Each service prints one JSON line naming the address it bound, for example
`{"svc":"erp","addr":"127.0.0.1:8082"}`, and then one JSON line per request, without timestamps.
`--listen 127.0.0.1:0` picks a free port and prints it, which is how the grader starts them. The
third call loads what the twin holds before your connector runs: the cost centers, the UoM
conversions and the exchange rates, standing in for "another integration already loaded them", and
the night's captured invoices, which wait for `POST /v1/match`. Without it the twin starts empty.
`--no-chaos` turns fault injection off for local work and changes nothing else. If your bundle has a
`Makefile`, `make up` wraps exactly these commands.

Runtime state goes to `var/`. Keep it out of your commits.

## See it working

| What | Where |
|---|---|
| Twin UI: datasets, batches, receipts, exceptions, proposals, audit chain | <http://127.0.0.1:8081/ui/> |
| ERP UI: entities, documents, duplicates, idempotency keys, request log | <http://127.0.0.1:8082/ui/> |
| Twin health, state digest, metrics | `/healthz`, `/admin/v1/state/digest`, `/admin/v1/metrics` |
| ERP health, metrics, request log | `/healthz`, `/erp-admin/v1/metrics`, `/erp-admin/v1/requests` |
| The credentials of the seeded run | `GET /erp-admin/v1/credentials` |

Both UIs are read-only, cost no quota and need no login. The `/admin/v1` and `/erp-admin/v1`
surfaces need the header `X-Admin-Token` and are free of charge; the connector never gets those
tokens. The ERP's admin token is derived from the seed (read it from
`GET /erp-admin/v1/credentials`, or pass `--admin-token`); the twin's defaults to
`miniblp-admin-token` unless `--admin-token` or `MINIBLP_ADMIN_TOKEN` says otherwise.

A first request against the ERP, end to end:

```
curl -X POST http://127.0.0.1:8082/erp/v1/auth/token \
     -d '{"client_id":"blp-connector","client_secret":"<from /erp-admin/v1/credentials>"}'
curl -H 'Authorization: Bearer <access_token>' \
     'http://127.0.0.1:8082/erp/v1/suppliers?limit=2'
```

## Where things are

| Path | What |
|---|---|
| `CHALLENGE.md` | the brief: what to build, what is graded, how long to spend |
| `docs/DELIVERABLES.md` | the exact list of what you hand back |
| `docs/customer/CSM-ticket-4412.md` | the internal ticket carrying the business requirement |
| `docs/SLA.md` | the operating rules this integration is held to |
| `docs/templates/DECISIONS.md` | the decision log template. The filled-in copy belongs at `DECISIONS.md` |
| `docs/templates/RUNBOOK.md` | optional operational template, worth zero points |
| `connector/README.md` | your working directory: CLI contract, environment, exit codes, reports |

## Who owns what

Yours to write:

```
connector/**        any language, any third-party library
DECISIONS.md        from docs/templates/DECISIONS.md
```

Ours, and not to be modified: everything else, including `cmd/`, `internal/`, `grading/`,
`testdata/` and `docs/`. The grader assembles the tree it grades in from our files with only the two
paths above taken from your submission, and rebuilds both services from that, so a change anywhere
else cannot help you. It is also checked against our own copy of the manifest and reported.

## Checks

```
gofmt -l .        # empty
go vet ./...
go test ./...
```

`go test` runs offline: there is no `go.sum` and no import outside the standard library. Do not
touch `go.mod`.
