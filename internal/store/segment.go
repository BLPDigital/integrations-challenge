package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// segmentExt is the suffix of every segment file.
const segmentExt = ".jsonl"

// readChunk is the granularity of a random-access line read. Revision lines are
// well under this in every dataset of this landscape, so a record is normally
// one ReadAt.
const readChunk = 8 << 10

// segment is one dataset's append-only file. size is the offset the next append
// lands at and is guarded by the store mutex; the file handle itself is used
// only through ReadAt and WriteAt, which are safe for concurrent use.
type segment struct {
	dataset string
	f       *os.File
	size    int64
}

// append writes one newline-terminated line at the end of the segment and
// fsyncs it, returning the offset it was written at. A failed or partial write
// is truncated away, so the segment never keeps a record the caller was told
// failed.
func (sg *segment) append(line []byte) (int64, error) {
	off := sg.size
	n, err := sg.f.WriteAt(line, off)
	if err != nil {
		if n > 0 {
			_ = sg.f.Truncate(off)
		}
		return 0, err
	}
	if err := sg.f.Sync(); err != nil {
		_ = sg.f.Truncate(off)
		return 0, err
	}
	sg.size = off + int64(n)
	return off, nil
}

// lineReader reads newline-terminated lines at arbitrary offsets of one segment,
// reusing a single buffer so a full-dataset scan allocates it once. It is not
// safe for concurrent use; each reading operation makes its own.
type lineReader struct {
	f    *os.File
	size int64
	buf  []byte
}

// at returns the line starting at off, including its newline. The returned
// slice aliases the reader's buffer and is invalidated by the next call.
func (lr *lineReader) at(off int64) ([]byte, error) {
	if off < 0 || off >= lr.size {
		return nil, io.ErrUnexpectedEOF
	}
	lr.buf = lr.buf[:0]
	for {
		have := int64(len(lr.buf))
		want := int64(readChunk)
		if rem := lr.size - off - have; rem < want {
			want = rem
		}
		if want <= 0 {
			return nil, io.ErrUnexpectedEOF
		}
		lr.buf = grow(lr.buf, int(want))
		n, err := lr.f.ReadAt(lr.buf[have:have+want], off+have)
		lr.buf = lr.buf[:have+int64(n)]
		if i := bytes.IndexByte(lr.buf[have:], '\n'); i >= 0 {
			return lr.buf[:have+int64(i)+1], nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
}

// grow extends b by n bytes of length, reallocating if necessary.
func grow(b []byte, n int) []byte {
	if cap(b)-len(b) >= n {
		return b[:len(b)+n]
	}
	nb := make([]byte, len(b), 2*(len(b)+n))
	copy(nb, b)
	return nb[:len(b)+n]
}

// revisionWire is the on-disk shape of a Revision. Payload stays raw so it can
// be decoded through model.DecodeJSONTree, which keeps numbers as json.Number
// and therefore never introduces a float.
type revisionWire struct {
	Seq            int64           `json:"seq"`
	Dataset        string          `json:"dataset"`
	Key            string          `json:"key"`
	Version        int             `json:"version"`
	Deleted        bool            `json:"deleted"`
	ContentHash    string          `json:"content_hash"`
	Payload        json.RawMessage `json:"payload"`
	Provenance     Provenance      `json:"provenance"`
	ProvenanceOnly bool            `json:"provenance_only"`
}

// encodeRevision renders rev as its canonical newline-terminated segment line.
func encodeRevision(rev Revision) ([]byte, error) {
	b, err := model.CanonicalJSON(rev)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayload, err)
	}
	return append(b, '\n'), nil
}

// decodeRevision parses one segment line.
func decodeRevision(line []byte) (Revision, error) {
	var w revisionWire
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&w); err != nil {
		return Revision{}, err
	}
	rev := Revision{
		Seq:            w.Seq,
		Dataset:        w.Dataset,
		Key:            w.Key,
		Version:        w.Version,
		Deleted:        w.Deleted,
		ContentHash:    w.ContentHash,
		Provenance:     w.Provenance,
		ProvenanceOnly: w.ProvenanceOnly,
	}
	if len(w.Payload) > 0 && !bytes.Equal(w.Payload, []byte("null")) {
		payload, err := model.DecodeJSONTree(w.Payload)
		if err != nil {
			return Revision{}, err
		}
		rev.Payload = payload
	}
	return rev, nil
}

