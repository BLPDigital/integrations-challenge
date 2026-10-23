package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// warnRecorder collects the warnings a store emits so recovery behavior can be
// asserted instead of being read off stderr. It is mutex-guarded because the
// concurrency test's goroutines can reach it.
type warnRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnRecorder) warn(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msg)
}

func (w *warnRecorder) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

func (w *warnRecorder) joined() string { return strings.Join(w.all(), "\n") }

// openTemp opens a store in a fresh temporary directory and returns it with its
// directory and warning recorder.
func openTemp(t *testing.T) (*Store, string, *warnRecorder) {
	t.Helper()
	dir := t.TempDir()
	return reopen(t, dir)
}

// reopen opens a store over an existing directory.
func reopen(t *testing.T, dir string) (*Store, string, *warnRecorder) {
	t.Helper()
	rec := &warnRecorder{}
	s, err := OpenWith(dir, Options{Warn: rec.warn})
	if err != nil {
		t.Fatalf("OpenWith(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir, rec
}

// supplier builds a supplier fixture.
func supplier(number, name string, changeSeq int64) model.Supplier {
	return model.Supplier{
		SupplierNumber:   number,
		Name:             name,
		Country:          "CH",
		Currency:         "CHF",
		IBAN:             "CH9300762011623852957",
		VATNumber:        "CHE-295.990.745",
		PaymentTermsDays: 30,
		ChangeSeq:        changeSeq,
	}
}

// prov builds a provenance fixture; every field is delivery detail and must never
// reach a digest.
func prov(batch, runID, file string, line int64) Provenance {
	return Provenance{
		BatchID:      batch,
		Channel:      ChannelFile,
		SourceFile:   file,
		SourceLine:   &line,
		SourceSHA256: model.Sha256Hex([]byte(file)),
		Profile:      "blp-canonical-v1",
		Format:       "csv",
		Encoding:     "UTF-8",
		SourceSystem: "erp-prod",
		RunID:        runID,
	}
}

// intp returns a pointer to i, for IfVersion.
func intp(i int) *int { return &i }

func TestApplySequence(t *testing.T) {
	ds := model.DatasetSupplier.String()
	steps := []struct {
		name        string
		payload     model.Supplier
		ifVersion   *int
		wantResult  Result
		wantVersion int
		wantSeq     int64
		wantHistory int
	}{
		{"create", supplier("0000417", "Atlas AG", 1), nil, ResultApplied, 1, 1, 1},
		{"redeliver identical", supplier("0000417", "Atlas AG", 1), nil, ResultUnchanged, 1, 2, 2},
		{"change", supplier("0000417", "Atlas Holding AG", 2), nil, ResultApplied, 2, 3, 3},
		{"if_version matches", supplier("0000417", "Atlas Group AG", 3), intp(2), ResultApplied, 3, 4, 4},
		{"if_version stale", supplier("0000417", "Nope AG", 4), intp(2), ResultConflict, 3, 0, 4},
		{"if_version must-not-exist", supplier("0000417", "Nope AG", 4), intp(0), ResultConflict, 3, 0, 4},
		{"redeliver identical again", supplier("0000417", "Atlas Group AG", 3), nil, ResultUnchanged, 3, 5, 5},
	}
	s, _, rec := openTemp(t)
	for _, step := range steps {
		out, err := s.Apply(Revision{
			Dataset:    ds,
			Key:        step.payload.Key(),
			Payload:    step.payload,
			Provenance: prov("b1", "run_1", "suppliers.csv", 1),
			IfVersion:  step.ifVersion,
		})
		if err != nil {
			t.Fatalf("%s: Apply: %v", step.name, err)
		}
		if out.Result != step.wantResult {
			t.Errorf("%s: result = %q, want %q", step.name, out.Result, step.wantResult)
		}
		if out.Version != step.wantVersion {
			t.Errorf("%s: version = %d, want %d", step.name, out.Version, step.wantVersion)
		}
		if out.Seq != step.wantSeq {
			t.Errorf("%s: seq = %d, want %d", step.name, out.Seq, step.wantSeq)
		}
		hist, err := s.History(ds, step.payload.Key())
		if err != nil {
			t.Fatalf("%s: History: %v", step.name, err)
		}
		if len(hist) != step.wantHistory {
			t.Errorf("%s: history length = %d, want %d", step.name, len(hist), step.wantHistory)
		}
	}
	if got := s.Count(ds); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
	if got := s.HighSeq(); got != 5 {
		t.Errorf("HighSeq = %d, want 5", got)
	}
	if msgs := rec.all(); len(msgs) > 0 {
		t.Errorf("unexpected warnings: %v", msgs)
	}
}

func TestApplyUnchangedIsProvenanceOnly(t *testing.T) {
	ds := model.DatasetSupplier.String()
	sup := supplier("0000417", "Atlas AG", 1)
	s, _, _ := openTemp(t)
	first := prov("b1", "run_1", "suppliers.csv", 42)
	if _, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: first}); err != nil {
		t.Fatal(err)
	}
	second := prov("b2", "run_2", "suppliers-delta.csv", 7)
	second.Channel = ChannelREST
	out, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: second})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultUnchanged {
		t.Fatalf("result = %q, want %q", out.Result, ResultUnchanged)
	}
	hist, err := s.History(ds, sup.Key())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history length = %d, want 2", len(hist))
	}
	if hist[0].ProvenanceOnly {
		t.Error("first revision must not be provenance-only")
	}
	if !hist[1].ProvenanceOnly {
		t.Error("re-delivery must be flagged provenance-only so History can tell it apart")
	}
	if hist[1].Version != hist[0].Version {
		t.Errorf("provenance-only version = %d, want %d", hist[1].Version, hist[0].Version)
	}
	if hist[1].ContentHash != hist[0].ContentHash {
		t.Error("provenance-only content hash must repeat the stored one")
	}
	if hist[1].Seq == hist[0].Seq {
		t.Error("provenance-only revision must have its own seq")
	}
	if hist[1].Provenance.BatchID != "b2" || hist[1].Provenance.Channel != ChannelREST {
		t.Errorf("re-delivery provenance not recorded: %+v", hist[1].Provenance)
	}
	// The current revision stays the one that actually set the content.
	cur, ok := s.Get(ds, sup.Key())
	if !ok {
		t.Fatal("Get: record missing")
	}
	if cur.Provenance.BatchID != "b1" {
		t.Errorf("current revision batch = %q, want b1", cur.Provenance.BatchID)
	}
}

