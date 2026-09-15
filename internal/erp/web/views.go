package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/erp"
	"github.com/fatjonblp/coding_challange_integrations/internal/seed"
	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// A KV is one labeled value in a summary block.
type KV struct {
	// Label names the value, Value is the value itself, already stringified.
	Label string
	Value string
	// Class is a cell class, used to mute an absent value and to mark the one
	// counter in this UI that is genuinely a warning.
	Class string
}

// A Section is a labeled block of values: the overview's summary boxes and the
// purchase order detail header.
type Section struct {
	// Title is the block heading, Items the values it holds, in the order they
	// are to be read.
	Title string
	Items []KV
}

// A Banner is the notice strip at the top of a page. There are two kinds and no
// more: an informational one, and the red one that says a document was posted
// twice.
type Banner struct {
	// Class is "warn" for the red banner and "info" otherwise.
	Class string
	// Title is the one line a reviewer reads first, Lines the explanation.
	Title string
	Lines []string
}

// A SearchBox is the search form of an entity browser. It is a plain GET form:
// no script, and the browser's own back button is the history.
type SearchBox struct {
	// Action is the page's own path, Q the term already entered and Placeholder
	// the hint naming the fields the search covers.
	Action      string
	Q           string
	Placeholder string
}

// An Option is one choice of a filter select.
type Option struct {
	// Value is the query parameter value, Label what the reader sees, and
	// Selected marks the choice currently in force.
	Value    string
	Label    string
	Selected bool
}

// A Filter is the request log's filter form: by endpoint and by status class.
type Filter struct {
	// Action is the page's own path; Endpoints and Classes are the two selects.
	Action    string
	Endpoints []Option
	Classes   []Option
}

// A Summary is the strip above the request log. It is the point of the view: an
// N+1 read pattern and a retry storm are both invisible in a table of two
// thousand rows and obvious in four numbers above it.
type Summary struct {
	// Requests, RateLimited and Injected are the run totals.
	Requests    int64
	RateLimited int64
	Injected    int64
	// Endpoints is the request count per endpoint class, sorted by class name.
	Endpoints []KV
	// SingleGets and ListPages are the two counts whose ratio distinguishes a
	// connector that pages master data from one that fetches it one entity at a
	// time.
	SingleGets int64
	ListPages  int64
	// Ratio is SingleGets per list page, rendered with three decimals from
	// integer arithmetic, and "n/a" when no list page was served.
	Ratio string
	// Note explains what the strip is for.
	Note string
}

// A pageData is everything a template may read. Sections that do not apply to a
// page are nil, and each page's template touches only its own.
type pageData struct {
	// Chrome, filled by UI.finish for every page.
	Prefix     string
	Stylesheet string
	Nav        []navItem
	Path       string
	Title      string
	Active     string

	// The auto-refresh toggle.
	RefreshSeconds int
	RefreshMillis  int
	RefreshLabel   string
	RefreshHref    string

	// Heading and Lede are the page's own title and one-line description.
	Heading string
	Lede    string

	// Scenario and Seed identify the loaded landscape and are shown on every
	// page, because a reviewer looking at a screenshot has to know which one it
	// was.
	Scenario string
	Seed     string

	// Error is the message of the error page.
	Error string

	Banner   *Banner
	Sections []Section
	Search   *SearchBox
	Filter   *Filter
	Summary  *Summary

	// Table is the page's main table, Pager its paging strip.
	Table *Table
	Pager Pager

	// Lines is a secondary table, used by the purchase order detail for the
	// order's own lines.
	Lines      *Table
	LinesTitle string
}

// header fills the identity strip and the page title.
func (u *UI) header(data *pageData, scenario string, seedValue int64) {
	data.Scenario = scenario
	data.Seed = strconv.FormatInt(seedValue, 10)
}

