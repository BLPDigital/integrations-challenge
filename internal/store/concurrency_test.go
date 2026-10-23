package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// TestConcurrentApplyReachesTheSerialDigest checks the concurrency guarantee the
// HTTP server depends on: writers serialize, readers never see a half-written
// revision, and the resulting logical content is exactly the serial one. Run with
// -race.
func TestConcurrentApplyReachesTheSerialDigest(t *testing.T) {
	const writers = 8
	const perWriter = 25
	ds := model.DatasetSupplier.String()

	// Each key is owned by exactly one goroutine, so the final content is
	// deterministic even though the interleaving is not.
	key := func(w, i int) string { return fmt.Sprintf("%03d%04d", w, i) }

	serial, _, _ := openTemp(t)
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			sup := supplier(key(w, i), fmt.Sprintf("S-%d-%d", w, i), int64(i))
			if _, err := serial.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b", "r", "f", int64(i))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	want, err := serial.Digest()
	if err != nil {
		t.Fatal(err)
	}

	par, dir, _ := openTemp(t)
	var wg sync.WaitGroup
	errs := make(chan error, writers*2)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				sup := supplier(key(w, i), fmt.Sprintf("S-%d-%d", w, i), int64(i))
				out, err := par.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b", "r", "f", int64(i))})
				if err != nil {
					errs <- err
					return
				}
				if out.Result != ResultApplied || out.Version != 1 {
					errs <- fmt.Errorf("writer %d record %d: outcome %+v", w, i, out)
					return
				}
				if _, ok := par.Get(ds, sup.Key()); !ok {
					errs <- fmt.Errorf("writer %d record %d: not readable after Apply", w, i)
					return
				}
			}
		}(w)
	}
	// Readers run against the same store throughout.
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := par.Digest(); err != nil {
					errs <- err
					return
				}
				if err := par.Scan(ds, func(Revision) bool { return true }); err != nil {
					errs <- err
					return
				}
				if _, err := par.PutRaw([]byte(fmt.Sprintf("blob-%d", i))); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	got, err := par.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("concurrent digest = %s, want the serial %s", got, want)
	}
	if seq := par.HighSeq(); seq != writers*perWriter {
		t.Errorf("HighSeq = %d, want %d", seq, writers*perWriter)
	}
	if err := par.Close(); err != nil {
		t.Fatal(err)
	}
	// Every append was fsynced at a record boundary, so a reopen is clean.
	s2, _, rec := reopen(t, dir)
	if msgs := rec.all(); len(msgs) > 0 {
		t.Errorf("reopen after concurrent writes warned: %v", msgs)
	}
	after, err := s2.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if after != want {
		t.Errorf("digest after reopen = %s, want %s", after, want)
	}
}
