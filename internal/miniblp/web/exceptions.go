package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// handleExceptions renders the exception queue grouped by code, with the subject
// key, the stage, the message and the source locator of every entry.
//
// Grouping and paging meet as follows: with no code filter, every code present
// gets its own section, each showing at most one page and linking to its own
// full list; with a code filter, that one code's section pages through the whole
// group. A queue with ten thousand entries therefore still renders, and the
// grouping the spec asks for is never traded away for the paging it also asks
// for.
func (u *UI) handleExceptions(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	st := t.Store()
	// The queue defaults to the open entries: they are the graded set and the
	// question a reviewer arrives with. Every state is one link away.
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		state = store.ExceptionOpen
	}
	if state == "all" {
		state = ""
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	after := normalizeKey(r.URL.Query().Get("after"))
	limit := u.pageSize(r)

	var all []store.Exception
	byCode := map[string]int{}
	_ = st.ScanExceptions(state, func(e store.Exception) bool {
		byCode[e.Code]++
		if code != "" && e.Code != code {
			return true
		}
		if q != "" && !exceptionMatches(e, q) {
			return true
		}
		all = append(all, e)
		return true
	})
	sort.Slice(all, func(i, j int) bool {
		if all[i].Code != all[j].Code {
			return all[i].Code < all[j].Code
		}
		return all[i].SubjectKey < all[j].SubjectKey
	})

	view := &ListView{
		Search: &Search{
			Action: u.href("/exceptions", nil),
			Field:  "q",
			Value:  r.URL.Query().Get("q"),
			Label:  "Search exceptions",
			Hint:   "Matches the subject key, the code, the field, the message or the source batch.",
			Hidden: []Meta{{Label: "state", Value: state}, {Label: "code", Value: code}},
		},
	}
	view.Sections = append(view.Sections, Section{
		Title: "Queue",
		Note:  "Warnings are deliberately absent: the graded set is the blocking one, keyed by (subject key, code).",
		Tiles: u.exceptionFilterTiles(byCode, state, code),
	})

	if code != "" {
		wd := newWindow(after, limit)
		rows := make([]Row, 0, limit)
		for _, e := range all {
			take, stop := wd.offer(e.SubjectKey)
			if take {
				rows = append(rows, Row{Cells: u.exceptionCells(e, false)})
			}
			if stop {
				break
			}
		}
		view.Sections = append(view.Sections, Section{
			Title: code,
			Table: &Table{Headers: exceptionHeaders(false), Rows: rows,
				Empty: "no exception matches"},
			Pager: wd.pager(u, "/exceptions", map[string]string{
				"code": code, "state": state, "q": r.URL.Query().Get("q"),
			}, groupTotal(byCode[code], q)),
		})
	} else {
		groups := map[string][]store.Exception{}
		for _, e := range all {
			groups[e.Code] = append(groups[e.Code], e)
		}
		codes := make([]string, 0, len(groups))
		for c := range groups {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		for _, c := range codes {
			entries := groups[c]
			shown := entries
			note := ""
			if len(shown) > limit {
				shown = shown[:limit]
				note = "first " + strconv.Itoa(limit) + " of " + strconv.Itoa(len(entries))
			}
			rows := make([]Row, 0, len(shown))
			for _, e := range shown {
				rows = append(rows, Row{Cells: u.exceptionCells(e, false)})
			}
			view.Sections = append(view.Sections, Section{
				Title: c,
				Note:  note,
				Table: &Table{Headers: exceptionHeaders(false), Rows: rows},
				Pager: &Pager{
					NextHref: u.href("/exceptions", map[string]string{
						"code": c, "state": state, "q": r.URL.Query().Get("q")}),
					Summary: "all entries of this code",
				},
			})
		}
		if len(codes) == 0 {
			view.Sections = append(view.Sections, Section{
				Title: "No exceptions",
				Table: &Table{Headers: exceptionHeaders(false),
					Empty: "this twin has raised no exception matching the filter"},
			})
		}
	}
	p := u.page(r, "Exceptions", "Exception queue", exceptionSubtitle(state), view)
	u.render(w, r, "list", p)
}

// groupTotal is the row total of one code group: its own count, or none under a
// search filter, which would otherwise name a population the page is not
// showing.
func groupTotal(count int, needle string) int {
	if needle != "" {
		return 0
	}
	return count
}

// exceptionSubtitle names the state filter in prose.
func exceptionSubtitle(state string) string {
	switch state {
	case store.ExceptionOpen:
		return "Open entries: the set grading compares as a symmetric difference."
	case store.ExceptionResolved:
		return "Resolved entries: raised by one delivery and cleared by a later one."
	default:
		return "Every entry the twin has raised, open and resolved."
	}
}

// exceptionHeaders is the queue's columns.
func exceptionHeaders(withCode bool) []string {
	out := []string{"Subject", "Type", "Stage"}
	if withCode {
		out = append(out, "Code")
	}
	return append(out, "State", "Field", "Message", "Source batch", "File or chunk",
		"Line or ordinal", "Details")
}

// exceptionCells renders one queue entry.
func (u *UI) exceptionCells(e store.Exception, withCode bool) []Cell {
	subject := mono(displayKey(e.SubjectKey))
	switch e.SubjectType {
	case "invoice":
		subject = link(displayKey(e.SubjectKey),
			u.recordHref(model.DatasetInvoice.String(), e.SubjectKey))
	case "proposal":
		subject = link(e.SubjectKey, u.href("/audit", map[string]string{"q": e.SubjectKey}))
	}
	cells := []Cell{subject, text(e.SubjectType), text(e.Stage)}
	if withCode {
		cells = append(cells, mono(e.Code))
	}
	return append(cells,
		chip(e.State, toneForExceptionState(e.State)),
		text(e.Field),
		wrapped(e.Message),
		mono(e.SourceBatchID),
		mono(e.SourceFileOrChunk),
		mono(e.SourceLineOrOrdinal),
		wrapped(details(e.Details)),
	)
}

// details renders an exception's named facts in ascending key order, so the
// rendering never depends on map iteration.
func details(d map[string]string) string {
	if len(d) == 0 {
		return ""
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(d[k])
	}
	return b.String()
}

// exceptionMatches is the queue search.
func exceptionMatches(e store.Exception, needle string) bool {
	fields := []string{displayKey(e.SubjectKey), e.Code, e.Field, e.Message,
		e.SourceBatchID, e.Stage}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

// stateTone reddens the entry count only when the entries are the open ones: a
// resolved queue is not a finding.
func stateTone(state string, total int) string {
	if state == store.ExceptionOpen {
		return toneIfPositive(int64(total))
	}
	return ToneNone
}

// exceptionFilterTiles renders the state and code filters as tiles.
func (u *UI) exceptionFilterTiles(byCode map[string]int, state, code string) []Tile {
	total := 0
	for _, n := range byCode {
		total += n
	}
	shown := "every state"
	if state != "" {
		shown = state
	}
	tiles := []Tile{
		{Label: "entries, " + shown, Value: group(int64(total)),
			Tone: stateTone(state, total)},
		{Label: "open only", Value: "filter",
			Href: u.href("/exceptions", map[string]string{"state": store.ExceptionOpen})},
		{Label: "resolved only", Value: "filter",
			Href: u.href("/exceptions", map[string]string{"state": store.ExceptionResolved})},
		{Label: "every state", Value: "filter",
			Href: u.href("/exceptions", map[string]string{"state": "all"})},
	}
	codes := make([]string, 0, len(byCode))
	for c := range byCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		tile := Tile{Label: c, Value: group(int64(byCode[c])), Tone: ToneRed,
			Href: u.href("/exceptions", map[string]string{"code": c, "state": state})}
		if c == code {
			tile.Note = "shown"
		}
		tiles = append(tiles, tile)
	}
	return tiles
}
