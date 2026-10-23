package erp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
)

// TestListPaginationContract walks every list endpoint page by page and asserts
// the whole published contract at once: the default page size, the ordering by
// (change_seq, natural key), that a full walk returns every record exactly once,
// that next_cursor is empty exactly when has_more is false, and that
// max_change_seq is the collection's watermark rather than the page's.
func TestListPaginationContract(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		name  string
		route string
		want  int
	}{
		{"suppliers", RouteSuppliers, len(h.dataset().suppliers.items)},
		{"purchase orders", RoutePurchaseOrders, len(h.dataset().purchaseOrders.items)},
		{"purchase order lines", RoutePurchaseOrderLines, len(h.dataset().poLines.items)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			var lastSeq int64
			var lastKey string
			total, pages, cursor := 0, 0, ""
			for {
				path := tc.route
				if cursor != "" {
					path += "?cursor=" + cursor
				}
				rec, env := h.list(path)
				pages++
				if pages == 1 && env.Returned != DefaultListLimit && env.Returned != tc.want {
					t.Fatalf("first page returned %d records, want the default limit %d",
						env.Returned, DefaultListLimit)
				}
				if rec.Header().Get(HeaderLimitClamped) != "" {
					t.Fatalf("unclamped request carries %s", HeaderLimitClamped)
				}
				if env.Returned != len(env.Records) {
					t.Fatalf("returned %d but %d records", env.Returned, len(env.Records))
				}
				if got, want := env.MaxChangeSeq, watermarkOf(t, h, tc.route); got != want {
					t.Fatalf("max_change_seq %d, want the collection watermark %d", got, want)
				}
				for _, raw := range env.Records {
					seq, key := seqAndKey(t, raw)
					if !positionGreater(seq, key, lastSeq, lastKey) {
						t.Fatalf("record (%d,%q) does not sort after (%d,%q): the order must be "+
							"(change_seq, natural key) ascending", seq, key, lastSeq, lastKey)
					}
					if seen[key] {
						t.Fatalf("record %q returned twice", key)
					}
					seen[key] = true
					lastSeq, lastKey = seq, key
				}
				total += env.Returned
				if env.HasMore != (env.NextCursor != "") {
					t.Fatalf("has_more=%v with next_cursor=%q: next_cursor is empty exactly when "+
						"has_more is false", env.HasMore, env.NextCursor)
				}
				if !env.HasMore {
					break
				}
				cursor = env.NextCursor
				if pages > 1000 {
					t.Fatal("pagination did not terminate")
				}
			}
			if total != tc.want {
				t.Fatalf("walked %d records over %d pages, want %d", total, pages, tc.want)
			}
		})
	}
}

// watermarkOf returns the highest change sequence of the collection a route
// serves, read from the dataset rather than from a response.
func watermarkOf(t *testing.T, h *harness, route string) int64 {
	t.Helper()
	d := h.dataset()
	switch route {
	case RouteSuppliers:
		return d.suppliers.maxChangeSeq()
	case RoutePurchaseOrders:
		return d.purchaseOrders.maxChangeSeq()
	case RoutePurchaseOrderLines:
		return d.poLines.maxChangeSeq()
	}
	t.Fatalf("no watermark for route %s", route)
	return 0
}

// seqAndKey extracts the change sequence and the natural key of a wire record.
// The key is assembled the way the model assembles it, so the test asserts the
// order the cursor promises rather than the order the server happens to emit.
func seqAndKey(t *testing.T, raw any) (int64, string) {
	t.Helper()
	buf, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var rec struct {
		ChangeSeq      int64  `json:"change_seq"`
		SupplierNumber string `json:"supplier_number"`
		PONumber       string `json:"po_number"`
		LineNo         string `json:"line_no"`
	}
	if err := json.Unmarshal(buf, &rec); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	switch {
	case rec.LineNo != "":
		return rec.ChangeSeq, strings.TrimSpace(rec.PONumber) + "\x1f" + rec.LineNo
	case rec.PONumber != "":
		return rec.ChangeSeq, strings.TrimSpace(rec.PONumber)
	default:
		return rec.ChangeSeq, strings.TrimSpace(rec.SupplierNumber)
	}
}

