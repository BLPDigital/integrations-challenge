package miniblp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// ackBody builds one ack submission.
func ackBody(acks ...map[string]any) []byte {
	buf, _ := json.Marshal(acks)
	return buf
}

// postAck submits one ack set and returns the status and the per-ack results.
func (h *harness) postAck(key string, acks ...map[string]any) (int, []AckResult) {
	h.t.Helper()
	status, body := h.post("/v1/outbox/acks", ackBody(acks...),
		map[string]string{httpx.HeaderIdempotencyKey: key})
	var out struct {
		Records []AckResult `json:"records"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	if status != http.StatusOK && status != http.StatusMultiStatus &&
		status != http.StatusUnprocessableEntity {
		h.t.Fatalf("acks: status %d body %s", status, body)
	}
	return status, out.Records
}

// oneProposal builds the landscape, delivers one matching invoice and returns the
// proposal it produced.
func oneProposal(t *testing.T) (*harness, store.Proposal) {
	t.Helper()
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	b := h.deliverInvoices(invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35"))
	if len(b.Proposals) != 1 {
		t.Fatalf("proposals = %v, want one", b.Proposals)
	}
	p, ok, err := h.srv.Store().GetProposal(b.Proposals[0])
	if err != nil || !ok {
		t.Fatalf("proposal: ok %v err %v", ok, err)
	}
	return h, p
}

// TestAckPostedClosesTheChain asserts the first row of the ack table.
func TestAckPostedClosesTheChain(t *testing.T) {
	h, p := oneProposal(t)
	status, results := h.postAck("ack-1", map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
		"external_fiscal_year":     2026,
		"external_posting_date":    "2026-03-16",
		"idempotency_key":          "blp:acme-ch:" + p.ProposalID,
		"run_id":                   "run_test",
		"attempts":                 1,
		"http_status":              200,
	})
	if status != http.StatusOK || len(results) != 1 {
		t.Fatalf("status = %d results = %+v", status, results)
	}
	if results[0].Effect != string(store.EffectAcknowledged) {
		t.Fatalf("effect = %q", results[0].Effect)
	}
	after, _, _ := h.srv.Store().GetProposal(p.ProposalID)
	if after.Status != store.ProposalAcknowledged {
		t.Fatalf("proposal status = %q", after.Status)
	}
	if after.Ack == nil || after.Ack.ExternalDocumentNumber != "AP-2026-0004311" {
		t.Fatalf("ack = %+v", after.Ack)
	}
	// The ack is a record of the outbox_ack dataset too, on every channel.
	if _, ok := h.srv.Store().Get(model.DatasetOutboxAck.String(), p.ProposalID); !ok {
		t.Fatal("the ack was not stored in the outbox_ack dataset")
	}
	if counts, _ := h.srv.Store().CountProposalsByStatus(); counts[store.ProposalPending] != 0 {
		t.Fatalf("pending = %d, want 0: the headline assertion of a happy path",
			counts[store.ProposalPending])
	}
}

// TestAckReplayIsAWarning asserts the second row: the same document number again
// changes nothing and is reported as a warning, because an at-least-once
// connector re-reporting a posting is behaving correctly.
func TestAckReplayIsAWarning(t *testing.T) {
	h, p := oneProposal(t)
	ack := map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
	}
	h.postAck("ack-1", ack)
	digestBefore := h.digest()
	status, results := h.postAck("ack-2", ack)
	if status != http.StatusOK || len(results) != 1 {
		t.Fatalf("status = %d results = %+v", status, results)
	}
	if results[0].Effect != string(store.EffectReplay) {
		t.Fatalf("effect = %q, want a replay", results[0].Effect)
	}
	if len(results[0].Warnings) == 0 || results[0].Warnings[0].Code != CodeAckReplay {
		t.Fatalf("warnings = %+v, want %s", results[0].Warnings, CodeAckReplay)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("a replay opened an exception: %v", got)
	}
	if h.digest() != digestBefore {
		t.Fatal("a replayed ack changed the state digest")
	}
}

// TestAckConflictIsTheDoublePostDetector asserts the third row: a different
// document number for one proposal flags it needs_investigation and raises a
// blocking exception naming both documents.
func TestAckConflictIsTheDoublePostDetector(t *testing.T) {
	h, p := oneProposal(t)
	h.postAck("ack-1", map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
	})
	status, results := h.postAck("ack-2", map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0009999",
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: every ack of the set was refused", status)
	}
	if len(results) != 1 || results[0].Effect != string(store.EffectConflict) {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Errors[0].Code != CodeAckConflict {
		t.Fatalf("errors = %+v, want %s", results[0].Errors, CodeAckConflict)
	}
	after, _, _ := h.srv.Store().GetProposal(p.ProposalID)
	if after.Status != store.ProposalNeedsInvestigation {
		t.Fatalf("proposal status = %q", after.Status)
	}
	e, ok, err := h.srv.Store().GetException(p.ProposalID, CodeAckConflict)
	if err != nil || !ok {
		t.Fatalf("exception: ok %v err %v", ok, err)
	}
	if e.Details["erp_document_number_first"] != "AP-2026-0004311" ||
		e.Details["erp_document_number_second"] != "AP-2026-0009999" {
		t.Fatalf("details = %+v, want both document numbers", e.Details)
	}
	if e.SubjectType != SubjectTypeProposal || e.Stage != store.StageAck {
		t.Fatalf("exception = %+v", e)
	}
}

// TestAckRejectedReturnsTheInvoiceToTheQueue asserts the fourth row.
func TestAckRejectedReturnsTheInvoiceToTheQueue(t *testing.T) {
	h, p := oneProposal(t)
	status, results := h.postAck("ack-1", map[string]any{
		"proposal_id": p.ProposalID,
		"status":      store.AckRejected,
		"error":       "ERP_PERIOD_CLOSED",
		"http_status": 200,
	})
	if status != http.StatusOK || len(results) != 1 {
		t.Fatalf("status = %d results %+v", status, results)
	}
	after, _, _ := h.srv.Store().GetProposal(p.ProposalID)
	if after.Status != store.ProposalRejected {
		t.Fatalf("proposal status = %q", after.Status)
	}
	e, ok, err := h.srv.Store().GetException(p.InvoiceKey, CodeAckRejected)
	if err != nil || !ok {
		t.Fatalf("exception: ok %v err %v", ok, err)
	}
	if e.Details["erp_code"] != "ERP_PERIOD_CLOSED" {
		t.Fatalf("details = %+v, want the ERP's own code", e.Details)
	}
}

// TestAckFailedLeavesTheProposalPending asserts the fifth row: resume, not
// restart. A transport failure is not a business finding, so nothing is opened.
func TestAckFailedLeavesTheProposalPending(t *testing.T) {
	h, p := oneProposal(t)
	if _, results := h.postAck("ack-1", map[string]any{
		"proposal_id": p.ProposalID,
		"status":      store.AckFailed,
		"error":       "connection reset",
		"attempts":    2,
	}); len(results) != 1 || results[0].ProposalStatus != store.ProposalPending {
		t.Fatalf("results = %+v, want the proposal still pending", results)
	}
	after, _, _ := h.srv.Store().GetProposal(p.ProposalID)
	if after.Status != store.ProposalPending || after.Attempts != 1 {
		t.Fatalf("proposal = status %q attempts %d, want pending with one attempt",
			after.Status, after.Attempts)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("a failed ack opened an exception: %v", got)
	}
	// The next run picks it up: it is still on the outbox.
	status, body := h.get("/v1/outbox/proposals?status=pending")
	if status != http.StatusOK {
		t.Fatalf("outbox status = %d", status)
	}
	var page struct {
		Returned int `json:"returned"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("outbox page: %v", err)
	}
	if page.Returned != 1 {
		t.Fatalf("outbox returned %d, want the failed proposal to be resumable", page.Returned)
	}
}