// buildOverview renders the run summary: what is loaded, where the quota and the
// clock stand, and the counters that decide a submission.
func (u *UI) buildOverview(src Source, q Query) (*pageData, error) {
	m, err := src.Metrics()
	if err != nil {
		return nil, err
	}
	state, err := src.State()
	if err != nil {
		return nil, err
	}
	entries, err := src.Requests()
	if err != nil {
		return nil, err
	}
	bucket := bucketState(entries)

	data := &pageData{
		Active:  "overview",
		Title:   "ERP overview",
		Heading: "Overview",
		Lede: "System of record. Read-only view of the seeded landscape, the run's " +
			"accounting and the counters the grader reads.",
	}
	u.header(data, state.Scenario, state.Seed)

	quotaLimit := "unlimited"
	quotaLeft := "unlimited"
	if m.QuotaLimit > 0 {
		quotaLimit = strconv.FormatInt(m.QuotaLimit, 10)
		quotaLeft = strconv.FormatInt(m.QuotaLimit-m.QuotaUsed, 10)
	}
	chaos := "off"
	if state.Chaos {
		chaos = "on"
	}

	data.Sections = []Section{
		{Title: "Seeded scenario", Items: []KV{
			{Label: "scenario", Value: state.Scenario},
			{Label: "seed", Value: strconv.FormatInt(state.Seed, 10)},
			{Label: "fault injection", Value: chaos},
			{Label: "max change_seq", Value: strconv.FormatInt(state.MaxChangeSeq, 10)},
			{Label: "export files", Value: strconv.Itoa(len(state.ExportFiles))},
		}},
		{Title: "Quota and virtual clock", Items: []KV{
			{Label: "quota limit", Value: quotaLimit},
			{Label: "quota used", Value: strconv.FormatInt(m.QuotaUsed, 10)},
			{Label: "quota remaining", Value: quotaLeft},
			{Label: "virtual clock", Value: strconv.FormatInt(m.VirtualClockMs, 10) + " vms"},
			{Label: "token bucket", Value: strconv.FormatInt(bucket.Level, 10) + " / " +
				strconv.FormatInt(bucket.Capacity, 10) + " tokens (reconstructed)"},
			{Label: "bucket refill", Value: "1 token / " +
				strconv.FormatInt(bucket.RefillIntervalVms, 10) + " vms"},
		}},
		{Title: "Counters", Items: []KV{
			{Label: "requests", Value: strconv.FormatInt(m.RequestsTotal, 10)},
			{Label: "429s", Value: strconv.FormatInt(m.RateLimited, 10)},
			{Label: "injected 5xx", Value: strconv.FormatInt(m.FiveXXInjected, 10)},
			{Label: "injected 5xx retried", Value: strconv.FormatInt(m.FiveXXRetried, 10)},
			{Label: "duplicate_document_attempts", Value: strconv.FormatInt(m.DuplicateDocumentAttempts, 10),
				Class: warnClassIf(m.DuplicateDocumentAttempts > 0)},
			{Label: "records returned", Value: strconv.FormatInt(m.RecordsReturned, 10)},
		}},
		{Title: "Received", Items: []KV{
			{Label: "AP documents", Value: strconv.Itoa(m.DocumentsCreated)},
			{Label: "idempotency keys", Value: strconv.Itoa(m.IdempotencyKeys)},
			{Label: "SOAP calls", Value: strconv.Itoa(m.SOAPCalls)},
			{Label: "business rejections", Value: strconv.FormatInt(sumCounts(m.BusinessRejections), 10)},
			{Label: "request log entries", Value: strconv.Itoa(m.RequestLogEntries)},
		}},
	}

	if m.DuplicateDocumentAttempts > 0 {
		data.Banner = &Banner{
			Class: "warn",
			Title: "duplicate_document_attempts = " + strconv.FormatInt(m.DuplicateDocumentAttempts, 10),
			Lines: []string{
				"An external reference was posted more than once. No second document " +
					"was created, but the attempt is the graded evidence: grading asserts " +
					"duplicate_document_attempts == 0.",
				"Open the duplicates view for the external references involved.",
			},
		}
	}

	// The entity counts, with the map's keys sorted explicitly: a map is never
	// ranged over into a rendered page.
	data.Table = &Table{
		Columns: []Column{{Title: "Dataset"}, {Title: "Records", Class: cellNum}},
		Empty:   "no dataset loaded",
		Note:    "Record counts of the loaded dataset, as the generator produced them.",
	}
	for _, name := range sortedKeys(state.Counts) {
		data.Table.Rows = append(data.Table.Rows, Row{Cells: []Cell{
			keyCell(name), num(int64(state.Counts[name])),
		}})
	}

	if len(m.BusinessRejections) > 0 {
		items := make([]KV, 0, len(m.BusinessRejections))
		for _, code := range sortedKeys64(m.BusinessRejections) {
			items = append(items, KV{Label: code,
				Value: strconv.FormatInt(m.BusinessRejections[code], 10)})
		}
		data.Sections = append(data.Sections, Section{Title: "Business rejections", Items: items})
	}
	if len(state.ExportFiles) > 0 {
		items := make([]KV, 0, len(state.ExportFiles))
		for i, name := range state.ExportFiles {
			items = append(items, KV{Label: "file " + strconv.Itoa(i+1), Value: name})
		}
		data.Sections = append(data.Sections,
			Section{Title: "Legacy export drop (write order)", Items: items})
	}
	return data, nil
}

// warnClassIf returns the warning cell class when cond holds.
func warnClassIf(cond bool) string {
	if cond {
		return cellWarn
	}
	return ""
}

// sumCounts adds the values of a counter map. The result does not depend on
// iteration order, because addition does not.
func sumCounts(m map[string]int64) int64 {
	var total int64
	for _, v := range m {
		total += v
	}
	return total
}

// sortedKeys returns the keys of a count map in ascending byte order.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedKeys64 returns the keys of an int64 count map in ascending byte order.
func sortedKeys64(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildSuppliers renders the creditor browser.
//
// The IBAN column is masked. Nothing forces that here, since this is our own
// screen rather than a candidate artifact, but a reviewer's screen share is a
// place a payment detail has no business being, and the specification names
// masking as preferred over omission because the record stays recognizable.
func (u *UI) buildSuppliers(src Source, q Query) (*pageData, error) {
	set, err := src.Dataset()
	if err != nil {
		return nil, err
	}
	data := u.browserPage(set, q, "suppliers", "Suppliers",
		"Creditor master data as the ERP serves it. Supplier numbers are strings "+
			"with significant leading zeros; legacy_id is a JSON number and is never a key.",
		"supplier number, name, country")

	rows := make([]Row, 0, len(set.Suppliers))
	for _, s := range set.Suppliers {
		if !q.matches(s.SupplierNumber, s.Name, s.Country, s.Currency, s.VATNumber) {
			continue
		}
		rows = append(rows, Row{Cells: []Cell{
			keyCell(s.SupplierNumber),
			text(s.Name),
			text(s.Country),
			text(s.Currency),
			text(maskIBAN(s.IBAN)),
			text(s.VATNumber),
			num(int64(s.PaymentTermsDays)),
			yesNo(s.Blocked),
			num(int64(s.LegacyID)),
			num(s.ChangeSeq),
		}})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "supplier_number"}, {Title: "name"}, {Title: "country"},
			{Title: "currency"}, {Title: "iban (masked)"}, {Title: "vat_number"},
			{Title: "terms", Class: cellNum}, {Title: "blocked"},
			{Title: "legacy_id", Class: cellNum}, {Title: "change_seq", Class: cellNum},
		},
		Empty: "no supplier matches this search",
		Note: "Sorted by supplier number in byte order. A blocked creditor is " +
			"rejected on posting with ERP_SUPPLIER_BLOCKED.",
	}
	u.paginate(data, rows, q, u.prefix+"/suppliers")
	return data, nil
}

