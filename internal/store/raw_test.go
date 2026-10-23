package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

func TestPutRawIsContentAddressedAndIdempotent(t *testing.T) {
	// CP1252 high bytes: the raw area stores bytes, never text.
	body := []byte("VORLAUF;KRED-EXP;2.1\r\nKOPF;0000417;\"Z\xfcrcher Kantonalbank\";1250.00-\r\n")
	s, dir, _ := openTemp(t)
	sha, err := s.PutRaw(body)
	if err != nil {
		t.Fatal(err)
	}
	if sha != model.Sha256Hex(body) {
		t.Errorf("PutRaw returned %s, want the sha256 of the bytes", sha)
	}
	path := filepath.Join(dir, rawDir, sha[:2], sha)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("blob not stored under raw/xx/hash: %v", err)
	}
	if !s.HasRaw(sha) {
		t.Error("HasRaw = false for a stored blob")
	}
	again, err := s.PutRaw(body)
	if err != nil {
		t.Fatal(err)
	}
	if again != sha {
		t.Errorf("second PutRaw returned %s, want %s", again, sha)
	}
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info2.Size() != info.Size() {
		t.Error("writing the same bytes twice was not a no-op")
	}
	got, err := s.GetRaw(sha)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("GetRaw returned different bytes")
	}
	// No temporary file left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestPutRawEmpty(t *testing.T) {
	s, _, _ := openTemp(t)
	sha, err := s.PutRaw(nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRaw(sha)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("GetRaw returned %d bytes, want 0", len(got))
	}
}

func TestGetRawErrors(t *testing.T) {
	s, _, _ := openTemp(t)
	cases := []struct {
		name string
		sha  string
		want error
	}{
		{"empty", "", ErrSha256},
		{"short", "abc123", ErrSha256},
		{"uppercase", strings.Repeat("A", 64), ErrSha256},
		{"non hex", strings.Repeat("z", 64), ErrSha256},
		{"path traversal", "../../../../etc/passwd", ErrSha256},
		{"unknown", strings.Repeat("0", 64), ErrNotFound},
	}
	for _, c := range cases {
		_, err := s.GetRaw(c.sha)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: GetRaw = %v, want %v", c.name, err, c.want)
		}
		if s.HasRaw(c.sha) {
			t.Errorf("%s: HasRaw = true", c.name)
		}
	}
}

func TestRawSurvivesReopen(t *testing.T) {
	body := []byte("manifest bytes")
	s, dir, _ := openTemp(t)
	sha, err := s.PutRaw(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _, _ := reopen(t, dir)
	got, err := s2.GetRaw(sha)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Error("raw blob did not survive a reopen")
	}
	// The raw directory must not be mistaken for a dataset segment.
	if len(s2.Datasets()) != 0 {
		t.Errorf("Datasets = %v, want none", s2.Datasets())
	}
}
