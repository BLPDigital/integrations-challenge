package store

import "errors"

// Store errors. They are sentinel values; callers compare with errors.Is.
var (
	// ErrClosed reports use of a store after [Store.Close].
	ErrClosed = errors.New("store: closed")
	// ErrDataset reports a dataset name that cannot be a segment file name.
	ErrDataset = errors.New("store: invalid dataset name")
	// ErrKeyEmpty reports an empty natural key.
	ErrKeyEmpty = errors.New("store: empty natural key")
	// ErrContentHash reports a Revision whose ContentHash disagrees with its
	// Payload. The store computes the hash itself; a caller-supplied one is only
	// ever a cross-check.
	ErrContentHash = errors.New("store: content hash does not match payload")
	// ErrPayload reports a payload that cannot be canonicalized, for instance one
	// containing a float64.
	ErrPayload = errors.New("store: payload is not canonicalizable")
	// ErrNotFound reports a record, proposal or exception that does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrCorrupt reports an unparseable revision line that is not a torn final
	// write and therefore cannot be discarded safely.
	ErrCorrupt = errors.New("store: corrupt segment")
	// ErrSha256 reports a malformed content address: a raw blob key must be 64
	// lowercase hex digits.
	ErrSha256 = errors.New("store: malformed sha256")
	// ErrAckStatus reports an ack carrying a status outside the closed set.
	ErrAckStatus = errors.New("store: unknown ack status")
)

// Ingest channels. The channel a revision arrived through is provenance and is
// deliberately absent from the state digest: the same logical record delivered
// by file or by REST must hash identically.
const (
	// ChannelFile is the inbox drop directory channel.
	ChannelFile = "file"
	// ChannelREST is the HTTP ingest channel.
	ChannelREST = "rest"
	// ChannelInternal is the twin itself: seeded data, matching output, acks.
	ChannelInternal = "internal"
)

// Provenance records where one revision came from. Every field is a fact about
// the delivery, never about the content, which is why none of it reaches
// [Store.Digest].
//
// The four ordinal fields are pointers because "not applicable" and "zero" are
// different facts: a REST chunk has no source line, and line 0 does not exist.
// They marshal as JSON null when absent.
type Provenance struct {
	// BatchID is the ingest batch this delivery belonged to.
	BatchID string `json:"batch_id"`
	// Channel is one of ChannelFile, ChannelREST or ChannelInternal.
	Channel string `json:"channel"`
	// SourceFile is the file name inside the batch, "" on the REST channel.
	SourceFile string `json:"source_file"`
	// SourceLine is the 1-based line in SourceFile the record was parsed from.
	SourceLine *int64 `json:"source_line"`
	// ChunkOrdinal is the X-Chunk-Ordinal of the REST chunk.
	ChunkOrdinal *int64 `json:"chunk_ordinal"`
	// RecordOrdinal is the 0-based position of the record within its file or chunk.
	RecordOrdinal *int64 `json:"record_ordinal"`
	// SourceSHA256 is the content address of the raw source bytes, as stored by
	// [Store.PutRaw]. It closes the audit chain back to the delivered file.
	SourceSHA256 string `json:"source_sha256"`
	// Profile is the manifest profile, e.g. "blp-canonical-v1" or "kredexp-2.1".
	Profile string `json:"profile"`
	// Format is the wire format the record was decoded from, e.g. "csv".
	Format string `json:"format"`
	// Encoding is the declared source encoding, e.g. "windows-1252".
	Encoding string `json:"encoding"`
	// SourceSystem is the upstream system named by the manifest.
	SourceSystem string `json:"source_system"`
	// RunID is the connector run that delivered the record.
	RunID string `json:"run_id"`
	// ReceivedScan is the inbox scan counter that picked the batch up. It is a
	// monotone integer, never a timestamp.
	ReceivedScan *int64 `json:"received_scan"`
	// RequestSeq is the server request sequence of the REST call.
	RequestSeq *int64 `json:"request_seq"`
}

// Revision is one line of a segment file: a single delivery of a single record.
//
// On input to [Store.Apply] only Dataset, Key, Payload, Provenance, Deleted and
// IfVersion are read. Seq, Version and ContentHash are assigned by the store;
// a non-empty ContentHash is checked against the payload and otherwise ignored,
// so a revision read back from [Store.History] can be replayed unchanged.
type Revision struct {
	// Seq is the store-wide append sequence, monotone across all datasets.
	Seq int64 `json:"seq"`
	// Dataset is the segment this revision lives in.
	Dataset string `json:"dataset"`
	// Key is the record's natural key, trimmed. Leading zeros are significant.
	Key string `json:"key"`
	// Version is the record's version after this revision, counting from 1.
	Version int `json:"version"`
	// Deleted marks a tombstone: the record is absent from Scan and Digest but
	// its history is preserved.
	Deleted bool `json:"deleted"`
	// ContentHash is [model.ContentHash] of Payload.
	ContentHash string `json:"content_hash"`
	// Payload is the record content. After a replay from disk it is the generic
	// tree [model.DecodeJSONTree] produces, whose canonical form is byte-identical
	// to the one that was written, so no float ever appears.
	Payload any `json:"payload"`
	// Provenance is where this delivery came from.
	Provenance Provenance `json:"provenance"`
	// ProvenanceOnly marks a re-delivery of content the store already held: the
	// version and content hash repeat the preceding revision and only the
	// provenance is new. [Store.History] uses it to tell an audit entry from a
	// change; [Store.Get], [Store.Scan] and [Store.Digest] ignore such lines.
	ProvenanceOnly bool `json:"provenance_only,omitempty"`
	// IfVersion is an optional optimistic-concurrency precondition: the version
	// the caller believes is current, 0 for "must not exist". A mismatch yields
	// [ResultConflict] and writes nothing. It is a property of the request, not
	// of the record, and is never persisted.
	IfVersion *int `json:"-"`
}

// Result is the outcome class of an [Store.Apply].
type Result string

// The three Apply results.
const (
	// ResultApplied means the record was created or its content changed; the
	// version was incremented and a content-bearing revision was appended.
	ResultApplied Result = "applied"
	// ResultUnchanged means the incoming payload hashed to the stored content
	// hash. The version is untouched and a provenance-only revision was appended
	// so the re-delivery stays auditable. Receipts report this as
	// "skipped_unchanged".
	ResultUnchanged Result = "unchanged"
	// ResultConflict means the IfVersion precondition failed. Nothing was
	// written and Outcome.Version carries the version that is actually current.
	ResultConflict Result = "conflict"
)

// Outcome is what [Store.Apply] did, and enough state to answer the caller's
// receipt line without a second lookup.
type Outcome struct {
	// Result is the outcome class.
	Result Result `json:"result"`
	// Dataset and Key identify the record.
	Dataset string `json:"dataset"`
	Key     string `json:"key"`
	// Version is the record's current version after Apply: the new version for
	// ResultApplied, the untouched one for ResultUnchanged, and the actual
	// current one for ResultConflict.
	Version int `json:"version"`
	// ContentHash is the record's current content hash, which for ResultConflict
	// is the stored one, not the rejected one.
	ContentHash string `json:"content_hash"`
	// Deleted reports whether the record is currently a tombstone.
	Deleted bool `json:"deleted"`
	// Seq is the sequence of the appended revision, 0 for ResultConflict.
	Seq int64 `json:"seq"`
}
