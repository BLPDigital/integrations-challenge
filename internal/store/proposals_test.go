package store

import (
	"errors"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// proposalFixture builds a pending proposal for the EUR invoice of the digest
// fixtures.
func proposalFixture(id string) Proposal {
	return Proposal{
		ProposalID:        id,
		InvoiceKey:        model.InvoiceKey("0000417", "0004711"),
		InvoiceVersion:    2,
		CreatedInRun:      "run_7f3c1a",
		CreatedSeq:        17,
		MatchedPO:         "PO-004417",
		MatchedCostCenter: "CC-2200",
		FxRateUsed:        model.MustDecimal("1.082250"),
		FxRateFactor:      1,
		Amount:            model.Money{AmountMinor: 10823, Currency: "CHF", Scale: 2},
		SourceAmount:      model.Money{AmountMinor: 10000, Currency: "GBP", Scale: 2},
		Warnings:          []string{"ambiguous_field_semantics", "ambiguous_field_semantics", "W_UOM_CONVERTED"},
	}
}

func ackFixture(status, doc string) ProposalAck {
	return ProposalAck{
		Status:                 status,
		ExternalDocumentNumber: doc,
		ExternalRevision:       1,
		ExternalFiscalYear:     2026,
		ExternalPostingDate:    "2026-03-29",
		IdempotencyKey:         "blp:acme-ch:prp_0000001:9f21c8aa3b0e4d17",
		RunID:                  "run_7f3c1a",
		PostedAt:               "2026-03-29",
		Attempts:               1,
		HTTPStatus:             200,
	}
}

func TestPutProposalNormalizesAndHashes(t *testing.T) {
	s, dir, _ := openTemp(t)
	p := proposalFixture("prp_0000001")
	out, err := s.PutProposal(p, prov("b", "run_7f3c1a", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultApplied || out.Version != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	got, ok, err := s.GetProposal("prp_0000001")
	if err != nil || !ok {
		t.Fatalf("GetProposal: %v, ok=%v", err, ok)
	}
	if got.Status != ProposalPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	wantWarnings := []string{"W_UOM_CONVERTED", "ambiguous_field_semantics"}
	if len(got.Warnings) != 2 || got.Warnings[0] != wantWarnings[0] || got.Warnings[1] != wantWarnings[1] {
		t.Errorf("warnings = %v, want %v sorted and de-duplicated", got.Warnings, wantWarnings)
	}
	if got.ProposalContentHash != model.ContentHash(got.Content()) {
		t.Error("stored content hash does not match the content it names")
	}
	if !got.FxRateUsed.EqualStrict(model.MustDecimal("1.082250")) {
		t.Errorf("fx rate = %s (scale %d), want 1.082250 at scale 6", got.FxRateUsed, got.FxRateUsed.Scale())
	}
	if got.Amount != (model.Money{AmountMinor: 10823, Currency: "CHF", Scale: 2}) {
		t.Errorf("amount = %+v", got.Amount)
	}

	// The content hash must be stable across runs and across lifecycle changes:
	// it is folded into the idempotency key.
	other := proposalFixture("prp_0000001")
	other.CreatedInRun = "run_other"
	other.CreatedSeq = 999
	other.Attempts = 3
	if model.ContentHash(other.Content()) != got.ProposalContentHash {
		t.Error("content hash moved with run metadata; the idempotency key would not be stable")
	}
	// Re-writing identical content only appends provenance.
	again, err := s.PutProposal(p, prov("b2", "run_next", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if again.Result != ResultUnchanged || again.Version != 1 {
		t.Errorf("re-put = %+v, want unchanged at version 1", again)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _, _ := reopen(t, dir)
	back, ok, err := s2.GetProposal("prp_0000001")
	if err != nil || !ok {
		t.Fatalf("GetProposal after reopen: %v, ok=%v", err, ok)
	}
	if back.ProposalContentHash != got.ProposalContentHash {
		t.Error("content hash changed across a restart")
	}
	if !back.FxRateUsed.EqualStrict(got.FxRateUsed) {
		t.Error("fx rate scale lost across a restart")
	}
}

func TestPutProposalRejectsEmptyID(t *testing.T) {
	s, _, _ := openTemp(t)
	if _, err := s.PutProposal(Proposal{}, Provenance{}); !errors.Is(err, ErrKeyEmpty) {
		t.Errorf("err = %v, want ErrKeyEmpty", err)
	}
}

func TestAckProposalTable(t *testing.T) {
	type step struct {
		name        string
		ack         ProposalAck
		wantEffect  AckEffect
		wantResult  Result
		wantStatus  string
		wantVersion int
		wantAttempt int
		wantDoc     string
	}
	steps := []step{
		{"first posted", ackFixture(AckPosted, "AP-2026-0004311"), EffectAcknowledged, ResultApplied, ProposalAcknowledged, 2, 0, "AP-2026-0004311"},
		{"replayed posted", ackFixture(AckPosted, "AP-2026-0004311"), EffectReplay, ResultUnchanged, ProposalAcknowledged, 2, 0, "AP-2026-0004311"},
		{"double post", ackFixture(AckPosted, "AP-2026-0009999"), EffectConflict, ResultApplied, ProposalNeedsInvestigation, 3, 0, "AP-2026-0004311"},
	}
	s, _, _ := openTemp(t)
	if _, err := s.PutProposal(proposalFixture("prp_0000001"), prov("b", "r", "", 0)); err != nil {
		t.Fatal(err)
	}
	for _, st := range steps {
		out, err := s.AckProposal("prp_0000001", st.ack, prov("b", "r", "", 0))
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if out.Effect != st.wantEffect {
			t.Errorf("%s: effect = %q, want %q", st.name, out.Effect, st.wantEffect)
		}
		if out.Outcome.Result != st.wantResult {
			t.Errorf("%s: result = %q, want %q", st.name, out.Outcome.Result, st.wantResult)
		}
		if out.Outcome.Version != st.wantVersion {
			t.Errorf("%s: version = %d, want %d", st.name, out.Outcome.Version, st.wantVersion)
		}
		if out.Proposal.Status != st.wantStatus {
			t.Errorf("%s: status = %q, want %q", st.name, out.Proposal.Status, st.wantStatus)
		}
		if out.Proposal.Ack == nil || out.Proposal.Ack.ExternalDocumentNumber != st.wantDoc {
			t.Errorf("%s: stored ack = %+v, want document %s", st.name, out.Proposal.Ack, st.wantDoc)
		}
	}
	p, ok, err := s.GetProposal("prp_0000001")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(p.AckConflicts) != 1 || p.AckConflicts[0].ExternalDocumentNumber != "AP-2026-0009999" {
		t.Fatalf("ack conflicts = %+v, want the second document number recorded", p.AckConflicts)
	}
	if p.Ack.ExternalDocumentNumber != "AP-2026-0004311" {
		t.Error("the conflicting ack must not overwrite the document number that closed the chain")
	}
	hist, revs, err := s.ProposalHistory("prp_0000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 || len(revs) != 4 {
		t.Fatalf("history length = %d, want 4 (create, ack, replay, conflict)", len(hist))
	}
	if !revs[2].ProvenanceOnly {
		t.Error("the replayed ack must be a provenance-only revision")
	}
	if hist[0].Status != ProposalPending || hist[1].Status != ProposalAcknowledged {
		t.Errorf("history statuses = %q, %q", hist[0].Status, hist[1].Status)
	}
}

func TestAckProposalFailedAndSkipped(t *testing.T) {
	s, _, _ := openTemp(t)
	if _, err := s.PutProposal(proposalFixture("prp_0000002"), prov("b", "r", "", 0)); err != nil {
		t.Fatal(err)
	}
	failed := ackFixture(AckFailed, "")
	failed.HTTPStatus = 503
	failed.Error = "QUOTA_EXHAUSTED"
	for i := 1; i <= 2; i++ {
		out, err := s.AckProposal("prp_0000002", failed, prov("b", "r", "", 0))
		if err != nil {
			t.Fatal(err)
		}
		if out.Effect != EffectRetry {
			t.Errorf("attempt %d: effect = %q, want retry", i, out.Effect)
		}
		if out.Proposal.Status != ProposalPending {
			t.Errorf("attempt %d: status = %q, want pending: a failed posting must be resumed, not restarted", i, out.Proposal.Status)
		}
		if out.Proposal.Attempts != i {
			t.Errorf("attempt %d: attempts = %d", i, out.Proposal.Attempts)
		}
		if out.Outcome.Result != ResultApplied {
			t.Errorf("attempt %d: result = %q, want applied: the attempt counter changed", i, out.Outcome.Result)
		}
	}
	skipped := ackFixture(AckSkipped, "")
	skipped.Reason = "quota budget exhausted for this run"
	out, err := s.AckProposal("prp_0000002", skipped, prov("b", "r", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Effect != EffectSkipped || out.Proposal.Status != ProposalPending {
		t.Errorf("skipped ack = %+v", out)
	}
	if out.Proposal.Attempts != 2 {
		t.Errorf("attempts = %d, want 2: a skip is not an attempt", out.Proposal.Attempts)
	}

	rejected := ackFixture(AckRejected, "")
	rejected.Error = "ERP_PO_CLOSED"
	out, err = s.AckProposal("prp_0000002", rejected, prov("b", "r", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Effect != EffectRejected || out.Proposal.Status != ProposalRejected {
		t.Errorf("rejected ack = %+v", out)
	}
}

func TestAckProposalErrors(t *testing.T) {
	s, _, _ := openTemp(t)
	if _, err := s.AckProposal("prp_missing", ackFixture(AckPosted, "AP-1"), Provenance{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown proposal: %v, want ErrNotFound", err)
	}
	if _, err := s.PutProposal(proposalFixture("prp_0000003"), Provenance{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AckProposal("prp_0000003", ProposalAck{Status: "maybe"}, Provenance{}); !errors.Is(err, ErrAckStatus) {
		t.Errorf("unknown ack status: %v, want ErrAckStatus", err)
	}
	if v, _, _, _, revs, _ := s.Head(DatasetProposal, "prp_0000003"); v != 1 || revs != 1 {
		t.Errorf("a rejected ack wrote a revision: version %d over %d revisions", v, revs)
	}
}

func TestSupersedeProposal(t *testing.T) {
	s, _, _ := openTemp(t)
	old := proposalFixture("prp_0000001")
	if _, err := s.PutProposal(old, prov("b", "r", "", 0)); err != nil {
		t.Fatal(err)
	}
	newer := proposalFixture("prp_0000002")
	newer.Amount = model.Money{AmountMinor: 10824, Currency: "CHF", Scale: 2}
	if _, err := s.PutProposal(newer, prov("b", "r", "", 0)); err != nil {
		t.Fatal(err)
	}
	out, err := s.SupersedeProposal("prp_0000001", "prp_0000002", prov("b", "r", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultApplied || out.Version != 2 {
		t.Fatalf("outcome = %+v", out)
	}
	p, ok, err := s.GetProposal("prp_0000001")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if p.Status != ProposalSuperseded || p.SupersededBy != "prp_0000002" {
		t.Errorf("proposal = %+v", p)
	}
	// The matched content is untouched: supersession is not an edit.
	if p.ProposalContentHash != model.ContentHash(old.normalized().Content()) {
		t.Error("supersession changed the matched content hash")
	}
	if _, err := s.SupersedeProposal("prp_missing", "prp_0000002", Provenance{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown proposal: %v, want ErrNotFound", err)
	}
	if _, err := s.SupersedeProposal("prp_0000002", "  ", Provenance{}); !errors.Is(err, ErrKeyEmpty) {
		t.Errorf("empty successor: %v, want ErrKeyEmpty", err)
	}
}

func TestScanProposalsOrderAndCounts(t *testing.T) {
	s, _, _ := openTemp(t)
	ids := []string{"prp_0000003", "prp_0000001", "prp_0000002"}
	for _, id := range ids {
		p := proposalFixture(id)
		p.CreatedSeq = 1
		if _, err := s.PutProposal(p, prov("b", "r", "", 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AckProposal("prp_0000002", ackFixture(AckPosted, "AP-1"), Provenance{}); err != nil {
		t.Fatal(err)
	}
	var order []string
	if err := s.ScanProposals(func(p Proposal) bool {
		order = append(order, p.ProposalID)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"prp_0000001", "prp_0000002", "prp_0000003"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("scan order = %v, want %v (zero-padded ids make key order created order)", order, want)
		}
	}
	counts, err := s.CountProposalsByStatus()
	if err != nil {
		t.Fatal(err)
	}
	if counts[ProposalPending] != 2 || counts[ProposalAcknowledged] != 1 {
		t.Errorf("counts = %v", counts)
	}
	// Early stop.
	n := 0
	if err := s.ScanProposals(func(Proposal) bool { n++; return false }); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("early stop visited %d proposals, want 1", n)
	}
}