// buildPurchaseOrders renders the purchase order browser, and the detail of one
// order with its lines when the query names a key.
func (u *UI) buildPurchaseOrders(src Source, q Query) (*pageData, error) {
	set, err := src.Dataset()
	if err != nil {
		return nil, err
	}
	data := u.browserPage(set, q, "purchase-orders", "Purchase orders",
		"Order headers with their line counts. Select an order to see its lines. "+
			"Order numbers arrive in three spellings and are compared numerically "+
			"after the leading zeros are stripped.",
		"order number, supplier, cost center")

	// Line counts per canonical order number, probed by key only.
	lineCount := make(map[string]int, len(set.PurchaseOrders))
	for i := range set.PurchaseOrderLines {
		lineCount[seed.CanonicalPONumber(set.PurchaseOrderLines[i].PONumber)]++
	}

	if q.Key != "" {
		return u.purchaseOrderDetail(data, set, q, lineCount)
	}

	rows := make([]Row, 0, len(set.PurchaseOrders))
	for _, po := range set.PurchaseOrders {
		if !q.matches(po.PONumber, po.SupplierNumber, po.CostCenter, po.Status, po.CompanyCode) {
			continue
		}
		rows = append(rows, Row{Cells: []Cell{
			keyCell(po.PONumber),
			text(po.SupplierNumber),
			text(po.CompanyCode),
			text(po.Currency),
			statusCell(po.Status),
			text(po.OrderDate),
			text(po.CostCenter),
			num(int64(lineCount[seed.CanonicalPONumber(po.PONumber)])),
			num(po.ChangeSeq),
		}})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "po_number"}, {Title: "supplier_number"}, {Title: "company_code"},
			{Title: "currency"}, {Title: "status"}, {Title: "order_date"},
			{Title: "cost_center"}, {Title: "lines", Class: cellNum},
			{Title: "change_seq", Class: cellNum},
		},
		Empty: "no purchase order matches this search",
		Note: "Sorted by order number as delivered, which is not canonical order: " +
			"some records carry a PO- prefix or surrounding spaces. A CLOSED order " +
			"is rejected on posting with ERP_PO_CLOSED.",
	}
	u.paginate(data, rows, q, u.prefix+"/purchase-orders")
	return data, nil
}

// purchaseOrderDetail renders one order and its lines.
func (u *UI) purchaseOrderDetail(data *pageData, set *seed.Dataset, q Query, lineCount map[string]int) (*pageData, error) {
	canon := seed.CanonicalPONumber(q.Key)
	found := false
	for _, po := range set.PurchaseOrders {
		if seed.CanonicalPONumber(po.PONumber) != canon {
			continue
		}
		found = true
		data.Heading = "Purchase order " + po.PONumber
		data.Sections = []Section{{Title: "Header", Items: []KV{
			{Label: "po_number (as delivered)", Value: po.PONumber},
			{Label: "canonical", Value: canon},
			{Label: "supplier_number", Value: po.SupplierNumber},
			{Label: "company_code", Value: po.CompanyCode},
			{Label: "currency", Value: po.Currency},
			{Label: "status", Value: po.Status},
			{Label: "order_date", Value: po.OrderDate},
			{Label: "cost_center", Value: po.CostCenter},
			{Label: "lines", Value: strconv.Itoa(lineCount[canon])},
			{Label: "change_seq", Value: strconv.FormatInt(po.ChangeSeq, 10)},
		}}}
		break
	}
	if !found {
		data.Banner = &Banner{Class: "info", Title: "no such purchase order",
			Lines: []string{"No order resolves to the canonical number " + canon + "."}}
	}

	rows := make([]Row, 0, 16)
	for _, l := range set.PurchaseOrderLines {
		if seed.CanonicalPONumber(l.PONumber) != canon {
			continue
		}
		rows = append(rows, Row{Cells: []Cell{
			keyCell(l.LineNo),
			text(l.Material),
			text(l.Description),
			{Text: l.Quantity.String(), Class: cellNum},
			text(l.UoM),
			{Text: l.UnitPrice.String(), Class: cellNum},
			text(l.Currency),
			text(l.GLAccount),
			text(l.CostCenter),
			num(l.ChangeSeq),
		}})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "line_no"}, {Title: "material"}, {Title: "description"},
			{Title: "quantity", Class: cellNum}, {Title: "uom"},
			{Title: "unit_price", Class: cellNum}, {Title: "currency"},
			{Title: "gl_account"}, {Title: "cost_center"},
			{Title: "change_seq", Class: cellNum},
		},
		Empty: "this order has no lines",
		Note: "Line numbers keep their leading zeros. Quantities and unit prices are " +
			"exact decimals; nothing on this screen passes through a float.",
	}
	u.paginate(data, rows, q, u.prefix+"/purchase-orders")
	return data, nil
}

// statusCell renders a master-data status, marking a closed order so a reviewer
// can see at a glance why a posting was rejected.
func statusCell(status string) Cell {
	if strings.EqualFold(status, "CLOSED") {
		return Cell{Text: status, Class: cellMuted}
	}
	return text(status)
}

