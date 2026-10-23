package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/miniblp"
)

// outcomeFilters is the outcome vocabulary of a receipt, in the order the
// closure invariant sums them, so the filter row on a batch reads like the
// invariant it belongs to.
var outcomeFilters = []string{
	miniblp.OutcomeAccepted,
	miniblp.OutcomeAcceptedWithWarning,
	miniblp.OutcomeRejected,
	miniblp.OutcomeSkippedUnchanged,
	miniblp.OutcomeQuarantined,
	miniblp.OutcomeReplayed,
}

// handleBatches lists every batch the twin has seen, newest first.
func (u *UI) handleBatches(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	view := &ListView{}
	p := u.page(r, "Batches", "Batches and receipts",
		"One row per delivery, on either channel, with the status its receipt reports.", view)
	batches, err := u.batches(t, r)
	if err != nil {
		p.Notices = append(p.Notices, "The batches could not be read: "+err.Error())
		view.Sections = append(view.Sections, Section{
			Title: "Batches",
			Table: &Table{Headers: batchHeaders, Empty: "no batch could be read"},
		})
		u.render(w, r, "list", p)
		return
	}
	view.Sections = append(view.Sections, Section{
		Title: "Batches",
		Note:  "Most recent first. Follow a batch for its per-file results and per-record outcomes.",
		Table: u.batchTable(lastN(batches, len(batches))),
	})
	u.render(w, r, "list", p)
}

// batchHeaders is the batch list's columns.
var batchHeaders = []string{
	"Batch", "Channel", "Status", "Scan", "Seen", "Accepted", "Rejected",
	"Skipped", "Quarantined", "Replayed", "Files", "Codes",
}

// batchTable renders a batch list.
func (u *UI) batchTable(batches []batchSummary) *Table {
	rows := make([]Row, 0, len(batches))
	for _, b := range batches {
		c := b.Counts
		// A partially accepted batch has no status of its own worth coloring,
		// so its tally decides: any reject reddens the chip.
		tone := toneForBatchStatus(b.Status)
		if tone == ToneNone {
			tone = toneForCounts(c)
		}
		rows = append(rows, Row{Cells: []Cell{
			link(b.ID, u.href("/batches/"+b.ID, nil)),
			text(b.Channel),
			chip(b.Status, tone),
			num64(b.Scan),
			num(c.Seen),
			num(c.Accepted + c.AcceptedWithWarning),
			num(c.Rejected),
			num(c.SkippedUnchanged),
			num(c.Quarantined),
			num(c.Replayed),
			num(len(b.Files)),
			codesCell(b.Codes),
		}})
	}
	return &Table{
		Headers: batchHeaders,
		Rows:    rows,
		Empty:   "no batch has reached this twin yet",
	}
}

// codesCell renders a code list, red as soon as there is one: a batch-level code
// is always a finding.
func codesCell(codes []string) Cell {
	if len(codes) == 0 {
		return text("")
	}
	return Cell{Text: joinCodes(sortStrings(codes)), Tone: ToneRed, Wrap: true}
}

