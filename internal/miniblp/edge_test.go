package miniblp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// TestEmptyChunkIsANoOp asserts that a chunk carrying no records is a no-op and
// not a malformed body: a connector that reaches the end of a dataset with an
// empty page has done nothing wrong.
func TestEmptyChunkIsANoOp(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-empty"))
	status, body := h.chunk(ref, "supplier", 1, "empty", []any{})
	if status != http.StatusOK {
		t.Fatalf("status = %d body %s, want 200", status, body)
	}
	var res struct {
		Counts  Counts         `json:"counts"`
		Records []RecordResult `json:"records"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("body: %v", err)
	}
	if res.Counts.Seen != 0 || len(res.Records) != 0 || !res.Counts.closes() {
		t.Fatalf("counts = %+v records = %+v", res.Counts, res.Records)
	}
	if status, receipt := h.commit(ref); status != http.StatusOK || !receipt.ClosureOK {
		t.Fatalf("commit status = %d closure %v", status, receipt.ClosureOK)
	}
}

// TestUnknownDatasetIsRefused asserts that a dataset outside the closed set is a
// request defect and not a new dataset.
func TestUnknownDatasetIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-ds"))
	status, body := h.chunk(ref, "kreditoren", 1, "k", []any{map[string]any{"a": "b"}})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", status, body)
	}
}

// TestChunkOnUnknownBatchIsRefused asserts that records cannot be pushed into a
// batch nobody opened.
func TestChunkOnUnknownBatchIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	status, body := h.chunk("no-such-batch", "supplier", 1, "k",
		[]any{supplierRecord("0000417", "A", false)})
	if status != http.StatusNotFound {
		t.Fatalf("status = %d body %s, want 404", status, body)
	}
}

// TestChunkAfterCommitIsRefused asserts that a committed batch is closed.
func TestChunkAfterCommitIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-closed"))
	h.chunk(ref, "supplier", 1, "k1", []any{supplierRecord("0000417", "A", false)})
	if status, _ := h.commit(ref); status != http.StatusOK {
		t.Fatalf("commit status = %d", status)
	}
	status, body := h.chunk(ref, "supplier", 2, "k2",
		[]any{supplierRecord("0000418", "B", false)})
	if status != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", status, body)
	}
}

// TestAbortLeavesNoProposal asserts that an abort closes the batch without
// matching and says so, and that it does not pretend to unapply what was already
// delivered.
func TestAbortLeavesNoProposal(t *testing.T) {
	h := newHarness(t, nil)
	seedMatchingLandscape(h)
	_, ref := h.openBatch(restManifest("batch-abort-rest"))
	invoices := mustJSON(t, []any{
		invoiceRecord("0000000417", "0004711", "CHF", "1250.00", "89.35", "1160.65"),
	})
	if status, body := h.rawChunk(ref, "invoice", 1, "k1", "application/json", invoices); status != http.StatusOK {
		t.Fatalf("chunk status = %d body %s", status, body)
	}
	status, body := h.post("/v1/ingest/batches/"+ref+"/abort", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("abort status = %d body %s", status, body)
	}
	var receipt Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if receipt.Status != BatchAborted {
		t.Fatalf("status = %q, want %q", receipt.Status, BatchAborted)
	}
	if len(receipt.Proposals) != 0 {
		t.Fatalf("an aborted batch proposed %v", receipt.Proposals)
	}
	counts, err := h.srv.Store().CountProposalsByStatus()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("proposals = %v, want none", counts)
	}
	// The record itself was delivered and stays delivered: the store is
	// append-only and an abort is not a rollback.
	if _, ok := h.srv.Store().Get("invoice", key("0000000417", "0004711")); !ok {
		t.Fatal("the record vanished, which the append-only store cannot honestly do")
	}
}

// TestAssertClosure is the table for the anti-silent-drop invariant. A violation
// is an internal error and never a warning, because the receipt exists precisely
// so that nobody has to trust the twin's arithmetic.
func TestAssertClosure(t *testing.T) {
	cases := []struct {
		name    string
		receipt Receipt
		wantErr bool
	}{
		{
			name: "batch and file agree",
			receipt: Receipt{
				Counts: Counts{Seen: 3, Accepted: 1, AcceptedWithWarning: 1, Rejected: 1},
				Files: []FileReceipt{{Path: "a", ParsedRecords: 3,
					Counts: Counts{Seen: 3, Accepted: 1, AcceptedWithWarning: 1, Rejected: 1}}},
			},
		},
		{
			name: "skipped and quarantined count",
			receipt: Receipt{
				Counts: Counts{Seen: 2, SkippedUnchanged: 1, Quarantined: 1},
				Files: []FileReceipt{{Path: "a", ParsedRecords: 2,
					Counts: Counts{Seen: 2, SkippedUnchanged: 1, Quarantined: 1}}},
			},
		},
		{
			name: "a replay delivers nothing",
			receipt: Receipt{
				Replay: true,
				Counts: Counts{Seen: 0, Replayed: 4},
				Files:  []FileReceipt{{Path: "a", Counts: Counts{Replayed: 4}}},
			},
		},
		{
			name: "a dropped record fails the batch tally",
			receipt: Receipt{
				Counts: Counts{Seen: 3, Accepted: 2},
				Files: []FileReceipt{{Path: "a", ParsedRecords: 3,
					Counts: Counts{Seen: 3, Accepted: 2}}},
			},
			wantErr: true,
		},
		{
			name: "a file tally that does not close",
			receipt: Receipt{
				Counts: Counts{Seen: 2, Accepted: 2},
				Files: []FileReceipt{{Path: "a", ParsedRecords: 2,
					Counts: Counts{Seen: 2, Accepted: 1}}},
			},
			wantErr: true,
		},
		{
			name: "parsed records that do not add up to seen",
			receipt: Receipt{
				Counts: Counts{Seen: 2, Accepted: 2},
				Files: []FileReceipt{{Path: "a", ParsedRecords: 3,
					Counts: Counts{Seen: 2, Accepted: 2}}},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := assertClosure(&tc.receipt)
			if tc.wantErr && err == nil {
				t.Fatal("want a closure violation, got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected closure violation: %v", err)
			}
			if tc.wantErr && err != nil {
				if got := err.Error(); got == "" || !contains([]string{CodeClosureViolation},
					firstWord(got)) {
					t.Fatalf("error does not name the code: %q", got)
				}
			}
		})
	}
}

// TestCanonicalPONumber is the table for the identifier rule of BUILD-SPEC 16.1:
// an all-digit number compares numerically, everything else compares verbatim,
// and cost centers are never normalized at all.
func TestCanonicalPONumber(t *testing.T) {
	cases := []struct{ in, want string }{
		{"4500001234", "4500001234"},
		{"PO-4500001234", "4500001234"},
		{"  4500001234  ", "4500001234"},
		{"0004500001234", "4500001234"},
		{"PO-0004500001234", "4500001234"},
		{"4500-1234", "4500-1234"},
		{"po-4500001234", "po-4500001234"},
		{"000", "0"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := canonicalPONumber(tc.in); got != tc.want {
			t.Errorf("canonicalPONumber(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSplitPOReference asserts the line suffix of BUILD-SPEC 8.3.
func TestSplitPOReference(t *testing.T) {
	cases := []struct{ in, wantNumber, wantLine string }{
		{"4500001234/00010", "4500001234", "00010"},
		{"4500001234", "4500001234", ""},
		{" 4500001234 / 00010 ", "4500001234", "00010"},
	}
	for _, tc := range cases {
		number, line := splitPOReference(tc.in)
		if number != tc.wantNumber || line != tc.wantLine {
			t.Errorf("splitPOReference(%q) = (%q, %q), want (%q, %q)",
				tc.in, number, line, tc.wantNumber, tc.wantLine)
		}
	}
}

// TestConvertQuantity is the table for the unit conversion rules of BUILD-SPEC
// 16.1: the factor-12 carton, the 1000:1 ton, the 1000:3 material that needs
// three decimals and the conversion that would need a fourth.
func TestConvertQuantity(t *testing.T) {
	cases := []struct {
		name    string
		qty     string
		conv    UoMConversion
		want    string
		wantErr bool
	}{
		{name: "carton of twelve", qty: "10.000",
			conv: UoMConversion{AltUoM: "CTN", Numerator: 12, Denominator: 1, BaseUoM: "EA"},
			want: "120.000"},
		{name: "ton to kilogram", qty: "1.500",
			conv: UoMConversion{AltUoM: "TON", Numerator: 1000, Denominator: 1, BaseUoM: "KG"},
			want: "1500.000"},
		{name: "one to one", qty: "3.000",
			conv: UoMConversion{AltUoM: "EA", Numerator: 1, Denominator: 1, BaseUoM: "EA"},
			want: "3.000"},
		{name: "three decimals exactly", qty: "0.003",
			conv: UoMConversion{Material: "MAT-1000-3", AltUoM: "CTN",
				Numerator: 1000, Denominator: 3, BaseUoM: "EA"},
			want: "1.000"},
		{name: "a fourth decimal is unconvertible", qty: "1.000",
			conv: UoMConversion{Material: "MAT-1000-3", AltUoM: "CTN",
				Numerator: 1000, Denominator: 3, BaseUoM: "EA"},
			wantErr: true},
		{name: "a zero denominator is a broken table", qty: "1.000",
			conv:    UoMConversion{AltUoM: "CTN", Numerator: 12, Denominator: 0},
			wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := convertQuantity(mustDec(t, tc.qty), tc.conv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestResolveFormatDispatchesProfiles asserts the profile dispatch, including the
// reference-importer switch.
func TestResolveFormatDispatchesProfiles(t *testing.T) {
	cases := []struct {
		profile, format string
		wantID          string
		wantErr         bool
	}{
		{ProfileCanonical, "csv", "blp-csv-v1", false},
		{ProfileCanonical, "xml", "blp-xml-v1", false},
		{ProfileCanonical, "json", "blp-json-v1", false},
		{ProfileCanonical, "ndjson", "blp-json-v1", false},
		{ProfileCanonical, "txt", "", true},
		{"blp-csv-v1", "", "blp-csv-v1", false},
		{"sap-idoc-4.7", "csv", "", true},
	}
	for _, tc := range cases {
		f, err := ResolveFormat(tc.profile, tc.format)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ResolveFormat(%q, %q) = %v, want an error", tc.profile, tc.format, f.ID())
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveFormat(%q, %q): %v", tc.profile, tc.format, err)
			continue
		}
		if f.ID() != tc.wantID {
			t.Errorf("ResolveFormat(%q, %q) = %q, want %q", tc.profile, tc.format, f.ID(), tc.wantID)
		}
	}
}

// TestPointerLocator asserts the pointer shapes of the four wire formats.
func TestPointerLocator(t *testing.T) {
	cases := []struct {
		name   string
		format string
		body   string
		want   string
	}{
		{name: "json array", format: "json", body: `[{"a":1},{"a":2}]`, want: "/1/vat_amount"},
		{name: "json envelope", format: "json", body: `{"records":[{"a":1},{"a":2}]}`,
			want: "/records/1/vat_amount"},
		{name: "json single object", format: "json", body: `{"a":1}`, want: "/vat_amount"},
		{name: "ndjson", format: "ndjson", body: "{\"a\":1}\n{\"a\":2}\n", want: "2:/vat_amount"},
		{name: "xml", format: "xml", body: "<records/>", want: "/records/record[2]/vat_amount"},
		{name: "csv", format: "csv", body: "gross_amount,vat_amount\n1,2\n", want: "3:2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locate := pointerLocator(formatOf(tc.format), []byte(tc.body), dialectForTest())
			if got := locate(2, "vat_amount"); got != tc.want {
				t.Fatalf("pointer = %q, want %q", got, tc.want)
			}
		})
	}
}

// firstWord returns the first space-separated word of a string.
func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == ' ' {
			return s[:i]
		}
	}
	return s
}

// mustDec parses a decimal for a test table.
func mustDec(t *testing.T, s string) model.Decimal {
	t.Helper()
	d, err := model.ParseDecimal(s)
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", s, err)
	}
	return d
}

// formatOf maps a short format name to the wire format.
func formatOf(name string) httpx.Format {
	switch name {
	case "ndjson":
		return httpx.FormatNDJSON
	case "xml":
		return httpx.FormatXML
	case "csv":
		return httpx.FormatCSV
	default:
		return httpx.FormatJSON
	}
}

// dialectForTest is the canonical CSV dialect.
func dialectForTest() httpx.CSVDialect { return httpx.DefaultCSVDialect() }

// TestBodyOverEightMiBIsBatchTooLarge asserts the second published cap: the twin
// refuses an oversized chunk rather than truncating it, which is the same failure
// the file channel's checksum exists to catch.
func TestBodyOverEightMiBIsBatchTooLarge(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-huge"))
	// One JSON array whose single record carries a padding field just over the
	// limit, so the body is too large without the record count being.
	padding := strings.Repeat("x", httpx.MaxRequestBodyBytes)
	body := []byte(`[{"supplier_number":"0000417","name":"` + padding + `"}]`)
	status, out := h.rawChunk(ref, "supplier", 1, "huge", httpx.MediaJSON, body)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body %s, want 413", status, out)
	}
	if !strings.Contains(string(out), CodeBatchTooLarge) {
		t.Fatalf("body = %s, want %s", out, CodeBatchTooLarge)
	}
	if _, ok := h.srv.Store().Get("supplier", "0000417"); ok {
		t.Fatal("an oversized chunk was applied")
	}
}

// TestReferenceImporterIsPreferred asserts the switch of BUILD-SPEC 10 that
// decouples the pipeline score from the candidate's parser: with
// MINIBLP_REFERENCE_IMPORTERS=1 the legacy profile resolves to the format
// registered under the reference id, and without it that format is invisible.
func TestReferenceImporterIsPreferred(t *testing.T) {
	// Registered once, because the registry is process wide and -count=2 runs
	// this test twice in the same process.
	if _, ok := importer.Lookup(ProfileKredExpReference); !ok {
		importer.Register(referenceStub{})
	}

	if _, err := ResolveFormat(ProfileKredExp, "txt"); err == nil {
		t.Fatal("the reference importer resolved without the environment switch")
	}
	t.Setenv(EnvReferenceImporters, "1")
	f, err := ResolveFormat(ProfileKredExp, "txt")
	if err != nil {
		t.Fatalf("with the switch on: %v", err)
	}
	if f.ID() != ProfileKredExpReference {
		t.Fatalf("resolved %q, want %q", f.ID(), ProfileKredExpReference)
	}
	// The switch changes nothing else: the profile a receipt and a provenance
	// entry report stays the declared one.
	if ProfileKredExp == ProfileKredExpReference {
		t.Fatal("the reference id must be distinct from the declared profile")
	}
}

// referenceStub stands in for our reference implementation of the legacy
// importer. It parses nothing: the test is about resolution, not about parsing.
type referenceStub struct{}

// ID returns the reference id.
func (referenceStub) ID() string { return ProfileKredExpReference }

// Detect never claims a file.
func (referenceStub) Detect(head []byte, opt importer.Options) bool { return false }

// Parse returns an empty accepted result.
func (referenceStub) Parse(ctx context.Context, raw []byte, opt importer.Options) (*importer.Result, error) {
	if ctx == nil {
		return nil, importer.ErrNilContext
	}
	b := importer.NewBuilder(ProfileKredExpReference, opt)
	b.SetSourceBytes(raw)
	return b.Result(), nil
}

// TestQuotaExhaustedIsNotRetriable asserts that the hard per-run quota is wired
// through and that its 503 says so: a client that retries a retriable:false
// answer is making a mistake the grader scores.
func TestQuotaExhaustedIsNotRetriable(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Quota = 2 })
	// The token request already spent one unit.
	h.get("/v1/outbox/proposals")
	status, body := h.get("/v1/outbox/proposals")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body %s, want 503", status, body)
	}
	var e struct {
		Code      string `json:"code"`
		Retriable bool   `json:"retriable"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body: %v", err)
	}
	if e.Code != httpx.CodeQuotaExhausted || e.Retriable {
		t.Fatalf("error = %+v, want a non-retriable %s", e, httpx.CodeQuotaExhausted)
	}
	// The admin surface still answers, because it is free of the quota.
	if status, _ := h.adminGet("/admin/v1/metrics"); status != http.StatusOK {
		t.Fatalf("admin metrics under an exhausted quota: status %d", status)
	}
}

