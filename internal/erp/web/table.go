package web

import (
	"net/url"
	"strconv"
	"strings"
)

// Cell classes. They are the only values that ever reach a class attribute, and
// they are constants so no rendered class can come from data.
const (
	// cellNum right-aligns a numeric cell.
	cellNum = "num"
	// cellWarn marks a cell that carries a genuine warning: in this UI that is
	// a duplicate posting attempt and a returned fault, and nothing else.
	cellWarn = "warn"
	// cellMuted marks a cell whose value is absent or not applicable.
	cellMuted = "muted"
	// cellKey marks the natural key of a row, so a reviewer's eye finds the
	// column it searched by.
	cellKey = "key"
)

// A Column is one table heading.
type Column struct {
	// Title is the heading text.
	Title string
	// Class is a cell class applied to the heading, so a numeric column's
	// heading is right-aligned like its cells.
	Class string
}

// A Cell is one table cell: text, already stringified, plus one of the cell
// classes. The text is rendered through html/template and is therefore escaped;
// the class comes from a constant in this package and never from data.
type Cell struct {
	// Text is the rendered value, Class one of the cell classes above.
	Text  string
	Class string
}

// A Row is one table row. Flagged rows are the ones a reviewer must not be able
// to miss: in this UI, an external reference posted more than once.
type Row struct {
	// Cells are the row's cells in column order, Flagged marks the row as a
	// warning.
	Cells   []Cell
	Flagged bool
}

// A Table is a rendered table: headings, rows and the message to show instead of
// an empty body.
type Table struct {
	// Columns are the headings, Rows the body in the order it is to be read.
	Columns []Column
	Rows    []Row
	// Empty is the text shown when there are no rows at all.
	Empty string
	// Note is an optional line under the table explaining what it does and does
	// not show.
	Note string
}

// num returns a right-aligned cell holding an integer.
func num(v int64) Cell { return Cell{Text: strconv.FormatInt(v, 10), Class: cellNum} }

// text returns a plain cell.
func text(s string) Cell { return Cell{Text: s} }

// keyCell returns a cell marked as the row's natural key.
func keyCell(s string) Cell { return Cell{Text: s, Class: cellKey} }

// dash returns a muted placeholder cell for a value that is absent. It is a
// visible em-space dash rather than an empty cell, so an absent value cannot be
// confused with a rendering bug.
func dash() Cell { return Cell{Text: "-", Class: cellMuted} }

// flag returns a cell in the warning class.
func flag(s string) Cell { return Cell{Text: s, Class: cellWarn} }

// yesNo renders a boolean as "yes" or "no". A true value is only ever a warning
// where the caller says so, because "blocked: yes" on a supplier is master data
// and not a problem.
func yesNo(v bool) Cell {
	if v {
		return Cell{Text: "yes"}
	}
	return Cell{Text: "no", Class: cellMuted}
}

// A Query is the parsed state of an entity browser: the search term and the page
// window. Paging is by offset over a collection that is already in a total,
// deterministic order, so a page is stable: the same query against the same
// dataset always renders the same rows.
type Query struct {
	// Q is the trimmed search term. An empty term matches everything.
	Q string
	// Offset is the zero-based index of the first row shown.
	Offset int
	// Limit is the page size.
	Limit int
	// Key selects a single record for a detail view, where the browser has one.
	Key string
	// Endpoint and Class are the request log's two filters.
	Endpoint string
	Class    string
}

// Paging bounds. A page size is capped so that a hand-edited limit cannot ask the
// UI to render a hundred thousand rows into one response.
const (
	// defaultPageSize is the page size a browser gets when it asks for none.
	defaultPageSize = 50
	// maxPageSize is the largest page the UI renders.
	maxPageSize = 500
)

