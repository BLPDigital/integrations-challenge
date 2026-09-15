package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// DatasetProposal is the segment holding posting proposals, keyed by proposal id.
//
// Proposals live in the same append-only store as master data rather than in a
// type of their own, because they need exactly what the store already
// guarantees: fsynced appends, a full revision history with provenance, content
// -hash idempotence and crash recovery. They are excluded from [Store.Digest]
// (see its documentation) because their payload carries the run id, the created
// sequence and the attempt counter, none of which is logical content.
const DatasetProposal = "proposal"

// Proposal statuses. The set is closed.
const (
	// ProposalPending is the initial status: matched, not yet posted.
	ProposalPending = "pending"
	// ProposalAcknowledged means the ERP posted it and the chain is closed.
	ProposalAcknowledged = "acknowledged"
	// ProposalRejected means the ERP refused it for a business reason.
	ProposalRejected = "rejected"
	// ProposalSuperseded means a newer proposal replaced it; see
	// [Proposal.SupersededBy].
	ProposalSuperseded = "superseded"
	// ProposalNeedsInvestigation means two different ERP document numbers were
	// acknowledged for it: the double-post detector fired.
	ProposalNeedsInvestigation = "needs_investigation"
)

// Ack statuses, as delivered by the connector on POST /v1/outbox/acks.
const (
	// AckPosted reports an ERP document.
	AckPosted = "posted"
	// AckRejected reports a business rejection.
	AckRejected = "rejected"
	// AckFailed reports a transport failure; the proposal stays pending and the
	// next run must pick it up.
	AckFailed = "failed"
	// AckSkipped reports a deliberate non-attempt; the proposal stays pending.
	AckSkipped = "skipped"
)

// Proposal is one immutable posting proposal emitted by the matching engine.
//
// "Immutable" means the matched content is: ProposalContentHash covers the
// fields the decision was made from, and a changed decision is a new proposal
// that supersedes this one, never an edit. The lifecycle fields - Status,
// Attempts, SupersededBy, Ack and AckConflicts - do change, and every change is
// a new revision of the same key, so the previous state is never lost.
type Proposal struct {
	// ProposalID is the natural key. Ids are zero-padded so that ascending key
	// order equals ascending CreatedSeq order, which is the order the outbox
	// pages in.
	ProposalID string `json:"proposal_id"`
	// InvoiceKey is the natural key of the invoice, i.e. [model.InvoiceKey].
	InvoiceKey string `json:"invoice_key"`
	// InvoiceVersion is the invoice revision the decision was made on.
	InvoiceVersion int `json:"invoice_version"`
	// ProposalContentHash identifies the matched content. See [ProposalContent].
	ProposalContentHash string `json:"proposal_content_hash"`
	// CreatedInRun and CreatedSeq place the proposal in the run that made it.
	CreatedInRun string `json:"created_in_run"`
	CreatedSeq   int64  `json:"created_seq"`
	// Status is one of the Proposal* constants.
	Status string `json:"status"`
	// MatchedPO is the purchase order the invoice matched, "" if none.
	MatchedPO string `json:"matched_po"`
	// MatchedCostCenter is the resolved cost center.
	MatchedCostCenter string `json:"matched_cost_center"`
	// FxRateUsed and FxRateFactor are the exchange rate as delivered, kept
	// separate on purpose: a factor of 100 misread as 1 is a hundredfold posting
	// error, and dividing early loses precision. Both are zero when the invoice
	// is already in CHF.
	FxRateUsed   model.Decimal `json:"fx_rate_used"`
	FxRateFactor int64         `json:"fx_rate_factor"`
	// Amount is the amount to post, in the minor units of its currency.
	Amount model.Money `json:"amount"`
	// SourceAmount is the invoice amount before conversion; it equals Amount when
	// no conversion happened.
	SourceAmount model.Money `json:"source_amount"`
	// SupersededBy is the id of the proposal that replaced this one, "" while it
	// is current.
	SupersededBy string `json:"superseded_by"`
	// Attempts counts posting attempts reported by acks.
	Attempts int `json:"attempts"`
	// Warnings carries importer and matching warnings, e.g.
	// "ambiguous_field_semantics". A warning never blocks a proposal; it travels
	// with it so silence is impossible.
	Warnings []string `json:"warnings"`
	// Ack is the last accepted ack, nil while the proposal is unacknowledged.
	Ack *ProposalAck `json:"ack"`
	// AckConflicts holds acks that named a different ERP document number than the
	// one already acknowledged. It is non-empty exactly when Status is
	// [ProposalNeedsInvestigation], and it keeps both document numbers on the
	// record so the exception can name them.
	AckConflicts []ProposalAck `json:"ack_conflicts"`
}