// buildCostCenters renders the cost center browser.
func (u *UI) buildCostCenters(src Source, q Query) (*pageData, error) {
	set, err := src.Dataset()
	if err != nil {
		return nil, err
	}
	data := u.browserPage(set, q, "cost-centers", "Cost centers",
		"Cost centers are pre-seeded in the twin as though another integration had "+
			"already loaded them. Leading zeros are significant here and nowhere else: "+
			"0815 and 815 are two different cost centers.",
		"code, name, company code")

	rows := make([]Row, 0, len(set.CostCenters))
	for _, cc := range set.CostCenters {
		if !q.matches(cc.Code, cc.Name, cc.CompanyCode) {
			continue
		}
		validTo := text(cc.ValidTo)
		if cc.ValidTo == "" {
			validTo = Cell{Text: "open-ended", Class: cellMuted}
		}
		rows = append(rows, Row{Cells: []Cell{
			keyCell(cc.Code), text(cc.Name), text(cc.CompanyCode),
			text(cc.ValidFrom), validTo, yesNo(cc.Blocked),
		}})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "code"}, {Title: "name"}, {Title: "company_code"},
			{Title: "valid_from"}, {Title: "valid_to"}, {Title: "blocked"},
		},
		Empty: "no cost center matches this search",
		Note: "Sorted by code in byte order, so 0815 sorts before 815. A code not " +
			"configured for the posting's company code is rejected with " +
			"ERP_COST_CENTER_UNKNOWN.",
	}
	u.paginate(data, rows, q, u.prefix+"/cost-centers")
	return data, nil
}

// buildUoMConversions renders the unit-of-measure conversion table.
func (u *UI) buildUoMConversions(src Source, q Query) (*pageData, error) {
	set, err := src.Dataset()
	if err != nil {
		return nil, err
	}
	data := u.browserPage(set, q, "uom-conversions", "UoM conversions",
		"The conversion table behind GET /erp/v1/uom-conversions. A base quantity is "+
			"alt * numerator / denominator. An alternative unit with no row here is "+
			"EXC_UOM_UNMAPPABLE and never a guess.",
		"material, alt unit, base unit")

	rows := make([]Row, 0, len(set.UoMConversions))
	for _, c := range set.UoMConversions {
		if !q.matches(c.Material, c.AltUoM, c.BaseUoM) {
			continue
		}
		material := text(c.Material)
		if c.Material == "" {
			material = Cell{Text: "(generic rule)", Class: cellMuted}
		}
		rows = append(rows, Row{Cells: []Cell{
			material, keyCell(c.AltUoM), num(c.Numerator), num(c.Denominator), text(c.BaseUoM),
		}})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "material"}, {Title: "alt_uom"},
			{Title: "numerator", Class: cellNum}, {Title: "denominator", Class: cellNum},
			{Title: "base_uom"},
		},
		Empty: "no conversion matches this search",
		Note: "Sorted by (material, alternative unit). A row with a material overrides " +
			"the generic rule for that material. A conversion whose base quantity would " +
			"need more than three decimals is EXC_UOM_UNCONVERTIBLE, never a silent round.",
	}
	u.paginate(data, rows, q, u.prefix+"/uom-conversions")
	return data, nil
}

// browserPage returns the common shell of an entity browser: heading, search box
// and the identity strip.
func (u *UI) browserPage(set *seed.Dataset, q Query, active, heading, lede, placeholder string) *pageData {
	data := &pageData{
		Active:  active,
		Title:   "ERP " + strings.ToLower(heading),
		Heading: heading,
		Lede:    lede,
		Search: &SearchBox{
			Action:      u.prefix + "/" + active,
			Q:           q.Q,
			Placeholder: placeholder,
		},
	}
	u.header(data, set.Scenario, set.Seed)
	return data
}

// paginate clamps the query's window to rows, fills the page's table body and
// builds the paging strip.
func (u *UI) paginate(data *pageData, rows []Row, q Query, path string) {
	from, to := q.window(len(rows))
	data.Table.Rows = rows[from:to]
	data.Pager = q.pager(path, len(rows), from, to)
}

// buildDocuments renders the received AP documents.
func (u *UI) buildDocuments(src Source, q Query) (*pageData, error) {
	docs, err := src.Documents()
	if err != nil {
		return nil, err
	}
	entries, err := src.Requests()
	if err != nil {
		return nil, err
	}
	m, err := src.Metrics()
	if err != nil {
		return nil, err
	}
	attempts := postingAttempts(entries)
	anyDuplicate := m.DuplicateDocumentAttempts > 0

	data := &pageData{
		Active:  "documents",
		Title:   "ERP AP documents",
		Heading: "Received AP documents",
		Lede: "One row per document the ERP booked, with the idempotency key of the " +
			"request that created it. A second posting of the same external reference " +
			"never creates a second document; it is answered with this one.",
		Search: &SearchBox{
			Action:      u.prefix + "/documents",
			Q:           q.Q,
			Placeholder: "document number, external reference, supplier",
		},
	}
	u.header(data, m.Scenario, m.Seed)

	rows := make([]Row, 0, len(docs))
	for _, d := range docs {
		if !q.matches(d.DocumentNumber, d.ExternalReference, d.SupplierNumber,
			d.SupplierInvoiceNumber, d.PONumber, d.CostCenter, d.IdempotencyKey) {
			continue
		}
		n := attempts[d.ExternalReference]
		duplicate := anyDuplicate && n > 1
		status := Cell{Text: "posted"}
		switch {
		case duplicate:
			status = flag("posted, " + strconv.Itoa(n) + " attempts")
		case n > 1:
			status = Cell{Text: "posted, " + strconv.Itoa(n) + " attempts", Class: cellMuted}
		}
		rows = append(rows, Row{
			Flagged: duplicate,
			Cells: []Cell{
				keyCell(d.DocumentNumber),
				num(int64(d.FiscalYear)),
				text(d.ExternalReference),
				text(d.SupplierNumber),
				text(d.SupplierInvoiceNumber),
				text(d.CompanyCode),
				text(d.DocumentType),
				text(d.PostingDate),
				text(d.Currency),
				{Text: d.GrossAmount.String(), Class: cellNum},
				{Text: d.VATAmount.String(), Class: cellNum},
				text(d.PONumber),
				text(d.CostCenter),
				status,
				text(d.IdempotencyKey),
			},
		})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "document_number"}, {Title: "fiscal_year", Class: cellNum},
			{Title: "external_reference"}, {Title: "supplier_number"},
			{Title: "supplier_invoice_number"}, {Title: "company_code"},
			{Title: "type"}, {Title: "posting_date"}, {Title: "currency"},
			{Title: "gross_amount", Class: cellNum}, {Title: "vat_amount", Class: cellNum},
			{Title: "po_number"}, {Title: "cost_center"}, {Title: "status"},
			{Title: "idempotency_key"},
		},
		Empty: "no document has been posted in this run",
		Note: "Creation order, which is document number order. Amounts are exact " +
			"decimals as the client sent them; the ERP never recomputes a conversion.",
	}
	u.paginate(data, rows, q, u.prefix+"/documents")
	return data, nil
}

