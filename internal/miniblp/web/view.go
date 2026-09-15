package web

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// funcs is the template function map. It is empty on purpose: every value a
// template prints is computed in Go, where it can be tested, sorted and
// formatted exactly once, and no template does arithmetic or string surgery of
// its own.
var funcs = template.FuncMap{}

// A Page is the shell every view renders into: the header with the lockup, the
// navigation, the run's identity, the auto-refresh toggle and the view's own
// Data.
type Page struct {
	// Title is the view's heading and the document title. It sits directly on
	// the page surface: the brand book forbids a fill, band or banner behind a
	// title, so the shell gives it space instead.
	Title string
	// Subtitle is one line of context under the title, unfilled for the same
	// reason.
	Subtitle string
	// Base is the mount point, the prefix of every link the page builds.
	Base string
	// LogoURL and StyleURL address the embedded lockup and stylesheet.
	LogoURL  string
	StyleURL string
	// Nav is the primary navigation, in fixed order.
	Nav []NavItem
	// Meta is the run's identity, shown small in the header: scenario, virtual
	// clock, state digest.
	Meta []Meta
	// Notices explain a degraded view, such as an admin surface the UI has no
	// token for. They are prose, never a substitute for data.
	Notices []string
	// RefreshSeconds is the auto-refresh interval, 0 when the toggle is off.
	// RefreshMs is the same interval for the one inline script.
	RefreshSeconds int
	RefreshMs      int
	// RefreshHref flips the toggle and RefreshLabel names what it will do.
	RefreshHref  string
	RefreshLabel string
	// Data is the view's own model.
	Data any
	// Status is the HTTP status the page is written with.
	Status int
}

// A NavItem is one primary navigation link.
type NavItem struct {
	// Label is the link text and Href its target.
	Label string
	Href  string
	// Active marks the view being shown, which is rendered in weight rather
	// than in color: BLP Blue is for links and buttons only.
	Active bool
}

// A Meta is one small labeled fact in the header, and the label-value pair a
// search form carries as a hidden field.
type Meta struct {
	Label string
	Value string
}

// A Tile is one number with its label: the dashboard's unit of "did the data
// arrive". Tiles sit on a Pale Blue surface, which is a surface and not a title
// band.
type Tile struct {
	// Label names the number and Value is the number itself, already formatted.
	Label string
	Value string
	// Note is an optional second line, e.g. what a funnel stage means.
	Note string
	// Href makes the tile a link to the view that explains it.
	Href string
	// Tone is "", "red" or "green": the two accent families this interface is
	// allowed, red for exceptions and rejects, green for posted and accepted.
	Tone string
}

// A Table is a rendered table. Wide ones scroll inside their own container, so
// the page body never scrolls horizontally.
type Table struct {
	// Headers are the column headings, one per cell of every row.
	Headers []string
	Rows    []Row
	// Empty is the sentence shown instead of an empty table body.
	Empty string
}

// A Row is one table row. A row is never filled or tinted: a status belongs on
// a chip, where the label carries the meaning and color is only a second
// signal.
type Row struct {
	Cells []Cell
}

// A Cell is one table cell: text, optionally a link, optionally a status chip,
// optionally monospaced because it is a hash, a key or a locator.
type Cell struct {
	// Text is the cell's value, Href makes it a link, and Chip renders it as a
	// status chip instead. Tone is "", "red" or "green".
	Text string
	Href string
	Chip string
	Tone string
	// Mono aligns the value as data: a key, a hash, an ordinal.
	Mono bool
	// Wrap allows a long message to wrap instead of widening the table.
	Wrap bool
}

// text returns a plain text cell.
func text(s string) Cell { return Cell{Text: s} }

// mono returns a monospaced cell, for keys, hashes, ids and locators.
func mono(s string) Cell { return Cell{Text: s, Mono: true} }

// link returns a linked, monospaced cell.
func link(s, href string) Cell { return Cell{Text: s, Href: href, Mono: true} }

// chip returns a status chip cell in one of the two permitted accent families.
func chip(label, tone string) Cell { return Cell{Chip: label, Tone: tone} }

// wrapped returns a cell whose long text may wrap.
func wrapped(s string) Cell { return Cell{Text: s, Wrap: true} }

// num returns a right-aligned integer cell.
func num(n int) Cell { return Cell{Text: strconv.Itoa(n), Mono: true} }

// num64 returns a right-aligned int64 cell.
func num64(n int64) Cell { return Cell{Text: strconv.FormatInt(n, 10), Mono: true} }

// A Pager is the forward and backward link pair of a paginated view.
//
// Paging is by natural key, never by offset: a page names the last key of the
// page before it, so inserting a record while a reviewer pages does not shift
// rows across page boundaries. Prev is the page of the same size ending where
// this one starts.
type Pager struct {
	// FirstHref, PrevHref and NextHref are the three navigation links, empty
	// when the page they would name does not exist.
	FirstHref string
	PrevHref  string
	NextHref  string
	// Summary is the human count, e.g. "50 of 12'000 records, page starts at ...".
	Summary string
}

// A Section is one titled block of a view. Sections are separated by whitespace
// alone: the brand book forbids divider rules between them.
type Section struct {
	Title string
	// Note is an optional explanatory line under the section title.
	Note string
	// Exactly one of Tiles, Table, Chain or Pre carries the section's content.
	Tiles []Tile
	Table *Table
	Chain []ChainStep
	// Pre is preformatted text: a canonical payload, a receipt, delivered
	// bytes. It scrolls inside its own container like a wide table does.
	Pre string
	// PreLabel names what Pre holds.
	PreLabel string
	Pager    *Pager
}

