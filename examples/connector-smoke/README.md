# Smoke connectors

Two files, one in Python and one in Go, that do the same seven things in the same
order and then stop:

1. read the CLI contract's flags, tolerating a leading `run` subcommand
2. take a bearer token from the ERP
3. read one page of suppliers at the maximum page size, and print `has_more` and
   `max_change_seq`
4. call `GetExchangeRateTable` over SOAP with a WS-Security header, and retry the
   identical request once if the answer carries a `soap:Fault` over HTTP 200
5. take a bearer token from the twin
6. publish one batch through the atomic drop protocol: staging directory, fsync,
   one rename
7. write a schema-valid `run.json` and both CSV headers, then exit **3**

They are not a solution and they say so on the way out. Their whole purpose is to
answer "is my plumbing alive" before you spend real time on semantics.

## Running one

```sh
make up
eval "$(make -s creds)"
python3 examples/connector-smoke/python/smoke.py run \
    --run-id smoke --erp-base-url http://127.0.0.1:8082 \
    --twin-base-url http://127.0.0.1:8081 \
    --erp-export-dir var/erp/export --twin-drop-dir var/miniblp/inbox \
    --state-dir /tmp/smoke-state --report-dir /tmp/smoke-reports
```

The Go one takes the same flags: `go run ./examples/connector-smoke/go run ...`.
Note that `go run` reports a non-zero child exit as `exit status 3` and then
returns 1 itself; build the binary if you want to see the 3 directly.

Then look at what happened:

```sh
tools/validate-reports.sh /tmp/smoke-reports
curl -fsS -X POST -H 'X-Admin-Token: dev-twin-admin' \
    http://127.0.0.1:8081/admin/v1/inbox/scan
ls var/miniblp/inbox/receipts/
```

## Three things they demonstrate that are easy to get wrong

**The SOAPAction quotes are part of the value.** Send it unquoted and the service
answers HTTP 200 with a `soap:Fault`. On this channel the HTTP status is never
your success signal.

**The SOAP response is ISO-8859-1 and says so only in its XML prolog.** No HTTP
charset header. Decode it as UTF-8 and the rate provider's umlaut becomes a
replacement character or an error, depending on your library's mood.

**`records_read == records_ingested + records_rejected + records_skipped_unchanged`.**
Both smokes read a 240-record page and publish one record, so they report
`records_read: 1` and not 240: the counter means "records this run took into the
pipeline and can account for", and reporting the page size would be reporting
239 silent drops. `tools/validate-reports.sh` checks this before the grader does.