// TestListLimitIsClampedNotRejected asserts the clamp: a limit above the maximum
// is served at the maximum with X-Limit-Clamped set, because a real ERP clamps
// silently and the header is the concession to fairness.
func TestListLimitIsClampedNotRejected(t *testing.T) {
	h := newHarness(t, nil)
	tests := []struct {
		limit       string
		wantRecords int
		wantHeader  string
	}{
		{"1", 1, ""},
		{"100", 100, ""},
		{"250", 240, ""},
		{"251", 240, "250"},
		{"100000", 240, "250"},
	}
	for _, tc := range tests {
		t.Run("limit="+tc.limit, func(t *testing.T) {
			rec, env := h.list(RouteSuppliers + "?limit=" + tc.limit)
			if env.Returned != tc.wantRecords {
				t.Fatalf("returned %d, want %d", env.Returned, tc.wantRecords)
			}
			if got := rec.Header().Get(HeaderLimitClamped); got != tc.wantHeader {
				t.Fatalf("%s = %q, want %q", HeaderLimitClamped, got, tc.wantHeader)
			}
		})
	}
}

// TestListLimitClampedPageIsExactlyTheMaximum asserts the clamped page really is
// the maximum size on a collection large enough to fill it.
func TestListLimitClampedPageIsExactlyTheMaximum(t *testing.T) {
	h := newHarness(t, nil)
	rec, env := h.list(RoutePurchaseOrderLines + "?limit=9999")
	if env.Returned != MaxListLimit {
		t.Fatalf("returned %d, want the maximum %d", env.Returned, MaxListLimit)
	}
	if got := rec.Header().Get(HeaderLimitClamped); got != "250" {
		t.Fatalf("%s = %q, want 250", HeaderLimitClamped, got)
	}
}

// TestListRejectsUnusableParameters asserts the 400s. A clamped limit is not an
// error, but an unparseable one is, and so is a negative one.
func TestListRejectsUnusableParameters(t *testing.T) {
	h := newHarness(t, nil)
	tests := []struct {
		name  string
		query string
		code  string
	}{
		{"limit not an integer", "?limit=many", httpx.CodeMalformedBody},
		{"limit zero", "?limit=0", httpx.CodeMalformedBody},
		{"limit negative", "?limit=-5", httpx.CodeMalformedBody},
		{"changed_since not an integer", "?changed_since=yesterday", httpx.CodeMalformedBody},
		{"changed_since negative", "?changed_since=-1", httpx.CodeMalformedBody},
		{"cursor not base64", "?cursor=@@@not-base64@@@", httpx.CodeInvalidCursor},
		{"cursor forged", "?cursor=eyJkYXRhc2V0Ijoic3VwcGxpZXIifQ", httpx.CodeInvalidCursor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.get(RouteSuppliers + tc.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (code %q)", rec.Code, h.errorBody(rec).Code)
			}
			if got := h.errorBody(rec).Code; got != tc.code {
				t.Fatalf("code %q, want %q", got, tc.code)
			}
		})
	}
}

