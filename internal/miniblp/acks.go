package miniblp

import (
	"net/http"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer/canonical"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The ack-stage codes of the twin, from the ack table of BUILD-SPEC 8.4.
const (
	// CodeAckReplay reports a posted ack repeating the document number already
	// acknowledged. It is a warning: an at-least-once connector that re-reports
	// a posting it already reported is behaving correctly.
	CodeAckReplay = "W_ACK_REPLAY"
	// CodeAckConflict reports a posted ack naming a DIFFERENT document number
	// than the one already acknowledged. This is the double-post detector: two
	// documents exist in the customer's ledger for one invoice, the proposal is
	// flagged needs_investigation and a blocking exception is raised, because
	// there is no reversal endpoint and a human has to decide.
	CodeAckConflict = "E_ACK_CONFLICT"
	// CodeAckRejected reports an ERP business rejection. The proposal becomes
	// rejected, the invoice returns to the open queue and the exception carries
	// the ERP's own code in its details.
	CodeAckRejected = "E_ACK_REJECTED"
	// CodeAckUnknownProposal reports an ack for a proposal the twin never
	// emitted.
	CodeAckUnknownProposal = "E_ACK_UNKNOWN_PROPOSAL"
	// CodeAckInvalid reports an ack whose own fields do not validate.
	CodeAckInvalid = "E_ACK_INVALID"
)

// An AckResult is the per-ack half of the answer to POST /v1/outbox/acks.
type AckResult struct {
	Ordinal    int    `json:"ordinal"`
	ProposalID string `json:"proposal_id"`
	// Effect is the row of the ack table that applied: acknowledged, replay,
	// conflict, rejected, retry or skipped.
	Effect string `json:"effect"`
	// ProposalStatus is the proposal's status after the ack.
	ProposalStatus string `json:"proposal_status"`
	// ERPDocumentNumber is the document number the chain is closed with.
	ERPDocumentNumber string    `json:"erp_document_number,omitempty"`
	Attempts          int       `json:"attempts"`
	Errors            []Finding `json:"errors"`
	Warnings          []Finding `json:"warnings"`
	Pointer           string    `json:"pointer,omitempty"`
}

// handleAcks serves POST /v1/outbox/acks.
//
// An ack is an event about a proposal and a record of the outbox_ack dataset at
// the same time. It is stored as a record on both channels, so a run that
// acknowledges its postings by file and a run that acknowledges them over HTTP
// reach the same state and the same digest; the double-post detector therefore
// cannot depend on which route the connector chose.
func (s *Server) handleAcks(w http.ResponseWriter, r *http.Request) {
	key, err := httpx.RequireIdempotencyKey(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, tooLargeAsBatch(err))
		return
	}
	format, err := httpx.RequestFormat(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	records, err := httpx.DecodeRecords(r, model.DatasetOutboxAck.String())
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	if len(records) > MaxAcks {
		e := &httpx.Error{Code: CodeBatchTooLarge,
			Message:   "one ack submission carries at most " + strconv.Itoa(MaxAcks) + " acks",
			Retriable: false, Status: http.StatusRequestEntityTooLarge}
		httpx.Fail(w, e.WithDetail("limit", MaxAcks).WithDetail("seen", len(records)))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idemKey := httpx.IdempotencyKey{Tenant: "", Method: r.Method, Path: r.URL.Path, Key: key}
	if rec, outcome := s.idem.Check(idemKey, httpx.BodyHash(body)); outcome != httpx.IdempotencyNew {
		if outcome == httpx.IdempotencyConflict {
			httpx.Fail(w, httpx.IdempotencyKeyReused())
			return
		}
		httpx.WriteReplay(w, rec)
		return
	}

	locate := pointerLocator(format, body, httpx.CSVDialectFrom(r.Context()))
	parsedAcks, _, err := parseRESTRecords(r.Context(), model.DatasetOutboxAck, format, records,
		httpx.CSVDialectFrom(r.Context()), locate)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}

	prov := store.Provenance{
		Channel:      ChannelREST,
		Profile:      ProfileCanonical,
		Format:       format.String(),
		Encoding:     importer.EncodingUTF8,
		SourceSystem: "connector",
		RunID:        s.cfg.RunID,
	}
	if seq := httpx.RequestSeq(r.Context()); seq > 0 {
		q := seq
		prov.RequestSeq = &q
	}

	results := make([]AckResult, 0, parsedAcks.Seen)
	rejected := 0
	for ord := 1; ord <= parsedAcks.Seen; ord++ {
		res := AckResult{Ordinal: ord, Errors: []Finding{}, Warnings: []Finding{},
			Pointer: locate(ord, "")}
		if errs, ok := parsedAcks.Errors[ord]; ok {
			res.Errors = errs
			res.Effect = OutcomeRejected
			rejected++
			results = append(results, res)
			continue
		}
		doc, ok := parsedAcks.Docs[ord]
		if !ok {
			res.Errors = []Finding{{Code: CodeAckInvalid, Message: "the ack could not be read"}}
			res.Effect = OutcomeRejected
			rejected++
			results = append(results, res)
			continue
		}
		record, id, err := s.applyAckDocument(nil, doc, prov)
		if err != nil {
			httpx.Fail(w, httpx.AsError(err))
			return
		}
		res.ProposalID = id
		res.Errors = record.Errors
		res.Warnings = append(res.Warnings, record.Warnings...)
		res.Effect = ackEffectOf(record)
		if p, ok, err := s.st.GetProposal(id); err == nil && ok {
			res.ProposalStatus = p.Status
			res.Attempts = p.Attempts
			if p.Ack != nil {
				res.ERPDocumentNumber = p.Ack.ExternalDocumentNumber
			}
		}
		if len(res.Errors) > 0 {
			rejected++
		}
		results = append(results, res)
	}

	status := http.StatusOK
	switch {
	case len(results) == 0 || rejected == 0:
		status = http.StatusOK
	case rejected == len(results):
		status = http.StatusUnprocessableEntity
	default:
		status = http.StatusMultiStatus
	}
	payload := map[string]any{
		"acknowledged": len(results) - rejected,
		"rejected":     rejected,
		"records":      results,
	}
	body2, err := renderRecords(httpx.FormatJSON, payload, httpx.DefaultCSVDialect())
	if err != nil {
		httpx.Fail(w, httpx.Internal("the ack response could not be encoded"))
		return
	}
	s.idem.Put(idemKey, httpx.IdempotencyRecord{
		Status: status, Header: http.Header{"Content-Type": []string{httpx.MediaJSON}},
		Body: body2, BodyHash: httpx.BodyHash(body),
	})
	w.Header().Set("Content-Type", httpx.MediaJSON)
	w.WriteHeader(status)
	_, _ = w.Write(body2)
}

// ackEffectOf names the row of the ack table a record result came from, for the
// per-ack response.
func ackEffectOf(res RecordResult) string {
	for _, e := range res.Errors {
		switch e.Code {
		case CodeAckConflict:
			return string(store.EffectConflict)
		case CodeAckUnknownProposal, CodeAckInvalid:
			return OutcomeRejected
		}
	}
	for _, wn := range res.Warnings {
		if wn.Code == CodeAckReplay {
			return string(store.EffectReplay)
		}
	}
	if res.AckEffect != "" {
		return res.AckEffect
	}
	return string(store.EffectAcknowledged)
}

// applyAckDocument applies one ack document, whichever channel delivered it.
//
// Two things happen, in this order and under the server lock: the ack is stored
// as a record of the outbox_ack dataset, so the state digest sees it on both
// channels, and then it is applied to its proposal through the store's own ack
// table. The store owns the state machine; this function owns the reporting:
// which finding a row of the table produces, and which exception it opens.
//
// b may be nil, which is the direct POST /v1/outbox/acks path: the exception is
// still raised, it is simply not attributed to a batch.
func (s *Server) applyAckDocument(b *batchState, doc importer.Document, prov store.Provenance) (RecordResult, string, error) {
	res := RecordResult{
		Dataset:    model.DatasetOutboxAck.String(),
		NaturalKey: doc.Key,
		Errors:     []Finding{},
		Warnings:   []Finding{},
		TwinID:     twinID(model.DatasetOutboxAck.String(), doc.Key),
	}
	var record canonical.AckRecord
	if err := decodePayloadBytes(doc.Payload, &record); err != nil {
		res.Outcome = OutcomeRejected
		res.Errors = append(res.Errors, Finding{Code: CodeAckInvalid,
			Message: "the ack is not a readable outbox_ack record"})
		return res, "", nil
	}

	// The ack is a record like any other: stored, content-hash idempotent, with
	// provenance. A re-delivered identical ack is skipped_unchanged here and a
	// replay in the ack table below, which is the same fact seen from the two
	// sides.
	outcome, err := s.st.Apply(store.Revision{
		Dataset:    model.DatasetOutboxAck.String(),
		Key:        doc.Key,
		Payload:    jsonPayload(doc.Payload),
		Provenance: prov,
	})
	if err != nil {
		res.Outcome = OutcomeQuarantined
		res.Errors = append(res.Errors, Finding{Code: httpx.CodeInternal,
			Message: "the ack could not be stored: " + err.Error()})
		return res, record.ProposalID, nil
	}
	res.Version = outcome.Version
	res.ContentHash = outcome.ContentHash
	res.RawExcerptSHA256 = outcome.ContentHash

	ackOutcome, err := s.st.AckProposal(record.ProposalID, record.ProposalAck, prov)
	switch {
	case err != nil && errorIsNotFound(err):
		res.Outcome = OutcomeRejected
		res.Errors = append(res.Errors, Finding{Code: CodeAckUnknownProposal, Field: "proposal_id",
			Message: "no proposal with this id was ever emitted"})
		return res, record.ProposalID, nil
	case err != nil && errorIsAckStatus(err):
		res.Outcome = OutcomeRejected
		res.Errors = append(res.Errors, Finding{Code: CodeAckInvalid, Field: "status",
			Message: "an ack status is posted, rejected, failed or skipped"})
		return res, record.ProposalID, nil
	case err != nil:
		return res, record.ProposalID, err
	}

	res.AckEffect = string(ackOutcome.Effect)
	p := ackOutcome.Proposal
	switch ackOutcome.Effect {
	case store.EffectAcknowledged:
		res.Outcome = OutcomeAccepted

	case store.EffectReplay:
		res.Outcome = OutcomeSkippedUnchanged
		res.Warnings = append(res.Warnings, Finding{Code: CodeAckReplay,
			Message: "this posting was already acknowledged with the same document number"})

	case store.EffectConflict:
		res.Outcome = OutcomeRejected
		res.Errors = append(res.Errors, Finding{Code: CodeAckConflict, Field: "external_document_number",
			Message: "this proposal was already acknowledged with a different ERP document number"})
		details := map[string]string{
			"proposal_id": p.ProposalID,
			"invoice_key": p.InvoiceKey,
		}
		if p.Ack != nil {
			details["erp_document_number_first"] = p.Ack.ExternalDocumentNumber
		}
		if n := len(p.AckConflicts); n > 0 {
			details["erp_document_number_second"] = p.AckConflicts[n-1].ExternalDocumentNumber
		}
		if err := s.raise(b, store.Exception{
			SubjectKey: p.ProposalID, SubjectType: SubjectTypeProposal,
			Stage: store.StageAck, Code: CodeAckConflict, Field: "external_document_number",
			Message: "two different ERP document numbers were acknowledged for one proposal",
			State:   store.ExceptionOpen, SourceBatchID: prov.BatchID, Details: details,
		}, prov); err != nil {
			return res, record.ProposalID, err
		}

	case store.EffectRejected:
		res.Outcome = OutcomeAccepted
		details := map[string]string{"proposal_id": p.ProposalID}
		if record.Error != "" {
			details["erp_code"] = record.Error
		}
		if record.HTTPStatus != 0 {
			details["http_status"] = strconv.Itoa(record.HTTPStatus)
		}
		if err := s.raise(b, store.Exception{
			SubjectKey: p.InvoiceKey, SubjectType: SubjectTypeInvoice,
			Stage: store.StagePost, Code: CodeAckRejected,
			Message: "the ERP rejected the posting of this invoice",
			State:   store.ExceptionOpen, SourceBatchID: prov.BatchID, Details: details,
		}, prov); err != nil {
			return res, record.ProposalID, err
		}

	default:
		// failed and skipped: the proposal stays pending and the next run picks
		// it up. This is resume, not restart, and it is deliberately not an
		// exception: a transport failure is not a business finding.
		res.Outcome = OutcomeAccepted
	}
	return res, record.ProposalID, nil
}