func TestApplyConflictWritesNothing(t *testing.T) {
	ds := model.DatasetSupplier.String()
	sup := supplier("0000417", "Atlas AG", 1)
	s, _, _ := openTemp(t)
	if _, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b1", "r1", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changed := supplier("0000417", "Other AG", 2)
	out, err := s.Apply(Revision{Dataset: ds, Key: changed.Key(), Payload: changed, IfVersion: intp(9)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultConflict {
		t.Fatalf("result = %q, want conflict", out.Result)
	}
	if out.Version != 1 {
		t.Errorf("conflict version = %d, want the current 1", out.Version)
	}
	if out.ContentHash != model.ContentHash(sup) {
		t.Error("conflict must report the stored content hash, not the rejected one")
	}
	if out.Seq != 0 {
		t.Errorf("conflict seq = %d, want 0", out.Seq)
	}
	if got := s.HighSeq(); got != 1 {
		t.Errorf("HighSeq = %d, want 1: a conflict must not append", got)
	}
	after, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("conflict changed the digest")
	}
}

func TestApplyRejects(t *testing.T) {
	ds := model.DatasetSupplier.String()
	sup := supplier("0000417", "Atlas AG", 1)
	cases := []struct {
		name string
		rec  Revision
		want error
	}{
		{"empty dataset", Revision{Key: "k", Payload: sup}, ErrDataset},
		{"dataset with slash", Revision{Dataset: "../etc", Key: "k", Payload: sup}, ErrDataset},
		{"dataset leading digit", Revision{Dataset: "1supplier", Key: "k", Payload: sup}, ErrDataset},
		{"empty key", Revision{Dataset: ds, Key: "   ", Payload: sup}, ErrKeyEmpty},
		{"float payload", Revision{Dataset: ds, Key: "k", Payload: map[string]any{"amount": 1.5}}, ErrPayload},
		{"content hash mismatch", Revision{Dataset: ds, Key: "k", Payload: sup, ContentHash: strings.Repeat("0", 64)}, ErrContentHash},
	}
	s, _, _ := openTemp(t)
	for _, c := range cases {
		_, err := s.Apply(c.rec)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if got := s.HighSeq(); got != 0 {
		t.Errorf("HighSeq = %d, want 0: a rejected Apply must not append", got)
	}
}

func TestApplyAcceptsRoundTrippedRevision(t *testing.T) {
	ds := model.DatasetSupplier.String()
	sup := supplier("0000417", "Atlas AG", 1)
	s, _, _ := openTemp(t)
	if _, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b1", "r1", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.History(ds, sup.Key())
	if err != nil {
		t.Fatal(err)
	}
	// A revision read back carries its content hash; replaying it must be
	// accepted and recognized as unchanged.
	out, err := s.Apply(hist[0])
	if err != nil {
		t.Fatalf("replaying a stored revision: %v", err)
	}
	if out.Result != ResultUnchanged {
		t.Errorf("result = %q, want unchanged", out.Result)
	}
}

func TestDeleteAndRevive(t *testing.T) {
	ds := model.DatasetSupplier.String()
	sup := supplier("0000417", "Atlas AG", 1)
	s, _, _ := openTemp(t)
	if _, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b1", "r1", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Delete(ds, sup.Key(), prov("b2", "r1", "f", 2))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultApplied || out.Version != 2 || !out.Deleted {
		t.Fatalf("delete outcome = %+v", out)
	}
	if _, ok := s.Get(ds, sup.Key()); ok {
		t.Error("Get returned a tombstone")
	}
	if got := s.Count(ds); got != 0 {
		t.Errorf("Count = %d, want 0", got)
	}
	n := 0
	if err := s.Scan(ds, func(Revision) bool { n++; return true }); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("Scan yielded %d tombstones, want 0", n)
	}
	again, err := s.Delete(ds, sup.Key(), prov("b3", "r1", "f", 3))
	if err != nil {
		t.Fatal(err)
	}
	if again.Result != ResultUnchanged || again.Version != 2 {
		t.Errorf("second delete = %+v, want unchanged at version 2", again)
	}
	revived, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b4", "r1", "f", 4)})
	if err != nil {
		t.Fatal(err)
	}
	if revived.Result != ResultApplied || revived.Version != 3 || revived.Deleted {
		t.Errorf("revive = %+v, want applied at version 3", revived)
	}
	hist, err := s.History(ds, sup.Key())
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 {
		t.Errorf("history length = %d, want 4 (create, delete, redelete, revive)", len(hist))
	}
}

