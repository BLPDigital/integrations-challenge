package miniblp

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// supplierCSV is a canonical supplier CSV file with a header row.
const supplierCSV = "supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\r\n" +
	"0000417,Steinbach Industrie AG,CH,CHF,CH9300762011623852957,CHE-123.456.789,30,false,118422\r\n" +
	"0000418,Meier Transport,CH,CHF,CH5604835012345678009,CHE-987.654.321,14,false,118423\r\n"

// TestFileBatchHappyPath publishes a batch the way the contract requires and
// asserts the receipt directory, the closure invariant and the record outcomes.
func TestFileBatchHappyPath(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-file-1", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-file-1", m, map[string]string{"suppliers.csv": supplierCSV})

	rep := h.scan()
	if len(rep.Batches) != 1 {
		t.Fatalf("scan report = %+v, want one batch", rep)
	}
	if rep.Scan != 1 {
		t.Fatalf("scan counter = %d, want 1", rep.Scan)
	}
	got := rep.Batches[0]
	if got.Status != BatchAccepted {
		t.Fatalf("status = %q codes %v", got.Status, got.Codes)
	}
	if got.Counts.Seen != 2 || got.Counts.Accepted != 2 || !got.Counts.closes() {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000418"); !ok {
		t.Fatal("the second supplier was not applied")
	}

	// The batch directory moved to processed, files preserved.
	if _, err := os.Stat(filepath.Join(h.inboxDir, dirProcessed, "batch-file-1", "suppliers.csv")); err != nil {
		t.Fatalf("processed batch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.inboxDir, dirIncoming, "batch-file-1")); !os.IsNotExist(err) {
		t.Fatalf("the batch is still in incoming: %v", err)
	}

	// The receipt directory: receipt.json, records.csv, rejects.csv and DONE.
	dir := filepath.Join(h.inboxDir, dirReceipts, "batch-file-1")
	for _, name := range []string{receiptJSON, recordsCSV, rejectsCSV, doneFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("receipt file %s: %v", name, err)
		}
	}
	done, err := os.ReadFile(filepath.Join(dir, doneFile))
	if err != nil || len(done) != 0 {
		t.Fatalf("DONE = %q err %v, want a zero-byte sentinel", done, err)
	}
	var receipt Receipt
	buf, err := os.ReadFile(filepath.Join(dir, receiptJSON))
	if err != nil {
		t.Fatalf("receipt.json: %v", err)
	}
	if err := json.Unmarshal(buf, &receipt); err != nil {
		t.Fatalf("receipt.json: %v", err)
	}
	if !receipt.ClosureOK || receipt.Channel != ChannelFile || receipt.ReceivedScan != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(receipt.Files) != 1 || receipt.Files[0].ParsedRecords != 2 {
		t.Fatalf("receipt files = %+v", receipt.Files)
	}
	// One line per record, plus the header: the record count is visible in the
	// file itself.
	rows, err := os.ReadFile(filepath.Join(dir, recordsCSV))
	if err != nil {
		t.Fatalf("records.csv: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(rows), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("records.csv has %d lines, want 3:\n%s", len(lines), rows)
	}
	if !strings.HasPrefix(lines[0], "file,line,record_ordinal,dataset,natural_key,outcome") {
		t.Fatalf("records.csv header = %q", lines[0])
	}
}

