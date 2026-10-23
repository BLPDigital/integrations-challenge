package erp

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// A ListEnvelope is the response of every list endpoint, identical on all of
// them so one client-side parser covers the whole surface.
//
// MaxChangeSeq is the highest change sequence in the whole collection, NOT of
// this page, so a connector can persist a watermark without inspecting records.
// It is only a valid watermark once HasMore is false: persisting it after the
// first page of a multi-page pull would skip everything the client has not read
// yet.
//
// NextCursor is empty exactly when HasMore is false. Records is never null.
type ListEnvelope struct {
	Records      []any  `json:"records"`
	NextCursor   string `json:"next_cursor"`
	MaxChangeSeq int64  `json:"max_change_seq"`
	Returned     int    `json:"returned"`
	HasMore      bool   `json:"has_more"`
}

// listRequest is a parsed, validated list request: which view, how big a page,
// from which position and above which change sequence.
type listRequest struct {
	view    view
	limit   int
	clamped bool
	// changedSince filters change_seq > value. Zero returns everything, which
	// is a cold load.
	changedSince int64
	cursor       httpx.Cursor
	// ids is the sorted canonical id set of the bulk form, empty otherwise.
	ids []string
}

// parseListRequest validates the paging parameters of a list request.
//
// limit above MaxListLimit is clamped rather than rejected, because a real ERP
// clamps silently; the response says so in X-Limit-Clamped. A limit below one, a
// non-numeric limit or changed_since, and a cursor this server did not sign are
// all 400: the first two are unusable, and a forged cursor is how a client would
// page past a filter.
func (s *Server) parseListRequest(r *http.Request, v view) (*listRequest, error) {
	limit, err := intQuery(r, "limit", DefaultListLimit)
	if err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, errBadQuery("limit", "out_of_range")
	}
	lr := &listRequest{view: v, limit: int(limit)}
	if limit > MaxListLimit {
		lr.limit = MaxListLimit
		lr.clamped = true
	}
	if lr.changedSince, err = intQuery(r, "changed_since", 0); err != nil {
		return nil, err
	}
	_, st := s.state()
	if lr.cursor, err = httpx.ParseCursor(strings.TrimSpace(r.URL.Query().Get("cursor")), st.cursorSecret); err != nil {
		return nil, err
	}
	if !lr.cursor.IsZero() && lr.cursor.Dataset != v.dataset() {
		return nil, httpx.InvalidCursor()
	}
	lr.ids = canonicalIDs(r.URL.Query().Get("ids"))
	return lr, nil
}

// canonicalIDs splits a bulk ids= parameter into the sorted, deduplicated set of
// canonical identifiers it names. It is used for the fault-injection signature,
// so it must be a pure function of the set and not of the order the client wrote
// it in.
func canonicalIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		id := poKey(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	// Collected with a duplicate-filter map; the order is imposed here.
	sort.Strings(out)
	return out
}

// listPage is the resolved page: where it starts, how many records it carries
// and whether the collection continues past it.
type listPage struct {
	start   int
	count   int
	hasMore bool
}

// page resolves the request against its view.
//
// The view is sorted by (change_seq, natural key) ascending, so both filters are
// prefixes to skip: changed_since skips every record at or below the watermark,
// and the cursor skips every record at or before the position already returned.
// The page starts at the later of the two, which is why a cursor and a watermark
// can be combined without either losing records.
func (lr *listRequest) page() listPage {
	v := lr.view
	n := v.length()
	afterWatermark := sort.Search(n, func(i int) bool { return v.seqAt(i) > lr.changedSince })
	afterCursor := sort.Search(n, func(i int) bool {
		return positionGreater(v.seqAt(i), v.keyAt(i), lr.cursor.ChangeSeqHigh, lr.cursor.LastKey)
	})
	start := afterWatermark
	if afterCursor > start {
		start = afterCursor
	}
	count := n - start
	if count > lr.limit {
		count = lr.limit
	}
	return listPage{start: start, count: count, hasMore: start+count < n}
}

// positionGreater reports whether (seq, key) sorts strictly after (seqRef,
// keyRef) in the list order.
func positionGreater(seq int64, key string, seqRef int64, keyRef string) bool {
	if seq != seqRef {
		return seq > seqRef
	}
	return key > keyRef
}

// signature is the content-addressed signature of this logical list page.
//
// It carries the DECODED cursor position, never the opaque cursor string: the
// cursor string is a signed encoding of the position, and two clients holding
// two encodings of one position are making the same request. The bulk id set is
// appended when present, sorted, because a bulk read of one set of orders is a
// different logical request from a bulk read of another.
func (lr *listRequest) signature() string {
	var b strings.Builder
	b.WriteString("GET ")
	b.WriteString(lr.view.route())
	b.WriteString("|changed_since=")
	b.WriteString(strconv.FormatInt(lr.changedSince, 10))
	b.WriteString("|after=")
	b.WriteString(lr.cursor.LastKey)
	if len(lr.ids) > 0 {
		b.WriteString("|ids=")
		b.WriteString(strings.Join(lr.ids, ","))
	}
	return b.String()
}