// TestAckSkippedLeavesTheProposalPending asserts the sixth row.
func TestAckSkippedLeavesTheProposalPending(t *testing.T) {
	h, p := oneProposal(t)
	if _, results := h.postAck("ack-1", map[string]any{
		"proposal_id": p.ProposalID,
		"status":      store.AckSkipped,
		"reason":      "held for review",
	}); len(results) != 1 || results[0].ProposalStatus != store.ProposalPending {
		t.Fatalf("results = %+v", results)
	}
	if got := h.openPairs(); len(got) != 0 {
		t.Fatalf("a skipped ack opened an exception: %v", got)
	}
}

// TestAckUnknownProposalIsRejected asserts that an ack for a proposal the twin
// never emitted is refused rather than absorbed.
func TestAckUnknownProposalIsRejected(t *testing.T) {
	h, _ := oneProposal(t)
	status, results := h.postAck("ack-1", map[string]any{
		"proposal_id":              "prp_9999999",
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0000001",
	})
	if status != http.StatusUnprocessableEntity || len(results) != 1 {
		t.Fatalf("status = %d results %+v", status, results)
	}
	if results[0].Errors[0].Code != CodeAckUnknownProposal {
		t.Fatalf("errors = %+v", results[0].Errors)
	}
}

// TestAckIdempotencyReplaysTheStoredResponse asserts that the same key with the
// same body replays verbatim and does not ack twice.
func TestAckIdempotencyReplaysTheStoredResponse(t *testing.T) {
	h, p := oneProposal(t)
	ack := map[string]any{
		"proposal_id":              p.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
	}
	_, first := h.post("/v1/outbox/acks", ackBody(ack),
		map[string]string{httpx.HeaderIdempotencyKey: "same"})
	_, second := h.post("/v1/outbox/acks", ackBody(ack),
		map[string]string{httpx.HeaderIdempotencyKey: "same"})
	if string(first) != string(second) {
		t.Fatalf("replay differs:\n%s\n%s", first, second)
	}
	history, _, err := h.srv.Store().ProposalHistory(p.ProposalID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("proposal has %d revisions, want the initial one and one ack", len(history))
	}
}