// A dupGroup is one external reference with everything known about the attempts
// to post it.
type dupGroup struct {
	Reference string
	Attempts  int
	Document  *erp.Document
	// Duplicate reports whether this group is a double posting rather than a
	// safe retry. See buildDuplicates for how the two are told apart.
	Duplicate bool
}

// buildDuplicates renders the duplicate view: posting attempts grouped by
// external reference, with every group that is a double posting flagged.
//
// The attempt count comes from the request log, because the log carries the
// content-addressed signature of every posting and that signature IS the sorted
// set of external references the request carried. The documents themselves cannot
// answer the question: there is only ever one document per reference, which is
// precisely why the ERP's counter, and this view, exist.
//
// A safe retry is not a double posting, and this view must not cry wolf about
// one. Two requests for one reference under the SAME idempotency key are a client
// correctly retrying after a timeout; the ERP replays the stored response and
// increments nothing. Two requests under DIFFERENT keys are the double posting,
// and the ERP counts each one in duplicate_document_attempts. The request log
// carries the references but not the key, so the two are separated by the
// counter: while duplicate_document_attempts is zero, no reference has been
// posted twice under a different key, so no group is flagged however many times
// it was retried. Once the counter moves, every group with more than one attempt
// is flagged, because at that point one of them is the double posting and a
// reviewer must see all the candidates rather than the wrong one.
func (u *UI) buildDuplicates(src Source, q Query) (*pageData, error) {
	docs, err := src.Documents()
	if err != nil {
		return nil, err
	}
	entries, err := src.Requests()
	if err != nil {
		return nil, err
	}
	m, err := src.Metrics()
	if err != nil {
		return nil, err
	}
	attempts := postingAttempts(entries)
	// The ERP's own verdict on whether any double posting happened at all.
	anyDuplicate := m.DuplicateDocumentAttempts > 0

	byRef := make(map[string]*erp.Document, len(docs))
	for i := range docs {
		byRef[docs[i].ExternalReference] = &docs[i]
	}
	refs := make([]string, 0, len(attempts)+len(byRef))
	seen := make(map[string]bool, len(attempts)+len(byRef))
	for ref := range attempts {
		if !seen[ref] {
			seen[ref], refs = true, append(refs, ref)
		}
	}
	for ref := range byRef {
		if !seen[ref] {
			seen[ref], refs = true, append(refs, ref)
		}
	}
	// Collected out of two maps, so the order is imposed explicitly: flagged
	// groups first, then by external reference ascending. A reviewer opening
	// this view must find the double posting at the top of the first page and
	// never on page nine.
	sort.Slice(refs, func(i, j int) bool {
		fi := anyDuplicate && attempts[refs[i]] > 1
		fj := anyDuplicate && attempts[refs[j]] > 1
		if fi != fj {
			return fi
		}
		return refs[i] < refs[j]
	})

	groups := make([]dupGroup, 0, len(refs))
	flagged := 0
	for _, ref := range refs {
		g := dupGroup{Reference: ref, Attempts: attempts[ref], Document: byRef[ref]}
		g.Duplicate = anyDuplicate && g.Attempts > 1
		if g.Duplicate {
			flagged++
		}
		groups = append(groups, g)
	}

	data := &pageData{
		Active:  "duplicates",
		Title:   "ERP duplicates",
		Heading: "Duplicate view",
		Lede: "Posting attempts grouped by external_reference. A flagged group is a " +
			"double posting in a customer ledger, which is the most serious thing this " +
			"UI can show: there is no reversal endpoint.",
		Search: &SearchBox{
			Action:      u.prefix + "/duplicates",
			Q:           q.Q,
			Placeholder: "external reference, document number",
		},
	}
	u.header(data, m.Scenario, m.Seed)

	switch {
	case anyDuplicate:
		data.Banner = &Banner{
			Class: "warn",
			Title: strconv.Itoa(flagged) + " external reference(s) posted more than once; " +
				"duplicate_document_attempts = " + strconv.FormatInt(m.DuplicateDocumentAttempts, 10),
			Lines: []string{
				"The ERP refused to create a second document and answered with the " +
					"original document number, duplicate:true. The counter is the graded " +
					"evidence, and grading asserts it is zero.",
			},
		}
	default:
		data.Banner = &Banner{
			Class: "info",
			Title: "no duplicate posting attempt in this run",
			Lines: []string{"duplicate_document_attempts = 0, and every external " +
				"reference was posted exactly once."},
		}
	}

	rows := make([]Row, 0, len(groups))
	for _, g := range groups {
		if !q.matches(g.Reference, documentNumberOf(g.Document)) {
			continue
		}
		verdict := Cell{Text: "single posting", Class: cellMuted}
		switch {
		case g.Duplicate:
			verdict = flag("DUPLICATE: " + strconv.Itoa(g.Attempts) + " attempts")
		case g.Attempts > 1:
			verdict = Cell{Text: strconv.Itoa(g.Attempts) +
				" attempts, no duplicate recorded", Class: cellMuted}
		case g.Document == nil:
			verdict = Cell{Text: "no document (rejected)", Class: cellMuted}
		}
		cells := []Cell{
			keyCell(g.Reference),
			num(int64(g.Attempts)),
			verdict,
		}
		if g.Document != nil {
			cells = append(cells,
				text(g.Document.DocumentNumber),
				num(int64(g.Document.FiscalYear)),
				text(g.Document.SupplierNumber),
				text(g.Document.Currency),
				Cell{Text: g.Document.GrossAmount.String(), Class: cellNum},
				text(g.Document.IdempotencyKey))
		} else {
			cells = append(cells, dash(), dash(), dash(), dash(), dash(), dash())
		}
		rows = append(rows, Row{Cells: cells, Flagged: g.Duplicate})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "external_reference"}, {Title: "attempts", Class: cellNum},
			{Title: "verdict"}, {Title: "document_number"},
			{Title: "fiscal_year", Class: cellNum}, {Title: "supplier_number"},
			{Title: "currency"}, {Title: "gross_amount", Class: cellNum},
			{Title: "creating idempotency_key"},
		},
		Empty: "no posting has been attempted in this run",
		Note: "An attempt is a posting request that reached the handler and was not " +
			"refused as an idempotency key conflict: an injected fault, a 429, a " +
			"quarantine 403 and a quota 503 never reached it, so a retry storm does not " +
			"inflate this column. More than one attempt is flagged only while the ERP " +
			"itself records a duplicate, because retrying one posting under the same key " +
			"is correct client behavior. Flagged groups sort first.",
	}
	u.paginate(data, rows, q, u.prefix+"/duplicates")
	return data, nil
}

