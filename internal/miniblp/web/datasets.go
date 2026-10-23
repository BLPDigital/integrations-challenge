package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// A window is one page of a key-ordered scan.
//
// Paging is by natural key rather than by offset, which is what makes it stable:
// a record inserted while a reviewer pages shifts no row onto another page, and
// the same URL always names the same window. The window also remembers the keys
// it skipped, so it can hand back the "after" of the previous page without a
// second scan.
type window struct {
	limit int
	after string
	// taken are the keys of this page, in scan order.
	taken []string
	// skipped are the keys before the page, trimmed to limit+1 from the end.
	skipped []string
	// more reports at least one further key after the page.
	more bool
}

// newWindow returns a window for a page size and an after cursor.
func newWindow(after string, limit int) *window {
	return &window{limit: limit, after: after}
}

// offer presents one key in ascending key order. It reports whether the key
// belongs to the page and whether the scan may stop.
func (wd *window) offer(key string) (take, stop bool) {
	if wd.after != "" && key <= wd.after {
		wd.skipped = append(wd.skipped, key)
		if len(wd.skipped) > wd.limit+1 {
			wd.skipped = wd.skipped[1:]
		}
		return false, false
	}
	if len(wd.taken) < wd.limit {
		wd.taken = append(wd.taken, key)
		return true, false
	}
	wd.more = true
	return false, true
}

// pager renders the window's navigation. rel and params are the view's own link
// shape, so one window serves every paginated view.
func (wd *window) pager(u *UI, rel string, params map[string]string, total int) *Pager {
	p := &Pager{}
	with := func(after string) string {
		q := map[string]string{}
		for k, v := range params {
			q[k] = v
		}
		q["after"] = after
		if wd.limit != u.cfg.PageSize {
			q["limit"] = strconv.Itoa(wd.limit)
		}
		return u.href(rel, q)
	}
	if wd.after != "" {
		p.FirstHref = with("")
		prev := ""
		if len(wd.skipped) > wd.limit {
			prev = wd.skipped[0]
		}
		p.PrevHref = with(prev)
	}
	if wd.more && len(wd.taken) > 0 {
		p.NextHref = with(wd.taken[len(wd.taken)-1])
	}
	p.Summary = wd.summary(total)
	return p
}

// summary is the human count under a paginated table.
func (wd *window) summary(total int) string {
	var b strings.Builder
	b.WriteString(group(int64(len(wd.taken))))
	b.WriteString(" rows")
	if total > 0 {
		b.WriteString(" of ")
		b.WriteString(group(int64(total)))
	}
	if wd.after != "" {
		b.WriteString(", after key ")
		b.WriteString(displayKey(wd.after))
	}
	return b.String()
}

// handleDatasets lists the datasets the twin holds, with their record counts. It
// is the entry point of the browser and the answer to "which segments exist".
func (u *UI) handleDatasets(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	st := t.Store()
	rows := make([]Row, 0, 8)
	for _, ds := range st.Datasets() {
		rows = append(rows, Row{Cells: []Cell{
			link(ds, u.href("/datasets/"+ds, nil)),
			num(st.Count(ds)),
			text(datasetNote(ds)),
		}})
	}
	view := &ListView{Sections: []Section{{
		Title: "Datasets",
		Note:  "Every segment this twin has written, with its live record count.",
		Table: &Table{
			Headers: []string{"Dataset", "Live records", "Note"},
			Rows:    rows,
			Empty:   "this twin holds no records yet",
		},
	}}}
	p := u.page(r, "Datasets", "Datasets", "Browse the twin's segments by natural key.", view)
	u.render(w, r, "list", p)
}

// datasetNote explains the two segments the store keeps for its own bookkeeping,
// which have views of their own.
func datasetNote(ds string) string {
	switch ds {
	case store.DatasetProposal:
		return "posting proposals; see the Proposals view for their ack state"
	case store.DatasetException:
		return "the exception queue; see the Exceptions view grouped by code"
	default:
		return ""
	}
}