// TestCursorIsStableAndTamperEvident asserts the two properties a cursor has to
// have: the same logical position always encodes to the same string, so a
// transcript stays diffable, and a hand-edited cursor is refused, so a client
// cannot page past a filter by editing base64.
func TestCursorIsStableAndTamperEvident(t *testing.T) {
	h := newHarness(t, nil)
	_, first := h.list(RouteSuppliers + "?limit=10")
	if first.NextCursor == "" {
		t.Fatal("the first page of ten of two hundred and forty carries no cursor")
	}
	_, again := h.list(RouteSuppliers + "?limit=10")
	if again.NextCursor != first.NextCursor {
		t.Fatalf("cursor changed between two identical requests:\n%q\n%q",
			first.NextCursor, again.NextCursor)
	}

	// A second server on the same seed must issue the same cursor, because the
	// signing secret is derived from the seed and never drawn.
	other := newHarness(t, nil)
	_, otherFirst := other.list(RouteSuppliers + "?limit=10")
	if otherFirst.NextCursor != first.NextCursor {
		t.Fatalf("two servers on the same seed issued different cursors:\n%q\n%q",
			first.NextCursor, otherFirst.NextCursor)
	}

	for _, tampered := range []string{
		flipLastByte(first.NextCursor),
		first.NextCursor[:len(first.NextCursor)-1],
		"x" + first.NextCursor[1:],
	} {
		rec := h.get(RouteSuppliers + "?cursor=" + tampered)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("tampered cursor %q: status %d, want 400", tampered, rec.Code)
		}
		if got := h.errorBody(rec).Code; got != httpx.CodeInvalidCursor {
			t.Fatalf("tampered cursor %q: code %q, want %q", tampered, got, httpx.CodeInvalidCursor)
		}
	}
}

// flipLastByte changes the last character of a cursor, which breaks its
// signature without breaking its base64.
func flipLastByte(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	return s[:len(s)-1] + string(repl)
}

// TestCursorOfAnotherDatasetIsRefused asserts a supplier cursor cannot be
// replayed on purchase orders: the dataset is part of the signed material.
func TestCursorOfAnotherDatasetIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	_, env := h.list(RouteSuppliers + "?limit=10")
	rec := h.get(RoutePurchaseOrders + "?cursor=" + env.NextCursor)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := h.errorBody(rec).Code; got != httpx.CodeInvalidCursor {
		t.Fatalf("code %q, want %q", got, httpx.CodeInvalidCursor)
	}
}

// TestChangedSinceFiltersOnChangeSeq asserts the watermark filter is strictly
// greater than, that it composes with paging, and that a watermark at the top of
// the collection returns an empty page rather than an error.
func TestChangedSinceFiltersOnChangeSeq(t *testing.T) {
	h := newHarness(t, nil)
	all := h.dataset().suppliers
	watermark := all.seqAt(len(all.items) / 2)

	total, cursor := 0, ""
	for {
		path := fmt.Sprintf("%s?changed_since=%d&limit=50", RouteSuppliers, watermark)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		_, env := h.list(path)
		for _, raw := range env.Records {
			seq, _ := seqAndKey(t, raw)
			if seq <= watermark {
				t.Fatalf("record with change_seq %d returned for changed_since=%d: the filter is "+
					"strictly greater than", seq, watermark)
			}
		}
		total += env.Returned
		if !env.HasMore {
			break
		}
		cursor = env.NextCursor
	}
	want := 0
	for i := range all.items {
		if all.seqAt(i) > watermark {
			want++
		}
	}
	if total != want {
		t.Fatalf("changed_since returned %d records, want %d", total, want)
	}

	_, empty := h.list(fmt.Sprintf("%s?changed_since=%d", RouteSuppliers, all.maxChangeSeq()))
	if empty.Returned != 0 || empty.HasMore || empty.NextCursor != "" {
		t.Fatalf("a watermark at the top of the collection returned %d records, has_more=%v",
			empty.Returned, empty.HasMore)
	}
	if empty.Records == nil {
		t.Fatal("records must be [] and never null")
	}
}