// recordHead is the index entry of one record. It deliberately holds no payload:
// the whole dataset must never be resident.
type recordHead struct {
	version     int
	contentHash string
	deleted     bool
	seq         int64
	// current is the offset of the last content-bearing revision, the one Get,
	// Scan and Digest read. Provenance-only lines do not move it.
	current int64
	// offsets are the offsets of every revision line for this key, in append
	// order, which is seq order.
	offsets []int64
}

// segmentPath returns the file backing a dataset.
func (s *Store) segmentPath(dataset string) string {
	return filepath.Join(s.dir, dataset+segmentExt)
}

// openSegment opens or creates a dataset's segment, replays it into the index
// and registers it. It assumes the store mutex is held exclusively.
func (s *Store) openSegment(dataset string) (*segment, error) {
	if sg, ok := s.segs[dataset]; ok {
		return sg, nil
	}
	path := s.segmentPath(dataset)
	_, statErr := os.Stat(path)
	fresh := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if fresh {
		// The directory entry must survive a crash too, or a fsynced record can
		// live in a file nobody can find.
		if err := syncDir(s.dir); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	sg := &segment{dataset: dataset, f: f, size: st.Size()}
	idx := make(map[string]*recordHead)
	if err := s.replay(sg, idx); err != nil {
		_ = f.Close()
		return nil, err
	}
	s.segs[dataset] = sg
	s.index[dataset] = idx
	return sg, nil
}

// replay rebuilds idx from sg, repairing a torn final write.
func (s *Store) replay(sg *segment, idx map[string]*recordHead) error {
	name := filepath.Base(s.segmentPath(sg.dataset))
	lr := &lineReader{f: sg.f, size: sg.size}
	var off int64
	for off < sg.size {
		line, err := lr.at(off)
		if err != nil {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				return err
			}
			// No terminating newline: the process died mid-append. Discard the
			// fragment and truncate so the next append starts at a boundary.
			s.warn(fmt.Sprintf("%s: discarding torn final record of %d bytes at offset %d", name, sg.size-off, off))
			if err := sg.f.Truncate(off); err != nil {
				return err
			}
			sg.size = off
			return nil
		}
		n := int64(len(line))
		if len(bytes.TrimSpace(line)) == 0 {
			s.warn(fmt.Sprintf("%s: skipping blank line at offset %d", name, off))
			off += n
			continue
		}
		rev, derr := decodeRevision(line)
		if derr != nil {
			if off+n == sg.size {
				// A complete but unparseable last line is the same accident as a
				// torn one on a filesystem that pads a partial block.
				s.warn(fmt.Sprintf("%s: discarding unparseable final record at offset %d: %v", name, off, derr))
				if err := sg.f.Truncate(off); err != nil {
					return err
				}
				sg.size = off
				return nil
			}
			return fmt.Errorf("%w: %s offset %d: %v", ErrCorrupt, name, off, derr)
		}
		s.indexRevision(idx, rev, off)
		off += n
	}
	return nil
}

// indexRevision folds one replayed or freshly appended revision into an index.
func (s *Store) indexRevision(idx map[string]*recordHead, rev Revision, off int64) {
	h := idx[rev.Key]
	if h == nil {
		h = &recordHead{}
		idx[rev.Key] = h
	}
	h.offsets = append(h.offsets, off)
	if !rev.ProvenanceOnly {
		h.version = rev.Version
		h.contentHash = rev.ContentHash
		h.deleted = rev.Deleted
		h.seq = rev.Seq
		h.current = off
	}
	if rev.Seq > s.seq {
		s.seq = rev.Seq
	}
}

// syncDir fsyncs a directory so a newly created name is durable.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
