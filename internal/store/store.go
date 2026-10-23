package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// Options configures a store. The zero value is the production configuration.
type Options struct {
	// Warn receives recovery and consistency warnings, one per call, without a
	// trailing newline. The default writes them to stderr prefixed "store: ".
	// There is deliberately no timestamp: log lines are compared byte for byte.
	Warn func(msg string)
}

// Store is an append-only revision store over a directory. It is safe for
// concurrent use; see the package documentation for the exact guarantee.
type Store struct {
	dir  string
	warn func(string)

	mu     sync.RWMutex
	closed bool
	// seq is the highest revision sequence written, store-wide.
	seq int64
	// segs and index are keyed by dataset name. A dataset appears in both or in
	// neither.
	segs  map[string]*segment
	index map[string]map[string]*recordHead
}

// Open opens or creates the store rooted at dir, replaying every segment file to
// rebuild the index. A torn final record is discarded with a warning on stderr
// and the store still opens; see the package documentation.
func Open(dir string) (*Store, error) { return OpenWith(dir, Options{}) }

// OpenWith is [Open] with explicit options.
func OpenWith(dir string, opts Options) (*Store, error) {
	warn := opts.Warn
	if warn == nil {
		warn = func(msg string) { fmt.Fprintf(os.Stderr, "store: %s\n", msg) }
	}
	if dir == "" {
		return nil, fmt.Errorf("store: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:   dir,
		warn:  warn,
		segs:  make(map[string]*segment),
		index: make(map[string]map[string]*recordHead),
	}
	names, err := filepath.Glob(filepath.Join(dir, "*"+segmentExt))
	if err != nil {
		return nil, err
	}
	// Glob order is filesystem order on some platforms; sort so replay, and any
	// warning it emits, is reproducible.
	sort.Strings(names)
	for _, name := range names {
		dataset := strings.TrimSuffix(filepath.Base(name), segmentExt)
		if !ValidDataset(dataset) {
			s.warn(fmt.Sprintf("ignoring segment file with invalid dataset name %q", filepath.Base(name)))
			continue
		}
		if _, err := s.openSegment(dataset); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Close closes every segment file. Every subsequent operation reports
// [ErrClosed]. Close is idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var first error
	for _, dataset := range sortedKeys(s.segs) {
		if err := s.segs[dataset].f.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

// ValidDataset reports whether name can be a dataset: 1 to 64 characters, a
// lowercase letter followed by lowercase letters, digits and underscores. The
// restriction exists because a dataset name is a file name; it is deliberately
// wider than [model.IsKnownDataset] so the store can hold its own auxiliary
// datasets ([DatasetProposal], [DatasetException]).
func ValidDataset(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}
	return true
}

// Datasets returns the datasets that have a segment file, in ascending byte
// order.
func (s *Store) Datasets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedKeys(s.segs)
}

// HighSeq returns the highest revision sequence written, 0 for an empty store.
// It is monotone across every dataset, so it is a usable store-wide watermark.
func (s *Store) HighSeq() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seq
}

// Count returns the number of live records in a dataset: tombstones and unknown
// datasets both count 0.
func (s *Store) Count(dataset string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, h := range s.index[dataset] {
		if !h.deleted {
			n++
		}
	}
	return n
}

// Apply upserts one record and returns what it did.
//
// The content hash of rec.Payload decides:
//
//   - unknown key, or a hash different from the stored one: [ResultApplied]. The
//     version is incremented and a content-bearing revision is appended.
//   - the same hash and the same Deleted flag: [ResultUnchanged]. The version is
//     untouched, but a provenance-only revision IS appended, so a re-delivery
//     stays auditable and [Store.History] shows it.
//   - rec.IfVersion set and different from the current version (0 for a record
//     that does not exist): [ResultConflict], and nothing is written.
//
// The revision is fsynced before Apply returns. On any write error the segment is
// truncated back and the index is untouched, so a reported failure never left a
// record behind.
func (s *Store) Apply(rec Revision) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(rec)
}