// TestAckRequiresIdempotencyKey asserts the published requirement.
func TestAckRequiresIdempotencyKey(t *testing.T) {
	h, p := oneProposal(t)
	status, body := h.post("/v1/outbox/acks", ackBody(map[string]any{
		"proposal_id": p.ProposalID, "status": store.AckSkipped,
	}), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", status, body)
	}
}

// TestAckByFileReachesTheSameStateAsAckByREST asserts that the two ack routes are
// interchangeable: the same ack delivered as an outbox_ack file in a batch and
// posted over HTTP produce the same digest and the same proposal state. If they
// did not, the double-post detector would depend on which route the connector
// chose.
func TestAckByFileReachesTheSameStateAsAckByREST(t *testing.T) {
	build := func() (*harness, store.Proposal) {
		h := newHarness(t, nil)
		seedMatchingLandscape(h)
		b := h.deliverInvoices(invoiceFixture("0000000417", "0004711", "CHF", "1250.00", "89.35"))
		p, _, _ := h.srv.Store().GetProposal(b.Proposals[0])
		return h, p
	}
	byREST, pRest := build()
	byREST.postAck("ack-1", map[string]any{
		"proposal_id":              pRest.ProposalID,
		"status":                   store.AckPosted,
		"external_document_number": "AP-2026-0004311",
		"idempotency_key":          "blp:acme-ch:" + pRest.ProposalID,
	})

	byFile, pFile := build()
	if pFile.ProposalID != pRest.ProposalID {
		t.Fatalf("proposal ids differ across runs: %q and %q", pFile.ProposalID, pRest.ProposalID)
	}
	acksCSV := "proposal_id,status,external_document_number,idempotency_key\n" +
		pFile.ProposalID + ",posted,AP-2026-0004311,blp:acme-ch:" + pFile.ProposalID + "\n"
	m := fileManifest("batch-acks", fileEntry("acks.csv", "outbox_ack", "csv", 1))
	byFile.writeBatchDir("batch-acks", m, m2(m, map[string]string{"acks.csv": acksCSV}))
	rep := byFile.scan()
	if rep.Batches[0].Status != BatchAccepted {
		t.Fatalf("ack batch = %+v", rep.Batches[0])
	}

	after, _, _ := byFile.srv.Store().GetProposal(pFile.ProposalID)
	if after.Status != store.ProposalAcknowledged ||
		after.Ack.ExternalDocumentNumber != "AP-2026-0004311" {
		t.Fatalf("file-delivered ack did not close the chain: %+v", after)
	}
	if byFile.digest() != byREST.digest() {
		t.Fatalf("the ack channel changed the digest:\n file %s\n rest %s",
			byFile.digest(), byREST.digest())
	}
}