// handleSuppliers serves the paginated creditor list.
func (s *Server) handleSuppliers(w http.ResponseWriter, r *http.Request) {
	d, _ := s.state()
	s.serveList(w, r, d.suppliers)
}

// handlePurchaseOrders serves the paginated purchase order list, including the
// bulk ids= form.
func (s *Server) handlePurchaseOrders(w http.ResponseWriter, r *http.Request) {
	d, _ := s.state()
	s.serveList(w, r, s.viewForPORequest(r, d))
}

// handlePurchaseOrderLines serves the paginated purchase order line list.
func (s *Server) handlePurchaseOrderLines(w http.ResponseWriter, r *http.Request) {
	d, _ := s.state()
	s.serveList(w, r, d.poLines)
}

// viewForPORequest returns the view a purchase order list request reads: the
// whole collection, or the subset the bulk ids= form names.
//
// The bulk form is an optimization and never a requirement: every scenario stays
// inside its published request budget with plain pagination alone, and the ids
// form only ever saves a client that already knows which orders it wants.
func (s *Server) viewForPORequest(r *http.Request, d *data) view {
	ids := canonicalIDs(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		return d.purchaseOrders
	}
	return d.poSubset(ids)
}

// serveList answers one list request.
func (s *Server) serveList(w http.ResponseWriter, r *http.Request, v view) {
	lr, err := s.parseListRequest(r, v)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	_, st := s.state()
	page := lr.page()
	if s.serveEmptyPage(st, v, lr) {
		// One deliberately empty page, with has_more still true and the client's
		// own cursor echoed back, so the next request returns the page this one
		// withheld. has_more is the authority on whether a collection continues;
		// an empty page is not a terminator, and a client that treats it as one
		// loses every record after it.
		//
		// It only ever fires on a page the client reached with a cursor, so the
		// echoed cursor is always one this server signed.
		writeJSON(w, http.StatusOK, ListEnvelope{
			Records:      make([]any, 0),
			MaxChangeSeq: v.maxChangeSeq(),
			Returned:     0,
			HasMore:      true,
			NextCursor:   lr.cursor.Encode(st.cursorSecret),
		})
		return
	}
	env := ListEnvelope{
		Records:      make([]any, 0, page.count),
		MaxChangeSeq: v.maxChangeSeq(),
		Returned:     page.count,
		HasMore:      page.hasMore,
	}
	for i := page.start; i < page.start+page.count; i++ {
		env.Records = append(env.Records, v.recordAt(i))
	}
	if page.hasMore && page.count > 0 {
		last := page.start + page.count - 1
		env.NextCursor = httpx.Cursor{
			Dataset:       v.dataset(),
			LastKey:       v.keyAt(last),
			ChangeSeqHigh: v.seqAt(last),
		}.Encode(st.cursorSecret)
	}
	if lr.clamped {
		w.Header().Set(HeaderLimitClamped, strconv.Itoa(MaxListLimit))
	}
	writeJSON(w, http.StatusOK, env)
}

// serveEmptyPage reports whether this page request is the one the scenario wants
// answered empty.
//
// Counted per dataset so that every paginated collection gets one, and only on a
// request that carried a cursor: an empty FIRST page would have no cursor to echo
// and would leave the client no way to continue, which is a broken server rather
// than a hard one.
func (s *Server) serveEmptyPage(st *runState, v view, lr *listRequest) bool {
	if s.cfg.EmptyPageAt <= 0 || lr.cursor.LastKey == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.emptyPageServed == nil {
		st.emptyPageServed = map[string]bool{}
	}
	ds := v.dataset()
	if st.emptyPageServed[ds] {
		return false
	}
	if st.pageRequests == nil {
		st.pageRequests = map[string]int{}
	}
	st.pageRequests[ds]++
	if st.pageRequests[ds] != s.cfg.EmptyPageAt {
		return false
	}
	st.emptyPageServed[ds] = true
	return true
}

// handleUoMConversions serves the unit-of-measure conversion table.// handleUoMConversions serves the unit-of-measure conversion table.
//
// It is one page: the table is small, it carries no change sequence and it is
// not incremental, so max_change_seq is zero and has_more is always false. The
// envelope is the list envelope anyway, so a client reuses its list parser.
func (s *Server) handleUoMConversions(w http.ResponseWriter, r *http.Request) {
	d, _ := s.state()
	env := ListEnvelope{Records: make([]any, 0, len(d.uom)), Returned: len(d.uom)}
	for i := range d.uom {
		env.Records = append(env.Records, d.uom[i])
	}
	writeJSON(w, http.StatusOK, env)
}

// handleSupplier serves the single creditor GET.
//
// It exists for the correction path and is net token negative on purpose: it
// earns half a token and costs one, so a client that pulls a whole creditor
// master one record at a time runs out of budget, which is the lesson.
func (s *Server) handleSupplier(w http.ResponseWriter, r *http.Request) {
	d, _ := s.state()
	number := r.PathValue("supplier_number")
	sup, ok := d.supplier(number)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, &httpx.Error{
			Code:      "SUPPLIER_NOT_FOUND",
			Message:   "no creditor with this supplier number",
			Retriable: false,
			Status:    http.StatusNotFound,
		})
		return
	}
	writeJSON(w, http.StatusOK, sup)
}
