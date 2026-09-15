# Examples

Everything in here is **optional** and you may delete all of it. It exists so
your six hours go to the parts we grade rather than to plumbing.

| Directory | What it is |
|---|---|
| `helpers/` | The zero-signal plumbing: token acquire and refresh, a retry wrapper that honors `retriable`, a request counter, an atomic file drop. Copy it, rewrite it or ignore it. |
| `batch/` | One complete, valid batch directory for the file channel. |
| `batch-receipt/` | The four files the twin writes back for it, captured from a clean twin. |
| `reports/` | Filled-in `run.json`, `postings.csv` and `exceptions.csv`, generated from the **S0 smoke seed only**, so you can see the shape without seeing a scored scenario's answers. |
| `connector-smoke/` | One file each in Python and Go that proves the plumbing end to end in about a minute: token, one page, the SOAP call, one batch through the atomic drop, a valid `run.json`. They exit **3** and say they are not a solution. |

Copying the helpers costs you nothing. Claiming in the review conversation that
you wrote something you copied costs a lot, and it is the one thing here that can
hurt you.

The signal in this exercise is not writing a retry loop. It is knowing that
`retriable: false` means never, that an injected fault succeeds on the retry of
the same logical request, and that a token dies after a published number of
requests. All three are in `docs/spec.md`.
