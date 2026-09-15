# Example batch

One complete, valid batch directory for the twin's file channel. The receipt the
twin writes for it is in `../batch-receipt/`, one level up, so that this directory
stays exactly a batch and nothing else.

    make up
    make smoke-file

`smoke-file` copies this directory into `var/miniblp/inbox/incoming/` through the
atomic drop protocol (staging directory, fsync, one rename), triggers a scan, and
prints the receipt.

What to notice in the manifest:

- `record_count` and `sha256` per file are **mandatory and verified** before a
  single record is applied. That is the truncated-transfer guard, and it is the
  file channel's structural advantage over REST.
- the `csv` dialect block declares the separators, the encoding and the date
  format, so nothing about the file's shape has to be guessed,
- `batch_id` is the replay and conflict key: the same id with the same content is
  a no-op replay, the same id with different content is `BATCH_ID_CONFLICT`.

One deliberate wrinkle: `make smoke-file` copies this whole directory, README and
all, so the receipt carries a `W_UNDECLARED_FILE` finding for `README.md`. That is
the twin telling you it saw a file the manifest does not name. It is a warning and
not a rejection, because a stray file is not a data error, but it is reported
rather than ignored: an undeclared file in a real delivery is usually the half of a
transfer that arrived.