// documentNumberOf returns a document's number, or the empty string for no
// document.
func documentNumberOf(d *erp.Document) string {
	if d == nil {
		return ""
	}
	return d.DocumentNumber
}

// postingAttempts counts, per external reference, the posting requests that
// reached the posting handler.
//
// The count is read off the request log's signature, which for a posting is the
// route template followed by the sorted, comma-joined set of the request's
// external references. That is the ERP's own content-addressed identity of the
// logical request, so this view and the ERP's fault injection agree by
// construction on what "the same posting" means.
func postingAttempts(entries []erp.RequestLogEntry) map[string]int {
	out := map[string]int{}
	for _, e := range entries {
		if !reachedHandler(e) {
			continue
		}
		refs, ok := postingSignatureRefs(e.Signature)
		if !ok {
			continue
		}
		for _, ref := range refs {
			if ref != "" {
				out[ref]++
			}
		}
	}
	return out
}

// postingSignatureRefs splits the external reference set out of a posting
// signature. The batch route is tested first, because the single route's path is
// a prefix of it up to the separator.
func postingSignatureRefs(sig string) ([]string, bool) {
	for _, prefix := range []string{
		"POST " + erp.RouteDocumentsBatch + "|",
		"POST " + erp.RouteDocuments + "|",
	} {
		if rest, ok := strings.CutPrefix(sig, prefix); ok {
			if rest == "" {
				return nil, true
			}
			return strings.Split(rest, ","), true
		}
	}
	return nil, false
}

// reachedHandler reports whether a logged request got as far as attempting the
// work it asked for.
//
// An injected fault, a 429, a quarantine 403, a quota 503 and a 401 all stop
// before the handler, so none of them is an attempt at anything. A 409 does reach
// the handler, but an idempotency key conflict is refused before any item is
// looked at, so it is not an attempt at posting either.
func reachedHandler(e erp.RequestLogEntry) bool {
	if e.InjectedFault != "" {
		return false
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests,
		http.StatusConflict:
		return false
	case http.StatusServiceUnavailable:
		// The only 503 the ERP writes itself is the quota rejection, and that
		// one never reaches a handler either.
		return false
	}
	return true
}

// buildIdempotency renders the idempotency key table.
func (u *UI) buildIdempotency(src Source, q Query) (*pageData, error) {
	stored, err := src.IdempotencyKeys()
	if err != nil {
		return nil, err
	}
	m, err := src.Metrics()
	if err != nil {
		return nil, err
	}
	stats := mergeIdempotency(stored, u.observer.snapshot())

	data := &pageData{
		Active:  "idempotency",
		Title:   "ERP idempotency keys",
		Heading: "Idempotency keys",
		Lede: "One row per key the ERP retains. A key is a promise about one " +
			"operation, so the method and the path are part of its identity: the same " +
			"client-chosen key on two endpoints is two keys.",
		Search: &SearchBox{
			Action:      u.prefix + "/idempotency-keys",
			Q:           q.Q,
			Placeholder: "key, path",
		},
	}
	u.header(data, m.Scenario, m.Seed)

	observed := 0
	conflicts := 0
	for _, st := range stats {
		if st.Observed {
			observed++
		}
		conflicts += st.Conflicts
	}
	if observed == 0 && len(stats) > 0 {
		data.Banner = &Banner{
			Class: "info",
			Title: "first status, replays and conflicts are not being observed",
			Lines: []string{
				"The ERP's admin surface publishes the keys it retains, not the traffic " +
					"that produced them. Wrap the server in UI.Observe to fill those three " +
					"columns.",
			},
		}
	} else if conflicts > 0 {
		data.Banner = &Banner{
			Class: "warn",
			Title: strconv.Itoa(conflicts) + " idempotency key conflict(s)",
			Lines: []string{
				"A key was offered a second time with a different body, answered with " +
					"409 IDEMPOTENCY_KEY_REUSED. The key was derived from something other " +
					"than the content it is supposed to protect.",
			},
		}
	}

	rows := make([]Row, 0, len(stats))
	for _, st := range stats {
		if !q.matches(st.Key, st.Path, st.Method, st.Tenant) {
			continue
		}
		first, replays, conflictCell, attemptCell := dash(), dash(), dash(), dash()
		if st.Observed {
			first = statusCode(st.FirstStatus)
			replays = num(int64(st.Replays))
			conflictCell = num(int64(st.Conflicts))
			if st.Conflicts > 0 {
				conflictCell = flag(strconv.Itoa(st.Conflicts))
			}
			attemptCell = num(int64(st.Attempts))
		}
		tenant := text(st.Tenant)
		if st.Tenant == "" {
			tenant = dash()
		}
		rows = append(rows, Row{
			Flagged: st.Conflicts > 0,
			Cells: []Cell{
				keyCell(st.Key), tenant, text(st.Method), text(st.Path),
				first, replays, conflictCell, attemptCell,
			},
		})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "key"}, {Title: "tenant"}, {Title: "method"}, {Title: "path"},
			{Title: "first_status", Class: cellNum}, {Title: "replays", Class: cellNum},
			{Title: "conflicts", Class: cellNum}, {Title: "attempts", Class: cellNum},
		},
		Empty: "no idempotency key has been used in this run",
		Note: "Store insertion order, which is also the eviction order. A replay is " +
			"correct client behavior after a timeout; a conflict never is.",
	}
	u.paginate(data, rows, q, u.prefix+"/idempotency-keys")
	return data, nil
}

