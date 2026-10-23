package miniblp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpShapes prints the receipt, a records.csv and an outbox page so a
// reviewer can read the shapes the twin produces. It asserts nothing; run it
// with -v.
func TestDumpShapes(t *testing.T) {
	if os.Getenv("MINIBLP_DUMP") != "1" {
		t.Skip("set MINIBLP_DUMP=1 to print the wire shapes")
	}
	h := newHarness(t, nil)
	files := map[string]string{"invoices.json": invoiceWithEmbeddedLines}
	for k, v := range masterDataFiles {
		files[k] = v
	}
	m := fileManifest("batch-dump",
		fileEntry("suppliers.csv", "supplier", "csv", 1),
		fileEntry("cost_centers.csv", "cost_center", "csv", 1),
		fileEntry("fx_rates.csv", "fx_rate", "csv", 1),
		fileEntry("invoices.json", "invoice", "json", 1))
	h.writeBatchDir("batch-dump", m, m2(m, files))
	h.scan()

	for _, name := range []string{receiptJSON, recordsCSV} {
		buf, err := os.ReadFile(filepath.Join(h.inboxDir, dirReceipts, "batch-dump", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("=== %s ===\n%s", name, buf)
	}
	_, body := h.get("/v1/outbox/proposals?status=pending")
	t.Logf("=== outbox page ===\n%s", body)
	buf, err := os.ReadFile(filepath.Join(h.outboxDir, "run_test", "proposals-0001.csv"))
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	t.Logf("=== proposals-0001.csv ===\n%s", buf)
}