// ProposalAck is the connector's report of one posting attempt.
type ProposalAck struct {
	// Status is one of the Ack* constants.
	Status string `json:"status"`
	// ExternalDocumentNumber is the ERP document number, e.g. "AP-2026-0004311".
	ExternalDocumentNumber string `json:"external_document_number"`
	// ExternalRevision is the ERP document revision.
	ExternalRevision int `json:"external_revision"`
	// ExternalFiscalYear is the ERP fiscal year.
	ExternalFiscalYear int `json:"external_fiscal_year"`
	// ExternalPostingDate is the posting date the ERP recorded, YYYY-MM-DD.
	ExternalPostingDate string `json:"external_posting_date"`
	// IdempotencyKey is the key the posting was made under. It must be a pure
	// function of the proposal, so the same key across runs yields one document.
	IdempotencyKey string `json:"idempotency_key"`
	// RunID is the connector run that posted.
	RunID string `json:"run_id"`
	// PostedAt is the instant the ERP reported, verbatim. The store never invents
	// it: there is no wall clock in this landscape.
	PostedAt string `json:"posted_at"`
	// Attempts is the connector's own attempt count for this posting.
	Attempts int `json:"attempts"`
	// HTTPStatus is the transport status of the final attempt.
	HTTPStatus int `json:"http_status"`
	// IdempotencyReplay reports that the ERP replayed a stored response.
	IdempotencyReplay bool `json:"idempotency_replay"`
	// Error carries the ERP or transport error code for a rejected or failed ack.
	Error string `json:"error"`
	// Reason carries the connector's reason for a skipped ack.
	Reason string `json:"reason"`
}

// ProposalContent is the immutable part of a proposal: everything the matching
// decision consisted of, and nothing about the run, the attempt or the ack. Its
// [model.ContentHash] is [Proposal.ProposalContentHash], which is what the
// idempotency key recipe blp:{tenant}:{proposal_id}:{hash[0:16]} folds in, so the
// key is stable across runs.
type ProposalContent struct {
	ProposalID        string        `json:"proposal_id"`
	InvoiceKey        string        `json:"invoice_key"`
	InvoiceVersion    int           `json:"invoice_version"`
	MatchedPO         string        `json:"matched_po"`
	MatchedCostCenter string        `json:"matched_cost_center"`
	FxRateUsed        model.Decimal `json:"fx_rate_used"`
	FxRateFactor      int64         `json:"fx_rate_factor"`
	Amount            model.Money   `json:"amount"`
	SourceAmount      model.Money   `json:"source_amount"`
}

// Content returns the immutable projection of the proposal.
func (p Proposal) Content() ProposalContent {
	return ProposalContent{
		ProposalID:        p.ProposalID,
		InvoiceKey:        p.InvoiceKey,
		InvoiceVersion:    p.InvoiceVersion,
		MatchedPO:         p.MatchedPO,
		MatchedCostCenter: p.MatchedCostCenter,
		FxRateUsed:        p.FxRateUsed,
		FxRateFactor:      p.FxRateFactor,
		Amount:            p.Amount,
		SourceAmount:      p.SourceAmount,
	}
}

// AckEffect classifies what an ack did to a proposal, so the caller can raise the
// right warning or exception without re-deriving the state machine.
type AckEffect string

// The ack effects, one per row of the twin's ack table.
const (
	// EffectAcknowledged: a first posted ack closed the chain.
	EffectAcknowledged AckEffect = "acknowledged"
	// EffectReplay: a posted ack repeated the stored document number. Nothing
	// changed; the caller reports W_ACK_REPLAY.
	EffectReplay AckEffect = "replay"
	// EffectConflict: a posted ack named a different document number. The
	// proposal is flagged needs_investigation and the caller raises
	// E_ACK_CONFLICT. This is the double-post detector.
	EffectConflict AckEffect = "conflict"
	// EffectRejected: the ERP rejected the posting; the caller raises an
	// exception carrying the ERP code and returns the invoice to the open queue.
	EffectRejected AckEffect = "rejected"
	// EffectRetry: a failed ack. The proposal stays pending with Attempts
	// incremented; the next run resumes it.
	EffectRetry AckEffect = "retry"
	// EffectSkipped: a skipped ack. The proposal stays pending with a reason.
	EffectSkipped AckEffect = "skipped"
)

