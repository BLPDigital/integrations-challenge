package web

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// fieldChangeSeq is the payload field the upstream systems order their own
// changes by. It is what makes a late delivery of an early version detectable:
// see staleOverwrites.
const fieldChangeSeq = "change_seq"

// handleRecord renders one record: its current payload and its full revision
// history with the provenance of every revision.
//
// The history is the point of the view. A reviewer must be able to see at a
// glance that an older version of a record overwrote a newer one, which is one
// of the defects this landscape hunts, so every revision carries the batch, the
// channel, the source file and line or chunk and ordinal, the source content
// address, the run and the upstream change sequence, and a revision that moved
// the change sequence backwards is chipped as such.
func (u *UI) handleRecord(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	dataset := strings.TrimSpace(r.URL.Query().Get("dataset"))
	key := normalizeKey(r.URL.Query().Get("key"))
	if dataset == "" || key == "" {
		u.fail(w, r, http.StatusBadRequest, "a record is addressed by dataset and key")
		return
	}
	if !store.ValidDataset(dataset) {
		u.fail(w, r, http.StatusNotFound, "no such dataset")
		return
	}
	st := t.Store()
	history, err := st.History(dataset, key)
	if err != nil {
		u.fail(w, r, http.StatusInternalServerError, "the history could not be read: "+err.Error())
		return
	}
	version, contentHash, seq, deleted, revisions, exists := st.Head(dataset, key)
	if !exists && len(history) == 0 {
		u.fail(w, r, http.StatusNotFound, "no record with that key in "+dataset)
		return
	}
	current, _ := st.Get(dataset, key)

	view := &ListView{}
	p := u.page(r, "Datasets", displayKey(key), dataset+" record, all revisions and their provenance", view)

	stale := staleOverwrites(history)
	if len(stale) > 0 {
		p.Notices = append(p.Notices, staleNotice(stale, history))
	}

	facts := []Row{
		{Cells: []Cell{text("dataset"), link(dataset, u.href("/datasets/"+dataset, nil))}},
		{Cells: []Cell{text("natural key"), mono(displayKey(key))}},
		{Cells: []Cell{text("version"), num(version)}},
		{Cells: []Cell{text("revisions"), num(revisions)}},
		{Cells: []Cell{text("content hash"), mono(contentHash)}},
		{Cells: []Cell{text("store sequence"), num64(seq)}},
	}
	if deleted {
		facts = append(facts, Row{Cells: []Cell{text("state"), chip("deleted", ToneRed)}})
	}
	if dataset == model.DatasetInvoice.String() {
		facts = append(facts, Row{Cells: []Cell{text("audit chain"),
			link("resolve this invoice", u.href("/audit", map[string]string{"q": displayKey(key)}))}})
	}
	view.Sections = append(view.Sections, Section{
		Title: "Record",
		Table: &Table{Headers: []string{"Fact", "Value"}, Rows: facts},
	})

	if current != nil {
		view.Sections = append(view.Sections, Section{
			Title:    "Current payload",
			Note:     "Canonical JSON: sorted keys, exact decimals as strings, no float anywhere.",
			PreLabel: "payload of version " + strconv.Itoa(version),
			Pre:      prettyPayload(current.Payload),
		})
	}

	view.Sections = append(view.Sections, Section{
		Title: "Revision history",
		Note: "Oldest first. A provenance-only row is a re-delivery of content the twin " +
			"already held: the version is untouched and the delivery is still recorded.",
		Table: u.historyTable(history, stale),
	})

	if dataset == model.DatasetInvoice.String() {
		if table := u.proposalsOfInvoice(st, key); table != nil {
			view.Sections = append(view.Sections, Section{
				Title: "Proposals of this invoice",
				Table: table,
			})
		}
		if table := u.exceptionsOfSubject(st, key); table != nil {
			view.Sections = append(view.Sections, Section{
				Title: "Exceptions about this invoice",
				Table: table,
			})
		}
	}
	u.render(w, r, "list", p)
}

