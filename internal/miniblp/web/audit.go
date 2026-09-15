package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// handleAudit resolves one audit chain and renders it as an ordered list of
// links: source file and line, raw bytes, twin revisions, proposal, idempotency
// key, ERP document number, ack.
//
// The resolution is the twin's own, through GET /admin/v1/audit/{key}, which
// accepts an invoice key, a proposal id or an ERP document number and answers in
// both directions. The UI re-resolves nothing: it renders what the twin says,
// which is the whole point of an audit view.
func (u *UI) handleAudit(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	view := &ListView{
		Search: &Search{
			Action: u.href("/audit", nil),
			Field:  "q",
			Value:  raw,
			Label:  "Invoice key, proposal id or ERP document number",
			Hint: "A composite invoice key is written with a pipe, for example " +
				"0000417|RE-2026-0001. The chain resolves in both directions.",
		},
	}
	p := u.page(r, "Audit chain", "Audit chain",
		"From the delivered bytes to the ERP document number, and back.", view)
	if raw == "" {
		view.Sections = append(view.Sections, Section{
			Title: "Paste a key",
			Note: "Nothing is resolved yet. Three keys resolve: the natural key of an " +
				"invoice, a proposal id and an ERP document number.",
		})
		u.render(w, r, "list", p)
		return
	}

	chain, err := u.auditChain(t, r, normalizeKey(raw))
	if err != nil {
		var ae *adminError
		switch {
		case errors.Is(err, errNoAdminToken):
			p.Notices = append(p.Notices, err.Error())
		case errors.As(err, &ae) && ae.notFound():
			p.Notices = append(p.Notices, "Nothing in this twin resolves "+raw+".")
		default:
			p.Notices = append(p.Notices, "The chain could not be resolved: "+err.Error())
		}
		u.render(w, r, "list", p)
		return
	}

	view.Sections = append(view.Sections, Section{
		Title: "Resolved",
		Tiles: []Tile{
			{Label: "query", Value: displayKey(chain.Query)},
			{Label: "resolved as", Value: chain.Resolved},
			{Label: "invoice", Value: displayKey(chain.InvoiceKey),
				Href: u.recordHref(model.DatasetInvoice.String(), chain.InvoiceKey)},
			{Label: "revisions", Value: strconv.Itoa(len(chain.Revisions))},
			{Label: "proposals", Value: strconv.Itoa(len(chain.Proposals))},
			{Label: "ERP documents", Value: strconv.Itoa(len(chain.ERPDocumentNumbers)),
				Tone: greenIfAny(len(chain.ERPDocumentNumbers))},
			{Label: "exceptions", Value: strconv.Itoa(len(chain.Exceptions)),
				Tone: toneIfPositive(int64(len(chain.Exceptions)))},
		},
	})
	view.Sections = append(view.Sections, Section{
		Title: "The chain",
		Note:  "In order, from the bytes that arrived to the document the ERP posted.",
		Chain: u.chainSteps(chain),
	})

	if len(chain.Revisions) > 0 {
		view.Sections = append(view.Sections, Section{
			Title: "Invoice revisions",
			Note:  "Every delivery of this invoice with the provenance of each.",
			Table: u.historyTable(chain.Revisions, staleOverwrites(chain.Revisions)),
		})
	}
	if len(chain.Proposals) > 0 {
		rows := make([]Row, 0, len(chain.Proposals))
		for _, prop := range chain.Proposals {
			rows = append(rows, Row{Cells: u.proposalCells(prop)})
		}
		view.Sections = append(view.Sections, Section{
			Title: "Proposals",
			Table: &Table{Headers: proposalHeaders, Rows: rows},
		})
	}
	if len(chain.Exceptions) > 0 {
		rows := make([]Row, 0, len(chain.Exceptions))
		exceptions := append([]store.Exception(nil), chain.Exceptions...)
		sort.Slice(exceptions, func(i, j int) bool {
			if exceptions[i].SubjectKey != exceptions[j].SubjectKey {
				return exceptions[i].SubjectKey < exceptions[j].SubjectKey
			}
			return exceptions[i].Code < exceptions[j].Code
		})
		for _, e := range exceptions {
			rows = append(rows, Row{Cells: u.exceptionCells(e, true)})
		}
		view.Sections = append(view.Sections, Section{
			Title: "Exceptions on this chain",
			Table: &Table{Headers: exceptionHeaders(true), Rows: rows},
		})
	}
	u.render(w, r, "list", p)
}