// applyLocked is [Store.Apply] with the write lock already held. The typed
// proposal and exception mutators use it so their read-modify-write is atomic.
func (s *Store) applyLocked(rec Revision) (Outcome, error) {
	if s.closed {
		return Outcome{}, ErrClosed
	}
	if !ValidDataset(rec.Dataset) {
		return Outcome{}, fmt.Errorf("%w: %q", ErrDataset, rec.Dataset)
	}
	key := strings.TrimSpace(rec.Key)
	if key == "" {
		return Outcome{}, ErrKeyEmpty
	}
	payloadJSON, err := model.CanonicalJSON(rec.Payload)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrPayload, err)
	}
	hash := model.Sha256Hex(payloadJSON)
	if rec.ContentHash != "" && rec.ContentHash != hash {
		return Outcome{}, fmt.Errorf("%w: %s/%s", ErrContentHash, rec.Dataset, key)
	}
	sg, err := s.openSegment(rec.Dataset)
	if err != nil {
		return Outcome{}, err
	}
	head := s.index[rec.Dataset][key]
	cur := 0
	if head != nil {
		cur = head.version
	}
	if rec.IfVersion != nil && *rec.IfVersion != cur {
		out := Outcome{Result: ResultConflict, Dataset: rec.Dataset, Key: key, Version: cur}
		if head != nil {
			out.ContentHash = head.contentHash
			out.Deleted = head.deleted
		}
		return out, nil
	}
	unchanged := head != nil && head.contentHash == hash && head.deleted == rec.Deleted
	line := Revision{
		Seq:         s.seq + 1,
		Dataset:     rec.Dataset,
		Key:         key,
		Deleted:     rec.Deleted,
		ContentHash: hash,
		Payload:     rec.Payload,
		Provenance:  rec.Provenance,
	}
	if unchanged {
		line.Version = head.version
		line.ProvenanceOnly = true
	} else {
		line.Version = cur + 1
	}
	encoded, err := encodeRevision(line)
	if err != nil {
		return Outcome{}, err
	}
	off, err := sg.append(encoded)
	if err != nil {
		return Outcome{}, err
	}
	s.seq = line.Seq
	s.indexRevision(s.index[rec.Dataset], line, off)
	res := ResultApplied
	if unchanged {
		res = ResultUnchanged
	}
	h := s.index[rec.Dataset][key]
	return Outcome{
		Result:      res,
		Dataset:     rec.Dataset,
		Key:         key,
		Version:     h.version,
		ContentHash: h.contentHash,
		Deleted:     h.deleted,
		Seq:         line.Seq,
	}, nil
}

// Delete appends a tombstone for a record. The record disappears from
// [Store.Count], [Store.Scan] and [Store.Digest]; its history is preserved and a
// later Apply revives it at the next version. Deleting an already deleted record
// is [ResultUnchanged].
func (s *Store) Delete(dataset, key string, prov Provenance) (Outcome, error) {
	return s.Apply(Revision{Dataset: dataset, Key: key, Deleted: true, Provenance: prov})
}

// Get returns the current revision of a record, or false when the record does not
// exist or is a tombstone.
//
// The signature carries no error because every caller treats a record as present
// or absent. A read error on the segment file is reported through [Options.Warn]
// and returns false; callers that must distinguish use [Store.GetErr].
func (s *Store) Get(dataset, key string) (*Revision, bool) {
	rev, ok, err := s.GetErr(dataset, key)
	if err != nil {
		s.warn(fmt.Sprintf("read %s/%s: %v", dataset, key, err))
		return nil, false
	}
	return rev, ok
}

