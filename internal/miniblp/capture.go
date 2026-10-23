package miniblp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	_ "github.com/fatjonblp/coding_challange_integrations/internal/importer/kredexp"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// SourceSystemCapture is the source system of every batch BLP's own invoice capture loads.
const SourceSystemCapture = "blp-capture"

// MaxExceptionsPage is the largest page GET /v1/exceptions returns.
const MaxExceptionsPage = 500

// exceptionsCursorDataset is the dataset an exception queue cursor is signed for.
const exceptionsCursorDataset = store.DatasetException

// NightResult is what POST /admin/v1/seed and POST /admin/v1/advance report about the invoices and rates they loaded.
type NightResult struct {
	FxRates          int      `json:"fx_rates"`
	CapturedBatches  []string `json:"captured_batches"`
	CapturedInvoices int      `json:"captured_invoices"`
}

// loadNight loads what other systems put into the twin before the connector's run: the rate table and BLP's own invoice capture.
func (s *Server) loadNight(ds *seed.Dataset, prov store.Provenance) (NightResult, error) {
	res := NightResult{CapturedBatches: []string{}}
	s.mu.Lock()
	for _, r := range ds.ResolvedRates() {
		if _, err := s.st.Apply(store.Revision{
			Dataset: model.DatasetFxRate.String(), Key: r.Key(), Payload: r, Provenance: prov,
		}); err != nil {
			s.mu.Unlock()
			return res, err
		}
		res.FxRates++
	}
	var staged []string
	for _, del := range ds.Deliveries {
		id := captureBatchID(del.Name)
		if _, seen := s.batches[id]; seen {
			continue
		}
		if err := s.stageCapture(id, del); err != nil {
			s.mu.Unlock()
			return res, err
		}
		s.captured[id] = true
		staged = append(staged, id)
	}
	s.mu.Unlock()
	if len(staged) == 0 {
		return res, nil
	}
	rep, err := s.Scan()
	if err != nil {
		return res, err
	}
	for _, b := range rep.Batches {
		if !s.isCaptured(b.BatchID) {
			continue
		}
		if b.Status == BatchRejected {
			return res, fmt.Errorf("miniblp: capture batch %s was rejected: %s", b.BatchID, strings.Join(b.Codes, " "))
		}
		res.CapturedBatches = append(res.CapturedBatches, b.BatchID)
		res.CapturedInvoices += b.Counts.Accepted + b.Counts.AcceptedWithWarning
	}
	return res, nil
}

// isCaptured reports under the lock whether a batch came from BLP's own capture.
func (s *Server) isCaptured(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captured[id]
}

// captureBatchID returns the batch id a legacy delivery is captured under, stable so a re-capture is a replay.
func captureBatchID(fileName string) string {
	return "capture-" + strings.TrimSuffix(fileName, filepath.Ext(fileName))
}

// stageCapture writes one delivery into the inbox by the atomic drop protocol, data file first and manifest last.
func (s *Server) stageCapture(id string, del seed.LegacyDelivery) error {
	dir := filepath.Join(s.cfg.InboxDir, dirIncoming, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, del.Name), del.Bytes, 0o644); err != nil {
		return err
	}
	count := del.KopfCount
	m := manifest{
		ManifestVersion: "1",
		BatchID:         id,
		Producer:        SourceSystemCapture,
		RunID:           s.cfg.RunID,
		SourceSystem:    SourceSystemCapture,
		Mode:            ModeUpsert,
		OnError:         OnErrorContinue,
		Files: []manifestFile{{
			Path:        del.Name,
			Format:      "txt",
			Profile:     ProfileKredExp,
			Encoding:    "windows-1252",
			RecordCount: &count,
			SHA256:      model.Sha256Hex(del.Bytes),
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), raw, 0o644)
}

// MatchResult is the body of POST /v1/match.
type MatchResult struct {
	InvoicesMatched  int `json:"invoices_matched"`
	ProposalsEmitted int `json:"proposals_emitted"`
	ExceptionsOpened int `json:"exceptions_opened"`
}

