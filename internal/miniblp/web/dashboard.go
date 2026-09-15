package web

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// The funnel labels. They are the stages of BUILD-SPEC 14 and each one is a
// projection of data the twin already holds, never a counter of its own.
const (
	stageIngested  = "Ingested"
	stageMatched   = "Matched"
	stageProposed  = "Proposal pending"
	statePosted    = "Posted"
	stageException = "Open exception"
)

// handleDashboard answers the one question the dashboard exists for: did the
// data arrive, and what happened to it. Everything on it is one glance away and
// nothing on it costs a request.
func (u *UI) handleDashboard(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	st := t.Store()
	view := &ListView{}
	p := u.page(r, "Dashboard", "Digital twin", "Read-only inspection of the twin's state. Nothing on this page costs quota.", view)

	// Records per dataset.
	var tiles []Tile
	for _, ds := range datasetsWithSegments(st) {
		tiles = append(tiles, Tile{
			Label: ds,
			Value: group(int64(st.Count(ds))),
			Href:  u.href("/datasets/"+ds, nil),
		})
	}
	if len(tiles) == 0 {
		tiles = append(tiles, Tile{Label: "records", Value: "0",
			Note: "no batch has reached this twin yet"})
	}
	view.Sections = append(view.Sections, Section{
		Title: "Records per dataset",
		Note:  "Live records, tombstones excluded. Follow a dataset to browse it.",
		Tiles: tiles,
	})

	// The funnel and the proposal states, both read from the proposal and
	// exception queues in one pass each.
	funnel, proposals, exceptions := u.funnel(st)
	view.Sections = append(view.Sections, Section{
		Title: "Invoice funnel",
		Note: "Derived, not counted: matched is an invoice with at least one proposal, " +
			"posted is one with an acknowledged proposal, open exception is one named by an " +
			"open exception directly or through one of its proposals.",
		Tiles: funnel,
	})
	view.Sections = append(view.Sections, Section{
		Title: "Proposals",
		Note:  "Proposal states from the twin's own queue. proposals_pending == 0 is the headline assertion of a happy-path run.",
		Tiles: proposals,
	})
	view.Sections = append(view.Sections, Section{
		Title: "Open exceptions by code",
		Note:  "The graded set: keyed by (subject key, code), warnings deliberately absent.",
		Table: exceptions,
	})

	// The last run and the last batches, both from the twin's admin surface.
	if run, err := u.lastRun(t, r); err != nil {
		p.Notices = append(p.Notices, "The last run could not be read: "+err.Error())
	} else if run != nil {
		view.Sections = append(view.Sections, Section{
			Title: "Last run",
			Tiles: runTiles(run),
		})
	}
	if batches, err := u.batches(t, r); err != nil {
		p.Notices = append(p.Notices, "The batches could not be read: "+err.Error())
	} else {
		view.Sections = append(view.Sections, Section{
			Title: "Last batches",
			Note:  "Most recent first, with the status the receipt reports.",
			Table: u.batchTable(lastN(batches, 10)),
		})
	}

	// Requests, quota and the state digest.
	m := t.Governor().Metrics()
	view.Sections = append(view.Sections, Section{
		Title: "Requests and quota",
		Note:  "The Governor's accounting. 429s are informational; the scored budget is the request count.",
		Tiles: []Tile{
			{Label: "requests", Value: group(m.RequestsTotal)},
			{Label: "quota", Value: quotaText(m.QuotaUsed, m.QuotaLimit)},
			{Label: "429s", Value: group(m.RateLimited)},
			{Label: "5xx injected", Value: group(m.FiveXXInjected)},
			{Label: "5xx retried", Value: group(m.FiveXXRetried)},
			{Label: "records returned", Value: group(m.RecordsReturned)},
			{Label: "duplicate document attempts", Value: group(m.DuplicateDocumentAttempts),
				Tone: toneIfPositive(m.DuplicateDocumentAttempts)},
			{Label: "duplicate apply attempts", Value: group(m.DuplicateApplyAttempts),
				Tone: toneIfPositive(m.DuplicateApplyAttempts)},
			{Label: "virtual clock", Value: group(m.VirtualClockMs) + " ms"},
		},
	})
	endpoints := make([]string, 0, len(m.ByEndpoint))
	for k := range m.ByEndpoint {
		endpoints = append(endpoints, k)
	}
	sort.Strings(endpoints)
	rows := make([]Row, 0, len(endpoints))
	for _, e := range endpoints {
		rows = append(rows, Row{Cells: []Cell{text(e), num64(m.ByEndpoint[e])}})
	}
	view.Sections = append(view.Sections, Section{
		Title: "Requests by endpoint",
		Table: &Table{
			Headers: []string{"Endpoint", "Requests"},
			Rows:    rows,
			Empty:   "no accounted request has reached this twin yet",
		},
	})

	digest, err := st.Digest()
	if err != nil {
		p.Notices = append(p.Notices, "The state digest could not be computed: "+err.Error())
	}
	view.Sections = append(view.Sections, Section{
		Title: "State",
		Note:  "The digest covers natural keys and payloads only, so the same data delivered by any channel or format hashes identically.",
		Table: &Table{
			Headers: []string{"Fact", "Value"},
			Rows: []Row{
				{Cells: []Cell{text("state digest"), mono(digest)}},
				{Cells: []Cell{text("high sequence"), num64(st.HighSeq())}},
				{Cells: []Cell{text("datasets with a segment"), num(len(st.Datasets()))}},
			},
		},
	})
	u.render(w, r, "list", p)
}