// statusCode renders an HTTP status, marking anything that is not a success.
func statusCode(status int) Cell {
	c := Cell{Text: strconv.Itoa(status), Class: cellNum}
	switch {
	case status == 0:
		return dash()
	case status >= 500, status == http.StatusConflict:
		c.Class = cellWarn
	}
	return c
}

// statusClasses are the request log's status filters.
var statusClasses = []struct {
	Value, Label string
	match        func(int) bool
}{
	{"", "any status", func(int) bool { return true }},
	{"2xx", "2xx success", func(s int) bool { return s >= 200 && s < 300 }},
	{"4xx", "4xx client", func(s int) bool { return s >= 400 && s < 500 }},
	{"429", "429 rate limited", func(s int) bool { return s == http.StatusTooManyRequests }},
	{"5xx", "5xx server", func(s int) bool { return s >= 500 }},
}

// buildRequests renders the request log, newest first, with the summary strip
// that is the whole reason the view exists.
func (u *UI) buildRequests(src Source, q Query) (*pageData, error) {
	entries, err := src.Requests()
	if err != nil {
		return nil, err
	}
	m, err := src.Metrics()
	if err != nil {
		return nil, err
	}

	data := &pageData{
		Active:  "requests",
		Title:   "ERP request log",
		Heading: "Request log",
		Lede: "Every non-admin request of this run, newest first. Admin and UI " +
			"requests are absent by design: they cost nothing, count nothing and are " +
			"never injected into, so reading this page cannot change it.",
	}
	u.header(data, m.Scenario, m.Seed)
	data.Summary = requestSummary(m, entries)
	data.Filter = u.requestFilter(entries, q)

	match := statusClasses[0].match
	for _, c := range statusClasses {
		if c.Value == q.Class {
			match = c.match
		}
	}

	rows := make([]Row, 0, len(entries))
	// Newest first: the log arrives in arrival order, so it is walked backwards
	// rather than sorted, which keeps entries with an equal sequence in their
	// arrival order.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if q.Endpoint != "" && e.Endpoint != q.Endpoint {
			continue
		}
		if !match(e.Status) {
			continue
		}
		injected := dash()
		if e.InjectedFault != "" {
			injected = flag(e.InjectedFault)
		}
		rows = append(rows, Row{
			Flagged: false,
			Cells: []Cell{
				num(e.Sequence),
				text(e.Endpoint),
				text(e.Method),
				text(e.Path),
				statusCode(e.Status),
				num(e.VirtualCostVms),
				num(e.VirtualClockMs),
				num(e.QuotaUsed),
				injected,
				text(e.Signature),
			},
		})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "seq", Class: cellNum}, {Title: "endpoint"}, {Title: "method"},
			{Title: "path"}, {Title: "status", Class: cellNum},
			{Title: "cost_vms", Class: cellNum}, {Title: "clock_vms", Class: cellNum},
			{Title: "quota_used", Class: cellNum}, {Title: "injected_fault"},
			{Title: "signature"},
		},
		Empty: "no request matches this filter",
		Note: "cost_vms is what this request advanced the virtual clock by. The " +
			"signature is the content-addressed identity of the logical request, which " +
			"is what fault injection is keyed by: two clients that issue the same " +
			"logical requests meet the same faults whatever order they issue them in.",
	}
	u.paginate(data, rows, q, u.prefix+"/requests")
	return data, nil
}

// requestSummary builds the strip above the request log.
func requestSummary(m erp.AdminMetrics, entries []erp.RequestLogEntry) *Summary {
	s := &Summary{
		Requests:    m.RequestsTotal,
		RateLimited: m.RateLimited,
		Injected:    m.FiveXXInjected,
		SingleGets:  m.ByEndpoint[string(simclock.ERPSingleGet)],
		ListPages:   m.ByEndpoint[string(simclock.ERPListPage)],
		Note: "Requests per endpoint over the whole run, not the filtered table. " +
			"A single-GET count that grows with the record count instead of the page " +
			"count is the N+1 read pattern; a 429 or injected-5xx count far above the " +
			"request count of one endpoint is a retry storm.",
	}
	for _, ep := range sortedKeys64(m.ByEndpoint) {
		s.Endpoints = append(s.Endpoints, KV{Label: ep,
			Value: strconv.FormatInt(m.ByEndpoint[ep], 10)})
	}
	s.Ratio = ratioPerListPage(s.SingleGets, s.ListPages)
	return s
}