// parseQuery reads the browser state out of a URL query. Every value is clamped
// rather than rejected: this is a read-only development UI, and a hand-edited
// offset should show the nearest sensible page, not an error page.
func parseQuery(values url.Values) Query {
	q := Query{
		Q:        strings.TrimSpace(values.Get("q")),
		Key:      strings.TrimSpace(values.Get("key")),
		Endpoint: strings.TrimSpace(values.Get("endpoint")),
		Class:    strings.TrimSpace(values.Get("class")),
		Limit:    defaultPageSize,
	}
	if v, err := strconv.Atoi(strings.TrimSpace(values.Get("limit"))); err == nil && v > 0 {
		q.Limit = v
	}
	if q.Limit > maxPageSize {
		q.Limit = maxPageSize
	}
	if v, err := strconv.Atoi(strings.TrimSpace(values.Get("offset"))); err == nil && v > 0 {
		q.Offset = v
	}
	return q
}

// A Pager is the paging strip under a table: where the window sits in the
// collection and the links that move it. The links are relative URLs built from
// the page's own path, so the UI works under any mount prefix and needs no
// absolute origin.
type Pager struct {
	// Total is the number of rows after filtering, Shown the number on this
	// page.
	Total int
	Shown int
	// From and To are the one-based inclusive bounds of the window, both zero
	// when the window is empty.
	From int
	To   int
	// Page and Pages are the one-based page number and the page count.
	Page  int
	Pages int
	// FirstHref, PrevHref and NextHref are empty when the link does not apply.
	FirstHref string
	PrevHref  string
	NextHref  string
}

// window clamps q's offset to total and returns the half-open row window.
func (q Query) window(total int) (from, to int) {
	from = q.Offset
	if from > total {
		from = total
	}
	if from < 0 {
		from = 0
	}
	to = from + q.Limit
	if to > total {
		to = total
	}
	return from, to
}

// pager builds the paging strip for a window over total rows. path is the page's
// own path including the mount prefix, and extra carries the filter parameters
// that must survive a page change.
func (q Query) pager(path string, total, from, to int) Pager {
	p := Pager{Total: total, Shown: to - from, Page: 1, Pages: 1}
	if to > from {
		p.From, p.To = from+1, to
	}
	if q.Limit > 0 {
		p.Page = from/q.Limit + 1
		p.Pages = (total + q.Limit - 1) / q.Limit
		if p.Pages < 1 {
			p.Pages = 1
		}
	}
	href := func(offset int) string {
		v := url.Values{}
		if q.Q != "" {
			v.Set("q", q.Q)
		}
		if q.Key != "" {
			v.Set("key", q.Key)
		}
		if q.Endpoint != "" {
			v.Set("endpoint", q.Endpoint)
		}
		if q.Class != "" {
			v.Set("class", q.Class)
		}
		if q.Limit != defaultPageSize {
			v.Set("limit", strconv.Itoa(q.Limit))
		}
		if offset > 0 {
			v.Set("offset", strconv.Itoa(offset))
		}
		// url.Values.Encode sorts its keys, so the same query always renders
		// the same link.
		if enc := v.Encode(); enc != "" {
			return path + "?" + enc
		}
		return path
	}
	if from > 0 {
		p.FirstHref = href(0)
		prev := from - q.Limit
		if prev < 0 {
			prev = 0
		}
		p.PrevHref = href(prev)
	}
	if to < total {
		p.NextHref = href(to)
	}
	return p
}

// matches reports whether a row's searchable fields contain the search term,
// compared case-insensitively.
//
// The fold is Go's own simple Unicode lower-casing, which depends on no locale
// and no environment: the same term against the same data matches the same rows
// on every machine.
func (q Query) matches(fields ...string) bool {
	if q.Q == "" {
		return true
	}
	needle := strings.ToLower(q.Q)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

// maskIBAN returns an IBAN with its middle replaced by asterisks, keeping the
// first four and the last four characters, so the record stays recognizable
// without the payment detail being on screen.
//
// The build specification's data minimization rule (section 16.1, refined by
// 17.7) binds candidate-produced artifacts rather than our own screens, and
// masking is named there as preferred over omission. A reviewer's browser
// history, screen share and screenshot are the reason to apply the same rule
// here: nothing is lost, because the supplier number is the key and the IBAN is
// never one.
func maskIBAN(iban string) string {
	s := strings.TrimSpace(iban)
	if len(s) <= 8 {
		return s
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}
