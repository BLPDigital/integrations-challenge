package miniblp

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
	"github.com/fatjonblp/coding_challange_integrations/internal/store"
)

// outboxCursorDataset is the dataset a mirrored outbox cursor is signed for, so
// a cursor cannot be replayed against another paginated surface.
const outboxCursorDataset = store.DatasetProposal

// A ProposalRow is the wire shape of one proposal on the outbox.
//
// It is flat and carries scalars only, for two reasons. A CSV or XML consumer
// cannot read a nested money object out of a cell, and the row has to be
// posting-ready: everything the ERP's document POST needs is here, so a
// connector never has to fetch the invoice a second time to post it. That is
// also what makes postings.csv a one-line-per-document audit chain.
type ProposalRow struct {
	ProposalID            string `json:"proposal_id"`
	InvoiceKey            string `json:"invoice_key"`
	SupplierNumber        string `json:"supplier_number"`
	SupplierInvoiceNumber string `json:"supplier_invoice_number"`
	InvoiceVersion        int    `json:"invoice_version"`
	ProposalContentHash   string `json:"proposal_content_hash"`
	Status                string `json:"status"`
	CompanyCode           string `json:"company_code"`
	DocumentType          string `json:"document_type"`
	DocumentDate          string `json:"document_date"`
	PostingDate           string `json:"posting_date"`
	MatchedPO             string `json:"matched_po"`
	MatchedCostCenter     string `json:"matched_cost_center"`
	Currency              string `json:"currency"`
	GrossAmount           string `json:"gross_amount"`
	GrossAmountMinor      int64  `json:"gross_amount_minor"`
	VATAmount             string `json:"vat_amount"`
	SourceCurrency        string `json:"source_currency"`
	SourceGrossAmount     string `json:"source_gross_amount"`
	FxRate                string `json:"fx_rate"`
	FxRateFactor          int64  `json:"fx_rate_factor"`
	VATCode               string `json:"vat_code"`
	PaymentTermsDays      int    `json:"payment_terms_days"`
	DiscountRaw           string `json:"discount_raw"`
	CreatedInRun          string `json:"created_in_run"`
	CreatedSeq            int64  `json:"created_seq"`
	Attempts              int    `json:"attempts"`
	Warnings              string `json:"warnings"`
	ERPDocumentNumber     string `json:"erp_document_number"`
}

// outboxColumns fixes the header and the column order of a CSV outbox page. It
// is the declaration order of ProposalRow, so the four mirrored formats carry
// the same fields in the same order.
var outboxColumns = []string{
	"proposal_id", "invoice_key", "supplier_number", "supplier_invoice_number",
	"invoice_version", "proposal_content_hash", "status", "company_code",
	"document_type", "document_date", "posting_date", "matched_po",
	"matched_cost_center", "currency", "gross_amount", "gross_amount_minor",
	"vat_amount", "source_currency", "source_gross_amount", "fx_rate",
	"fx_rate_factor", "vat_code", "payment_terms_days", "discount_raw",
	"created_in_run", "created_seq", "attempts", "warnings", "erp_document_number",
}

// proposalRow renders one proposal as an outbox row, reading the invoice it was
// made from for the fields a posting needs and the proposal does not carry.
//
// The discount field travels verbatim, as DiscountRaw, and is never interpreted:
// the customer's own specification says both "discount amount" and "discount
// rate in percent" about the same field, and converting it would change what a
// supplier is paid.
func (s *Server) proposalRow(p store.Proposal) ProposalRow {
	row := ProposalRow{
		ProposalID:          p.ProposalID,
		InvoiceKey:          p.InvoiceKey,
		InvoiceVersion:      p.InvoiceVersion,
		ProposalContentHash: p.ProposalContentHash,
		Status:              p.Status,
		MatchedPO:           p.MatchedPO,
		MatchedCostCenter:   p.MatchedCostCenter,
		Currency:            p.Amount.Currency,
		GrossAmount:         p.Amount.Decimal().String(),
		GrossAmountMinor:    p.Amount.AmountMinor,
		SourceCurrency:      p.SourceAmount.Currency,
		SourceGrossAmount:   p.SourceAmount.Decimal().String(),
		FxRateFactor:        p.FxRateFactor,
		CreatedInRun:        p.CreatedInRun,
		CreatedSeq:          p.CreatedSeq,
		Attempts:            p.Attempts,
		// Comma-joined, which is what docs/twin-api.md publishes and therefore
		// what a client splits on. It was space-joined once, and a consumer
		// following the documentation got one blob containing every code.
		Warnings: strings.Join(p.Warnings, ","),
	}
	if p.FxRateFactor != 0 {
		row.FxRate = p.FxRateUsed.String()
	}
	if p.Ack != nil {
		row.ERPDocumentNumber = p.Ack.ExternalDocumentNumber
	}
	if parts := model.SplitKey(p.InvoiceKey); len(parts) == 2 {
		row.SupplierNumber = parts[0]
		row.SupplierInvoiceNumber = parts[1]
	}
	if rev, ok := s.st.Get(model.DatasetInvoice.String(), p.InvoiceKey); ok {
		var inv model.APInvoice
		if err := decodePayload(rev.Payload, &inv); err == nil {
			row.CompanyCode = inv.CompanyCode
			row.DocumentType = inv.DocumentType
			row.DocumentDate = inv.DocumentDate
			row.VATAmount = inv.VATAmount.String()
			row.VATCode = inv.VATCode
			row.PaymentTermsDays = inv.PaymentTermsDays
			row.DiscountRaw = inv.DiscountRaw
			if date, err := postingDateOf(inv); err == nil {
				row.PostingDate = date
			}
		}
	}
	return row
}