// ratioPerListPage renders single-entity GETs per list page with three decimals,
// computed with integer arithmetic: no float64 is involved, here or anywhere else
// in this package.
func ratioPerListPage(singles, lists int64) string {
	if lists <= 0 {
		if singles > 0 {
			return "no list page served at all"
		}
		return "n/a"
	}
	milli := singles * 1000 / lists
	whole, frac := milli/1000, milli%1000
	return strconv.FormatInt(whole, 10) + "." +
		strings.Repeat("0", 3-len(strconv.FormatInt(frac, 10))) +
		strconv.FormatInt(frac, 10) + " single GETs per list page"
}

// requestFilter builds the two filter selects. The endpoint options are the
// classes that actually appear in the log, sorted, so the select never offers a
// filter that would return nothing.
func (u *UI) requestFilter(entries []erp.RequestLogEntry, q Query) *Filter {
	present := map[string]bool{}
	for _, e := range entries {
		if e.Endpoint != "" {
			present[e.Endpoint] = true
		}
	}
	names := make([]string, 0, len(present))
	for name := range present {
		names = append(names, name)
	}
	sort.Strings(names)

	f := &Filter{Action: u.prefix + "/requests"}
	f.Endpoints = append(f.Endpoints, Option{Value: "", Label: "any endpoint",
		Selected: q.Endpoint == ""})
	for _, name := range names {
		f.Endpoints = append(f.Endpoints, Option{Value: name, Label: name,
			Selected: q.Endpoint == name})
	}
	for _, c := range statusClasses {
		f.Classes = append(f.Classes, Option{Value: c.Value, Label: c.Label,
			Selected: q.Class == c.Value})
	}
	return f
}

// buildSOAPCalls renders the SOAP call log.
func (u *UI) buildSOAPCalls(src Source, q Query) (*pageData, error) {
	calls, err := src.SOAPCalls()
	if err != nil {
		return nil, err
	}
	m, err := src.Metrics()
	if err != nil {
		return nil, err
	}

	data := &pageData{
		Active:  "soap",
		Title:   "ERP SOAP calls",
		Heading: "SOAP call log",
		Lede: "Every call to GetExchangeRateTable. A business fault arrives as HTTP " +
			"200 carrying soap:Fault, so the status column alone never tells you whether " +
			"a call succeeded.",
		Search: &SearchBox{
			Action:      u.prefix + "/soap-calls",
			Q:           q.Q,
			Placeholder: "company code, correlation id, fault code",
		},
	}
	u.header(data, m.Scenario, m.Seed)

	faults := 0
	for _, c := range calls {
		if c.FaultCode != "" {
			faults++
		}
	}
	if faults > 0 {
		data.Banner = &Banner{
			Class: "info",
			Title: strconv.Itoa(faults) + " of " + strconv.Itoa(len(calls)) + " call(s) returned a fault",
			Lines: []string{
				"CH20 has no rate table configured and faults ERP-FX-014 PERMANENT " +
					"every time; retrying it is a graded mistake. ERP-FX-503 is the one " +
					"RETRYABLE fault per run and succeeds on the retry of the same call.",
			},
		}
	}

	rows := make([]Row, 0, len(calls))
	for _, c := range calls {
		if !q.matches(c.CompanyCode, c.CorrelationID, c.FaultCode, c.Severity, c.Signature) {
			continue
		}
		faulted := Cell{Text: "no", Class: cellMuted}
		faultCode, severity := dash(), dash()
		if c.FaultCode != "" {
			faulted = flag("yes")
			faultCode = flag(c.FaultCode)
			severity = Cell{Text: c.Severity}
			if c.Severity == erp.SeverityPermanent {
				severity = flag(c.Severity)
			}
		}
		company := text(c.CompanyCode)
		if c.CompanyCode == "" {
			company = dash()
		}
		correlation := text(c.CorrelationID)
		if c.CorrelationID == "" {
			correlation = dash()
		}
		truncated := yesNo(c.Truncated)
		if c.Truncated {
			truncated = flag("yes")
		}
		rows = append(rows, Row{
			Flagged: false,
			Cells: []Cell{
				num(int64(c.Sequence)),
				company,
				soapActionCell(c),
				statusCode(c.HTTPStatus),
				faulted,
				faultCode,
				severity,
				num(int64(c.RowCount)),
				truncated,
				correlation,
			},
		})
	}
	data.Table = &Table{
		Columns: []Column{
			{Title: "seq", Class: cellNum}, {Title: "company_code"},
			{Title: "soap_action"}, {Title: "http_status", Class: cellNum},
			{Title: "faulted"}, {Title: "fault_code"}, {Title: "severity"},
			{Title: "rows", Class: cellNum}, {Title: "truncated"},
			{Title: "correlation_id"},
		},
		Empty: "no SOAP call in this run",
		Note: "Truncated=true means the table was cut and the meaning is in the " +
			"header: a correct client refuses to post on a truncated rate table.",
	}
	u.paginate(data, rows, q, u.prefix+"/soap-calls")
	return data, nil
}

// soapActionCell renders the SOAPAction verdict of one call.
//
// The call log does not carry the header verbatim, and it does not need to: the
// ERP answers an absent SOAPAction with HTTP 500 and ERP-FX-500, a present but
// wrong or unquoted one with HTTP 200 and ERP-FX-403, and it checks the media
// type before either, so a 415 means the action was never evaluated. Those three
// codes are therefore the verdict.
func soapActionCell(c erp.SOAPCall) Cell {
	switch {
	case c.FaultCode == erp.FaultActionMissing:
		return flag("missing")
	case c.FaultCode == erp.FaultBadAction:
		return flag("wrong or unquoted")
	case c.HTTPStatus == http.StatusUnsupportedMediaType:
		return Cell{Text: "not evaluated", Class: cellMuted}
	}
	return Cell{Text: "valid", Class: cellMuted}
}