// handleBatch renders one batch: the manifest facts, the per-file results, the
// receipt as the twin wrote it, and the per-record outcomes with their codes,
// filterable by outcome.
func (u *UI) handleBatch(w http.ResponseWriter, r *http.Request) {
	t := u.twinOrFail(w, r)
	if t == nil {
		return
	}
	id := r.PathValue("id")
	outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))
	view := &ListView{}
	p := u.page(r, "Batches", id, "One delivery, its receipt and every record in it", view)

	detail, err := u.batchDetail(t, r, id, outcome)
	if err != nil {
		var ae *adminError
		if errors.As(err, &ae) && ae.notFound() {
			u.fail(w, r, http.StatusNotFound, "no batch with that id")
			return
		}
		p.Notices = append(p.Notices, "The batch could not be read: "+err.Error())
		u.render(w, r, "list", p)
		return
	}
	rec := detail.Receipt

	if rec != nil {
		view.Sections = append(view.Sections, Section{
			Title: "Receipt",
			Note:  "Identical on both channels but for the channel field and the shape of the locators.",
			Tiles: receiptTiles(rec),
		})
		view.Sections = append(view.Sections, Section{
			Title: "Files",
			Note:  "One entry per file on the file channel, one per chunk on REST.",
			Table: fileTable(rec.Files),
		})
		if len(rec.Findings) > 0 {
			rows := make([]Row, 0, len(rec.Findings))
			for _, f := range rec.Findings {
				rows = append(rows, Row{Cells: []Cell{
					mono(f.Code), text(f.Field), wrapped(f.Message), mono(f.Pointer),
				}})
			}
			view.Sections = append(view.Sections, Section{
				Title: "Findings about the batch and its files",
				Note:  "Findings that are about a file or the batch, not about one record.",
				Table: &Table{Headers: []string{"Code", "Field", "Message", "Pointer"}, Rows: rows},
			})
		}
		if refs := exceptionRefsOf(rec); len(refs) > 0 {
			rows := make([]Row, 0, len(refs))
			for _, ref := range refs {
				rows = append(rows, Row{Cells: []Cell{
					link(displayKey(ref.SubjectKey),
						u.href("/exceptions", map[string]string{"q": displayKey(ref.SubjectKey)})),
					text(ref.SubjectType),
					link(ref.Code, u.href("/exceptions", map[string]string{"code": ref.Code})),
				}})
			}
			view.Sections = append(view.Sections, Section{
				Title: "Exceptions this batch opened",
				Table: &Table{Headers: []string{"Subject", "Type", "Code"}, Rows: rows},
			})
		}
	} else {
		p.Notices = append(p.Notices,
			"This batch has no receipt yet: it is still open, so nothing has been written for it.")
	}

	// Per-record outcomes. The filter row is links, so the filtered view is a
	// URL a reviewer can keep.
	view.Sections = append(view.Sections, Section{
		Title: "Outcome filter",
		Tiles: u.outcomeTiles(id, outcome, rec),
	})
	offset := intParam(r, "offset", 0, 0, 1<<30)
	limit := u.pageSize(r)
	records := detail.Records
	total := len(records)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	rows := make([]Row, 0, end-offset)
	for _, res := range records[offset:end] {
		rows = append(rows, Row{Cells: u.recordResultCells(res)})
	}
	view.Sections = append(view.Sections, Section{
		Title: recordsTitle(outcome),
		Note: "In delivery order. The counts above and these rows satisfy the anti-silent-drop " +
			"invariant: seen equals accepted plus accepted_with_warning plus rejected plus " +
			"skipped_unchanged plus quarantined.",
		Table: &Table{
			Headers: recordResultHeaders,
			Rows:    rows,
			Empty:   "no record of this batch matches the filter",
		},
		// A committed batch's record report never changes, so offset paging on
		// it is stable; the datasets and queues, which do change, page by key.
		Pager: u.offsetPager("/batches/"+id, map[string]string{"outcome": outcome},
			offset, limit, total),
	})

	if rec != nil {
		view.Sections = append(view.Sections, Section{
			Title:    "Receipt as written",
			Note:     "The twin's own report, verbatim, exactly as receipts/<batch_id>/receipt.json holds it.",
			PreLabel: "receipt.json",
			Pre:      jsonPre(rec),
		})
	}
	u.render(w, r, "list", p)
}

// recordsTitle names the filtered record list.
func recordsTitle(outcome string) string {
	if outcome == "" {
		return "Records"
	}
	return "Records: " + outcome
}

