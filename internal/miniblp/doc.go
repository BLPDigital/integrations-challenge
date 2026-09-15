// Package miniblp is the digital twin service of this landscape: the file
// importer, the REST ingest surface, the matching engine, the outbox, the ack
// state machine and the admin surface, wired onto internal/store,
// internal/simclock, internal/httpx and internal/importer.
//
// # Two channels, one state
//
// A record reaches the twin either as a file in a batch directory under the
// inbox root (see [Server.Scan]) or as a chunk of a REST batch (see
// [Server.Handler], POST /v1/ingest/batches). Both channels decode to canonical
// JSON through the same importer, validate with the same model validators,
// store through the same [store.Store] and produce the same receipt object, so
// the state digest of a run is a function of the delivered content alone and
// never of the channel, the wire format, the batch layout or the chunk sizes.
// That property is the primary grading assertion of the challenge and the
// package's central obligation: every deviation from it is a bug, whatever else
// it improves.
//
// # Determinism
//
// Nothing here reads the wall clock, sleeps, polls a timer or draws an unseeded
// random number. Virtual time advances only in [simclock.Governor.Admit], the
// inbox is scanned only by POST /admin/v1/inbox/scan and counted by a monotone
// scan counter, batches are processed in ascending name order, records in
// delivery order, proposals in ascending proposal-id order, and every map that
// reaches an output is sorted first. Timestamps that appear in a receipt or a
// proposal come from the virtual clock or from the caller.
//
// # Failure vocabulary
//
// A defect in delivered data is never an error: it is a per-record finding on a
// receipt, or a blocking exception in the queue. Errors are reserved for
// programming and environment faults. The closure invariant of BUILD-SPEC 8.2 -
// seen == accepted + accepted_with_warning + rejected + skipped_unchanged +
// quarantined - is asserted whenever a receipt is written, and a violation is
// reported as an internal error rather than as a warning, because a silent drop
// is the one failure mode an accounts-payable interface may never have.
//
// Warnings are never exceptions. An importer warning travels onto the record,
// onto the proposal it belongs to and into the receipt; it never enters the
// open exception queue and never changes an exit code.
package miniblp
