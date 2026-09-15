package store

import (
	"fmt"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// DatasetException is the segment holding the exception queue, keyed by
// [ExceptionKey].
//
// Exceptions are append-only for the same reason proposals are: the queue's whole
// value is that you can see when an item was raised, by which delivery, and when
// and by which delivery it was resolved. They are excluded from [Store.Digest],
// because an exception payload is a statement about a delivery, not logical
// content, and because the graded exception set is read from the queue itself.
const DatasetException = "exception"

// Exception states.
const (
	// ExceptionOpen is an exception awaiting resolution. The graded exception set
	// is exactly the open ones.
	ExceptionOpen = "open"
	// ExceptionResolved is one that a later delivery cleared.
	ExceptionResolved = "resolved"
)

// Exception stages, i.e. where in the pipeline the exception was raised.
const (
	// StageImport is parsing and record validation.
	StageImport = "import"
	// StageMatch is the matching engine.
	StageMatch = "match"
	// StagePost is the posting attempt.
	StagePost = "post"
	// StageAck is ack processing.
	StageAck = "ack"
	// StageInfo is an informational row that never blocks. Grading ignores these,
	// which is what makes it safe to surface a warning without over-rejecting.
	StageInfo = "info"
)

// Exception is one entry of the twin's exception queue.
//
// Its identity is (SubjectKey, Code), the pair grading compares as a symmetric
// difference: raising the same exception again from a later delivery is therefore
// content-idempotent and only appends provenance, while a changed message or a
// resolution is a new revision of the same entry.
type Exception struct {
	// SubjectKey is the natural key of the thing at fault: an invoice key, a
	// proposal id, a batch id.
	SubjectKey string `json:"subject_key"`
	// SubjectType names what SubjectKey refers to, e.g. "invoice", "proposal",
	// "batch", "file", "record".
	SubjectType string `json:"subject_type"`
	// Stage is one of the Stage* constants.
	Stage string `json:"stage"`
	// Code is the published exception code, e.g. "EXC_FX_RATE_MISSING". Only
	// published codes may be used: an unpublished code cannot be graded.
	Code string `json:"code"`
	// Field is the field at fault, "" when the exception is about the record as a
	// whole.
	Field string `json:"field"`
	// Message is human-readable and must never carry the value the client was
	// supposed to compute: an exception says what is wrong and why, never what the
	// right answer would have been.
	Message string `json:"message"`
	// State is [ExceptionOpen] or [ExceptionResolved].
	State string `json:"state"`
	// SourceBatchID, SourceFileOrChunk and SourceLineOrOrdinal locate the
	// delivery that raised it, in the shape exceptions.csv wants.
	SourceBatchID       string `json:"source_batch_id"`
	SourceFileOrChunk   string `json:"source_file_or_chunk"`
	SourceLineOrOrdinal string `json:"source_line_or_ordinal"`
	// Details carries additional named facts, e.g. both gross amounts of an
	// amended duplicate invoice. Canonical JSON sorts the keys, so the map's
	// iteration order can never reach a hash or an output.
	Details map[string]string `json:"details"`
}

// ExceptionKey returns the key of an exception: the subject key with the code
// appended as the final component, joined by [model.KeySeparator].
//
// SubjectKey may itself be composite - an invoice key is supplier number plus
// invoice number - so the code goes last and [SplitExceptionKey] splits at the
// last separator, which makes the join reversible.
func ExceptionKey(subjectKey, code string) string {
	return strings.TrimSpace(subjectKey) + model.KeySeparator + strings.TrimSpace(code)
}

// SplitExceptionKey returns the subject key and code of an exception key. A key
// without a separator yields the whole string as the subject key and an empty
// code.
func SplitExceptionKey(key string) (subjectKey, code string) {
	i := strings.LastIndex(key, model.KeySeparator)
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+len(model.KeySeparator):]
}

// Key returns the exception's natural key.
func (e Exception) Key() string { return ExceptionKey(e.SubjectKey, e.Code) }

// normalized fills the derived fields: default state and a non-nil details map,
// so two identical exceptions hash identically whichever way they were built.
func (e Exception) normalized() Exception {
	e.SubjectKey = strings.TrimSpace(e.SubjectKey)
	e.Code = strings.TrimSpace(e.Code)
	if e.State == "" {
		e.State = ExceptionOpen
	}
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	return e
}

// RaiseException appends an exception. Raising the identical exception again is
// [ResultUnchanged]: the queue does not grow, but the re-delivery is recorded as
// a provenance-only revision, so "this failed again in run 3" is answerable.
//
// A previously resolved exception whose content is raised again reopens, because
// the state field is part of the content.
func (s *Store) RaiseException(e Exception, prov Provenance) (Outcome, error) {
	if strings.TrimSpace(e.SubjectKey) == "" {
		return Outcome{}, fmt.Errorf("%w: exception subject key", ErrKeyEmpty)
	}
	if strings.TrimSpace(e.Code) == "" {
		return Outcome{}, fmt.Errorf("%w: exception code", ErrKeyEmpty)
	}
	e = e.normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(Revision{
		Dataset:    DatasetException,
		Key:        e.Key(),
		Payload:    e,
		Provenance: prov,
	})
}

// ResolveException moves an exception to [ExceptionResolved]. An already resolved
// exception is [ResultUnchanged]; an unknown one is [ErrNotFound].
func (s *Store) ResolveException(subjectKey, code string, prov Provenance) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.getExceptionLocked(subjectKey, code)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("%w: exception %s/%s", ErrNotFound, subjectKey, code)
	}
	e.State = ExceptionResolved
	e = e.normalized()
	return s.applyLocked(Revision{
		Dataset:    DatasetException,
		Key:        e.Key(),
		Payload:    e,
		Provenance: prov,
	})
}

// GetException returns one exception by subject key and code.
func (s *Store) GetException(subjectKey, code string) (Exception, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getExceptionLocked(subjectKey, code)
}

// getExceptionLocked reads and decodes an exception with the lock already held.
func (s *Store) getExceptionLocked(subjectKey, code string) (Exception, bool, error) {
	rev, ok, err := s.getLocked(DatasetException, ExceptionKey(subjectKey, code))
	if err != nil || !ok {
		return Exception{}, false, err
	}
	var e Exception
	if err := decodePayload(rev.Payload, &e); err != nil {
		return Exception{}, false, err
	}
	return e, true, nil
}

// ScanExceptions calls fn for every exception in ascending key order, stopping
// early when fn returns false. A non-empty state filters on [Exception.State];
// "" yields all of them. It streams; fn must not call back into the store.
func (s *Store) ScanExceptions(state string, fn func(Exception) bool) error {
	var derr error
	err := s.Scan(DatasetException, func(rev Revision) bool {
		var e Exception
		if derr = decodePayload(rev.Payload, &e); derr != nil {
			return false
		}
		if state != "" && e.State != state {
			return true
		}
		return fn(e)
	})
	if err != nil {
		return err
	}
	return derr
}

// Exceptions returns the exceptions in a state, in ascending key order. It
// materializes the slice, which is what an admin endpoint wants; the queue is
// bounded by design (a few hundred entries per scenario). Pass "" for all states
// and use [Store.ScanExceptions] when the bound is not acceptable.
func (s *Store) Exceptions(state string) ([]Exception, error) {
	var out []Exception
	err := s.ScanExceptions(state, func(e Exception) bool {
		out = append(out, e)
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CountExceptions returns the number of exceptions in a state; "" counts all.
func (s *Store) CountExceptions(state string) (int, error) {
	n := 0
	err := s.ScanExceptions(state, func(Exception) bool {
		n++
		return true
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