// receiptTiles renders the receipt's facts and tally.
func receiptTiles(rec *miniblp.Receipt) []Tile {
	c := rec.Counts
	tiles := []Tile{
		{Label: "channel", Value: rec.Channel},
		{Label: "status", Value: rec.Status, Tone: toneForBatchStatus(rec.Status)},
		{Label: "run", Value: rec.RunID},
		{Label: "tenant", Value: rec.Tenant},
		{Label: "source system", Value: rec.SourceSystem},
		{Label: "producer", Value: rec.Producer},
		{Label: "sequence", Value: strconv.Itoa(rec.Sequence)},
		{Label: "mode", Value: rec.Mode},
		{Label: "on error", Value: rec.OnError},
		{Label: "profiles", Value: joinCodes(sortStrings(rec.Profiles))},
		{Label: "received scan", Value: strconv.FormatInt(rec.ReceivedScan, 10)},
		{Label: "virtual clock", Value: group(rec.VirtualClockMs) + " ms"},
		{Label: "seen", Value: group(int64(c.Seen))},
		{Label: "accepted", Value: group(int64(c.Accepted + c.AcceptedWithWarning)), Tone: ToneGreen},
		{Label: "rejected", Value: group(int64(c.Rejected)), Tone: toneIfPositive(int64(c.Rejected))},
		{Label: "skipped unchanged", Value: group(int64(c.SkippedUnchanged))},
		{Label: "quarantined", Value: group(int64(c.Quarantined)),
			Tone: toneIfPositive(int64(c.Quarantined))},
		{Label: "replayed", Value: group(int64(c.Replayed))},
		{Label: "proposals emitted", Value: group(int64(len(rec.Proposals)))},
	}
	if rec.FullLoad {
		tiles = append(tiles, Tile{Label: "full load", Value: "yes",
			Note: "this batch declared a full reload of its datasets"})
	}
	if rec.Replay {
		tiles = append(tiles, Tile{Label: "replay", Value: "yes",
			Note: "a byte-identical re-delivery: nothing was applied"})
	}
	closure, tone := "yes", ToneGreen
	if !rec.ClosureOK {
		closure, tone = "no", ToneRed
	}
	tiles = append(tiles, Tile{Label: "counts close", Value: closure, Tone: tone,
		Note: "seen equals the sum of the outcomes"})
	if len(rec.Codes) > 0 {
		tiles = append(tiles, Tile{Label: "batch codes", Value: strconv.Itoa(len(rec.Codes)),
			Note: joinCodes(sortStrings(rec.Codes)), Tone: ToneRed})
	}
	if rec.ManifestSHA256 != "" {
		tiles = append(tiles, Tile{Label: "manifest sha256", Value: shortHash(rec.ManifestSHA256)})
	}
	return tiles
}

// fileTable renders the per-file half of a receipt.
func fileTable(files []miniblp.FileReceipt) *Table {
	rows := make([]Row, 0, len(files))
	for _, f := range files {
		name := f.Path
		if f.ChunkOrdinal > 0 {
			name += " (chunk " + strconv.FormatInt(f.ChunkOrdinal, 10) + ")"
		}
		rows = append(rows, Row{Cells: []Cell{
			mono(name),
			text(f.Dataset),
			text(f.Format),
			text(f.Profile),
			text(f.Encoding),
			num(f.DeclaredRecords),
			num(f.ParsedRecords),
			chip(f.Status, toneForBatchStatus(f.Status)),
			num(f.Counts.Accepted + f.Counts.AcceptedWithWarning),
			num(f.Counts.Rejected),
			num(f.Counts.SkippedUnchanged),
			mono(shortHash(f.SHA256)),
			codesCell(f.Codes),
		}})
	}
	return &Table{
		Headers: []string{"File or chunk", "Dataset", "Format", "Profile", "Encoding",
			"Declared", "Parsed", "Status", "Accepted", "Rejected", "Skipped",
			"sha256", "Codes"},
		Rows:  rows,
		Empty: "this batch carried no file",
	}
}

// recordResultHeaders is the per-record outcome table's columns: the fixed
// records.csv header, plus the twin id and the version, which is what makes a
// receipt line joinable to a record.
var recordResultHeaders = []string{
	"#", "Dataset", "Natural key", "Outcome", "Twin id", "v", "File", "Line",
	"Chunk", "Codes", "Field", "Message", "Pointer", "Ack effect",
}