// AckOutcome is the result of [Store.AckProposal].
type AckOutcome struct {
	// Outcome is the underlying store write. A replay is [ResultUnchanged], so it
	// appends a provenance-only revision and leaves the version alone.
	Outcome Outcome
	// Effect is which row of the ack table applied.
	Effect AckEffect
	// Proposal is the proposal state after the ack.
	Proposal Proposal
}

// PutProposal writes a proposal. A first write creates it at version 1; a
// re-write of byte-identical content is [ResultUnchanged] and appends only
// provenance, so re-running the matching engine over unchanged invoices is free
// and still auditable.
//
// ProposalContentHash is computed here from [Proposal.Content] and any value the
// caller set is overwritten, so the hash can never disagree with the content it
// names. Status defaults to [ProposalPending] and Warnings is normalized to a
// sorted, de-duplicated slice so the same set never produces two hashes.
func (s *Store) PutProposal(p Proposal, prov Provenance) (Outcome, error) {
	if strings.TrimSpace(p.ProposalID) == "" {
		return Outcome{}, fmt.Errorf("%w: proposal id", ErrKeyEmpty)
	}
	p = p.normalized()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(Revision{
		Dataset:    DatasetProposal,
		Key:        p.ProposalID,
		Payload:    p,
		Provenance: prov,
	})
}

// normalized returns the proposal with its derived fields fixed: default status,
// sorted unique warnings, non-nil slices and a recomputed content hash.
func (p Proposal) normalized() Proposal {
	if p.Status == "" {
		p.Status = ProposalPending
	}
	p.ProposalID = strings.TrimSpace(p.ProposalID)
	p.Warnings = normalizeWarnings(p.Warnings)
	if p.AckConflicts == nil {
		p.AckConflicts = []ProposalAck{}
	}
	p.ProposalContentHash = model.ContentHash(p.Content())
	return p
}

