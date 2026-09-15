package store

import (
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// invoiceFixture builds an invoice with one line.
func invoiceFixture(number, gross string) model.APInvoice {
	return model.APInvoice{
		SupplierNumber:        "0000417",
		SupplierInvoiceNumber: number,
		CompanyCode:           model.CompanyCodeCH10,
		DocumentType:          model.DocumentTypeInvoice,
		DocumentDate:          "2026-03-29",
		ReceiptDate:           "2026-03-30",
		PONumber:              "PO-004417",
		Currency:              "EUR",
		GrossAmount:           model.MustDecimal(gross),
		VATAmount:             model.MustDecimal("89.35"),
		VATCode:               "V1",
		PaymentTermsDays:      30,
		DiscountRaw:           "2.000",
		DiscountDays:          14,
		CostCenter:            "CC-2200",
		Text:                  "Wartung Zürich",
		Lines: []model.APInvoiceLine{{
			LineNo:     "00010",
			GLAccount:  "4000",
			CostCenter: "",
			Quantity:   model.MustDecimal("1.000"),
			UoM:        "EA",
			UnitPrice:  model.MustDecimal("1160.65"),
			LineAmount: model.MustDecimal("1160.65"),
			TaxCode:    "V1",
		}},
	}
}

// TestDigestIgnoresDeliveryDetail is the primary property of the store: two
// stores holding the same logical content must be byte-identical in digest even
// when nothing about how they were built matches.
func TestDigestIgnoresDeliveryDetail(t *testing.T) {
	supDS := model.DatasetSupplier.String()
	invDS := model.DatasetInvoice.String()
	supA := supplier("0000417", "Atlas Group AG", 3)
	supB := supplier("0000418", "Brunner GmbH", 4)
	inv := invoiceFixture("0004711", "1250.00")

	// Store A: file channel, one batch, insertion order supplier-supplier-invoice,
	// every record delivered exactly once, so every version is 1.
	a, _, _ := openTemp(t)
	for i, sup := range []model.Supplier{supA, supB} {
		if _, err := a.Apply(Revision{Dataset: supDS, Key: sup.Key(), Payload: sup, Provenance: prov("batch-a", "run_a", "suppliers.csv", int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Apply(Revision{Dataset: invDS, Key: inv.Key(), Payload: inv, Provenance: prov("batch-a", "run_a", "invoices.csv", 1)}); err != nil {
		t.Fatal(err)
	}

	// Store B: REST channel, three batches, reverse insertion order, and the
	// suppliers arrive as earlier versions first so their version numbers,
	// sequence numbers and provenance all differ from A's.
	b, _, _ := openTemp(t)
	restProv := func(batch string, ordinal int64) Provenance {
		return Provenance{
			BatchID: batch, Channel: ChannelREST, Format: "ndjson",
			Profile: "blp-canonical-v1", Encoding: "UTF-8", SourceSystem: "erp-prod",
			RunID: "run_b", ChunkOrdinal: &ordinal, RecordOrdinal: &ordinal,
		}
	}
	if _, err := b.Apply(Revision{Dataset: invDS, Key: inv.Key(), Payload: inv, Provenance: restProv("batch-b1", 0)}); err != nil {
		t.Fatal(err)
	}
	stale := supplier("0000418", "Brunner AG", 1) // superseded
	if _, err := b.Apply(Revision{Dataset: supDS, Key: stale.Key(), Payload: stale, Provenance: restProv("batch-b2", 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(Revision{Dataset: supDS, Key: supB.Key(), Payload: supB, Provenance: restProv("batch-b2", 2)}); err != nil {
		t.Fatal(err)
	}
	staleA := supplier("0000417", "Atlas AG", 1)
	if _, err := b.Apply(Revision{Dataset: supDS, Key: staleA.Key(), Payload: staleA, Provenance: restProv("batch-b3", 3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(Revision{Dataset: supDS, Key: supA.Key(), Payload: supA, Provenance: restProv("batch-b3", 4)}); err != nil {
		t.Fatal(err)
	}
	// A re-delivery, which appends provenance and must not move the digest.
	if _, err := b.Apply(Revision{Dataset: supDS, Key: supA.Key(), Payload: supA, Provenance: restProv("batch-b3", 5)}); err != nil {
		t.Fatal(err)
	}
	// A record that exists only as a tombstone must be absent from the digest.
	gone := supplier("0000999", "Gone AG", 9)
	if _, err := b.Apply(Revision{Dataset: supDS, Key: gone.Key(), Payload: gone, Provenance: restProv("batch-b3", 6)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Delete(supDS, gone.Key(), restProv("batch-b3", 7)); err != nil {
		t.Fatal(err)
	}

	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("digests differ for identical logical content:\n A = %s\n B = %s", da, db)
	}
	if a.HighSeq() == b.HighSeq() {
		t.Fatal("the two stores were supposed to differ in sequence numbers")
	}
	if _, hashA, _, _, revA, _ := a.Head(supDS, supA.Key()); revA != 1 {
		t.Fatalf("store A supplier revisions = %d, want 1 (hash %s)", revA, hashA)
	}
	if verB, _, _, _, revB, _ := b.Head(supDS, supA.Key()); verB != 2 || revB != 3 {
		t.Fatalf("store B supplier = version %d over %d revisions, want 2 over 3", verB, revB)
	}
}

func TestDigestChangesWithOnePayloadField(t *testing.T) {
	invDS := model.DatasetInvoice.String()
	base := invoiceFixture("0004711", "1250.00")
	cases := []struct {
		name  string
		alter func(model.APInvoice) model.APInvoice
	}{
		{"gross amount", func(i model.APInvoice) model.APInvoice {
			i.GrossAmount = model.MustDecimal("1250.01")
			return i
		}},
		{"gross amount scale", func(i model.APInvoice) model.APInvoice {
			i.GrossAmount = model.MustDecimal("1250.000")
			return i
		}},
		{"discount raw", func(i model.APInvoice) model.APInvoice {
			i.DiscountRaw = "2.00"
			return i
		}},
		{"line cost center filled in", func(i model.APInvoice) model.APInvoice {
			lines := append([]model.APInvoiceLine(nil), i.Lines...)
			lines[0].CostCenter = "CC-2200"
			i.Lines = lines
			return i
		}},
		{"key leading zero", func(i model.APInvoice) model.APInvoice {
			i.SupplierInvoiceNumber = "4711"
			return i
		}},
	}
	ref, _, _ := openTemp(t)
	if _, err := ref.Apply(Revision{Dataset: invDS, Key: base.Key(), Payload: base, Provenance: prov("b", "r", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	want, err := ref.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			alt := c.alter(base)
			s, _, _ := openTemp(t)
			if _, err := s.Apply(Revision{Dataset: invDS, Key: alt.Key(), Payload: alt, Provenance: prov("b", "r", "f", 1)}); err != nil {
				t.Fatal(err)
			}
			got, err := s.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Errorf("digest unchanged after altering %s", c.name)
			}
		})
	}
}

func TestDigestExcludesAuxiliaryDatasets(t *testing.T) {
	supDS := model.DatasetSupplier.String()
	sup := supplier("0000417", "Atlas AG", 1)
	s, _, _ := openTemp(t)
	if _, err := s.Apply(Revision{Dataset: supDS, Key: sup.Key(), Payload: sup, Provenance: prov("b", "r", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutProposal(proposalFixture("prp_0000001"), prov("b", "r", "f", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RaiseException(Exception{
		SubjectKey: model.InvoiceKey("0000417", "0004711"), SubjectType: "invoice",
		Stage: StageMatch, Code: "EXC_FX_RATE_MISSING", Message: "no DAILY rate covers the posting date",
	}, prov("b", "r", "f", 1)); err != nil {
		t.Fatal(err)
	}
	after, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Error("proposals and exceptions must not reach the state digest: they carry run ids and attempt counters")
	}
}

// TestDigestExcludesTheOutboundAckJournal is the regression test for a defect
// found while pinning the hidden scenarios: three runs of the same scenario, on
// the same seed, with identical record counts in every dataset and an identical
// high sequence number, produced three different digests. The outbound
// acknowledgment journal was reaching the hash, and its payload carries the run
// id, so the digest changed whenever the run id did - which is every run, and
// every candidate, since the connector picks its own run id.
//
// A pinned digest that no second run can reproduce is worse than no pin at all:
// it fails a correct submission. So the property is asserted here directly.
func TestDigestExcludesTheOutboundAckJournal(t *testing.T) {
	supDS := model.DatasetSupplier.String()
	ackDS := model.DatasetOutboxAck.String()
	s, _, _ := openTemp(t)
	sup := supplier("0000417", "Atlas AG", 1)
	if _, err := s.Apply(Revision{Dataset: supDS, Key: sup.Key(), Payload: sup,
		Provenance: prov("b", "r", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Two acks that differ only in what a second run of the same work changes:
	// the run id that posted it, the attempt count it took, and the document
	// number the ERP assigned in arrival order.
	for i, ack := range []map[string]any{
		{"proposal_id": "prp_0000001", "status": "posted", "run_id": "run_a_1111",
			"attempts": 1, "external_document_number": "AP-2026-0000001"},
		{"proposal_id": "prp_0000001", "status": "posted", "run_id": "run_b_2222",
			"attempts": 3, "external_document_number": "AP-2026-0000042"},
	} {
		if _, err := s.Apply(Revision{Dataset: ackDS, Key: "prp_0000001", Payload: ack,
			Provenance: prov("b", "r", "f", int64(i+1))}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if got != before {
			t.Fatalf("ack %d reached the digest: %s became %s; the journal carries the run id, so a pinned digest would fail every later run",
				i, before, got)
		}
	}
}

func TestDigestStableAcrossRestart(t *testing.T) {
	supDS := model.DatasetSupplier.String()
	s, dir, _ := openTemp(t)
	for i, n := range []string{"0000417", "0000418", "0000419"} {
		sup := supplier(n, "S"+n, int64(i))
		if _, err := s.Apply(Revision{Dataset: supDS, Key: sup.Key(), Payload: sup, Provenance: prov("b", "r", "f", int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	want, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _, _ := reopen(t, dir)
	got, err := s2.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("digest after restart = %s, want %s", got, want)
	}
	// Repeated calls are pure.
	again, err := s2.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Error("Digest is not idempotent")
	}
}

func TestDigestEmptyDatasetContributesNothing(t *testing.T) {
	supDS := model.DatasetSupplier.String()
	invDS := model.DatasetInvoice.String()
	sup := supplier("0000417", "Atlas AG", 1)
	inv := invoiceFixture("0004711", "1250.00")

	a, _, _ := openTemp(t)
	if _, err := a.Apply(Revision{Dataset: supDS, Key: sup.Key(), Payload: sup, Provenance: prov("b", "r", "f", 1)}); err != nil {
		t.Fatal(err)
	}

	// The same content, but the invoice dataset was touched and then emptied, so
	// its segment file exists and holds only a tombstone.
	b, _, _ := openTemp(t)
	if _, err := b.Apply(Revision{Dataset: supDS, Key: sup.Key(), Payload: sup, Provenance: prov("b", "r", "f", 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Apply(Revision{Dataset: invDS, Key: inv.Key(), Payload: inv, Provenance: prov("b", "r", "f", 2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Delete(invDS, inv.Key(), prov("b", "r", "f", 3)); err != nil {
		t.Fatal(err)
	}
	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Errorf("an emptied dataset changed the digest:\n A = %s\n B = %s", da, db)
	}
}