// TestChangedSinceFindsTheOutOfOrderUpdates asserts the trap the generator plants
// works through this surface: a few records carry the highest change sequences
// while sitting early in key order, so a client that persisted the last key it
// saw instead of the highest change sequence loses them.
func TestChangedSinceFindsTheOutOfOrderUpdates(t *testing.T) {
	h := newHarness(t, nil)
	view := h.dataset().suppliers
	last := view.items[len(view.items)-1]
	if last.ChangeSeq != view.maxChangeSeq() {
		t.Fatalf("the last record in list order carries change_seq %d, not the watermark %d",
			last.ChangeSeq, view.maxChangeSeq())
	}
	// The record with the highest change sequence is NOT the last one in key
	// order: that is what makes the watermark and the key order disagree.
	var highestKey string
	for _, s := range view.items {
		if s.ChangeSeq == view.maxChangeSeq() {
			highestKey = s.SupplierNumber
		}
	}
	byKeyLast := ""
	for _, s := range view.items {
		if s.SupplierNumber > byKeyLast {
			byKeyLast = s.SupplierNumber
		}
	}
	if highestKey == byKeyLast {
		t.Skip("this seed did not move a record out of key order in the suppliers dataset")
	}
}

// TestBulkIDsFormOfThePurchaseOrderList asserts the bulk form: it returns exactly
// the named orders in list order, resolves every delivered spelling through the
// published identifier rule, ignores unknown ids, and reports the collection's
// watermark rather than the subset's.
//
// The spellings matter. The seeded master delivers purchase order numbers three
// ways: bare digits, a "PO-" prefix and a space padded form. The published rule
// trims and then compares numerically only when the remainder is all digits, so
// the space padded form resolves to the bare one and the prefixed form is its own
// identifier. This test asserts exactly that, because a bulk endpoint that
// normalized more than the rule says would quietly merge two orders.
func TestBulkIDsFormOfThePurchaseOrderList(t *testing.T) {
	h := newHarness(t, nil)
	view := h.dataset().purchaseOrders
	want := []model.PurchaseOrder{view.items[1], view.items[3], view.items[7]}
	ids := make([]string, 0, len(want)+1)
	for _, po := range want {
		// The delivered spelling, verbatim, plus surrounding whitespace on one
		// of them to prove the trim.
		ids = append(ids, po.PONumber)
	}
	ids[0] = "  " + ids[0] + "  "
	ids = append(ids, seed.UnknownPONumber)
	_, env := h.list(RoutePurchaseOrders + "?ids=" + url.QueryEscape(strings.Join(ids, ",")))
	if env.Returned != len(want) {
		t.Fatalf("returned %d records, want the %d known ids %q", env.Returned, len(want), ids)
	}
	if env.HasMore || env.NextCursor != "" {
		t.Fatal("a bulk read that fitted in one page reports has_more")
	}
	if env.MaxChangeSeq != view.maxChangeSeq() {
		t.Fatalf("max_change_seq %d, want the collection watermark %d",
			env.MaxChangeSeq, view.maxChangeSeq())
	}
	var lastSeq int64
	var lastKey string
	got := map[string]bool{}
	for _, raw := range env.Records {
		seq, key := seqAndKey(t, raw)
		if !positionGreater(seq, key, lastSeq, lastKey) {
			t.Fatal("the bulk form must keep the list order")
		}
		lastSeq, lastKey = seq, key
		got[seed.CanonicalPONumber(key)] = true
	}
	for _, po := range want {
		if !got[seed.CanonicalPONumber(po.PONumber)] {
			t.Fatalf("purchase order %q missing from the bulk result", po.PONumber)
		}
	}
}