// handleMatch serves POST /v1/match: it matches every captured invoice still waiting, against the master data held now.
func (s *Server) handleMatch(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	var req struct {
		RunID string `json:"run_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httpx.Fail(w, httpx.MalformedBody("json_invalid", "the body is a JSON object with an optional run_id"))
			return
		}
	}
	if req.RunID != "" && !isSafeName(req.RunID) {
		httpx.Fail(w, httpx.MalformedBody("run_id_invalid",
			"run_id is 1 to 128 characters of letters, digits, dot, dash or underscore"))
		return
	}
	s.mu.Lock()
	res, err := s.matchDeferred(req.RunID)
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// matchDeferred matches the deferred capture batches in the order they were captured. The caller holds the lock.
func (s *Server) matchDeferred(runID string) (MatchResult, error) {
	var res MatchResult
	for len(s.deferred) > 0 {
		b := s.deferred[0]
		proposals, exceptions := len(b.Proposals), len(b.Exceptions)
		if err := s.matchBatchInRun(b, runID); err != nil {
			return res, err
		}
		res.InvoicesMatched += len(b.changed())
		res.ProposalsEmitted += len(b.Proposals) - proposals
		res.ExceptionsOpened += len(b.Exceptions) - exceptions
		s.deferred = s.deferred[1:]
	}
	return res, nil
}

// An ExceptionRow is the wire shape of one open exception on GET /v1/exceptions.
type ExceptionRow struct {
	SubjectKey          string `json:"subject_key"`
	SubjectType         string `json:"subject_type"`
	Stage               string `json:"stage"`
	Code                string `json:"code"`
	Field               string `json:"field"`
	Message             string `json:"message"`
	SourceBatchID       string `json:"source_batch_id"`
	SourceFileOrChunk   string `json:"source_file_or_chunk"`
	SourceLineOrOrdinal string `json:"source_line_or_ordinal"`
}

// exceptionColumns fixes the CSV column order of an exception page, the declaration order of ExceptionRow.
var exceptionColumns = []string{
	"subject_key", "subject_type", "stage", "code", "field", "message",
	"source_batch_id", "source_file_or_chunk", "source_line_or_ordinal",
}

// exceptionsQuery parses the query parameters of GET /v1/exceptions.
func (s *Server) exceptionsQuery(r *http.Request) (limit int, cur httpx.Cursor, err error) {
	q := r.URL.Query()
	limit = MaxExceptionsPage
	if raw := q.Get("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			return limit, cur, httpx.MalformedBody("limit_invalid", "limit is a positive whole number")
		}
		limit = n
	}
	if limit > MaxExceptionsPage {
		limit = MaxExceptionsPage
	}
	cur, err = httpx.ParseCursor(q.Get("cursor"), s.cursorSecret)
	if err != nil {
		return limit, httpx.Cursor{}, err
	}
	return limit, cur, nil
}

// exceptionsPageSize is the per-record half of a page's cost: the smaller of the limit and the open queue.
func (s *Server) exceptionsPageSize(limit int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.st.CountExceptions(store.ExceptionOpen)
	if err != nil {
		return 0
	}
	if n > limit {
		n = limit
	}
	return n
}

// handleExceptions serves GET /v1/exceptions: one page of the open exception queue in key order.
func (s *Server) handleExceptions(w http.ResponseWriter, r *http.Request) {
	limit, cur, err := s.exceptionsQuery(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	s.mu.Lock()
	rows, next, more, err := s.exceptionsPage(limit, cur)
	s.mu.Unlock()
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	payload := map[string]any{
		"records":     rows,
		"returned":    len(rows),
		"has_more":    more,
		"next_cursor": next,
	}
	d := httpx.DefaultCSVDialect()
	d.Columns = exceptionColumns
	ctx := httpx.WithCSVDialect(r.Context(), d)
	_ = httpx.WriteNegotiated(w, r.WithContext(ctx), payload)
}

// exceptionsPage returns one page of open exceptions after the cursor. The caller holds the lock.
func (s *Server) exceptionsPage(limit int, cur httpx.Cursor) ([]ExceptionRow, string, bool, error) {
	var selected []store.Exception
	err := s.st.ScanExceptions(store.ExceptionOpen, func(e store.Exception) bool {
		if !cur.IsZero() && e.Key() <= cur.LastKey {
			return true
		}
		selected = append(selected, e)
		return len(selected) <= limit
	})
	if err != nil {
		return nil, "", false, err
	}
	more := len(selected) > limit
	if more {
		selected = selected[:limit]
	}
	rows := make([]ExceptionRow, 0, len(selected))
	for _, e := range selected {
		rows = append(rows, ExceptionRow{
			SubjectKey: e.SubjectKey, SubjectType: e.SubjectType, Stage: e.Stage, Code: e.Code,
			Field: e.Field, Message: e.Message, SourceBatchID: e.SourceBatchID,
			SourceFileOrChunk: e.SourceFileOrChunk, SourceLineOrOrdinal: e.SourceLineOrOrdinal,
		})
	}
	next := ""
	if more && len(selected) > 0 {
		next = httpx.Cursor{Dataset: exceptionsCursorDataset, LastKey: selected[len(selected)-1].Key()}.Encode(s.cursorSecret)
	}
	return rows, next, more, nil
}