// A ChainStep is one link of the audit chain, rendered as an ordered list rather
// than a diagram: source file and line, raw bytes, twin revisions, proposal,
// idempotency key, ERP document number, ack.
type ChainStep struct {
	// Step is the position in the chain, counting from 1.
	Step int
	// Kind names the link, e.g. "source", "raw bytes", "twin revision".
	Kind string
	// Label is the value at this link.
	Label string
	// Href points at the view that shows it, "" when there is nothing to link.
	Href string
	// Detail is one line of context.
	Detail string
	// Tone marks a link that is a finding rather than a fact.
	Tone string
}

// A ListView is the model of every view that is a title, some sections and an
// optional search box.
type ListView struct {
	// Search, when non-nil, renders a GET form above the sections.
	Search *Search
	// Sections are rendered in order.
	Sections []Section
}

// A Search is one GET form: a single text field and a submit button.
type Search struct {
	// Action is the form target and Field the query parameter name.
	Action string
	Field  string
	// Value is the current query, Label the field's label, Hint one line under
	// it. Hidden carries the parameters the search must preserve.
	Value  string
	Label  string
	Hint   string
	Hidden []Meta
}

// nav returns the primary navigation with one item marked active.
func (u *UI) nav(active string) []NavItem {
	items := []NavItem{
		{Label: "Dashboard", Href: u.href("/", nil)},
		{Label: "Datasets", Href: u.href("/datasets/", nil)},
		{Label: "Batches", Href: u.href("/batches/", nil)},
		{Label: "Exceptions", Href: u.href("/exceptions", nil)},
		{Label: "Proposals", Href: u.href("/proposals", nil)},
		{Label: "Audit chain", Href: u.href("/audit", nil)},
	}
	for i := range items {
		items[i].Active = items[i].Label == active
	}
	return items
}

// page assembles the shell for one view: navigation, the run's identity from the
// Governor, and the auto-refresh toggle, whose state lives in the URL so the
// inline script has nothing to remember.
func (u *UI) page(r *http.Request, active, title, subtitle string, data any) *Page {
	refresh := intParam(r, "refresh", 0, 0, 3600)
	p := &Page{
		Title:          title,
		Subtitle:       subtitle,
		Base:           u.base,
		LogoURL:        u.base + "/static/blp-logo-full.svg",
		StyleURL:       u.base + "/static/ui.css",
		Nav:            u.nav(active),
		RefreshSeconds: refresh,
		RefreshMs:      refresh * 1000,
		Data:           data,
		Status:         http.StatusOK,
	}
	// The toggle is a link to this same view with refresh flipped, so no state
	// is kept anywhere and the inline script is one setTimeout.
	q := r.URL.Query()
	if refresh > 0 {
		q.Del("refresh")
		p.RefreshLabel = "Auto-refresh: on (" + strconv.Itoa(refresh) + "s)"
	} else {
		q.Set("refresh", strconv.Itoa(u.cfg.RefreshSeconds))
		p.RefreshLabel = "Auto-refresh: off"
	}
	target := r.URL.Path
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	p.RefreshHref = target
	if t := u.bound(); t != nil {
		m := t.Governor().Metrics()
		p.Meta = []Meta{
			{Label: "virtual clock", Value: strconv.FormatInt(m.VirtualClockMs, 10) + " ms"},
			{Label: "requests", Value: strconv.FormatInt(m.RequestsTotal, 10)},
			{Label: "quota", Value: quotaText(m.QuotaUsed, m.QuotaLimit)},
		}
	}
	return p
}

// quotaText renders the quota as used of limit, or as used with no limit.
func quotaText(used, limit int64) string {
	if limit <= 0 {
		used := strconv.FormatInt(used, 10)
		return used + " of unlimited"
	}
	return strconv.FormatInt(used, 10) + " of " + strconv.FormatInt(limit, 10)
}

// render writes one page. The template runs into a buffer first, so a template
// error cannot leave a half-written 200 on the wire and the response can carry
// its own length.
func (u *UI) render(w http.ResponseWriter, r *http.Request, name string, p *Page) {
	tpl, ok := u.pages[name]
	if !ok {
		http.Error(w, "web: no such page template: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "shell", p); err != nil {
		http.Error(w, "web: rendering "+name+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	status := p.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	// A view is a live report on a mutable twin: a cached copy of it is a lie.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

// fail renders the error page with a status. It is a page and not a bare status
// because the reviewer who lands on it still wants the navigation.
func (u *UI) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	p := u.page(r, "", http.StatusText(status), message, nil)
	p.Status = status
	u.render(w, r, "error", p)
}

// displayKey renders a natural key for a human. The store's key separator is
// U+001F, which no terminal and no browser shows, so it becomes the pipe the
// admin surface already accepts as its friendly spelling.
func displayKey(key string) string {
	return strings.ReplaceAll(key, model.KeySeparator, "|")
}

// normalizeKey is displayKey's inverse: it accepts a key as the store holds it,
// as the UI displays it, or as a reviewer pasted it out of a receipt. A key that
// already carries a separator is left alone, exactly as the admin surface does,
// so a natural key that genuinely contains a pipe stays addressable in its raw
// spelling.
func normalizeKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, model.KeySeparator) {
		return raw
	}
	return strings.ReplaceAll(raw, "|", model.KeySeparator)
}

// shortHash abbreviates a content address for a table cell. The full value is
// always one click away on the detail view, and a 64-character column would
// push every other column off the page.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12] + "…"
}

// group renders a thousands separator the Swiss way, which is what every other
// number in this landscape uses.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('\'')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