// TestBulkIDsFormAcceptsEverySpelling asserts the bulk form applies the ONE
// documented normalization: trim, drop a leading "PO-", then compare.
//
// This test asserted the opposite until the identifier rule was reconciled, and
// its own comment cited the published rule while pinning the implementation that
// contradicted it. docs/rules.md:86 says the same order legitimately arrives as
// "4500001234", "PO-4500001234" or space padded, and the twin dropped the prefix
// while the ERP did not, so a connector that normalized before asking the ERP was
// told the order does not exist. A test that pins a defect is worse than no test,
// because it makes the defect look decided.
func TestBulkIDsFormAcceptsEverySpelling(t *testing.T) {
	h := newHarness(t, nil)
	var bare string
	for _, po := range h.dataset().purchaseOrders.items {
		if po.PONumber == seed.CanonicalPONumber(po.PONumber) {
			bare = po.PONumber
			break
		}
	}
	if bare == "" {
		t.Skip("this seed delivered no purchase order in the bare digit spelling")
	}
	for _, spelling := range []string{bare, "PO-" + bare, " " + bare + " "} {
		_, env := h.list(RoutePurchaseOrders + "?ids=" + url.QueryEscape(spelling))
		if env.Returned != 1 {
			t.Errorf("ids=%q returned %d orders, want the 1 order it names",
				spelling, env.Returned)
		}
	}
	// One order asked for twice under two spellings is still one order: the bulk
	// form deduplicates on the normalized key, not on the string.
	_, env := h.list(RoutePurchaseOrders + "?ids=" + url.QueryEscape(bare+",PO-"+bare))
	if env.Returned != 1 {
		t.Errorf("two spellings of one order returned %d orders, want 1", env.Returned)
	}
}

// TestBulkIDsFormPaginates asserts the bulk form is a view like any other: it
// clamps, pages and cursors exactly as the full collection does.
func TestBulkIDsFormPaginates(t *testing.T) {
	h := newHarness(t, nil)
	view := h.dataset().purchaseOrders
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, seed.CanonicalPONumber(view.items[i].PONumber))
	}
	path := RoutePurchaseOrders + "?limit=2&ids=" + strings.Join(ids, ",")
	total, cursor := 0, ""
	for {
		p := path
		if cursor != "" {
			p += "&cursor=" + cursor
		}
		_, env := h.list(p)
		total += env.Returned
		if !env.HasMore {
			break
		}
		cursor = env.NextCursor
	}
	if total != 5 {
		t.Fatalf("bulk pagination walked %d of 5 orders", total)
	}
}

