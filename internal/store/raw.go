package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// rawDir is the name of the content-addressed blob area inside the store.
const rawDir = "raw"

// PutRaw stores source bytes content-addressed and returns their lowercase hex
// sha256. The blob lands at raw/<sha256[0:2]>/<sha256>, written through a
// temporary file in the same directory and renamed, then fsynced together with
// its directory.
//
// Writing the same bytes twice is a no-op that returns the same address: the
// blob area is immutable, so a re-delivered file costs one stat. The address is
// what [Provenance.SourceSHA256] refers to, which is how a record's audit chain
// reaches the exact bytes it was parsed from.
//
// PutRaw takes the store's write lock, so the temporary name needs no randomness
// and concurrent callers cannot collide.
func (s *Store) PutRaw(b []byte) (string, error) {
	sha := model.Sha256Hex(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	dir := filepath.Join(s.dir, rawDir, sha[:2])
	final := filepath.Join(dir, sha)
	if _, err := os.Stat(final); err == nil {
		return sha, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := syncDir(dir); err != nil {
		return "", err
	}
	return sha, nil
}

// GetRaw returns the bytes stored under a content address. An unknown address is
// [ErrNotFound]; an address that is not 64 lowercase hex digits is [ErrSha256],
// checked before any filesystem access so a path cannot be smuggled through.
func (s *Store) GetRaw(sha string) ([]byte, error) {
	if !validSha256(sha) {
		return nil, fmt.Errorf("%w: %q", ErrSha256, sha)
	}
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	b, err := os.ReadFile(filepath.Join(s.dir, rawDir, sha[:2], sha))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: raw %s", ErrNotFound, sha)
	}
	return b, err
}

// HasRaw reports whether a content address is present. A malformed address is
// simply absent.
func (s *Store) HasRaw(sha string) bool {
	if !validSha256(sha) {
		return false
	}
	_, err := os.Stat(filepath.Join(s.dir, rawDir, sha[:2], sha))
	return err == nil
}

// validSha256 reports whether s is exactly 64 lowercase hex digits.
func validSha256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
