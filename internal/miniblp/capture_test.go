package miniblp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// seedS0 loads the S0 night and returns what the seed reported.
func (h *harness) seedS0() SeedResult {
	h.t.Helper()
	status, body := h.adminPost("/admin/v1/seed", []byte(`{"scenario":"S0","seed":20260416}`))
	if status != http.StatusOK {
		h.t.Fatalf("seed: status %d body %s", status, body)
	}
	var res SeedResult
	if err := json.Unmarshal(body, &res); err != nil {
		h.t.Fatalf("seed result: %v", err)
	}
	return res
}

// match calls POST /v1/match and decodes the answer.
func (h *harness) match(runID string) MatchResult {
	h.t.Helper()
	status, body := h.post("/v1/match", []byte(`{"run_id":"`+runID+`"}`), nil)
	if status != http.StatusOK {
		h.t.Fatalf("match: status %d body %s", status, body)
	}
	var res MatchResult
	if err := json.Unmarshal(body, &res); err != nil {
		h.t.Fatalf("match result: %v", err)
	}
	return res
}

func TestSeedCapturesTheInvoicesAndHoldsTheirMatching(t *testing.T) {
	h := newHarness(t, nil)
	res := h.seedS0()
	if len(res.CapturedBatches) != 2 || res.CapturedInvoices == 0 || res.FxRates == 0 {
		t.Fatalf("seed result = %+v, want two capture batches, invoices and rates", res)
	}
	if got := h.srv.Store().Count(model.DatasetInvoice.String()); got != res.CapturedInvoices {
		t.Fatalf("invoices stored = %d, reported %d", got, res.CapturedInvoices)
	}
	if got := h.srv.Store().Count(model.DatasetFxRate.String()); got != res.FxRates {
		t.Fatalf("rates stored = %d, reported %d", got, res.FxRates)
	}
	if n, _ := h.srv.Store().CountExceptions(""); n != 0 {
		t.Fatalf("%d exceptions before POST /v1/match, want none", n)
	}
	if counts, _ := h.srv.Store().CountProposalsByStatus(); len(counts) != 0 {
		t.Fatalf("proposals before POST /v1/match: %v", counts)
	}
	for _, id := range res.CapturedBatches {
		if !strings.HasPrefix(id, "capture-KRED_") {
			t.Errorf("capture batch id %q", id)
		}
		if b := h.srv.batches[id]; b == nil || b.SourceSystem != SourceSystemCapture {
			t.Errorf("batch %s does not say it came from BLP's capture", id)
		}
	}
}

func TestMatchRunsOnceAgainstTheMasterDataHeldNow(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedS0()
	// No supplier was delivered, so every captured invoice is an exception and none is proposed.
	first := h.match("run_match")
	if first.InvoicesMatched != seeded.CapturedInvoices || first.ProposalsEmitted != 0 ||
		first.ExceptionsOpened != seeded.CapturedInvoices {
		t.Fatalf("first match = %+v, want %d invoices, 0 proposals, one exception each", first, seeded.CapturedInvoices)
	}
	if second := h.match("run_match"); second != (MatchResult{}) {
		t.Fatalf("second match = %+v, want nothing left to match", second)
	}
	err := h.srv.Store().ScanExceptions(store.ExceptionOpen, func(e store.Exception) bool {
		if e.SourceBatchID == "" || e.SourceFileOrChunk == "" || e.SourceLineOrOrdinal == "" {
			t.Fatalf("exception %s carries no source: %+v", e.Key(), e)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExceptionsPagesTheOpenQueue(t *testing.T) {
	h := newHarness(t, nil)
	seeded := h.seedS0()
	h.match("run_match")
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/v1/exceptions?limit=30"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, body := h.get(path)
		if status != http.StatusOK {
			t.Fatalf("exceptions: status %d body %s", status, body)
		}
		var page struct {
			Records    []ExceptionRow `json:"records"`
			HasMore    bool           `json:"has_more"`
			NextCursor string         `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range page.Records {
			k := store.ExceptionKey(r.SubjectKey, r.Code)
			if seen[k] {
				t.Fatalf("exception %q served twice", k)
			}
			seen[k] = true
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != seeded.CapturedInvoices || pages < 2 {
		t.Fatalf("%d exceptions over %d pages, want %d over several", len(seen), pages, seeded.CapturedInvoices)
	}
	status, body, _ := h.request(http.MethodGet, "/v1/exceptions?limit=1", nil, map[string]string{"Accept": "text/csv"})
	if status != http.StatusOK || !strings.HasPrefix(string(body), strings.Join(exceptionColumns, ",")) {
		t.Fatalf("csv page: status %d body %.200s", status, body)
	}
}