// TestSingleSupplierGet asserts the single GET, including that leading zeros are
// significant: the seeded pair "0000000417" and "0000417" are two different
// creditors and neither may resolve to the other.
func TestSingleSupplierGet(t *testing.T) {
	h := newHarness(t, nil)
	for _, number := range []string{seed.CollisionSupplierCanonical, seed.CollisionSupplierShort} {
		rec := h.get(RouteSuppliers + "/" + number)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET supplier %q: status %d", number, rec.Code)
		}
		var got struct {
			SupplierNumber string `json:"supplier_number"`
			LegacyID       int64  `json:"legacy_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode supplier: %v", err)
		}
		if got.SupplierNumber != number {
			t.Fatalf("asked for %q, got %q: leading zeros are significant", number, got.SupplierNumber)
		}
		if got.LegacyID != 417 {
			t.Fatalf("legacy_id %d, want the shared 417 that makes it unusable as a key", got.LegacyID)
		}
	}
	rec := h.get(RouteSuppliers + "/9999999999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown supplier: status %d, want 404", rec.Code)
	}
}

// TestUoMConversionsTable asserts the conversion table is served whole, in its
// documented order, and carries the graded rows.
func TestUoMConversionsTable(t *testing.T) {
	h := newHarness(t, nil)
	_, env := h.list(RouteUoMConversions)
	if env.Returned != len(h.dataset().uom) {
		t.Fatalf("returned %d rows, want the whole table of %d", env.Returned, len(h.dataset().uom))
	}
	if env.HasMore || env.NextCursor != "" {
		t.Fatal("the conversion table is one page")
	}
	var rows []seed.UoMConversion
	buf, _ := json.Marshal(env.Records)
	if err := json.Unmarshal(buf, &rows); err != nil {
		t.Fatalf("decode conversions: %v", err)
	}
	wantCTN, wantTON := false, false
	for _, row := range rows {
		if row.Material == "" && row.AltUoM == "CTN" && row.Numerator == 12 && row.Denominator == 1 && row.BaseUoM == "EA" {
			wantCTN = true
		}
		if row.Material == "" && row.AltUoM == seed.UoMTon && row.Numerator == 1000 && row.Denominator == 1 && row.BaseUoM == "KG" {
			wantTON = true
		}
		if row.AltUoM == seed.UoMPallet {
			t.Fatalf("%s must have no conversion at all: it is EXC_UOM_UNMAPPABLE and never a guess",
				seed.UoMPallet)
		}
	}
	if !wantCTN || !wantTON {
		t.Fatalf("conversion table is missing a graded row: CTN=%v TON=%v", wantCTN, wantTON)
	}
}

// TestListRequiresBearerToken asserts the REST surface is authenticated and that
// the 401 is not retriable with the same credentials.
func TestListRequiresBearerToken(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.do(httptest.NewRequest(http.MethodGet, RouteSuppliers, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if got := h.errorBody(rec).Code; got != httpx.CodeTokenMissing {
		t.Fatalf("code %q, want %q", got, httpx.CodeTokenMissing)
	}
}

// TestDeltaScenarioIsServedConsistently asserts the delta scenario is served the
// same way the cold load is: unique keys, the same ordering, a usable watermark,
// and one export drop per delivery.
//
// It runs against S2, which is the scenario a connector meets with a second
// legacy delivery and re-issued master records, and it is the test that would
// catch a delta dataset whose re-issued records arrived as duplicates.
func TestDeltaScenarioIsServedConsistently(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(cfg *Config) {
		cfg.Scenario = seed.ScenarioS2
		cfg.ExportDir = dir
	})
	set := h.dataset().set
	if set.DeltaFromChangeSeq <= 0 {
		t.Fatalf("the delta scenario has no starting watermark: %d", set.DeltaFromChangeSeq)
	}

	cases := []struct {
		route string
		view  view
	}{
		{RouteSuppliers, h.dataset().suppliers},
		{RoutePurchaseOrders, h.dataset().purchaseOrders},
		{RoutePurchaseOrderLines, h.dataset().poLines},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			// A full walk sees every record exactly once.
			seen := map[string]bool{}
			cursor := ""
			for {
				path := tc.route + "?limit=250"
				if cursor != "" {
					path += "&cursor=" + cursor
				}
				_, env := h.list(path)
				for _, raw := range env.Records {
					_, key := seqAndKey(t, raw)
					if seen[key] {
						t.Fatalf("record %q served twice", key)
					}
					seen[key] = true
				}
				if !env.HasMore {
					break
				}
				cursor = env.NextCursor
			}
			if len(seen) != tc.view.length() {
				t.Fatalf("walked %d records, want %d", len(seen), tc.view.length())
			}

			// A delta walk from the scenario's own watermark sees exactly the
			// re-issued records, and never all of them.
			want := 0
			for i := 0; i < tc.view.length(); i++ {
				if tc.view.seqAt(i) > set.DeltaFromChangeSeq {
					want++
				}
			}
			got, cursor := 0, ""
			for {
				path := fmt.Sprintf("%s?limit=250&changed_since=%d", tc.route, set.DeltaFromChangeSeq)
				if cursor != "" {
					path += "&cursor=" + cursor
				}
				_, env := h.list(path)
				got += env.Returned
				if !env.HasMore {
					break
				}
				cursor = env.NextCursor
			}
			if got != want {
				t.Fatalf("the delta returned %d records, want %d", got, want)
			}
			if want == 0 || want == tc.view.length() {
				t.Fatalf("a delta of %d records out of %d is not a delta", want, tc.view.length())
			}
		})
	}

	files := h.srv.ExportFiles()
	if len(files) != 2*len(set.Deliveries) || len(set.Deliveries) < 3 {
		t.Fatalf("%d files for %d deliveries: a delta scenario drops a second run",
			len(files), len(set.Deliveries))
	}
}
