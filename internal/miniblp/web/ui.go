package web

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// assets holds every byte the UI serves: the page templates, the one stylesheet
// and the official SVG lockup. They are embedded so the interface works from the
// binary alone, with no network, no CDN, no webfont fetch and no file tree next
// to the executable.
//
//go:embed templates/*.gohtml static/*.svg static/*.css
var assets embed.FS

// Twin is the read-only view of a running digital twin that a [UI] renders.
// *miniblp.Server satisfies it as it stands.
//
// The interface is deliberately three methods wide, because a UI that could
// reach further could become a second source of truth. Store and Governor are
// the twin's own state; Handler is the twin's HTTP handler, which the UI uses
// for one purpose only - GET requests against the free /admin/v1 surface, for
// the batch, receipt and run reports the Server holds in memory.
type Twin interface {
	// Store returns the revision store, which is safe for concurrent use.
	Store() *store.Store
	// Governor returns the request Governor, whose Metrics the dashboard reports.
	Governor() *simclock.Governor
	// Handler returns the twin's HTTP handler, whose /admin/v1 routes are free
	// of quota, of virtual time and of fault injection.
	Handler() http.Handler
}

// Defaults of the optional [Config] fields.
const (
	// DefaultBasePath is where BUILD-SPEC 0.2 mounts the interface.
	DefaultBasePath = "/ui"
	// DefaultPageSize is the row count of one page of a paginated view.
	DefaultPageSize = 50
	// MaxPageSize caps the ?limit= parameter, so a hand-edited URL cannot ask
	// the twin to render a hundred thousand rows.
	MaxPageSize = 500
	// DefaultRefreshSeconds is the auto-refresh interval the toggle turns on.
	DefaultRefreshSeconds = 5
	// DefaultMaxRawBytes is how much of a raw source file the raw-bytes view
	// renders before it truncates. A KRED-EXP file is up to 20 MiB and a browser
	// is not a pager.
	DefaultMaxRawBytes = 64 << 10
)

// Config configures a [UI]. Every field is optional: the zero value renders the
// twin bound by [UI.Bind] at /ui with the documented defaults, and an empty
// AdminToken degrades exactly the two views that need the admin surface.
type Config struct {
	// Twin is the twin to render. It may be nil at construction and supplied
	// later with [UI.Bind], which is what the wiring order of miniblp.Config
	// requires.
	Twin Twin
	// AdminToken is the token the twin's admin surface requires. Without it the
	// batch, receipt and audit views render an explanatory notice instead of
	// data, because the alternative - the UI reconstructing batch state of its
	// own - would be a second source of truth.
	AdminToken string
	// BasePath is where the interface is mounted, [DefaultBasePath] when empty.
	BasePath string
	// PageSize is the default rows per page, [DefaultPageSize] when zero.
	PageSize int
	// RefreshSeconds is the interval the auto-refresh toggle turns on,
	// [DefaultRefreshSeconds] when zero.
	RefreshSeconds int
	// MaxRawBytes caps the raw-bytes view, [DefaultMaxRawBytes] when zero.
	MaxRawBytes int
}

// A UI is the twin's read-only inspection interface: an http.Handler serving the
// seven views of BUILD-SPEC 14 under [Config.BasePath].
//
// It is safe for concurrent use. The only mutable state is the bound twin, which
// [UI.Bind] replaces under a lock.
type UI struct {
	cfg   Config
	base  string
	pages map[string]*template.Template
	mux   *http.ServeMux

	mu   sync.RWMutex
	twin Twin
}

// New parses the embedded templates and returns the interface. It fails only on
// a broken template, which is a programming error and is therefore worth
// failing at startup rather than on the first request.
func New(cfg Config) (*UI, error) {
	if cfg.BasePath == "" {
		cfg.BasePath = DefaultBasePath
	}
	cfg.BasePath = "/" + strings.Trim(cfg.BasePath, "/")
	if cfg.PageSize <= 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.PageSize > MaxPageSize {
		cfg.PageSize = MaxPageSize
	}
	if cfg.RefreshSeconds <= 0 {
		cfg.RefreshSeconds = DefaultRefreshSeconds
	}
	if cfg.MaxRawBytes <= 0 {
		cfg.MaxRawBytes = DefaultMaxRawBytes
	}
	u := &UI{cfg: cfg, base: cfg.BasePath, twin: cfg.Twin}
	pages, err := parsePages()
	if err != nil {
		return nil, err
	}
	u.pages = pages
	u.mux = u.routes()
	return u, nil
}

// Bind attaches, or replaces, the twin the interface renders.
//
// It exists because miniblp.Config carries the UI handler while the UI needs the
// Server that miniblp.New builds from that Config: one of the two has to be
// wired after the other exists. An unbound UI answers 503 with a page saying so,
// rather than panicking on a nil twin.
func (u *UI) Bind(t Twin) {
	u.mu.Lock()
	u.twin = t
	u.mu.Unlock()
}

// BasePath returns where the interface is mounted.
func (u *UI) BasePath() string { return u.base }