// outboxStatusAll is the documented value that disables the status filter.
const outboxStatusAll = "all"

// outboxQuery parses the query parameters of GET /v1/outbox/proposals.
func (s *Server) outboxQuery(r *http.Request) (status string, limit int, cur httpx.Cursor, err error) {
	q := r.URL.Query()
	status = q.Get("status")
	if status == "" {
		status = store.ProposalPending
	}
	limit = MaxOutboxPage
	if raw := q.Get("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			return status, limit, cur, httpx.MalformedBody("limit_invalid",
				"limit is a positive whole number")
		}
		limit = n
	}
	if limit > MaxOutboxPage {
		limit = MaxOutboxPage
	}
	cur, err = httpx.ParseCursor(q.Get("cursor"), s.cursorSecret)
	if err != nil {
		return status, limit, httpx.Cursor{}, err
	}
	return status, limit, cur, nil
}

// outboxPageSize estimates how many records an outbox page will return, which is
// the per-record half of its virtual cost.
//
// It is the smaller of the requested limit and the number of proposals in the
// requested status, which is a pure function of the request and of the twin's
// state and needs no scan. A page that returns fewer records than the estimate -
// because the cursor is already past most of them - is charged for the
// estimate, which is documented and deliberate: a client cannot lower its cost
// by paging from the far end.
func (s *Server) outboxPageSize(status string, limit int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts, err := s.st.CountProposalsByStatus()
	if err != nil {
		return 0
	}
	// "all" is the documented way to disable the filter, and outboxQuery has
	// already turned the empty status into "pending", so the sum has to key off
	// "all" as well: looking up the literal "all" in a per-status map charged a
	// documented query zero records, which is a page the client gets for free and
	// a budget the scenario cannot account for.
	n := 0
	if status == "" || status == outboxStatusAll {
		for _, v := range counts {
			n += v
		}
	} else {
		n = counts[status]
	}
	if n > limit {
		n = limit
	}
	return n
}

// handleOutboxProposals serves GET /v1/outbox/proposals: one stable page of
// proposals in created_seq order.
//
// Proposal ids are zero-padded, so ascending id order is ascending created_seq
// order and the cursor is a position in both. The order is stable under
// concurrent writes because a proposal's id never changes: a page boundary
// cannot skip a proposal that was written after the previous page was served,
// which is what "resume, not restart" needs from a paginated outbox.
func (s *Server) handleOutboxProposals(w http.ResponseWriter, r *http.Request) {
	status, limit, cur, err := s.outboxQuery(r)
	if err != nil {
		httpx.Fail(w, httpx.AsError(err))
		return
	}
	s.mu.Lock()
	rows, next, more, err := s.outboxPage(status, limit, cur)
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
	ctx := httpx.WithCSVDialect(r.Context(), outboxDialect())
	if err := httpx.WriteNegotiated(w, r.WithContext(ctx), payload); err != nil {
		return
	}
}

// outboxDialect is the CSV dialect of an outbox page: the canonical dialect with
// the column order fixed, so a CSV consumer can rely on positions as well as on
// names.
func outboxDialect() httpx.CSVDialect {
	d := httpx.DefaultCSVDialect()
	d.Columns = outboxColumns
	return d
}