// GetErr is [Store.Get] with the segment read error surfaced. It reports false
// with a nil error for an unknown key and for a tombstone.
func (s *Store) GetErr(dataset, key string) (*Revision, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, false, ErrClosed
	}
	rev, ok, err := s.getLocked(dataset, key)
	if err != nil || !ok {
		return nil, false, err
	}
	return &rev, true, nil
}

// getLocked returns the current revision of a live record with the lock already
// held.
func (s *Store) getLocked(dataset, key string) (Revision, bool, error) {
	head := s.index[dataset][strings.TrimSpace(key)]
	if head == nil || head.deleted {
		return Revision{}, false, nil
	}
	rev, err := s.readAtLocked(dataset, head.current, nil)
	if err != nil {
		return Revision{}, false, err
	}
	return rev, true, nil
}

// Head returns the indexed state of a record without touching the disk: its
// current version, content hash, seq, tombstone flag and revision count. It is
// the cheap probe for an If-Match check or a UI list.
func (s *Store) Head(dataset, key string) (version int, contentHash string, seq int64, deleted bool, revisions int, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	head := s.index[dataset][strings.TrimSpace(key)]
	if head == nil {
		return 0, "", 0, false, 0, false
	}
	return head.version, head.contentHash, head.seq, head.deleted, len(head.offsets), true
}

// History returns every revision of a record in append order, including
// provenance-only re-deliveries and tombstones. An unknown key yields an empty
// history and no error.
func (s *Store) History(dataset, key string) ([]Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	head := s.index[dataset][strings.TrimSpace(key)]
	if head == nil {
		return nil, nil
	}
	out := make([]Revision, 0, len(head.offsets))
	lr := s.readerLocked(dataset)
	for _, off := range head.offsets {
		rev, err := s.readAtLocked(dataset, off, lr)
		if err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, nil
}

// Scan calls fn for every live record of a dataset in ascending natural-key byte
// order, stopping early when fn returns false. Tombstones are skipped.
//
// The dataset is never materialized: one sorted key slice is built per call from
// the index, and payloads are read from the segment one at a time. Memory is
// therefore O(keys) for the slice plus O(1) records, not O(dataset).
//
// fn runs with the store's read lock held and must not call back into the store.
func (s *Store) Scan(dataset string, fn func(Revision) bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	keys, offsets := s.liveLocked(dataset)
	lr := s.readerLocked(dataset)
	for i, off := range offsets {
		rev, err := s.readAtLocked(dataset, off, lr)
		if err != nil {
			return fmt.Errorf("store: scan %s/%s: %w", dataset, keys[i], err)
		}
		if !fn(rev) {
			return nil
		}
	}
	return nil
}

// liveLocked returns the live keys of a dataset in ascending byte order and the
// offset of each one's current revision, in the same order.
func (s *Store) liveLocked(dataset string) ([]string, []int64) {
	recs := s.index[dataset]
	if len(recs) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(recs))
	for k, h := range recs {
		if !h.deleted {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	offsets := make([]int64, len(keys))
	for i, k := range keys {
		offsets[i] = recs[k].current
	}
	return keys, offsets
}

// readerLocked returns a fresh line reader over a dataset's segment, or nil when
// the dataset has no segment.
func (s *Store) readerLocked(dataset string) *lineReader {
	sg := s.segs[dataset]
	if sg == nil {
		return nil
	}
	return &lineReader{f: sg.f, size: sg.size}
}

// readAtLocked decodes the revision at off. lr may be nil, in which case a
// throwaway reader is used.
func (s *Store) readAtLocked(dataset string, off int64, lr *lineReader) (Revision, error) {
	if lr == nil {
		lr = s.readerLocked(dataset)
		if lr == nil {
			return Revision{}, fmt.Errorf("%w: dataset %q", ErrNotFound, dataset)
		}
	}
	line, err := lr.at(off)
	if err != nil {
		return Revision{}, err
	}
	return decodeRevision(line)
}

// sortedKeys returns the keys of m in ascending byte order, so no output ever
// depends on map iteration order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