func TestScanKeyOrderAndEarlyStop(t *testing.T) {
	ds := model.DatasetSupplier.String()
	// Deliberately unsorted insertion, with keys whose byte order differs from
	// their numeric order: leading zeros are significant.
	numbers := []string{"0000417", "417", "0000418", "0000042", "1000000"}
	s, _, _ := openTemp(t)
	for i, n := range numbers {
		sup := supplier(n, "S"+n, int64(i))
		if _, err := s.Apply(Revision{Dataset: ds, Key: sup.Key(), Payload: sup, Provenance: prov("b1", "r1", "f", int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := s.Scan(ds, func(rev Revision) bool { got = append(got, rev.Key); return true }); err != nil {
		t.Fatal(err)
	}
	want := []string{"0000042", "0000417", "0000418", "1000000", "417"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scan order = %v, want %v", got, want)
	}
	var partial []string
	if err := s.Scan(ds, func(rev Revision) bool {
		partial = append(partial, rev.Key)
		return len(partial) < 2
	}); err != nil {
		t.Fatal(err)
	}
	if len(partial) != 2 {
		t.Errorf("early stop yielded %d records, want 2", len(partial))
	}
}

func TestScanUnknownDataset(t *testing.T) {
	s, _, _ := openTemp(t)
	n := 0
	if err := s.Scan(model.DatasetInvoice.String(), func(Revision) bool { n++; return true }); err != nil {
		t.Fatalf("Scan of a dataset with no segment: %v", err)
	}
	if n != 0 {
		t.Errorf("yielded %d records, want 0", n)
	}
	if got := s.Count("nosuch"); got != 0 {
		t.Errorf("Count = %d, want 0", got)
	}
	if _, ok := s.Get("nosuch", "k"); ok {
		t.Error("Get on an unknown dataset returned a record")
	}
}

func TestHeadAndPayloadRoundTrip(t *testing.T) {
	ds := model.DatasetPurchaseOrderLine.String()
	line := model.PurchaseOrderLine{
		PONumber:    "PO-004417",
		LineNo:      "00010",
		Material:    "M-1",
		Description: "Zürcher Kantonalbank Gebühr",
		Quantity:    model.MustDecimal("12.000"),
		UoM:         "EA",
		UnitPrice:   model.MustDecimal("104.5500"),
		Currency:    "CHF",
		GLAccount:   "4000",
		CostCenter:  "0815",
		ChangeSeq:   118422,
	}
	s, dir, _ := openTemp(t)
	if _, err := s.Apply(Revision{Dataset: ds, Key: line.Key(), Payload: line, Provenance: prov("b1", "r1", "po-lines.csv", 3)}); err != nil {
		t.Fatal(err)
	}
	version, hash, seq, deleted, revisions, ok := s.Head(ds, line.Key())
	if !ok || version != 1 || hash != model.ContentHash(line) || seq != 1 || deleted || revisions != 1 {
		t.Fatalf("Head = (%d,%q,%d,%v,%d,%v)", version, hash, seq, deleted, revisions, ok)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _, rec := reopen(t, dir)
	if msgs := rec.all(); len(msgs) > 0 {
		t.Fatalf("reopen warned: %v", msgs)
	}
	rev, ok := s2.Get(ds, line.Key())
	if !ok {
		t.Fatal("record missing after reopen")
	}
	var back model.PurchaseOrderLine
	if err := decodePayload(rev.Payload, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Quantity.EqualStrict(line.Quantity) {
		t.Errorf("quantity = %s (scale %d), want %s (scale %d)", back.Quantity, back.Quantity.Scale(), line.Quantity, line.Quantity.Scale())
	}
	if !back.UnitPrice.EqualStrict(line.UnitPrice) {
		t.Errorf("unit price = %s, want %s", back.UnitPrice, line.UnitPrice)
	}
	if back.CostCenter != "0815" {
		t.Errorf("cost center = %q, want 0815: leading zeros are significant", back.CostCenter)
	}
	if back.Description != line.Description {
		t.Errorf("description = %q, want %q", back.Description, line.Description)
	}
	if got := s2.HighSeq(); got != 1 {
		t.Errorf("HighSeq after reopen = %d, want 1", got)
	}
}

func TestClosedStore(t *testing.T) {
	s, _, _ := openTemp(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
	if _, err := s.Apply(Revision{Dataset: "supplier", Key: "k", Payload: supplier("k", "n", 1)}); !errors.Is(err, ErrClosed) {
		t.Errorf("Apply after Close: %v, want ErrClosed", err)
	}
	if _, err := s.Digest(); !errors.Is(err, ErrClosed) {
		t.Errorf("Digest after Close: %v, want ErrClosed", err)
	}
	if err := s.Scan("supplier", func(Revision) bool { return true }); !errors.Is(err, ErrClosed) {
		t.Errorf("Scan after Close: %v, want ErrClosed", err)
	}
	if _, err := s.PutRaw([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("PutRaw after Close: %v, want ErrClosed", err)
	}
}

func TestValidDataset(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"supplier", true},
		{"purchase_order_line", true},
		{"proposal", true},
		{"exception", true},
		{"a", true},
		{"a1", true},
		{"", false},
		{"1a", false},
		{"_a", false},
		{"Supplier", false},
		{"sup-plier", false},
		{"../escape", false},
		{"sub/dir", false},
		{strings.Repeat("a", 65), false},
	}
	for _, c := range cases {
		if got := ValidDataset(c.in); got != c.want {
			t.Errorf("ValidDataset(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestOpenIgnoresForeignSegmentName(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, "Weird-Name.jsonl"), []byte("garbage\n")); err != nil {
		t.Fatal(err)
	}
	s, _, rec := reopen(t, dir)
	if !strings.Contains(rec.joined(), "invalid dataset name") {
		t.Errorf("warnings = %q, want a note about the invalid dataset name", rec.joined())
	}
	if len(s.Datasets()) != 0 {
		t.Errorf("Datasets = %v, want none", s.Datasets())
	}
}