// outboxPage returns one page of proposals. The caller holds the server lock.
func (s *Server) outboxPage(status string, limit int, cur httpx.Cursor) ([]ProposalRow, string, bool, error) {
	var selected []store.Proposal
	err := s.st.ScanProposals(func(p store.Proposal) bool {
		if status != "" && status != outboxStatusAll && p.Status != status {
			return true
		}
		if !cur.IsZero() && p.ProposalID <= cur.LastKey {
			return true
		}
		selected = append(selected, p)
		return len(selected) <= limit
	})
	if err != nil {
		return nil, "", false, err
	}
	more := false
	if len(selected) > limit {
		selected = selected[:limit]
		more = true
	}
	rows := make([]ProposalRow, 0, len(selected))
	for _, p := range selected {
		rows = append(rows, s.proposalRow(p))
	}
	next := ""
	if more && len(selected) > 0 {
		last := selected[len(selected)-1]
		next = httpx.Cursor{
			Dataset:       outboxCursorDataset,
			LastKey:       last.ProposalID,
			ChangeSeqHigh: last.CreatedSeq,
		}.Encode(s.cursorSecret)
	}
	return rows, next, more, nil
}

// emitOutbox mirrors the pending proposals to outboxDir/<run_id> as
// proposals-NNNN in all four wire formats, with DONE created last.
//
// The mirror exists so the outbox is readable with zero HTTP requests: a
// connector that has spent its request budget, or a reviewer with a shell, can
// still see exactly what the twin proposes. The bytes are produced by the same
// negotiation code the HTTP endpoint uses, so a page on disk and the same page
// over HTTP are byte-identical.
//
// A previous emission's pages are removed first, and DONE is removed before
// anything else is written: a reader that finds DONE finds a complete, current
// set, and one that finds no DONE knows to wait.
func (s *Server) emitOutbox(runID string) error {
	if runID == "" {
		runID = s.cfg.RunID
	}
	dir := filepath.Join(s.cfg.OutboxDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.own("outbox/" + runID)

	if err := os.Remove(filepath.Join(dir, doneFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "proposals-") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}

	var pending []store.Proposal
	if err := s.st.ScanProposals(func(p store.Proposal) bool {
		if p.Status == store.ProposalPending {
			pending = append(pending, p)
		}
		return true
	}); err != nil {
		return err
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].CreatedSeq < pending[j].CreatedSeq })

	pages := (len(pending) + MaxOutboxPage - 1) / MaxOutboxPage
	if pages == 0 {
		pages = 1
	}
	for page := 0; page < pages; page++ {
		lo := page * MaxOutboxPage
		hi := lo + MaxOutboxPage
		if hi > len(pending) {
			hi = len(pending)
		}
		rows := make([]ProposalRow, 0, hi-lo)
		for _, p := range pending[lo:hi] {
			rows = append(rows, s.proposalRow(p))
		}
		payload := map[string]any{
			"records":  rows,
			"returned": len(rows),
			"has_more": hi < len(pending),
			"page":     page + 1,
			"run_id":   runID,
		}
		for _, format := range []httpx.Format{httpx.FormatCSV, httpx.FormatXML, httpx.FormatJSON, httpx.FormatNDJSON} {
			buf, err := renderRecords(format, payload, outboxDialect())
			if err != nil {
				return err
			}
			name := fmt.Sprintf("proposals-%04d.%s", page+1, extensionOf(format))
			if err := writeFileAtomic(filepath.Join(dir, name), buf); err != nil {
				return err
			}
		}
	}
	return writeFileAtomic(filepath.Join(dir, doneFile), nil)
}

// extensionOf returns the file extension of a wire format.
func extensionOf(f httpx.Format) string {
	switch f {
	case httpx.FormatNDJSON:
		return "ndjson"
	case httpx.FormatXML:
		return "xml"
	case httpx.FormatCSV:
		return "csv"
	default:
		return "json"
	}
}

// renderRecords renders a payload in one wire format, exactly as the HTTP
// surface would.
//
// It drives httpx.WriteNegotiatedStatus against an in-memory response, rather
// than reimplementing four serializers, precisely so that a mirrored page and
// the same page fetched over HTTP cannot drift apart: there is one encoder and
// the file channel and the REST channel both go through it.
func renderRecords(format httpx.Format, payload any, dial httpx.CSVDialect) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, "/v1/outbox/proposals", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", format.MediaType())
	req = req.WithContext(httpx.WithCSVDialect(req.Context(), dial))
	rec := &captureWriter{header: http.Header{}}
	if err := httpx.WriteNegotiatedStatus(rec, req, http.StatusOK, payload); err != nil {
		return nil, err
	}
	return rec.body.Bytes(), nil
}

// captureWriter is an http.ResponseWriter that keeps the response in memory. It
// exists so renderRecords can reuse the negotiation layer without a network or a
// test server.
type captureWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

// Header implements http.ResponseWriter.
func (c *captureWriter) Header() http.Header { return c.header }

// Write implements http.ResponseWriter.
func (c *captureWriter) Write(p []byte) (int, error) { return c.body.Write(p) }

// WriteHeader implements http.ResponseWriter.
func (c *captureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
