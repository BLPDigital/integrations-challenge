package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// writeFile writes b to path, creating parents.
func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// appendFile appends b to path.
func appendFile(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// seedThree fills a fresh store with three suppliers and returns its directory
// and the digest of the first two, which is what recovery from a torn third
// record must reproduce.
func seedThree(t *testing.T) (dir string, digestOfTwo string) {
	t.Helper()
	ds := model.DatasetSupplier.String()
	numbers := []string{"0000417", "0000418", "0000419"}

	// Reference store: only the first two records.
	ref, _, _ := openTemp(t)
	for i, n := range numbers[:2] {
		sup := supplier(n, "S"+n, int64(i))
		if _, err := ref.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b1", "r1", "suppliers.csv", int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := ref.Digest()
	if err != nil {
		t.Fatal(err)
	}

	s, dir, _ := openTemp(t)
	for i, n := range numbers {
		sup := supplier(n, "S"+n, int64(i))
		if _, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b1", "r1", "suppliers.csv", int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, d
}

func TestOpenDiscardsTornFinalLine(t *testing.T) {
	dir, digestOfTwo := seedThree(t)
	path := filepath.Join(dir, model.DatasetSupplier.String()+segmentExt)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash in the middle of appending the third record: cut the file
	// so its final line has no terminating newline.
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lastNL := strings.LastIndexByte(string(full[:len(full)-1]), '\n')
	cut := int64(lastNL+1) + (info.Size()-int64(lastNL+1))/2
	if err := os.Truncate(path, cut); err != nil {
		t.Fatal(err)
	}

	s, _, rec := reopen(t, dir)
	if !strings.Contains(rec.joined(), "torn final record") {
		t.Errorf("warnings = %q, want a torn-record warning", rec.joined())
	}
	ds := model.DatasetSupplier.String()
	if got := s.Count(ds); got != 2 {
		t.Errorf("Count = %d, want 2 after discarding the torn record", got)
	}
	if _, ok := s.Get(ds, "0000419"); ok {
		t.Error("the torn record must not be visible")
	}
	got, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != digestOfTwo {
		t.Errorf("digest after recovery = %s, want %s", got, digestOfTwo)
	}
	if seq := s.HighSeq(); seq != 2 {
		t.Errorf("HighSeq = %d, want 2", seq)
	}
	// The segment must be usable again: the truncation put the next append on a
	// record boundary.
	sup := supplier("0000419", "S0000419", 2)
	out, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b2", "r2", "suppliers.csv", 3)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultApplied || out.Version != 1 || out.Seq != 3 {
		t.Errorf("re-apply after recovery = %+v", out)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _, rec2 := reopen(t, dir)
	if rec2.msgs != nil {
		t.Errorf("second reopen warned: %v", rec2.msgs)
	}
	if got := s2.Count(ds); got != 3 {
		t.Errorf("Count after clean reopen = %d, want 3", got)
	}
}

func TestOpenDiscardsUnparseableFinalLine(t *testing.T) {
	dir, digestOfTwo := seedThree(t)
	path := filepath.Join(dir, model.DatasetSupplier.String()+segmentExt)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lastNL := strings.LastIndexByte(string(full[:len(full)-1]), '\n')
	if err := os.Truncate(path, int64(lastNL+1)); err != nil {
		t.Fatal(err)
	}
	// A whole but unparseable last line: a filesystem that padded the tail of a
	// partial block, or a half-flushed record that happened to end in a newline.
	if err := appendFile(path, []byte("{\"seq\":3,\"dataset\":\"supp\n")); err != nil {
		t.Fatal(err)
	}
	s, _, rec := reopen(t, dir)
	if !strings.Contains(rec.joined(), "unparseable final record") {
		t.Errorf("warnings = %q, want an unparseable-record warning", rec.joined())
	}
	got, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != digestOfTwo {
		t.Errorf("digest after recovery = %s, want %s", got, digestOfTwo)
	}
}

func TestOpenFailsOnCorruptMiddleLine(t *testing.T) {
	dir, _ := seedThree(t)
	path := filepath.Join(dir, model.DatasetSupplier.String()+segmentExt)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Corruption anywhere but the tail is not a torn write, so it must not be
	// discarded silently: losing history is worse than refusing to open.
	firstNL := strings.IndexByte(string(full), '\n')
	corrupt := append([]byte("{not json}\n"), full[firstNL+1:]...)
	if err := writeFile(path, corrupt); err != nil {
		t.Fatal(err)
	}
	rec := &warnRecorder{}
	if _, err := OpenWith(dir, Options{Warn: rec.warn}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open = %v, want ErrCorrupt", err)
	}
}

func TestOpenSkipsBlankLine(t *testing.T) {
	dir, _ := seedThree(t)
	path := filepath.Join(dir, model.DatasetSupplier.String()+segmentExt)
	if err := appendFile(path, []byte("\n")); err != nil {
		t.Fatal(err)
	}
	s, _, rec := reopen(t, dir)
	if !strings.Contains(rec.joined(), "blank line") {
		t.Errorf("warnings = %q, want a blank-line warning", rec.joined())
	}
	if got := s.Count(model.DatasetSupplier.String()); got != 3 {
		t.Errorf("Count = %d, want 3", got)
	}
}

func TestOpenEmptyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open must create the directory: %v", err)
	}
	defer func() { _ = s.Close() }()
	if got := s.HighSeq(); got != 0 {
		t.Errorf("HighSeq = %d, want 0", got)
	}
	d, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 64 {
		t.Errorf("digest = %q, want 64 hex characters", d)
	}
}
