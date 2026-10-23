package erp

import (
	"os"
	"path/filepath"
	"strings"
)

// Export drop naming. The sender writes a data file and then, once the data file
// is completely written, an empty sentinel with the same base name and the .ok
// extension. A data file without its sentinel must not be read.
const (
	// ExportPrefix is the file name prefix of every KRED-EXP delivery.
	ExportPrefix = "KRED_"
	// ExportDataSuffix is the data file extension.
	ExportDataSuffix = ".txt"
	// ExportOKSuffix is the sentinel extension.
	ExportOKSuffix = ".ok"
)

// writeExport writes the scenario's legacy deliveries to the export directory,
// simulating the subsidiary's SFTP drop.
//
// Per delivery the data file is written, flushed and closed first, and only then
// is the empty .ok sentinel created: the sentinel means "these bytes are
// complete", so writing it first would make the contract a lie. Stale deliveries
// from an earlier seed are removed first, so a reseed cannot leave a file from
// another scenario behind for a connector to find.
//
// An empty ExportDir disables the drop, which is what a REST-only test wants.
func (s *Server) writeExport() error {
	s.mu.Lock()
	dir := s.cfg.ExportDir
	set := s.data.set
	st := s.st
	s.mu.Unlock()
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := removeStaleDeliveries(dir); err != nil {
		return err
	}
	written := make([]string, 0, 2*len(set.Deliveries))
	for i := range set.Deliveries {
		del := set.Deliveries[i]
		if err := writeFileSynced(filepath.Join(dir, del.Name), del.Bytes); err != nil {
			return err
		}
		written = append(written, del.Name)
		// The sentinel is empty and is written LAST.
		if err := writeFileSynced(filepath.Join(dir, del.OKName), nil); err != nil {
			return err
		}
		written = append(written, del.OKName)
	}
	s.mu.Lock()
	// A reseed may have replaced the run state while the files were being
	// written; in that case these names belong to a run that no longer exists.
	if s.st == st {
		st.exportFiles = written
	}
	s.mu.Unlock()
	return nil
}

// removeStaleDeliveries deletes the KRED-EXP data files and sentinels of an
// earlier seed. Only files whose names this service produces are touched: a
// reviewer's notes or a connector's state file in the same directory survive.
func removeStaleDeliveries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// os.ReadDir sorts its result, so the removal order is deterministic.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, ExportPrefix) {
			continue
		}
		if !strings.HasSuffix(name, ExportDataSuffix) && !strings.HasSuffix(name, ExportOKSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// writeFileSynced writes b to path and fsyncs it before returning, so the .ok
// sentinel that follows really does mean the bytes are on disk.
func writeFileSynced(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ExportFiles returns the names written to the export drop in write order, each
// delivery's data file immediately followed by its sentinel.
func (s *Server) ExportFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.st.exportFiles))
	copy(out, s.st.exportFiles)
	return out
}
