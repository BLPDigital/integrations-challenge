package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// proposalHeaders is the proposal list's columns, BUILD-SPEC 14: id, invoice,
// amount, status, attempts, idempotency key and ERP document number, plus the
// exchange rate the amount was computed with, because a rate and a factor read
// as one number is a hundredfold posting error.
var proposalHeaders = []string{
	"Proposal", "Invoice", "inv v", "Status", "Amount", "Source amount",
	"FX rate", "Factor", "Attempts", "Idempotency key", "ERP document", "Warnings",
}

// proposalCells renders one proposal row.
func (u *UI) proposalCells(p store.Proposal) []Cell {
	rate, factor := "", ""
	if p.FxRateFactor != 0 {
		rate = p.FxRateUsed.String()
		factor = strconv.FormatInt(p.FxRateFactor, 10)
	}
	idem, doc := "", ""
	if p.Ack != nil {
		idem, doc = p.Ack.IdempotencyKey, p.Ack.ExternalDocumentNumber
	}
	docCell := text("")
	if doc != "" {
		docCell = link(doc, u.href("/audit", map[string]string{"q": doc}))
	}
	warn := text("")
	if len(p.Warnings) > 0 {
		warn = wrapped(joinCodes(sortStrings(p.Warnings)))
	}
	return []Cell{
		link(p.ProposalID, u.href("/audit", map[string]string{"q": p.ProposalID})),
		link(displayKey(p.InvoiceKey), u.recordHref(model.DatasetInvoice.String(), p.InvoiceKey)),
		num(p.InvoiceVersion),
		chip(p.Status, toneForProposalStatus(p.Status)),
		mono(p.Amount.String()),
		mono(p.SourceAmount.String()),
		mono(rate),
		mono(factor),
		num(p.Attempts),
		mono(idem),
		docCell,
		warn,
	}
}

// handleProposals lists the proposals with their ack state, in created_seq
// order, filterable by status and searchable by id, invoice key, idempotency key
// or ERP document number.
func (u *UI) handleProposals(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	st := t.Store()
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	limit := u.pageSize(r)

	var all []store.Proposal
	counts := map[string]int{}
	_ = st.ScanProposals(func(p store.Proposal) bool {
		counts[p.Status]++
		if status != "" && p.Status != status {
			return true
		}
		if q != "" && !proposalMatches(p, q) {
			return true
		}
		all = append(all, p)
		return true
	})
	// The store scans proposals in key order and the ids are zero-padded, so
	// this sort is a no-op on a healthy store. It stays because the page's
	// paging contract is "ascending id" and a view must not inherit an order it
	// did not state.
	sort.Slice(all, func(i, j int) bool { return all[i].ProposalID < all[j].ProposalID })

	wd := newWindow(after, limit)
	rows := make([]Row, 0, limit)
	for _, p := range all {
		take, stop := wd.offer(p.ProposalID)
		if take {
			rows = append(rows, Row{Cells: u.proposalCells(p)})
		}
		if stop {
			break
		}
	}

	view := &ListView{
		Search: &Search{
			Action: u.href("/proposals", nil),
			Field:  "q",
			Value:  r.URL.Query().Get("q"),
			Label:  "Search proposals",
			Hint:   "Matches the proposal id, the invoice key, the idempotency key or the ERP document number.",
			Hidden: []Meta{{Label: "status", Value: status}},
		},
	}
	view.Sections = append(view.Sections, Section{
		Title: "Proposal states",
		Note:  "Follow a state to filter the list. pending is what the next run must resume.",
		Tiles: u.proposalStateTiles(counts, status),
	})
	view.Sections = append(view.Sections, Section{
		Title: proposalTitle(status),
		Table: &Table{
			Headers: proposalHeaders,
			Rows:    rows,
			Empty:   "no proposal matches",
		},
		Pager: wd.pager(u, "/proposals", map[string]string{
			"status": status, "q": r.URL.Query().Get("q"),
		}, len(all)),
	})
	p := u.page(r, "Proposals", "Proposals",
		"Immutable posting proposals and the acks that closed them.", view)
	u.render(w, r, "list", p)
}

// proposalMatches is the proposal search: the four identifiers a reviewer has in
// hand when they arrive at this page.
func proposalMatches(p store.Proposal, needle string) bool {
	if strings.Contains(strings.ToLower(p.ProposalID), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(displayKey(p.InvoiceKey)), needle) {
		return true
	}
	if p.Ack == nil {
		return false
	}
	return strings.Contains(strings.ToLower(p.Ack.IdempotencyKey), needle) ||
		strings.Contains(strings.ToLower(p.Ack.ExternalDocumentNumber), needle)
}

// proposalStateTiles renders the state filter as tiles, the active one marked.
func (u *UI) proposalStateTiles(counts map[string]int, active string) []Tile {
	statuses := []string{
		store.ProposalPending, store.ProposalAcknowledged, store.ProposalRejected,
		store.ProposalNeedsInvestigation, store.ProposalSuperseded,
	}
	extra := make([]string, 0, len(counts))
	for s := range counts {
		if !containsString(statuses, s) {
			extra = append(extra, s)
		}
	}
	sort.Strings(extra)
	statuses = append(statuses, extra...)

	total := 0
	for _, n := range counts {
		total += n
	}
	tiles := []Tile{{Label: "all", Value: group(int64(total)), Href: u.href("/proposals", nil)}}
	if active == "" {
		tiles[0].Note = "shown"
	}
	for _, s := range statuses {
		tile := Tile{
			Label: s, Value: group(int64(counts[s])), Tone: toneForProposalStatus(s),
			Href: u.href("/proposals", map[string]string{"status": s}),
		}
		if s == active {
			tile.Note = "shown"
		}
		tiles = append(tiles, tile)
	}
	return tiles
}

// proposalTitle names the filtered list.
func proposalTitle(status string) string {
	if status == "" {
		return "All proposals"
	}
	return "Proposals: " + status
}

// containsString reports membership.
func containsString(in []string, s string) bool {
	for _, v := range in {
		if v == s {
			return true
		}
	}
	return false
}