// recordResultCells renders one per-record outcome.
func (u *UI) recordResultCells(res miniblp.RecordResult) []Cell {
	keyCell := mono(displayKey(res.NaturalKey))
	if res.NaturalKey != "" && res.Dataset != "" {
		keyCell = link(displayKey(res.NaturalKey), u.recordHref(res.Dataset, res.NaturalKey))
	}
	codes, field, message := findingSummary(res)
	line := ""
	if res.Line > 0 {
		line = strconv.Itoa(res.Line)
	}
	chunk := ""
	if res.ChunkOrdinal > 0 {
		chunk = strconv.FormatInt(res.ChunkOrdinal, 10)
	}
	version := ""
	if res.Version > 0 {
		version = strconv.Itoa(res.Version)
	}
	return []Cell{
		num(res.Ordinal),
		text(res.Dataset),
		keyCell,
		chip(res.Outcome, toneForOutcome(res.Outcome)),
		mono(res.TwinID),
		mono(version),
		mono(res.File),
		mono(line),
		mono(chunk),
		Cell{Text: codes, Tone: findingTone(res), Wrap: true},
		text(field),
		wrapped(message),
		mono(res.Pointer),
		text(res.AckEffect),
	}
}

// findingSummary flattens a record's errors and warnings into the three columns
// records.csv has for them, errors first, in the order the twin reported them.
func findingSummary(res miniblp.RecordResult) (codes, field, message string) {
	all := append(append([]miniblp.Finding(nil), res.Errors...), res.Warnings...)
	names := make([]string, 0, len(all))
	for _, f := range all {
		names = append(names, f.Code)
		if field == "" {
			field = f.Field
		}
		if message == "" {
			message = f.Message
		}
	}
	return joinCodes(names), field, message
}

// findingTone reddens a record's findings only when at least one of them blocks.
func findingTone(res miniblp.RecordResult) string {
	if len(res.Errors) > 0 {
		return ToneRed
	}
	return ToneNone
}

// outcomeTiles renders the outcome filter of a batch as links, with each
// outcome's count from the receipt when there is one.
func (u *UI) outcomeTiles(id, active string, rec *miniblp.Receipt) []Tile {
	count := func(outcome string) string {
		if rec == nil {
			return "filter"
		}
		c := rec.Counts
		switch outcome {
		case miniblp.OutcomeAccepted:
			return group(int64(c.Accepted))
		case miniblp.OutcomeAcceptedWithWarning:
			return group(int64(c.AcceptedWithWarning))
		case miniblp.OutcomeRejected:
			return group(int64(c.Rejected))
		case miniblp.OutcomeSkippedUnchanged:
			return group(int64(c.SkippedUnchanged))
		case miniblp.OutcomeQuarantined:
			return group(int64(c.Quarantined))
		case miniblp.OutcomeReplayed:
			return group(int64(c.Replayed))
		default:
			return group(int64(c.Seen))
		}
	}
	tiles := []Tile{{Label: "every outcome", Value: count(""),
		Href: u.href("/batches/"+id, nil)}}
	if active == "" {
		tiles[0].Note = "shown"
	}
	for _, o := range outcomeFilters {
		tile := Tile{Label: o, Value: count(o), Tone: toneForOutcome(o),
			Href: u.href("/batches/"+id, map[string]string{"outcome": o})}
		if o == active {
			tile.Note = "shown"
		}
		tiles = append(tiles, tile)
	}
	return tiles
}

// offsetPager pages a fixed report by offset.
func (u *UI) offsetPager(rel string, params map[string]string, offset, limit, total int) *Pager {
	with := func(o int) string {
		q := map[string]string{}
		for k, v := range params {
			q[k] = v
		}
		if o > 0 {
			q["offset"] = strconv.Itoa(o)
		}
		if limit != u.cfg.PageSize {
			q["limit"] = strconv.Itoa(limit)
		}
		return u.href(rel, q)
	}
	p := &Pager{}
	if offset > 0 {
		p.FirstHref = with(0)
		prev := offset - limit
		if prev < 0 {
			prev = 0
		}
		p.PrevHref = with(prev)
	}
	if offset+limit < total {
		p.NextHref = with(offset + limit)
	}
	shown := total - offset
	if shown > limit {
		shown = limit
	}
	if shown < 0 {
		shown = 0
	}
	p.Summary = group(int64(shown)) + " of " + group(int64(total)) + " records, from position " +
		group(int64(offset+1))
	return p
}