// greenIfAny marks a count that is good news when it is not zero.
func greenIfAny(n int) string {
	if n > 0 {
		return ToneGreen
	}
	return ToneNone
}

// chainSteps renders the chain as the ordered list BUILD-SPEC 14 asks for. Each
// delivery contributes its source locator, its raw bytes and the twin revision
// it produced; each proposal contributes itself, the idempotency key it was
// posted under, the ERP document number it closed with and the ack that reported
// it.
func (u *UI) chainSteps(chain *miniblp.AuditChain) []ChainStep {
	steps := make([]ChainStep, 0, len(chain.Sources)*3+len(chain.Proposals)*4)
	add := func(kind, label, href, detail, tone string) {
		steps = append(steps, ChainStep{
			Step: len(steps) + 1, Kind: kind, Label: label, Href: href,
			Detail: detail, Tone: tone,
		})
	}
	for _, src := range chain.Sources {
		locator := src.SourceFile
		switch {
		case src.SourceLine > 0:
			locator += ":" + strconv.FormatInt(src.SourceLine, 10)
		case src.ChunkOrdinal > 0:
			locator = "chunk " + strconv.FormatInt(src.ChunkOrdinal, 10)
		}
		if locator == "" {
			locator = src.Channel
		}
		if src.RecordOrd > 0 {
			locator += " #" + strconv.FormatInt(src.RecordOrd, 10)
		}
		add("source", locator, "",
			"batch "+src.BatchID+", channel "+src.Channel+", run "+src.RunID, ToneNone)
		if src.SourceSHA256 != "" {
			add("raw bytes", shortHash(src.SourceSHA256),
				u.href("/raw/"+src.SourceSHA256, nil),
				"the delivered bytes, by content address", ToneNone)
		}
		add("twin revision", "version "+strconv.Itoa(src.Version),
			u.recordHref(model.DatasetInvoice.String(), chain.InvoiceKey),
			"profile "+src.Profile+", format "+src.Format, ToneNone)
	}
	for _, prop := range chain.Proposals {
		add("proposal", prop.ProposalID,
			u.href("/proposals", map[string]string{"q": prop.ProposalID}),
			prop.Amount.String()+", status "+prop.Status,
			toneForProposalStatus(prop.Status))
		if prop.Ack == nil {
			add("ack", "none yet", "",
				"the proposal is unacknowledged: the next run must resume it", ToneNone)
			continue
		}
		ack := prop.Ack
		if ack.IdempotencyKey != "" {
			add("idempotency key", ack.IdempotencyKey, "",
				"the key the posting was made under; a pure function of the proposal", ToneNone)
		}
		if ack.ExternalDocumentNumber != "" {
			add("ERP document number", ack.ExternalDocumentNumber, "",
				"fiscal year "+strconv.Itoa(ack.ExternalFiscalYear)+
					", posting date "+ack.ExternalPostingDate, ToneGreen)
		}
		detail := "status " + ack.Status + ", attempts " + strconv.Itoa(ack.Attempts) +
			", http " + strconv.Itoa(ack.HTTPStatus)
		if ack.IdempotencyReplay {
			detail += ", the ERP replayed a stored response"
		}
		if ack.Error != "" {
			detail += ", error " + ack.Error
		}
		add("ack", ack.Status, "", detail, ackTone(ack.Status))
		for _, c := range prop.AckConflicts {
			add("ack conflict", c.ExternalDocumentNumber, "",
				"a second ack named a different ERP document number: the double-post detector fired",
				ToneRed)
		}
	}
	for _, e := range chain.Exceptions {
		if e.State != store.ExceptionOpen {
			continue
		}
		add("open exception", e.Code,
			u.href("/exceptions", map[string]string{"code": e.Code, "state": store.ExceptionOpen}),
			e.Message, ToneRed)
	}
	return steps
}

// ackTone tones an ack status: green when the ERP posted, red when it refused.
func ackTone(status string) string {
	switch status {
	case store.AckPosted:
		return ToneGreen
	case store.AckRejected:
		return ToneRed
	default:
		return ToneNone
	}
}