// TestUnsafeBatchIDIsRefused asserts that a client cannot choose a path. A batch
// id and a run id both become directory names, so a traversal in either is a
// manifest defect and not a creative file name.
func TestUnsafeBatchIDIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []string{"../escape", "a/b", `a\b`, ".hidden", "..", strings.Repeat("x", 129)} {
		status, body := h.openBatch(restManifest(id))
		if status != http.StatusBadRequest {
			t.Fatalf("batch_id %q: status %d body %s, want 400", id, status, body)
		}
		if !strings.Contains(body, CodeManifestInvalid) {
			t.Fatalf("batch_id %q: body %s", id, body)
		}
	}
	m := restManifest("ok-batch")
	m["run_id"] = "../escape"
	if status, body := h.openBatch(m); status != http.StatusBadRequest {
		t.Fatalf("unsafe run_id: status %d body %s, want 400", status, body)
	}
}

// TestRESTReceiptIsWrittenToDisk asserts the symmetry of the two channels: a
// committed REST batch leaves the same receipt directory a file batch does, DONE
// included.
func TestRESTReceiptIsWrittenToDisk(t *testing.T) {
	h := newHarness(t, nil)
	_, ref := h.openBatch(restManifest("batch-receipt-rest"))
	h.chunk(ref, "supplier", 1, "k1", []any{supplierRecord("0000417", "A", false)})
	if status, _ := h.commit(ref); status != http.StatusOK {
		t.Fatalf("commit status = %d", status)
	}
	dir := filepath.Join(h.inboxDir, dirReceipts, "batch-receipt-rest")
	for _, name := range []string{receiptJSON, recordsCSV, rejectsCSV, doneFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("receipt file %s: %v", name, err)
		}
	}
	buf, err := os.ReadFile(filepath.Join(dir, receiptJSON))
	if err != nil {
		t.Fatalf("receipt.json: %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(buf, &receipt); err != nil {
		t.Fatalf("receipt.json: %v", err)
	}
	if receipt.Channel != ChannelREST || !receipt.ClosureOK {
		t.Fatalf("receipt = %+v", receipt)
	}
	// A receipt directory the twin wrote is the twin's own, so the next scan
	// does not report it as a foreign write.
	if rep := h.scan(); len(rep.ForeignWrites) != 0 {
		t.Fatalf("foreign writes = %v", rep.ForeignWrites)
	}
}