// historyTable renders the revisions with their provenance, one row each.
func (u *UI) historyTable(history []store.Revision, stale map[int]bool) *Table {
	rows := make([]Row, 0, len(history))
	for i, rev := range history {
		kind, tone := "changed", ToneNone
		switch {
		case rev.ProvenanceOnly:
			kind = "provenance only"
		case rev.Version == 1:
			kind = "created"
		}
		if rev.Deleted {
			kind, tone = "deleted", ToneRed
		}
		changeSeq := ""
		if f := fieldsOf(rev.Payload); f != nil {
			changeSeq = f[fieldChangeSeq]
		}
		order := text("in order")
		if stale[i] {
			order = chip("older version overwrote a newer one", ToneRed)
		}
		prov := rev.Provenance
		source := text("")
		if prov.SourceSHA256 != "" {
			source = link(shortHash(prov.SourceSHA256), u.href("/raw/"+prov.SourceSHA256, nil))
		}
		rows = append(rows, Row{Cells: []Cell{
			num(rev.Version),
			num64(rev.Seq),
			chip(kind, tone),
			mono(changeSeq),
			order,
			mono(shortHash(rev.ContentHash)),
			mono(prov.BatchID),
			text(prov.Channel),
			mono(locator(prov)),
			source,
			mono(prov.RunID),
			text(prov.Profile),
			text(prov.Format),
			text(prov.Encoding),
			mono(scanOrSeq(prov)),
		}})
	}
	return &Table{
		Headers: []string{"v", "seq", "change", fieldChangeSeq, "order", "content hash",
			"batch", "channel", "source locator", "source sha256", "run", "profile",
			"format", "encoding", "scan / request"},
		Rows:  rows,
		Empty: "this record has no revisions",
	}
}

// scanOrSeq renders whichever arrival counter the channel has: the monotone
// inbox scan on the file channel, the request sequence on REST. Neither is a
// timestamp; there is no clock in this landscape.
func scanOrSeq(prov store.Provenance) string {
	switch {
	case prov.ReceivedScan != nil:
		return "scan " + strconv.FormatInt(*prov.ReceivedScan, 10)
	case prov.RequestSeq != nil:
		return "req " + strconv.FormatInt(*prov.RequestSeq, 10)
	default:
		return ""
	}
}

// staleOverwrites returns the indices of revisions that carry a lower upstream
// change sequence than a revision already applied: an older version of the
// record delivered after a newer one, which is a silent regression of master
// data and one of the defects the challenge hunts.
//
// The signal is the payload's own change_seq, because it is the only ordering
// the upstream system publishes. A payload without the field cannot regress and
// is never flagged; provenance-only revisions repeat content and are skipped.
func staleOverwrites(history []store.Revision) map[int]bool {
	out := map[int]bool{}
	high, seen := int64(0), false
	for i, rev := range history {
		if rev.ProvenanceOnly {
			continue
		}
		f := fieldsOf(rev.Payload)
		if f == nil {
			continue
		}
		raw, ok := f[fieldChangeSeq]
		if !ok || raw == "" {
			continue
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		if seen && v < high {
			out[i] = true
			continue
		}
		if !seen || v > high {
			high, seen = v, true
		}
	}
	return out
}

// staleNotice phrases the regression for the page header, naming the versions
// involved so the reviewer knows where to look.
func staleNotice(stale map[int]bool, history []store.Revision) string {
	idx := make([]int, 0, len(stale))
	for i := range stale {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var b strings.Builder
	b.WriteString("An older version of this record overwrote a newer one: ")
	for n, i := range idx {
		if n > 0 {
			b.WriteString(", ")
		}
		b.WriteString("version ")
		b.WriteString(strconv.Itoa(history[i].Version))
		b.WriteString(" was applied from batch ")
		b.WriteString(history[i].Provenance.BatchID)
		b.WriteString(" carrying a lower ")
		b.WriteString(fieldChangeSeq)
	}
	b.WriteString(". The store kept every revision, so the regression is visible rather than lost.")
	return b.String()
}

// proposalsOfInvoice returns the proposals of one invoice, oldest first.
func (u *UI) proposalsOfInvoice(st *store.Store, invoiceKey string) *Table {
	var out []store.Proposal
	_ = st.ScanProposals(func(p store.Proposal) bool {
		if p.InvoiceKey == invoiceKey {
			out = append(out, p)
		}
		return true
	})
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedSeq < out[j].CreatedSeq })
	rows := make([]Row, 0, len(out))
	for _, p := range out {
		rows = append(rows, Row{Cells: u.proposalCells(p)})
	}
	return &Table{Headers: proposalHeaders, Rows: rows}
}

