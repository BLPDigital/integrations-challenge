package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// TestDisplayKeyRoundTrips asserts that a key a reviewer can read is a key the
// interface accepts back, which is what makes copy and paste work.
func TestDisplayKeyRoundTrips(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		display string
	}{
		{"simple key", "0000417", "0000417"},
		{"composite key", model.InvoiceKey("0000417", "RE-1"), "0000417|RE-1"},
		{"three components", model.FxRateKey("GBP", "CHF", "2026-03-01", "DAILY"),
			"GBP|CHF|2026-03-01|DAILY"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayKey(tc.key); got != tc.display {
				t.Fatalf("displayKey(%q) = %q, want %q", tc.key, got, tc.display)
			}
			if got := normalizeKey(tc.display); got != tc.key {
				t.Fatalf("normalizeKey(%q) = %q, want %q", tc.display, got, tc.key)
			}
			// A raw key survives normalization unchanged, so a natural key that
			// genuinely contains a pipe stays addressable.
			if got := normalizeKey(tc.key); got != tc.key {
				t.Fatalf("normalizeKey(%q) = %q, want it unchanged", tc.key, got)
			}
		})
	}
}

// TestMaskValue asserts the masked form BUILD-SPEC 17.7 promises passes the
// data-minimization assertions: the full value never appears.
func TestMaskValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"iban", "CH9300762011623852957", "CH93*************2957"},
		{"short", "CH93", "****"},
		{"exactly eight", "CH934567", "********"},
		{"empty", "", ""},
		{"trimmed", "  CH9300762011623852957  ", "CH93*************2957"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskValue(tc.in); got != tc.want {
				t.Fatalf("maskValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestGroupIsSwiss asserts the thousands separator every other number in this
// landscape uses.
func TestGroupIsSwiss(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {7, "7"}, {999, "999"}, {1000, "1'000"}, {12345, "12'345"},
		{1234567, "1'234'567"}, {-12345, "-12'345"},
	}
	for _, tc := range tests {
		if got := group(tc.in); got != tc.want {
			t.Fatalf("group(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatValueNeverProducesAFloat asserts that every payload value renders
// from its exact decimal form: an amount, a quantity and a rate are read from
// canonical JSON as json.Number and never through a binary float.
func TestFormatValueNeverProducesAFloat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"decimal string", `"1234.5678"`, "1234.5678"},
		{"integer", `118422`, "118422"},
		{"large integer", `9007199254740993`, "9007199254740993"},
		{"true", `true`, "yes"},
		{"false", `false`, "no"},
		{"null", `null`, ""},
		{"money", `{"amount_minor":10823,"currency":"CHF","scale":2}`, "108.23 CHF"},
		{"money with scale zero", `{"amount_minor":1200,"currency":"JPY","scale":0}`, "1200 JPY"},
		{"nested object", `{"a":1}`, "{…}"},
		{"empty list", `[]`, "none"},
		{"one item", `[1]`, "1 item"},
		{"many items", `[1,2,3]`, "3 items"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := model.DecodeJSONTree([]byte(tc.in))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := formatValue(v); got != tc.want {
				t.Fatalf("formatValue(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestFieldsOfRendersStructsAndTreesIdentically asserts that a payload straight
// from the importer and the same payload replayed from disk produce the same
// table row. If they did not, a page would change after a restart.
func TestFieldsOfRendersStructsAndTreesIdentically(t *testing.T) {
	typed := model.Supplier{
		SupplierNumber: "0000417", Name: "Steinbach Industrie AG", Country: "CH",
		Currency: "CHF", IBAN: "CH9300762011623852957", VATNumber: "CHE-123.456.789",
		PaymentTermsDays: 30, ChangeSeq: 118422,
	}
	buf, err := model.CanonicalJSON(typed)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	tree, err := model.DecodeJSONTree(buf)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	a, b := fieldsOf(typed), fieldsOf(tree)
	if len(a) == 0 {
		t.Fatal("the typed payload produced no fields")
	}
	if len(a) != len(b) {
		t.Fatalf("field counts differ: %d and %d", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Errorf("field %s: %q from the struct, %q from the tree", k, v, b[k])
		}
	}
}

// TestLocator asserts the two locator shapes, which is what a reviewer follows
// back into the delivered bytes.
func TestLocator(t *testing.T) {
	line, chunk, ord := int64(1042), int64(3), int64(11)
	tests := []struct {
		name string
		prov store.Provenance
		want string
	}{
		{"file with line and ordinal",
			store.Provenance{Channel: store.ChannelFile, SourceFile: "suppliers.csv",
				SourceLine: &line, RecordOrdinal: &ord},
			"suppliers.csv:1042 #11"},
		{"file without a line",
			store.Provenance{Channel: store.ChannelFile, SourceFile: "invoices.json"},
			"invoices.json"},
		{"rest chunk",
			store.Provenance{Channel: store.ChannelREST, ChunkOrdinal: &chunk, RecordOrdinal: &ord},
			"chunk 3 #11"},
		{"internal",
			store.Provenance{Channel: store.ChannelInternal},
			"internal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := locator(tc.prov); got != tc.want {
				t.Fatalf("locator = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStaleOverwrites asserts the detector behind the record view's loudest
// statement, including the cases where nothing is wrong.
func TestStaleOverwrites(t *testing.T) {
	rev := func(version int, changeSeq int64, provenanceOnly bool) store.Revision {
		payload := map[string]any{"key": "k", fieldChangeSeq: json.Number(itoa(changeSeq))}
		return store.Revision{Version: version, Payload: payload, ProvenanceOnly: provenanceOnly}
	}
	tests := []struct {
		name    string
		history []store.Revision
		want    []int
	}{
		{"ascending is fine", []store.Revision{rev(1, 10, false), rev(2, 11, false)}, nil},
		{"equal is fine", []store.Revision{rev(1, 10, false), rev(2, 10, false)}, nil},
		{"a regression is flagged",
			[]store.Revision{rev(1, 20, false), rev(2, 10, false)}, []int{1}},
		{"two regressions are both flagged",
			[]store.Revision{rev(1, 30, false), rev(2, 10, false), rev(3, 20, false)}, []int{1, 2}},
		{"a regression does not lower the watermark",
			[]store.Revision{rev(1, 30, false), rev(2, 10, false), rev(3, 40, false)}, []int{1}},
		{"provenance-only revisions are skipped",
			[]store.Revision{rev(1, 30, false), rev(2, 10, true)}, nil},
		{"a payload without the field never regresses",
			[]store.Revision{{Version: 1, Payload: map[string]any{"a": "b"}},
				{Version: 2, Payload: map[string]any{"a": "c"}}}, nil},
		{"an empty history", nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := staleOverwrites(tc.history)
			if len(got) != len(tc.want) {
				t.Fatalf("flagged %v, want %v", got, tc.want)
			}
			for _, i := range tc.want {
				if !got[i] {
					t.Fatalf("revision %d was not flagged: %v", i, got)
				}
			}
		})
	}
}

// itoa is strconv.FormatInt without the import, for the test's json.Number.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// TestWindowPaging asserts the paging contract: a page is named by the last key
// of the page before it, the window knows whether there is a page after it, and
// it can name the previous page without a second scan.
func TestWindowPaging(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f", "g"}
	tests := []struct {
		name     string
		after    string
		limit    int
		wantPage []string
		wantMore bool
		wantPrev string
		hasPrev  bool
	}{
		{"first page", "", 3, []string{"a", "b", "c"}, true, "", false},
		{"second page", "c", 3, []string{"d", "e", "f"}, true, "", true},
		{"third page", "f", 3, []string{"g"}, false, "c", true},
		{"whole dataset in one page", "", 100, keys, false, "", false},
		{"after the last key", "g", 3, nil, false, "d", true},
		{"page size one", "b", 1, []string{"c"}, true, "a", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wd := newWindow(tc.after, tc.limit)
			for _, k := range keys {
				_, stop := wd.offer(k)
				if stop {
					break
				}
			}
			if strings.Join(wd.taken, ",") != strings.Join(tc.wantPage, ",") {
				t.Fatalf("page = %v, want %v", wd.taken, tc.wantPage)
			}
			if wd.more != tc.wantMore {
				t.Fatalf("more = %v, want %v", wd.more, tc.wantMore)
			}
			ui, err := New(Config{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			p := wd.pager(ui, "/datasets/supplier", nil, len(keys))
			if tc.hasPrev != (p.PrevHref != "") {
				t.Fatalf("previous link = %q, want present = %v", p.PrevHref, tc.hasPrev)
			}
			if tc.hasPrev && tc.wantPrev != "" &&
				!strings.Contains(p.PrevHref, "after="+tc.wantPrev) {
				t.Fatalf("previous link = %q, want after=%s", p.PrevHref, tc.wantPrev)
			}
			if tc.wantMore != (p.NextHref != "") {
				t.Fatalf("next link = %q, want present = %v", p.NextHref, tc.wantMore)
			}
		})
	}
}

// TestPagingIsStableAcrossPages walks a dataset one row at a time through the
// rendered pages and asserts that every record appears exactly once, which is
// the property offset paging loses on a growing store.
func TestPagingIsStableAcrossPages(t *testing.T) {
	f := newFixture(t, nil)
	f.writeBatch("batch-many", []map[string]any{
		fileEntry("cost_centers.csv", "cost_center", "csv", 5),
	}, map[string]string{"cost_centers.csv": "code,name,company_code,valid_from,valid_to,blocked\n" +
		"CC-1000,One,CH10,2026-01-01,,false\n" +
		"CC-2000,Two,CH10,2026-01-01,,false\n" +
		"CC-3000,Three,CH10,2026-01-01,,false\n" +
		"CC-4000,Four,CH10,2026-01-01,,false\n" +
		"CC-5000,Five,CH10,2026-01-01,,false\n"})
	f.scan()

	seen := map[string]int{}
	href := "/ui/datasets/cost_center?limit=2"
	for pages := 0; href != "" && pages < 10; pages++ {
		body := f.body(href, http.StatusOK)
		for _, code := range []string{"CC-1000", "CC-2000", "CC-3000", "CC-4000", "CC-5000"} {
			if strings.Contains(body, ">"+code+"<") {
				seen[code]++
			}
		}
		href = nextHref(body)
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d of 5 cost centers: %v", len(seen), seen)
	}
	for code, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared on %d pages", code, n)
		}
	}
}

// nextHref extracts the "Next" link of a rendered page, unescaped.
func nextHref(body string) string {
	const marker = `>Next</a>`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], `href="`)
	if start < 0 {
		return ""
	}
	start += len(`href="`)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return ""
	}
	href := body[start : start+end]
	href = strings.ReplaceAll(href, "&amp;", "&")
	if _, err := url.Parse(href); err != nil {
		return ""
	}
	return href
}

// TestNewNormalizesConfig asserts the documented defaults and the caps.
func TestNewNormalizesConfig(t *testing.T) {
	tests := []struct {
		name     string
		in       Config
		wantBase string
		wantPage int
		wantRefr int
	}{
		{"zero value", Config{}, DefaultBasePath, DefaultPageSize, DefaultRefreshSeconds},
		{"trimmed base path", Config{BasePath: "/inspect/"}, "/inspect", DefaultPageSize,
			DefaultRefreshSeconds},
		{"bare base path", Config{BasePath: "inspect"}, "/inspect", DefaultPageSize,
			DefaultRefreshSeconds},
		{"page size capped", Config{PageSize: 100000}, DefaultBasePath, MaxPageSize,
			DefaultRefreshSeconds},
		{"explicit values", Config{PageSize: 10, RefreshSeconds: 30}, DefaultBasePath, 10, 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui, err := New(tc.in)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if ui.BasePath() != tc.wantBase {
				t.Fatalf("base path = %q, want %q", ui.BasePath(), tc.wantBase)
			}
			if ui.cfg.PageSize != tc.wantPage {
				t.Fatalf("page size = %d, want %d", ui.cfg.PageSize, tc.wantPage)
			}
			if ui.cfg.RefreshSeconds != tc.wantRefr {
				t.Fatalf("refresh = %d, want %d", ui.cfg.RefreshSeconds, tc.wantRefr)
			}
		})
	}
}

// TestConfigurableBasePath asserts that the interface can be mounted elsewhere
// and that every link it emits follows.
func TestConfigurableBasePath(t *testing.T) {
	f := newFixture(t, func(cfg *Config) { cfg.BasePath = "/inspect" })
	f.seed()
	body := f.body("/inspect/", http.StatusOK)
	contains(t, "/inspect/", body, `href="/inspect/datasets/"`, `src="/inspect/static/blp-logo-full.svg"`)
	if strings.Contains(body, `href="/ui/`) {
		t.Fatal("a link still points at the default mount point")
	}
}

// TestIntParamIsTotal asserts that a hand-edited query parameter never fails a
// page: it falls back to the default instead.
func TestIntParamIsTotal(t *testing.T) {
	tests := []struct {
		query string
		want  int
	}{
		{"", 50}, {"limit=10", 10}, {"limit=0", 50}, {"limit=-3", 50},
		{"limit=abc", 50}, {"limit=99999", MaxPageSize},
	}
	ui, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tc := range tests {
		r := &http.Request{URL: &url.URL{Path: "/ui/datasets/supplier", RawQuery: tc.query}}
		if got := ui.pageSize(r); got != tc.want {
			t.Fatalf("pageSize(%q) = %d, want %d", tc.query, got, tc.want)
		}
	}
}
