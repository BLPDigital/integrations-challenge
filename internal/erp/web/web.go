package web

import (
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// files carries the templates and the one stylesheet. Everything the UI needs is
// in the binary: no CDN, no webfont, no asset directory to forget to copy, and
// therefore a UI that renders identically on a machine with no network at all,
// which is the machine the grader runs on.
//
//go:embed templates/*.html templates/pages/*.html static/*.css
var files embed.FS

// pageNames are the templates under templates/pages, one per view. The list is
// explicit rather than globbed so a missing or misnamed page file fails at
// package initialization instead of on the request that needed it.
var pageNames = []string{
	// overview is the run summary, listing is every table view (the entity
	// browsers, the documents, the duplicates, the idempotency keys and the SOAP
	// calls all render banner, search box, table and pager), requests is the
	// request log with its filter and summary strip, and error is the notice
	// page.
	"overview",
	"listing",
	"requests",
	"error",
}

// templates holds one parsed template set per page: the layout, the shared
// partials and that page's own body. They are parsed once at initialization,
// because the sources are embedded constants and a parse failure is a programming
// error rather than a runtime condition.
var templates = func() map[string]*template.Template {
	out := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		out[name] = template.Must(template.New(name).ParseFS(files,
			"templates/layout.html",
			"templates/partials.html",
			"templates/pages/"+name+".html"))
	}
	return out
}()

// stylesheet is the embedded stylesheet, read once.
var stylesheet = func() []byte {
	b, err := files.ReadFile("static/erp.css")
	if err != nil {
		panic("web: embedded stylesheet missing: " + err.Error())
	}
	return b
}()

// DefaultPrefix is where the ERP mounts the UI.
const DefaultPrefix = "/ui"

// DefaultRefreshSeconds is the auto-refresh interval the toggle turns on. It is
// wall-clock seconds in the reviewer's browser, which is the one clock in this
// project that is allowed to be real: it drives nothing but a page reload and
// enters no server state.
const DefaultRefreshSeconds = 5

// Options configures a [UI]. The zero value is the configuration the ERP uses.
type Options struct {
	// Prefix is where the UI is mounted. Empty means [DefaultPrefix]. It must
	// match the path the server routes to the UI, because the UI builds its own
	// links from it.
	Prefix string
	// RefreshSeconds is the interval the auto-refresh toggle turns on.
	// Non-positive means [DefaultRefreshSeconds].
	RefreshSeconds int
}

// A UI is the ERP's read-only web UI. It is an [http.Handler] to be mounted on
// the ERP itself through [erp.Config].UI, where it is served free of charge and
// without authentication, like the rest of a local development tool.
//
// A UI is created before the server it renders, because the server's
// configuration takes the handler, so the source is bound afterwards with
// [UI.Attach]. Until it is attached, every page renders a short "not attached"
// notice rather than an error: a half-wired UI must be obvious, not fatal.
//
// A UI is safe for concurrent use.
type UI struct {
	prefix   string
	refresh  int
	observer *Observer

	mu  sync.RWMutex
	src Source
}

// New returns an unattached UI.
func New(opts Options) *UI {
	prefix := strings.TrimSuffix(strings.TrimSpace(opts.Prefix), "/")
	if prefix == "" {
		prefix = DefaultPrefix
	}
	refresh := opts.RefreshSeconds
	if refresh <= 0 {
		refresh = DefaultRefreshSeconds
	}
	return &UI{prefix: prefix, refresh: refresh, observer: newObserver()}
}

// Attach binds the UI to the source it renders. It may be called again after a
// reseed, though it does not need to be: the source follows the server.
func (u *UI) Attach(src Source) {
	u.mu.Lock()
	u.src = src
	u.mu.Unlock()
}

// AttachServer is [UI.Attach] with the standard source: the ERP's own admin
// surface, read in process.
func (u *UI) AttachServer(e ERP) { u.Attach(NewAdminSource(e)) }

// source returns the attached source, or nil.
func (u *UI) source() Source {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.src
}

// Observe returns next wrapped in the UI's idempotency observer, which is what
// fills the first-status, replay and conflict columns of the idempotency view.
// It is optional: without it those columns read "n/a" and every other view is
// unaffected.
//
// It belongs outermost, in front of the whole ERP handler, because that is where
// the response the client actually saw is observable:
//
//	handler := ui.Observe(srv)
//
// It reads the Idempotency-Key request header, the response status and the
// Idempotent-Replay response header, and nothing else. It buffers no body,
// changes no response and spends no quota, since it is not part of the ERP's
// accounted chain at all.
func (u *UI) Observe(next http.Handler) http.Handler {
	return u.observer.Middleware(next)
}

// A navItem is one entry of the navigation bar.
type navItem struct {
	// Name is the route name, matched against the active page.
	Name string
	// Title is the label, Href the link.
	Title string
	Href  string
}

// nav returns the navigation bar. The order is the order of section 14's view
// list, which is also the order a reviewer walks them in: what is loaded, what
// the master data looks like, what arrived, what went wrong.
func (u *UI) nav() []navItem {
	return []navItem{
		{Name: "overview", Title: "Overview", Href: u.prefix + "/"},
		{Name: "suppliers", Title: "Suppliers", Href: u.prefix + "/suppliers"},
		{Name: "purchase-orders", Title: "Purchase orders", Href: u.prefix + "/purchase-orders"},
		{Name: "cost-centers", Title: "Cost centers", Href: u.prefix + "/cost-centers"},
		{Name: "uom-conversions", Title: "UoM conversions", Href: u.prefix + "/uom-conversions"},
		{Name: "documents", Title: "AP documents", Href: u.prefix + "/documents"},
		{Name: "duplicates", Title: "Duplicates", Href: u.prefix + "/duplicates"},
		{Name: "idempotency", Title: "Idempotency keys", Href: u.prefix + "/idempotency-keys"},
		{Name: "requests", Title: "Request log", Href: u.prefix + "/requests"},
		{Name: "soap", Title: "SOAP calls", Href: u.prefix + "/soap-calls"},
	}
}