// exceptionsOfSubject returns every exception naming one subject key.
func (u *UI) exceptionsOfSubject(st *store.Store, subject string) *Table {
	var out []store.Exception
	_ = st.ScanExceptions("", func(e store.Exception) bool {
		if e.SubjectKey == subject {
			out = append(out, e)
		}
		return true
	})
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	rows := make([]Row, 0, len(out))
	for _, e := range out {
		rows = append(rows, Row{Cells: u.exceptionCells(e, true)})
	}
	return &Table{Headers: exceptionHeaders(true), Rows: rows}
}

// handleRaw serves the delivered bytes a revision was parsed from, addressed by
// their content hash. It is the far end of the audit chain: a provenance entry
// names a sha256 and this hands back the bytes it was computed over.
func (u *UI) handleRaw(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	sha := strings.ToLower(strings.TrimSpace(r.PathValue("sha256")))
	raw, err := t.Store().GetRaw(sha)
	if err != nil {
		u.fail(w, r, http.StatusNotFound, "no raw source with that content address")
		return
	}
	hexView := r.URL.Query().Get("view") == "hex"
	shown := raw
	truncated := false
	if len(shown) > u.cfg.MaxRawBytes {
		shown, truncated = shown[:u.cfg.MaxRawBytes], true
	}
	body := ""
	if hexView {
		var b strings.Builder
		d := hex.Dumper(&b)
		_, _ = d.Write(shown)
		_ = d.Close()
		body = b.String()
	} else {
		// The delivered encoding may be CP1252, so these bytes are not
		// necessarily UTF-8. Invalid sequences become U+FFFD rather than
		// reaching a browser as broken bytes; the hex view is the byte-exact
		// one and says so.
		body = strings.ToValidUTF8(string(shown), "�")
	}
	toggle := u.href("/raw/"+sha, map[string]string{"view": "hex"})
	label := "Show the bytes as hex"
	if hexView {
		toggle = u.href("/raw/"+sha, nil)
		label = "Show the bytes as text"
	}
	facts := []Row{
		{Cells: []Cell{text("content address"), mono(sha)}},
		{Cells: []Cell{text("size"), text(group(int64(len(raw))) + " bytes")}},
		{Cells: []Cell{text("verified hash"), mono(model.Sha256Hex(raw))}},
		{Cells: []Cell{text("rendering"), link(label, toggle)}},
	}
	note := "Decoded as UTF-8 for display; invalid sequences are shown as U+FFFD. " +
		"The hex view is byte-exact."
	if hexView {
		note = "Byte-exact hex dump."
	}
	if truncated {
		note += " Truncated to the first " + group(int64(u.cfg.MaxRawBytes)) + " bytes of " +
			group(int64(len(raw))) + "."
	}
	view := &ListView{Sections: []Section{
		{Title: "Raw source", Table: &Table{Headers: []string{"Fact", "Value"}, Rows: facts}},
		{Title: "Bytes", Note: note, PreLabel: sha, Pre: body},
	}}
	p := u.page(r, "", "Raw source bytes", "The delivered bytes behind a revision, by content address", view)
	u.render(w, r, "list", p)
}

// jsonPre renders a value as indented JSON for a preformatted block. It is used
// for the receipt, which is the twin's own report and is worth showing verbatim.
func jsonPre(v any) string {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(buf)
}