// handleDataset browses one dataset: search by natural key, stable paging, and
// the columns that matter for that dataset.
func (u *UI) handleDataset(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	dataset := r.PathValue("dataset")
	if !store.ValidDataset(dataset) {
		u.fail(w, r, http.StatusNotFound, "no such dataset")
		return
	}
	st := t.Store()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	needle := strings.ToLower(normalizeKey(q))
	after := normalizeKey(r.URL.Query().Get("after"))
	limit := u.pageSize(r)
	wd := newWindow(after, limit)

	// One streaming pass in key order. The callback runs under the store's read
	// lock and therefore only collects: no lookup, no second store call.
	type rowData struct {
		rev    store.Revision
		fields map[string]string
	}
	var collected []rowData
	scanErr := st.Scan(dataset, func(rev store.Revision) bool {
		if needle != "" && !strings.Contains(strings.ToLower(rev.Key), needle) {
			return true
		}
		take, stop := wd.offer(rev.Key)
		if take {
			collected = append(collected, rowData{rev: rev})
		}
		return !stop
	})
	if scanErr != nil {
		u.fail(w, r, http.StatusInternalServerError, "the dataset could not be read: "+scanErr.Error())
		return
	}
	for i := range collected {
		collected[i].fields = fieldsOf(collected[i].rev.Payload)
	}

	cols := datasetColumns[dataset]
	if len(cols) == 0 && len(collected) > 0 {
		for _, name := range scalarFieldNames(collected[0].fields, 8) {
			cols = append(cols, column{Header: name, Field: name})
		}
	}
	headers := []string{"Natural key", "v"}
	for _, c := range cols {
		headers = append(headers, c.Header)
	}
	headers = append(headers, "Batch", "Source")

	rows := make([]Row, 0, len(collected))
	for _, rd := range collected {
		cells := []Cell{
			link(displayKey(rd.rev.Key), u.recordHref(dataset, rd.rev.Key)),
			num(rd.rev.Version),
		}
		for _, c := range cols {
			v := rd.fields[c.Field]
			if c.Mask {
				v = maskValue(v)
			}
			if c.Num {
				cells = append(cells, mono(v))
			} else {
				cells = append(cells, text(v))
			}
		}
		cells = append(cells, mono(rd.rev.Provenance.BatchID), mono(locator(rd.rev.Provenance)))
		rows = append(rows, Row{Cells: cells})
	}

	// The row total is the dataset's own count; under a search filter it would
	// name a population the page is not showing, so it is dropped instead.
	total := st.Count(dataset)
	if q != "" {
		total = 0
	}
	params := map[string]string{"q": q}
	view := &ListView{
		Search: &Search{
			Action: u.href("/datasets/"+dataset, nil),
			Field:  "q",
			Value:  q,
			Label:  "Search by natural key",
			Hint: "Case-insensitive substring of the natural key. A composite key is written " +
				"with a pipe, exactly as the admin surface accepts it.",
		},
		Sections: []Section{{
			Title: dataset,
			Note:  "Current revision of every live record, in natural-key byte order.",
			Table: &Table{
				Headers: headers,
				Rows:    rows,
				Empty:   "no record of this dataset matches",
			},
			Pager: wd.pager(u, "/datasets/"+dataset, params, total),
		}},
	}
	p := u.page(r, "Datasets", dataset,
		group(int64(st.Count(dataset)))+" live records in this dataset.", view)
	u.render(w, r, "list", p)
}

// recordHref addresses one record. The key travels as a query parameter, so a
// natural key containing the store's U+001F separator, a slash or a percent sign
// needs no spelling of its own.
func (u *UI) recordHref(dataset, key string) string {
	return u.href("/record", map[string]string{"dataset": dataset, "key": key})
}

// locator renders a provenance entry's position in the delivered bytes: file and
// line on the file channel, chunk and record ordinal on REST.
func locator(prov store.Provenance) string {
	switch prov.Channel {
	case store.ChannelFile:
		out := prov.SourceFile
		if prov.SourceLine != nil {
			out += ":" + strconv.FormatInt(*prov.SourceLine, 10)
		}
		if prov.RecordOrdinal != nil {
			out += " #" + strconv.FormatInt(*prov.RecordOrdinal, 10)
		}
		return out
	case store.ChannelREST:
		out := "chunk"
		if prov.ChunkOrdinal != nil {
			out += " " + strconv.FormatInt(*prov.ChunkOrdinal, 10)
		}
		if prov.RecordOrdinal != nil {
			out += " #" + strconv.FormatInt(*prov.RecordOrdinal, 10)
		}
		return out
	default:
		return prov.Channel
	}
}
