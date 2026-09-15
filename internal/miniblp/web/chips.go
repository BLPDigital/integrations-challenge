package web

import (
	"sort"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// Status chips use at most two accent families, which is the brand book's cap
// and the build spec's instruction: red for what is wrong - exceptions, rejects,
// conflicts - and green for what closed - posted, acknowledged, accepted.
// Everything else is a Pale Blue chip with Atlas Blue text, because a landscape
// where every state is colored has no signal left.
const (
	// ToneNone is the neutral chip.
	ToneNone = ""
	// ToneRed marks an exception, a reject or a conflict.
	ToneRed = "red"
	// ToneGreen marks an accepted, posted or resolved state.
	ToneGreen = "green"
)

// toneForOutcome tones a per-record receipt outcome.
func toneForOutcome(outcome string) string {
	switch outcome {
	case miniblp.OutcomeAccepted, miniblp.OutcomeAcceptedWithWarning:
		return ToneGreen
	case miniblp.OutcomeRejected, miniblp.OutcomeQuarantined:
		return ToneRed
	default:
		return ToneNone
	}
}

// toneForBatchStatus tones a batch or file status.
func toneForBatchStatus(status string) string {
	switch status {
	case miniblp.BatchAccepted:
		return ToneGreen
	case miniblp.BatchRejected:
		return ToneRed
	default:
		return ToneNone
	}
}

// toneForProposalStatus tones a proposal status. needs_investigation is the
// double-post detector and is the loudest thing this interface can say.
func toneForProposalStatus(status string) string {
	switch status {
	case store.ProposalAcknowledged:
		return ToneGreen
	case store.ProposalRejected, store.ProposalNeedsInvestigation:
		return ToneRed
	default:
		return ToneNone
	}
}

// toneForExceptionState tones an exception state.
func toneForExceptionState(state string) string {
	switch state {
	case store.ExceptionOpen:
		return ToneRed
	case store.ExceptionResolved:
		return ToneGreen
	default:
		return ToneNone
	}
}

// toneForCounts tones a tally: red as soon as a record was rejected or
// quarantined, green when everything landed, neutral otherwise.
func toneForCounts(c miniblp.Counts) string {
	switch {
	case c.Rejected > 0 || c.Quarantined > 0:
		return ToneRed
	case c.Seen > 0 && c.Accepted+c.AcceptedWithWarning == c.Seen:
		return ToneGreen
	default:
		return ToneNone
	}
}

// sortExceptionRefs sorts exception references by subject then code, so a
// rendering never depends on the order a batch happened to raise them in.
func sortExceptionRefs(refs []miniblp.ExceptionRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].SubjectKey != refs[j].SubjectKey {
			return refs[i].SubjectKey < refs[j].SubjectKey
		}
		return refs[i].Code < refs[j].Code
	})
}

// sortStrings returns a sorted copy, for the several places that must not sort
// a slice the twin owns in place.
func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
