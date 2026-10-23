package miniblp

import (
	"net/http"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// costCenterCSV is a canonical cost center CSV file.
const costCenterCSV = "code,name,company_code,valid_from,valid_to,blocked\n" +
	"CC-2200,Werk Wil,CH10,2026-01-01,,false\n" +
	"0815,Werk Wil Legacy,CH10,2026-01-01,,false\n" +
	"815,Vertrieb DACH,CH10,2026-01-01,,false\n"

// TestDigestIsChannelAndFormatBlind is the primary property of the whole twin:
// the same logical data delivered by the file channel as CSV and JSON, and by
// the REST channel as CSV and JSON chunks, has to produce a byte-identical state
// digest. Channel, wire format, batch layout and chunk sizes are all provenance,
// and provenance is deliberately absent from the digest.
func TestDigestIsChannelAndFormatBlind(t *testing.T) {
	invoices := mustJSON(t, []any{
		invoiceRecord("0000417", "0004711", "CHF", "1250.00", "89.35", "1160.65"),
		invoiceRecord("0000418", "0004712", "CHF", "500.00", "35.75", "464.25"),
	})

	// The file channel, in two files and one batch.
	byFile := newHarness(t, nil)
	m := fileManifest("batch-mixed",
		fileEntry("suppliers.csv", "supplier", "csv", 2),
		fileEntry("cost_centers.csv", "cost_center", "csv", 3),
		fileEntry("invoices.json", "invoice", "json", 2))
	byFile.writeBatchDir("batch-mixed", m, m2(m, map[string]string{
		"suppliers.csv":    supplierCSV,
		"cost_centers.csv": costCenterCSV,
		"invoices.json":    string(invoices),
	}))
	rep := byFile.scan()
	if rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("file batch = %+v", rep.Batches[0])
	}

	// The REST channel, same bytes for the CSV datasets, three chunks, a
	// different batch id, a different run and a different delivery order.
	byREST := newHarness(t, nil)
	status, ref := byREST.openBatch(restManifest("batch-rest"))
	if status != http.StatusCreated {
		t.Fatalf("open batch = %d (%s)", status, ref)
	}
	if status, body := byREST.rawChunk(ref, "invoice", 1, "c1", httpx.MediaJSON, invoices); status != http.StatusOK {
		t.Fatalf("invoice chunk = %d %s", status, body)
	}
	if status, body := byREST.rawChunk(ref, "cost_center", 1, "c2", httpx.MediaCSV,
		[]byte(costCenterCSV)); status != http.StatusOK {
		t.Fatalf("cost center chunk = %d %s", status, body)
	}
	if status, body := byREST.rawChunk(ref, "supplier", 1, "c3", httpx.MediaCSV,
		[]byte(supplierCSV)); status != http.StatusOK {
		t.Fatalf("supplier chunk = %d %s", status, body)
	}
	if status, receipt := byREST.commit(ref); status != http.StatusOK || !receipt.ClosureOK {
		t.Fatalf("commit = %d closure %v", status, receipt.ClosureOK)
	}

	if byFile.digest() != byREST.digest() {
		t.Fatalf("the state digest is not channel blind:\n file %s\n rest %s",
			byFile.digest(), byREST.digest())
	}
	// The record counts have to agree too, or the digests agreed for the wrong
	// reason.
	for _, ds := range []string{"supplier", "cost_center", "invoice"} {
		if a, b := byFile.srv.Store().Count(ds), byREST.srv.Store().Count(ds); a != b {
			t.Fatalf("%s: file has %d records, rest has %d", ds, a, b)
		}
	}
	if byFile.srv.Store().Count("cost_center") != 3 {
		t.Fatal("the two cost centers whose leading zeros differ collapsed into one")
	}
}

// TestDigestIgnoresChaos asserts the promise of --no-chaos: it changes fault
// injection and nothing else, so the digest of a run is identical either way.
func TestDigestIgnoresChaos(t *testing.T) {
	load := func(chaos bool) string {
		h := newHarness(t, func(c *Config) { c.Chaos = chaos })
		m := fileManifest("batch-chaos", fileEntry("suppliers.csv", "supplier", "csv", 2))
		h.writeBatchDir("batch-chaos", m, m2(m, map[string]string{"suppliers.csv": supplierCSV}))
		if rep := h.scan(); rep.Batches[0].Status != BatchAccepted {
			t.Fatalf("batch = %+v", rep.Batches[0])
		}
		return h.digest()
	}
	if a, b := load(false), load(true); a != b {
		t.Fatalf("chaos changed the digest:\n off %s\n on  %s", a, b)
	}
}

// TestDigestIgnoresDeliveryOrder asserts that two batches delivering the same
// records in the opposite order reach the same digest.
func TestDigestIgnoresDeliveryOrder(t *testing.T) {
	forward := newHarness(t, nil)
	_, ref := forward.openBatch(restManifest("b1"))
	forward.chunk(ref, "supplier", 1, "a", []any{
		supplierRecord("0000417", "A", false), supplierRecord("0000418", "B", false)})
	forward.commit(ref)

	backward := newHarness(t, nil)
	_, ref2 := backward.openBatch(restManifest("b2"))
	backward.chunk(ref2, "supplier", 1, "a", []any{supplierRecord("0000418", "B", false)})
	backward.chunk(ref2, "supplier", 2, "b", []any{supplierRecord("0000417", "A", false)})
	backward.commit(ref2)

	if forward.digest() != backward.digest() {
		t.Fatalf("delivery order changed the digest:\n %s\n %s", forward.digest(), backward.digest())
	}
}

// TestSkippedUnchangedOnRedelivery asserts the content-hash upsert: a
// re-delivered identical record is skipped_unchanged, keeps its version and
// still appends a provenance entry.
func TestSkippedUnchangedOnRedelivery(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-unchanged"))
	records := []any{supplierRecord("0000417", "A", false)}
	h.chunk(ref, "supplier", 1, "k1", records)
	status, body := h.chunk(ref, "supplier", 2, "k2", records)
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s", status, body)
	}
	var res struct {
		Counts  Counts         `json:"counts"`
		Records []RecordResult `json:"records"`
	}
	if err := decodePayloadBytes(body, &res); err != nil {
		t.Fatalf("body: %v", err)
	}
	if res.Counts.SkippedUnchanged != 1 || res.Counts.Accepted != 0 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if res.Records[0].Outcome != OutcomeSkippedUnchanged || res.Records[0].Version != 1 {
		t.Fatalf("record = %+v", res.Records[0])
	}
	history, err := h.srv.Store().History("supplier", "0000417")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 || !history[1].ProvenanceOnly {
		t.Fatalf("history = %d revisions, want the re-delivery as provenance only", len(history))
	}
}
