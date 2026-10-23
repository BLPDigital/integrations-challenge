// Package store is the digital twin's database: an append-only revision store
// with an in-memory index, a content-addressed blob area for raw source bytes,
// and a state digest that identifies logical content independently of how it was
// delivered.
//
// # On-disk format
//
// One segment file per dataset, dir/<dataset>.jsonl. Each line is one [Revision]
// encoded as [model.CanonicalJSON] and terminated by a newline. Lines are only
// ever appended and are fsynced before [Store.Apply] returns; nothing is ever
// rewritten in place, so the file is a complete audit trail of every delivery.
// Because canonical JSON escapes newlines, a raw newline in a segment file is
// always a record boundary.
//
// Raw source bytes live outside the segments, content-addressed under
// dir/raw/<sha256[0:2]>/<sha256> and written exactly once. See [Store.PutRaw].
//
// # Recovery
//
// [Open] replays every segment to rebuild the index. A final line without its
// terminating newline is a torn write from a crash mid-append: it is discarded,
// the segment is truncated back to the last complete record, a warning is
// reported through [Options.Warn], and the store opens successfully. A complete
// but unparseable line is the same story if it is the last line of the segment;
// anywhere else it is corruption and [Open] fails rather than silently losing
// history.
//
// # Concurrency
//
// A [Store] is safe for concurrent use by any number of goroutines. One
// [sync.RWMutex] guards the index, the segment sizes and the sequence counter:
// [Store.Apply], [Store.PutRaw] and the typed mutators take it exclusively, and
// every read takes it shared. Writes are therefore serialized and each one is
// atomic with respect to every reader: a reader never observes a half-written
// revision, and the seq / version pair of an applied revision is unique.
//
// The package starts no goroutines. Consequently the callback of [Store.Scan],
// [Store.ScanProposals] and [Store.ScanExceptions] runs while the lock is held
// shared, and must not call back into the store: a re-entrant read deadlocks
// behind a waiting writer.
//
// # Memory
//
// The index holds, per record, the current version, content hash, deleted flag,
// seq and the offset of every revision line - never a payload. Payloads are read
// from disk on demand, one at a time, so [Store.Scan] and [Store.Digest] stream:
// their working set is one record plus a per-dataset sorted key slice, not the
// dataset.
//
// # Determinism
//
// Nothing here reads a clock, a random source or a locale, and no output depends
// on map iteration order: every ordering is an explicit sort. Given the same
// sequence of [Store.Apply] calls the bytes on disk are identical, and given the
// same logical content the digest is identical regardless of the order, channel,
// batch or run that produced it.
package store
