# The receipt for `../batch`

The four files the twin wrote for `examples/batch/`, captured from a clean twin so
the counts are the interesting ones: five records seen, five accepted, nothing
skipped. Drop the same batch a second time and `accepted` becomes
`skipped_unchanged`, because the store upserts on a content hash and an unchanged
record is not a new revision.

They live one directory up from the batch on purpose: `examples/batch/` is exactly
a valid batch directory, and putting the receipt inside it would make the twin
report the receipt as a file the manifest does not name.

## `DONE` is the only completion signal

It is zero bytes, and it is created **last, by rename**. `receipt.json` exists
before it is complete, so polling for `receipt.json` is a documented mistake: you
will read a truncated file and it will look like a data problem.

## `records.csv`, one row per record seen

The `outcome` column is the per-record verdict and the closure invariant runs on
it: every record the twin saw reaches exactly one terminal state, so the outcomes
sum to `seen`. `raw_excerpt_sha256` addresses the original bytes of that record,
which you can fetch back from `GET /admin/v1/records/raw/{sha256}` while
developing.

## `rejects.csv` is present even when it is empty

A header and no rows is a different fact from a missing file: it says the twin
looked and found nothing to reject. Do not treat an empty rejects file as an
error, and do not omit yours.