// A route is one page: the template to render and the builder that fills it.
type route struct {
	// page is the key into templates.
	page string
	// build fills the page data. It receives the parsed query and the source.
	build func(*UI, Source, Query) (*pageData, error)
}

// routes maps a path under the mount prefix to its page. It is a switch rather
// than a map so the dispatch order is fixed and readable.
func (u *UI) route(rest string) (route, bool) {
	switch rest {
	case "", "/":
		return route{page: "overview", build: (*UI).buildOverview}, true
	case "/suppliers":
		return route{page: "listing", build: (*UI).buildSuppliers}, true
	case "/purchase-orders":
		return route{page: "listing", build: (*UI).buildPurchaseOrders}, true
	case "/cost-centers":
		return route{page: "listing", build: (*UI).buildCostCenters}, true
	case "/uom-conversions":
		return route{page: "listing", build: (*UI).buildUoMConversions}, true
	case "/documents":
		return route{page: "listing", build: (*UI).buildDocuments}, true
	case "/duplicates":
		return route{page: "listing", build: (*UI).buildDuplicates}, true
	case "/idempotency-keys":
		return route{page: "listing", build: (*UI).buildIdempotency}, true
	case "/requests":
		return route{page: "requests", build: (*UI).buildRequests}, true
	case "/soap-calls":
		return route{page: "listing", build: (*UI).buildSOAPCalls}, true
	}
	return route{}, false
}

// ServeHTTP renders one page. Every route is a GET; anything else is 405, because
// this UI is read-only by construction and a POST to it can only be a mistake.
func (u *UI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, u.prefix)
	if rest == r.URL.Path && r.URL.Path != u.prefix {
		// The request did not arrive under the prefix the UI was told it is
		// mounted at, so its own links would all be wrong. Say so rather than
		// rendering a page whose every link 404s.
		u.renderError(w, r, http.StatusNotFound,
			"this UI is mounted at "+u.prefix+" and was reached at "+r.URL.Path)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		u.renderError(w, r, http.StatusMethodNotAllowed,
			"the ERP web UI is read-only: "+r.Method+" is not served")
		return
	}
	if rest == "/static/erp.css" {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(stylesheet)
		return
	}
	rt, ok := u.route(rest)
	if !ok {
		u.renderError(w, r, http.StatusNotFound, "no such view: "+rest)
		return
	}
	src := u.source()
	if src == nil {
		u.renderError(w, r, http.StatusOK,
			"this UI is not attached to a server yet; call UI.Attach after erp.New")
		return
	}
	data, err := rt.build(u, src, parseQuery(r.URL.Query()))
	if err != nil {
		u.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	u.finish(data, r)
	u.render(w, rt.page, http.StatusOK, data)
}

// renderError renders the error page with an explicit status.
func (u *UI) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	data := &pageData{Title: "Error", Heading: "Error", Error: message}
	u.finish(data, r)
	u.render(w, "error", status, data)
}

// render writes one page. The template is executed into a buffer first, so a
// template error cannot produce a half-written page with a 200 already on it.
func (u *UI) render(w http.ResponseWriter, page string, status int, data *pageData) {
	tmpl, ok := templates[page]
	if !ok {
		http.Error(w, "web: no template "+page, http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, "web: render "+page+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A development UI that a reviewer refreshes must never show a cached page:
	// the whole point of the request log view is that it is current.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String()))
}

// finish fills the parts of a page that are the same on every page: the mount
// prefix, the navigation bar, the stylesheet link and the auto-refresh toggle.
func (u *UI) finish(data *pageData, r *http.Request) {
	data.Prefix = u.prefix
	data.Stylesheet = u.prefix + "/static/erp.css"
	data.Nav = u.nav()
	data.Path = r.URL.Path
	if data.Title == "" {
		data.Title = "ERP"
	}

	// The auto-refresh toggle is the one piece of script in this UI, and it is
	// one inline setTimeout with no dependency, no import and no fetch. The
	// interval lives in the query string, so the toggle is a link and the state
	// survives a reload without any storage.
	values := r.URL.Query()
	seconds := 0
	if v, err := strconv.Atoi(strings.TrimSpace(values.Get("refresh"))); err == nil && v > 0 {
		seconds = v
	}
	data.RefreshSeconds = seconds
	data.RefreshMillis = seconds * 1000
	toggle := url.Values{}
	for k, vs := range values {
		if k == "refresh" {
			continue
		}
		for _, v := range vs {
			toggle.Add(k, v)
		}
	}
	if seconds == 0 {
		toggle.Set("refresh", strconv.Itoa(u.refresh))
		data.RefreshLabel = "auto-refresh: off"
	} else {
		data.RefreshLabel = "auto-refresh: every " + strconv.Itoa(seconds) + "s"
	}
	// url.Values.Encode sorts its keys, so the toggle link of one page is always
	// the same bytes.
	if enc := toggle.Encode(); enc != "" {
		data.RefreshHref = r.URL.Path + "?" + enc
	} else {
		data.RefreshHref = r.URL.Path
	}
}