// ServeHTTP implements http.Handler.
func (u *UI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		u.fail(w, r, http.StatusMethodNotAllowed,
			"this interface is read-only: every view is a GET")
		return
	}
	// The mount point without its trailing slash is the dashboard, so /ui and
	// /ui/ are the same page rather than a redirect a reviewer has to follow.
	if r.URL.Path == u.base {
		r2 := r.Clone(r.Context())
		r2.URL.Path = u.base + "/"
		u.mux.ServeHTTP(w, r2)
		return
	}
	u.mux.ServeHTTP(w, r)
}

// routes registers every view. The patterns are built from the base path so the
// mount point stays configurable, and the mux is built once.
func (u *UI) routes() *http.ServeMux {
	mux := http.NewServeMux()
	b := u.base
	mux.HandleFunc("GET "+b+"/{$}", u.handleDashboard)
	mux.HandleFunc("GET "+b+"/datasets/{$}", u.handleDatasets)
	mux.HandleFunc("GET "+b+"/datasets/{dataset}", u.handleDataset)
	mux.HandleFunc("GET "+b+"/record", u.handleRecord)
	mux.HandleFunc("GET "+b+"/raw/{sha256}", u.handleRaw)
	mux.HandleFunc("GET "+b+"/batches/{$}", u.handleBatches)
	mux.HandleFunc("GET "+b+"/batches/{id}", u.handleBatch)
	mux.HandleFunc("GET "+b+"/exceptions", u.handleExceptions)
	mux.HandleFunc("GET "+b+"/proposals", u.handleProposals)
	mux.HandleFunc("GET "+b+"/audit", u.handleAudit)
	mux.HandleFunc("GET "+b+"/static/{file}", u.handleStatic)
	mux.HandleFunc("GET "+b+"/", u.handleNotFound)
	return mux
}

// mediaTypes maps the extensions of the embedded static files to their media
// type. The mapping is explicit rather than mime.TypeByExtension, whose answer
// depends on the host's /etc/mime.types and is therefore not deterministic.
var mediaTypes = map[string]string{
	".svg": "image/svg+xml",
	".css": "text/css; charset=utf-8",
}

// handleStatic serves one embedded asset by name. It is a lookup rather than a
// file server: there is no directory listing, no traversal and no path cleaning
// to reason about, because {file} is a single path segment and the FS is
// read-only and compiled in.
func (u *UI) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	ct, ok := mediaTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		u.fail(w, r, http.StatusNotFound, "no such asset")
		return
	}
	body, err := assets.ReadFile("static/" + name)
	if err != nil {
		u.fail(w, r, http.StatusNotFound, "no such asset")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// handleNotFound answers an unknown path under the base with a rendered page
// rather than a bare status, because the reviewer who mistyped a URL still wants
// the navigation.
func (u *UI) handleNotFound(w http.ResponseWriter, r *http.Request) {
	u.fail(w, r, http.StatusNotFound, "no such view")
}

// bound returns the bound twin, or nil.
func (u *UI) bound() Twin {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.twin
}

// errUnbound is what every view reports when no twin is bound yet.
var errUnbound = errors.New("web: no twin is bound to this interface")

// twinOrFail returns the bound twin, or writes the unbound page and returns nil.
func (u *UI) twinOrFail(w http.ResponseWriter, r *http.Request) Twin {
	t := u.bound()
	if t == nil {
		u.fail(w, r, http.StatusServiceUnavailable, errUnbound.Error())
		return nil
	}
	return t
}

// query parsing helpers. Every one of them is total: a malformed parameter falls
// back to its default instead of failing the page, because a reviewer editing a
// URL by hand should get the view, not an error body.

// intParam returns a bounded integer parameter.
func intParam(r *http.Request, name string, def, min, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(name)))
	if err != nil || v < min {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// pageSize returns the ?limit= of a paginated view.
func (u *UI) pageSize(r *http.Request) int {
	return intParam(r, "limit", u.cfg.PageSize, 1, MaxPageSize)
}

// href builds a URL under the base path with the given query parameters. Empty
// values are dropped, so a link never carries q= or after= it does not need, and
// the keys are sorted by url.Values.Encode, so the same logical link is always
// the same bytes.
func (u *UI) href(rel string, params map[string]string) string {
	q := url.Values{}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	out := u.base + rel
	if len(q) > 0 {
		out += "?" + q.Encode()
	}
	return out
}

// parsePages parses one template set per page: the shared shell and partials,
// cloned, plus that page's own "content" definition. One set per page rather
// than one set for everything, because two pages cannot both define "content"
// in a single set.
func parsePages() (map[string]*template.Template, error) {
	base, err := template.New("base").Funcs(funcs).ParseFS(assets, "templates/base.gohtml")
	if err != nil {
		return nil, fmt.Errorf("web: parsing the shell: %w", err)
	}
	names, err := fs.Glob(assets, "templates/*.gohtml")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, name := range names {
		short := strings.TrimSuffix(path.Base(name), ".gohtml")
		if short == "base" {
			continue
		}
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := clone.ParseFS(assets, name); err != nil {
			return nil, fmt.Errorf("web: parsing %s: %w", short, err)
		}
		pages[short] = clone
	}
	if len(pages) == 0 {
		return nil, errors.New("web: no page templates were embedded")
	}
	return pages, nil
}