// toneIfPositive marks a counter that should be zero and is not.
func toneIfPositive(n int64) string {
	if n > 0 {
		return ToneRed
	}
	return ToneNone
}

// funnel builds the invoice funnel, the proposal state tiles and the open
// exception histogram from one pass over the proposal queue and one over the
// exception queue.
//
// The funnel is a projection and says so on the page: the twin keeps no invoice
// status field, and inventing one here - a second place where an invoice's state
// is decided - is exactly the drift this interface must not introduce.
//
// Every exception says whether its subject is an invoice or a proposal, so the
// attribution needs no lookup and the pass never calls back into the store,
// which its read lock forbids anyway.
func (u *UI) funnel(st *store.Store) (funnel []Tile, proposals []Tile, exceptions *Table) {
	// The invoice count comes from the index, not from a scan: a dashboard that
	// read every invoice payload on every refresh would be the slowest page in
	// the landscape for no added truth.
	ingested := st.Count(model.DatasetInvoice.String())

	byStatus := map[string]int{}
	proposalInvoice := map[string]string{}
	matched := map[string]bool{}
	pending := map[string]bool{}
	posted := map[string]bool{}
	_ = st.ScanProposals(func(p store.Proposal) bool {
		byStatus[p.Status]++
		proposalInvoice[p.ProposalID] = p.InvoiceKey
		matched[p.InvoiceKey] = true
		switch p.Status {
		case store.ProposalPending:
			pending[p.InvoiceKey] = true
		case store.ProposalAcknowledged:
			posted[p.InvoiceKey] = true
		}
		return true
	})

	byCode := map[string]int{}
	inException := map[string]bool{}
	_ = st.ScanExceptions(store.ExceptionOpen, func(e store.Exception) bool {
		byCode[e.Code]++
		// An exception names either the invoice or one of its proposals, and
		// says which. Both count as one invoice in exception, because "how many
		// invoices are stuck" is the question the funnel answers.
		switch e.SubjectType {
		case miniblp.SubjectTypeInvoice:
			inException[e.SubjectKey] = true
		default:
			if key, ok := proposalInvoice[e.SubjectKey]; ok {
				inException[key] = true
			}
		}
		return true
	})

	funnel = []Tile{
		{Label: stageIngested, Value: group(int64(ingested)),
			Href: u.href("/datasets/"+model.DatasetInvoice.String(), nil)},
		{Label: stageMatched, Value: group(int64(len(matched)))},
		{Label: stageProposed, Value: group(int64(len(pending))),
			Href: u.href("/proposals", map[string]string{"status": store.ProposalPending})},
		{Label: statePosted, Value: group(int64(len(posted))), Tone: ToneGreen,
			Href: u.href("/proposals", map[string]string{"status": store.ProposalAcknowledged})},
		{Label: stageException, Value: group(int64(len(inException))),
			Tone: toneIfPositive(int64(len(inException))),
			Href: u.href("/exceptions", nil)},
	}

	statuses := make([]string, 0, len(byStatus))
	for s := range byStatus {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	for _, s := range statuses {
		proposals = append(proposals, Tile{
			Label: s, Value: group(int64(byStatus[s])), Tone: toneForProposalStatus(s),
			Href: u.href("/proposals", map[string]string{"status": s}),
		})
	}
	if len(proposals) == 0 {
		proposals = []Tile{{Label: "proposals", Value: "0",
			Note: "the matching engine has emitted none yet"}}
	}

	// The histogram walks the published code set first, so a code with a count
	// of zero is still visible and the set on the page is the closed set the
	// grader compares against.
	rows := make([]Row, 0, len(byCode))
	seen := map[string]bool{}
	for _, code := range miniblp.ExceptionCodes() {
		seen[code] = true
		n := byCode[code]
		tone := toneIfPositive(int64(n))
		rows = append(rows, Row{Cells: []Cell{
			link(code, u.href("/exceptions", map[string]string{"code": code, "state": store.ExceptionOpen})),
			num(n), chip(openLabel(n), tone),
		}})
	}
	extra := make([]string, 0, len(byCode))
	for code := range byCode {
		if !seen[code] {
			extra = append(extra, code)
		}
	}
	sort.Strings(extra)
	for _, code := range extra {
		rows = append(rows, Row{Cells: []Cell{
			link(code, u.href("/exceptions", map[string]string{"code": code, "state": store.ExceptionOpen})),
			num(byCode[code]), chip("unpublished code", ToneRed),
		}})
	}
	exceptions = &Table{
		Headers: []string{"Code", "Open", "State"},
		Rows:    rows,
		Empty:   "no exception code is defined",
	}
	return funnel, proposals, exceptions
}

// openLabel names a histogram row's state without repeating its number.
func openLabel(n int) string {
	if n == 0 {
		return "clear"
	}
	return "open"
}

// runTiles renders one run report.
func runTiles(run *runReport) []Tile {
	tone := ToneGreen
	if run.Status != "ok" {
		tone = ToneRed
	}
	c := run.Counts
	tiles := []Tile{
		{Label: "run", Value: run.RunID},
		{Label: "status", Value: run.Status, Tone: tone},
		{Label: "batches", Value: group(int64(len(run.Batches)))},
		{Label: "scans", Value: group(run.Scans)},
		{Label: "records seen", Value: group(int64(c.Seen))},
		{Label: "accepted", Value: group(int64(c.Accepted + c.AcceptedWithWarning)), Tone: ToneGreen},
		{Label: "rejected", Value: group(int64(c.Rejected)), Tone: toneIfPositive(int64(c.Rejected))},
		{Label: "skipped unchanged", Value: group(int64(c.SkippedUnchanged))},
		{Label: "proposals emitted", Value: group(int64(run.Proposals))},
		{Label: "exceptions opened", Value: group(int64(run.Exceptions)),
			Tone: toneIfPositive(int64(run.Exceptions))},
	}
	if run.FullLoad {
		tiles = append(tiles, Tile{Label: "full load", Value: "yes",
			Note: "a batch of this run declared a full reload"})
	}
	if len(run.Codes) > 0 {
		tiles = append(tiles, Tile{Label: "run codes",
			Value: strconv.Itoa(len(run.Codes)),
			Note:  joinCodes(sortStrings(run.Codes)), Tone: ToneRed})
	}
	return tiles
}

// joinCodes renders a code list for a tile note.
func joinCodes(codes []string) string {
	out := ""
	for i, c := range codes {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

// lastN returns the last n elements, newest first, which is the order a
// reviewer reads a batch list in.
func lastN(in []batchSummary, n int) []batchSummary {
	if len(in) > n {
		in = in[len(in)-n:]
	}
	out := make([]batchSummary, 0, len(in))
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}