// TestStagingAndInFlightNamesAreIgnored asserts the atomic drop protocol: a
// staging directory and a .part name are invisible to the scanner.
func TestStagingAndInFlightNamesAreIgnored(t *testing.T) {
	h := newHarness(t, nil)
	incoming := filepath.Join(h.inboxDir, dirIncoming)
	for _, name := range []string{stagingPrefix + "batch-x", "batch-y.part", "batch-z.tmp", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(incoming, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	rep := h.scan()
	if len(rep.Batches) != 0 || len(rep.Pending) != 0 {
		t.Fatalf("scan report = %+v, want nothing seen", rep)
	}
}

// TestManifestMissingAfterTwoScans asserts that a batch with no manifest is
// left alone once and rejected on the second sighting, counted by the scan
// counter and never by a timer.
func TestManifestMissingAfterTwoScans(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(h.inboxDir, dirIncoming, "batch-nomanifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "suppliers.csv"), []byte(supplierCSV), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rep := h.scan()
	if len(rep.Pending) != 1 || rep.Pending[0] != "batch-nomanifest" {
		t.Fatalf("first scan = %+v, want the batch pending", rep)
	}
	rep = h.scan()
	if len(rep.Batches) != 1 {
		t.Fatalf("second scan = %+v, want one rejected batch", rep)
	}
	if rep.Batches[0].Status != BatchRejected ||
		!contains(rep.Batches[0].Codes, CodeManifestMissing) {
		t.Fatalf("batch = %+v, want %s", rep.Batches[0], CodeManifestMissing)
	}
	if _, err := os.Stat(filepath.Join(h.inboxDir, dirRejected, "batch-nomanifest")); err != nil {
		t.Fatalf("rejected batch: %v", err)
	}
}

// TestChecksumMismatchAppliesNothing asserts the truncated-transfer guard: the
// sha256 is verified before a single record is applied.
func TestChecksumMismatchAppliesNothing(t *testing.T) {
	h := newHarness(t, nil)
	entry := fileEntry("suppliers.csv", "supplier", "csv", 2)
	entry["sha256"] = strings.Repeat("0", 64)
	m := fileManifest("batch-checksum", entry)
	h.writeBatchDir("batch-checksum", m, map[string]string{"suppliers.csv": supplierCSV})

	rep := h.scan()
	if len(rep.Batches) != 1 || !contains(rep.Batches[0].Codes, CodeChecksumMismatch) {
		t.Fatalf("scan = %+v, want %s", rep.Batches, CodeChecksumMismatch)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000417"); ok {
		t.Fatal("a record was applied from a file whose checksum did not match")
	}
	// The file was never parsed, which is the point of the guard, so it
	// delivered no records and the tally is empty rather than invented.
	if !rep.Batches[0].Counts.closes() || rep.Batches[0].Counts.Seen != 0 {
		t.Fatalf("counts = %+v, want an empty closing tally", rep.Batches[0].Counts)
	}
	buf, err := os.ReadFile(filepath.Join(h.inboxDir, dirReceipts, "batch-checksum", receiptJSON))
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(buf, &receipt); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if len(receipt.Files) != 1 || receipt.Files[0].DeclaredRecords != 2 ||
		receipt.Files[0].ParsedRecords != 0 || receipt.Files[0].Status != BatchRejected {
		t.Fatalf("receipt files = %+v", receipt.Files)
	}
}

// TestRecordCountMismatchAppliesNothing asserts the second mandatory guard.
func TestRecordCountMismatchAppliesNothing(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-count", fileEntry("suppliers.csv", "supplier", "csv", 3))
	h.writeBatchDir("batch-count", m, map[string]string{"suppliers.csv": supplierCSV})

	rep := h.scan()
	if len(rep.Batches) != 1 || !contains(rep.Batches[0].Codes, CodeRecordCountMismatch) {
		t.Fatalf("scan = %+v, want %s", rep.Batches, CodeRecordCountMismatch)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000417"); ok {
		t.Fatal("a record was applied from a file whose record count did not match")
	}
}

// TestBatchReplayIsANoOp asserts that an identical re-delivery applies nothing,
// emits nothing and reports the same counts as a replay.
func TestBatchReplayIsANoOp(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-replay", fileEntry("suppliers.csv", "supplier", "csv", 2))
	files := map[string]string{"suppliers.csv": supplierCSV}
	h.writeBatchDir("batch-replay", m, m2(m, files))
	first := h.scan()
	if first.Batches[0].Status != BatchAccepted {
		t.Fatalf("first delivery = %+v", first.Batches[0])
	}
	digestAfterFirst := h.digest()
	highSeq := h.srv.Store().HighSeq()

	h.writeBatchDir("batch-replay", m, m2(m, files))
	second := h.scan()
	if len(second.Batches) != 1 {
		t.Fatalf("second scan = %+v", second)
	}
	if second.Batches[0].Status != BatchReplayed || !second.Batches[0].Replay {
		t.Fatalf("second delivery = %+v, want a replay", second.Batches[0])
	}
	if second.Batches[0].Counts.Seen != 0 || second.Batches[0].Counts.Replayed != 2 {
		t.Fatalf("replay counts = %+v", second.Batches[0].Counts)
	}
	if h.digest() != digestAfterFirst {
		t.Fatal("a replay changed the state digest")
	}
	if h.srv.Store().HighSeq() != highSeq {
		t.Fatal("a replay wrote a revision")
	}
}

// TestBatchIDConflictOnFileChannel asserts that the same batch id with different
// content is refused, never silently applied.
func TestBatchIDConflictOnFileChannel(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-conflict", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-conflict", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
	if rep := h.scan(); rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("first delivery = %+v", rep.Batches[0])
	}
	digest := h.digest()

	changed := strings.Replace(supplierCSV, "Meier Transport", "Meier Logistik", 1)
	m2nd := fileManifest("batch-conflict", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-conflict", m2nd, m2(m2nd, map[string]string{"suppliers.csv": changed}))
	rep := h.scan()
	if len(rep.Batches) != 1 || rep.Batches[0].Status != BatchRejected ||
		!contains(rep.Batches[0].Codes, CodeBatchIDConflict) {
		t.Fatalf("second delivery = %+v, want %s", rep.Batches, CodeBatchIDConflict)
	}
	if h.digest() != digest {
		t.Fatal("a conflicting batch changed the state")
	}
}

// TestUnknownProfileRejectsTheBatch asserts that a profile no importer is
// registered for is a whole-batch reject and applies nothing.
func TestUnknownProfileRejectsTheBatch(t *testing.T) {
	h := newHarness(t, nil)
	entry := fileEntry("suppliers.csv", "supplier", "csv", 2)
	entry["profile"] = "sap-idoc-4.7"
	m := fileManifest("batch-profile", entry)
	h.writeBatchDir("batch-profile", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))

	rep := h.scan()
	if len(rep.Batches) != 1 || !contains(rep.Batches[0].Codes, CodeUnknownProfile) {
		t.Fatalf("scan = %+v, want %s", rep.Batches, CodeUnknownProfile)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000417"); ok {
		t.Fatal("records were applied from a batch with an unknown profile")
	}
}

// TestKredExpProfileWithoutImporterIsAnUnknownProfile asserts the legacy profile
// dispatch: without the legacy importer linked in, the batch is rejected with
// UNKNOWN_PROFILE rather than silently accepted as something else.
func TestKredExpProfileWithoutImporterIsAnUnknownProfile(t *testing.T) {
	if _, ok := lookupKredExp(); ok {
		t.Skip("the legacy importer is linked into this build")
	}
	h := newHarness(t, nil)
	entry := fileEntry("KRED_0100_20260131_001.txt", "invoice", "txt", 1)
	entry["profile"] = ProfileKredExp
	m := fileManifest("batch-legacy", entry)
	h.writeBatchDir("batch-legacy", m, m2(m, map[string]string{
		"KRED_0100_20260131_001.txt": "VORLAUF;2.1;0100;3101260900;CHF;SUBSG01;BLP;P\r\n",
	}))
	rep := h.scan()
	if len(rep.Batches) != 1 || !contains(rep.Batches[0].Codes, CodeUnknownProfile) {
		t.Fatalf("scan = %+v, want %s", rep.Batches, CodeUnknownProfile)
	}
}

// TestForeignWriteDetected asserts that a connector writing into a twin-owned
// directory fails the run.
func TestForeignWriteDetected(t *testing.T) {
	h := newHarness(t, nil)
	if err := os.MkdirAll(filepath.Join(h.inboxDir, dirProcessed, "batch-forged"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rep := h.scan()
	if len(rep.ForeignWrites) != 1 || rep.ForeignWrites[0] != dirProcessed+"/batch-forged" {
		t.Fatalf("foreign writes = %v", rep.ForeignWrites)
	}
	status, body := h.adminGet("/admin/v1/runs/last")
	if status != 200 {
		t.Fatalf("runs/last status %d", status)
	}
	var run runReport
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != "failed" || !contains(run.Codes, CodeForeignWriteDetected) {
		t.Fatalf("run = %+v, want a failed run carrying %s", run, CodeForeignWriteDetected)
	}
}

// TestOnErrorAbortBatchAppliesNothing asserts that abort_batch is exact: a batch
// with one bad record applies none of its good ones.
func TestOnErrorAbortBatchAppliesNothing(t *testing.T) {
	h := newHarness(t, nil)
	bad := supplierCSV + "0000419,Bad Country,Switzerland,CHF,,,,false,1\r\n"
	m := fileManifest("batch-abort", fileEntry("suppliers.csv", "supplier", "csv", 3))
	m["on_error"] = OnErrorAbortBatch
	h.writeBatchDir("batch-abort", m, m2(m, map[string]string{"suppliers.csv": bad}))

	rep := h.scan()
	if len(rep.Batches) != 1 || rep.Batches[0].Status != BatchRejected {
		t.Fatalf("scan = %+v, want a rejected batch", rep.Batches)
	}
	counts := rep.Batches[0].Counts
	if counts.Seen != 3 || counts.Rejected != 3 || !counts.closes() {
		t.Fatalf("counts = %+v, want three seen and three rejected", counts)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000417"); ok {
		t.Fatal("an aborted batch applied a record")
	}
}

// TestOnErrorContinueAppliesTheGoodRecords is the counterpart: with the default
// policy one bad line does not cost the other two.
func TestOnErrorContinueAppliesTheGoodRecords(t *testing.T) {
	h := newHarness(t, nil)
	bad := supplierCSV + "0000419,Bad Country,Switzerland,CHF,,,,false,1\r\n"
	m := fileManifest("batch-continue", fileEntry("suppliers.csv", "supplier", "csv", 3))
	h.writeBatchDir("batch-continue", m, m2(m, map[string]string{"suppliers.csv": bad}))

	rep := h.scan()
	counts := rep.Batches[0].Counts
	if counts.Seen != 3 || counts.Accepted != 2 || counts.Rejected != 1 || !counts.closes() {
		t.Fatalf("counts = %+v", counts)
	}
	if rep.Batches[0].Status != BatchPartiallyAccepted {
		t.Fatalf("status = %q", rep.Batches[0].Status)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000417"); !ok {
		t.Fatal("a good record was dropped because another line was bad")
	}
	// rejects.csv carries the rejected record and only that one.
	rows, err := os.ReadFile(filepath.Join(h.inboxDir, dirReceipts, "batch-continue", rejectsCSV))
	if err != nil {
		t.Fatalf("rejects.csv: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(rows), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("rejects.csv has %d lines, want the header and one row:\n%s", len(lines), rows)
	}
}

// TestUndeclaredFileIsAWarning asserts that a file the manifest does not name is
// reported and does not stop the batch.
func TestUndeclaredFileIsAWarning(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-extra", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-extra", m, m2(m, map[string]string{
		"suppliers.csv": supplierCSV,
		"leftover.csv":  "nobody,declared,me\n",
	}))
	rep := h.scan()
	if rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("status = %q codes %v", rep.Batches[0].Status, rep.Batches[0].Codes)
	}
	buf, err := os.ReadFile(filepath.Join(h.inboxDir, dirReceipts, "batch-extra", receiptJSON))
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if !strings.Contains(string(buf), CodeUndeclaredFile) {
		t.Fatalf("receipt does not mention the undeclared file:\n%s", buf)
	}
}

// TestProvenanceRecordsTheDelivery asserts that every revision carries the
// locator of the delivery it came from and the content address of the bytes.
func TestProvenanceRecordsTheDelivery(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-prov", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-prov", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
	h.scan()

	history, err := h.srv.Store().History("supplier", "0000418")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %d revisions, want 1", len(history))
	}
	prov := history[0].Provenance
	if prov.Channel != ChannelFile || prov.SourceFile != "suppliers.csv" {
		t.Fatalf("provenance = %+v", prov)
	}
	if prov.SourceLine == nil || *prov.SourceLine != 3 {
		t.Fatalf("source line = %v, want line 3 (header plus two records)", prov.SourceLine)
	}
	if prov.RecordOrdinal == nil || *prov.RecordOrdinal != 1 {
		t.Fatalf("record ordinal = %v, want 1", prov.RecordOrdinal)
	}
	if prov.ReceivedScan == nil || *prov.ReceivedScan != 1 {
		t.Fatalf("received scan = %v, want 1", prov.ReceivedScan)
	}
	if prov.SourceSHA256 != model.Sha256Hex([]byte(supplierCSV)) {
		t.Fatalf("source sha256 = %q", prov.SourceSHA256)
	}
	if raw, err := h.srv.Store().GetRaw(prov.SourceSHA256); err != nil || string(raw) != supplierCSV {
		t.Fatalf("raw source bytes: err %v", err)
	}
}

// m2 fills in the sha256 of every declared file entry from the file's own bytes
// and returns the files unchanged, so a test writes the two in one place.
func m2(m map[string]any, files map[string]string) map[string]string {
	entries, _ := m["files"].([]map[string]any)
	for _, e := range entries {
		name, _ := e["path"].(string)
		if content, ok := files[name]; ok {
			if _, set := e["sha256"]; !set {
				e["sha256"] = model.Sha256Hex([]byte(content))
			}
		}
	}
	return files
}

// contains reports whether a list holds a value.
func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// lookupKredExp reports whether the legacy importer is linked into this build.
func lookupKredExp() (any, bool) {
	f, ok := resolveKredExp()
	return f, ok
}

// TestRestartDoesNotReportItsOwnOutputAsForeign asserts that a twin which opens
// on a directory tree it wrote earlier adopts that tree instead of accusing the
// connector of forging it. The detector exists for writes that appear while the
// twin is running.
func TestRestartDoesNotReportItsOwnOutputAsForeign(t *testing.T) {
	h := newHarness(t, nil)
	m := fileManifest("batch-restart", fileEntry("suppliers.csv", "supplier", "csv", 2))
	h.writeBatchDir("batch-restart", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
	if rep := h.scan(); rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("first run = %+v", rep.Batches[0])
	}
	if err := h.srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	restarted, err := New(Config{
		DataDir: h.dataDir, InboxDir: h.inboxDir, OutboxDir: h.outboxDir,
		Scenario: "S0", Seed: 20260416, AdminToken: adminTokenForTests, RunID: "run_test",
		Log: io.Discard,
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer restarted.Close()

	rep, err := restarted.Scan()
	if err != nil {
		t.Fatalf("scan after restart: %v", err)
	}
	if len(rep.ForeignWrites) != 0 {
		t.Fatalf("foreign writes after a restart = %v", rep.ForeignWrites)
	}
	// The store came back with its records, and the digest is stable across the
	// restart.
	if restarted.Store().Count("supplier") != 2 {
		t.Fatalf("supplier count after restart = %d", restarted.Store().Count("supplier"))
	}
	// A write that appears after the twin opened is still caught.
	if err := os.MkdirAll(filepath.Join(h.inboxDir, dirProcessed, "batch-forged"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rep, err = restarted.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(rep.ForeignWrites) != 1 {
		t.Fatalf("foreign writes = %v, want the forged directory", rep.ForeignWrites)
	}
}