// normalizeWarnings sorts and de-duplicates a warning set, so two deliveries that
// collected the same warnings in a different order hash identically.
func normalizeWarnings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, w := range in {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// GetProposal returns a proposal by id.
func (s *Store) GetProposal(id string) (Proposal, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getProposalLocked(id)
}

// getProposalLocked reads and decodes a proposal with the lock already held.
func (s *Store) getProposalLocked(id string) (Proposal, bool, error) {
	rev, ok, err := s.getLocked(DatasetProposal, id)
	if err != nil || !ok {
		return Proposal{}, false, err
	}
	var p Proposal
	if err := decodePayload(rev.Payload, &p); err != nil {
		return Proposal{}, false, err
	}
	return p, true, nil
}

// ProposalHistory returns every revision of a proposal decoded, in append order:
// the initial pending state, every lifecycle change and every ack, including the
// provenance-only re-deliveries. It is the proposal half of the audit chain.
func (s *Store) ProposalHistory(id string) ([]Proposal, []Revision, error) {
	revs, err := s.History(DatasetProposal, id)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Proposal, 0, len(revs))
	for _, rev := range revs {
		var p Proposal
		if err := decodePayload(rev.Payload, &p); err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	return out, revs, nil
}

// ScanProposals calls fn for every live proposal in ascending proposal-id order,
// stopping early when fn returns false. It streams; fn must not call back into
// the store.
func (s *Store) ScanProposals(fn func(Proposal) bool) error {
	var derr error
	err := s.Scan(DatasetProposal, func(rev Revision) bool {
		var p Proposal
		if derr = decodePayload(rev.Payload, &p); derr != nil {
			return false
		}
		return fn(p)
	})
	if err != nil {
		return err
	}
	return derr
}

// CountProposalsByStatus returns the number of live proposals per status. The map
// is only ever read by key, and callers that render it sort the keys.
func (s *Store) CountProposalsByStatus() (map[string]int, error) {
	out := make(map[string]int)
	err := s.ScanProposals(func(p Proposal) bool {
		out[p.Status]++
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AckProposal applies one ack to a proposal and appends the resulting revision.
// The read and the write happen under one lock, so two concurrent acks for the
// same proposal cannot both be treated as the first.
//
// The effect follows the twin's ack table exactly:
//
//   - posted, no ack yet: status acknowledged, ack stored, [EffectAcknowledged].
//   - posted, same document number as the stored ack: nothing changes, so the
//     write is [ResultUnchanged] and the effect is [EffectReplay].
//   - posted, different document number: status needs_investigation, the original
//     ack is kept and the conflicting one is appended to AckConflicts, effect
//     [EffectConflict].
//   - rejected: status rejected, effect [EffectRejected].
//   - failed: status stays pending, Attempts incremented, effect [EffectRetry].
//   - skipped: status stays pending, effect [EffectSkipped].
//
// An unknown ack status is [ErrAckStatus] and writes nothing; an unknown
// proposal id is [ErrNotFound].
func (s *Store) AckProposal(id string, ack ProposalAck, prov Provenance) (AckOutcome, error) {
	switch ack.Status {
	case AckPosted, AckRejected, AckFailed, AckSkipped:
	default:
		return AckOutcome{}, fmt.Errorf("%w: %q", ErrAckStatus, ack.Status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok, err := s.getProposalLocked(id)
	if err != nil {
		return AckOutcome{}, err
	}
	if !ok {
		return AckOutcome{}, fmt.Errorf("%w: proposal %s", ErrNotFound, id)
	}
	effect := EffectAcknowledged
	switch ack.Status {
	case AckPosted:
		switch {
		case p.Ack != nil && p.Ack.Status == AckPosted && p.Ack.ExternalDocumentNumber == ack.ExternalDocumentNumber:
			// Re-delivery of the ack that already closed the chain. Re-apply the
			// proposal unchanged so the write is a provenance-only revision.
			effect = EffectReplay
		case p.Ack != nil && p.Ack.Status == AckPosted:
			effect = EffectConflict
			p.Status = ProposalNeedsInvestigation
			p.AckConflicts = append(p.AckConflicts, ack)
		default:
			p.Status = ProposalAcknowledged
			p.Ack = &ack
		}
	case AckRejected:
		effect = EffectRejected
		p.Status = ProposalRejected
		p.Ack = &ack
	case AckFailed:
		effect = EffectRetry
		p.Status = ProposalPending
		p.Attempts++
		p.Ack = &ack
	case AckSkipped:
		effect = EffectSkipped
		p.Status = ProposalPending
		p.Ack = &ack
	}
	p = p.normalized()
	out, err := s.applyLocked(Revision{
		Dataset:    DatasetProposal,
		Key:        p.ProposalID,
		Payload:    p,
		Provenance: prov,
	})
	if err != nil {
		return AckOutcome{}, err
	}
	return AckOutcome{Outcome: out, Effect: effect, Proposal: p}, nil
}

// SupersedeProposal marks a proposal replaced by another: status
// [ProposalSuperseded] and SupersededBy set to newID. The proposal's matched
// content is untouched, which is what makes supersession auditable rather than an
// edit.
//
// newID is recorded verbatim and is not required to exist yet, so the caller is
// free to write the superseding proposal before or after this call. An unknown
// oldID is [ErrNotFound].
func (s *Store) SupersedeProposal(oldID, newID string, prov Provenance) (Outcome, error) {
	if strings.TrimSpace(newID) == "" {
		return Outcome{}, fmt.Errorf("%w: superseding proposal id", ErrKeyEmpty)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok, err := s.getProposalLocked(oldID)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("%w: proposal %s", ErrNotFound, oldID)
	}
	p.Status = ProposalSuperseded
	p.SupersededBy = strings.TrimSpace(newID)
	p = p.normalized()
	return s.applyLocked(Revision{
		Dataset:    DatasetProposal,
		Key:        p.ProposalID,
		Payload:    p,
		Provenance: prov,
	})
}

// decodePayload re-decodes a revision payload into a typed value. It goes through
// canonical JSON so it works both for a payload still held as the caller's struct
// and for one replayed from disk as a generic tree, and so no number ever passes
// through a float.
func decodePayload(payload any, dst any) error {
	b, err := model.CanonicalJSON(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPayload, err)
	}
	return json.Unmarshal(b, dst)
}
