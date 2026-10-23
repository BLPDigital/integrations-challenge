package canonical

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"

	"github.com/fatjonblp/coding_challange_integrations/internal/importer"
	"github.com/fatjonblp/coding_challange_integrations/internal/model"
)

// parse runs a format over in and fails the test on a returned error, which is
// reserved for programming and environment faults: every data defect has to come
// back as a diagnostic.
func parse(t *testing.T, f importer.Format, in string, opt importer.Options) *importer.Result {
	t.Helper()
	res, err := f.Parse(context.Background(), []byte(in), opt)
	if err != nil {
		t.Fatalf("%s.Parse returned an error, which is reserved for programming faults: %v", f.ID(), err)
	}
	if res == nil {
		t.Fatalf("%s.Parse returned a nil Result and a nil error", f.ID())
	}
	return res
}

// codes returns the diagnostic codes of a result in the order they are reported.
func codes(r *importer.Result) []string {
	out := make([]string, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		out = append(out, d.Code)
	}
	return out
}

// payload decodes the canonical payload of the document with the given key.
func payload(t *testing.T, r *importer.Result, key string) map[string]any {
	t.Helper()
	for _, d := range r.Documents {
		if d.Key != key {
			continue
		}
		v, err := model.DecodeJSONTree(d.Payload)
		if err != nil {
			t.Fatalf("payload of %s is not JSON: %v", key, err)
		}
		obj, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("payload of %s is not an object", key)
		}
		return obj
	}
	keys := make([]string, 0, len(r.Documents))
	for _, d := range r.Documents {
		keys = append(keys, d.Key)
	}
	t.Fatalf("no document with key %q; got %v", key, keys)
	return nil
}

// hasDiag reports whether a result carries a diagnostic with the given code on
// the given field.
func hasDiag(r *importer.Result, code, field string) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code && d.Field == field {
			return true
		}
	}
	return false
}

func TestRegisteredFormats(t *testing.T) {
	for _, id := range []string{FormatCSV, FormatXML, FormatJSON} {
		f, ok := importer.Lookup(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		if f.ID() != id {
			t.Errorf("Lookup(%q).ID() = %q", id, f.ID())
		}
		if _, ok := f.(importer.StreamParser); !ok {
			t.Errorf("%s does not implement importer.StreamParser; a 50'000-record file must parse in bounded memory", id)
		}
	}
}

func TestDatasetFieldsMatchTheModel(t *testing.T) {
	// The field table drives all three formats. A field the model has and the
	// table has not is a field this package silently drops, and the record would
	// validate and store nothing.
	for _, ds := range model.Datasets() {
		fields := Fields(ds)
		if len(fields) == 0 {
			t.Errorf("dataset %s has no field table", ds)
			continue
		}
		want := canonicalKeysOf(t, ds)
		got := map[string]bool{}
		for _, f := range fields {
			if got[f] {
				t.Errorf("dataset %s lists field %q twice", ds, f)
			}
			got[f] = true
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("dataset %s: the model has field %q and the table does not", ds, w)
			}
		}
		for f := range got {
			if !contains(want, f) {
				t.Errorf("dataset %s: the table has field %q and the model does not", ds, f)
			}
		}
	}
}

// canonicalKeysOf returns the canonical JSON field names of a dataset's zero
// entity, which is the model's own answer to "what fields does this record have".
func canonicalKeysOf(t *testing.T, ds model.Dataset) []string {
	t.Helper()
	var v any
	switch ds {
	case model.DatasetSupplier:
		v = model.Supplier{}
	case model.DatasetCostCenter:
		v = model.CostCenter{}
	case model.DatasetPurchaseOrder:
		v = model.PurchaseOrder{}
	case model.DatasetPurchaseOrderLine:
		v = model.PurchaseOrderLine{}
	case model.DatasetFxRate:
		v = model.FxRate{}
	case model.DatasetInvoice:
		v = model.APInvoice{}
	case model.DatasetInvoiceLine:
		// A standalone line additionally carries its invoice's key and the
		// currency its amounts are expressed in.
		v = InvoiceLineRecord{}
	case model.DatasetOutboxAck:
		v = AckRecord{}
	default:
		t.Fatalf("no entity for dataset %s", ds)
	}
	raw, err := model.CanonicalJSON(v)
	if err != nil {
		t.Fatalf("canonicalizing %s: %v", ds, err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	if ds == model.DatasetInvoiceLine {
		out = append(out, "currency")
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestAllDatasetsRoundTripThroughEveryFormat(t *testing.T) {
	// Channel and format are independent axes: the same logical record
	// delivered as CSV, as XML and as JSON has to produce the same key and the
	// same canonical payload, because the state digest is computed over exactly
	// that and grading depends on it being channel-blind.
	tests := []struct {
		dataset model.Dataset
		key     string
		csv     string
		xml     string
		js      string
	}{
		{
			dataset: model.DatasetSupplier,
			key:     "0000417",
			csv: "supplier_number,name,country,currency,iban,vat_number,payment_terms_days,blocked,change_seq\n" +
				"0000417,Steinbach Industrie AG,CH,CHF,CH9300762011623852957,CHE-295.990.745,30,false,118422\n",
			xml: `<records><record><supplier_number>0000417</supplier_number><name>Steinbach Industrie AG</name>` +
				`<country>CH</country><currency>CHF</currency><iban>CH9300762011623852957</iban>` +
				`<vat_number>CHE-295.990.745</vat_number><payment_terms_days>30</payment_terms_days>` +
				`<blocked>false</blocked><change_seq>118422</change_seq></record></records>`,
			js: `[{"supplier_number":"0000417","name":"Steinbach Industrie AG","country":"CH","currency":"CHF",` +
				`"iban":"CH9300762011623852957","vat_number":"CHE-295.990.745","payment_terms_days":30,` +
				`"blocked":false,"change_seq":118422}]`,
		},
		{
			dataset: model.DatasetCostCenter,
			key:     "0815",
			csv:     "code,name,company_code,valid_from,valid_to,blocked\n0815,Werk Wil,CH10,2020-01-01,,false\n",
			xml: `<records><record><code>0815</code><name>Werk Wil</name><company_code>CH10</company_code>` +
				`<valid_from>2020-01-01</valid_from><valid_to></valid_to><blocked>false</blocked></record></records>`,
			js: `[{"code":"0815","name":"Werk Wil","company_code":"CH10","valid_from":"2020-01-01","valid_to":"","blocked":false}]`,
		},
		{
			dataset: model.DatasetPurchaseOrder,
			key:     "4500001234",
			csv: "po_number,supplier_number,company_code,currency,status,order_date,cost_center,change_seq\n" +
				"4500001234,0000417,CH10,CHF,OPEN,2026-01-02,0012340,118109\n",
			xml: `<records><record><po_number>4500001234</po_number><supplier_number>0000417</supplier_number>` +
				`<company_code>CH10</company_code><currency>CHF</currency><status>OPEN</status>` +
				`<order_date>2026-01-02</order_date><cost_center>0012340</cost_center><change_seq>118109</change_seq></record></records>`,
			js: `[{"po_number":"4500001234","supplier_number":"0000417","company_code":"CH10","currency":"CHF",` +
				`"status":"OPEN","order_date":"2026-01-02","cost_center":"0012340","change_seq":118109}]`,
		},
		{
			dataset: model.DatasetPurchaseOrderLine,
			key:     "4500001234" + model.KeySeparator + "00010",
			csv: "po_number,line_no,material,description,quantity,uom,unit_price,currency,gl_account,cost_center,change_seq\n" +
				"4500001234,00010,MAT-1,Ersatzteil,12.000,EA,45.5000,CHF,0006400,0012340,118109\n",
			xml: `<records><record><po_number>4500001234</po_number><line_no>00010</line_no><material>MAT-1</material>` +
				`<description>Ersatzteil</description><quantity>12.000</quantity><uom>EA</uom>` +
				`<unit_price>45.5000</unit_price><currency>CHF</currency><gl_account>0006400</gl_account>` +
				`<cost_center>0012340</cost_center><change_seq>118109</change_seq></record></records>`,
			js: `[{"po_number":"4500001234","line_no":"00010","material":"MAT-1","description":"Ersatzteil",` +
				`"quantity":"12.000","uom":"EA","unit_price":"45.5000","currency":"CHF","gl_account":"0006400",` +
				`"cost_center":"0012340","change_seq":118109}]`,
		},
		{
			dataset: model.DatasetFxRate,
			key:     "EUR" + model.KeySeparator + "CHF" + model.KeySeparator + "2026-01-15" + model.KeySeparator + "DAILY",
			csv: "base,quote,valid_from,rate_type,valid_to,rate,rate_factor,sequence,status\n" +
				"EUR,CHF,2026-01-15,DAILY,2026-01-16,0.931000,1,3,ACTIVE\n",
			xml: `<records><record><base>EUR</base><quote>CHF</quote><valid_from>2026-01-15</valid_from>` +
				`<rate_type>DAILY</rate_type><valid_to>2026-01-16</valid_to><rate>0.931000</rate>` +
				`<rate_factor>1</rate_factor><sequence>3</sequence><status>ACTIVE</status></record></records>`,
			js: `[{"base":"EUR","quote":"CHF","valid_from":"2026-01-15","rate_type":"DAILY","valid_to":"2026-01-16",` +
				`"rate":"0.931000","rate_factor":1,"sequence":3,"status":"ACTIVE"}]`,
		},
		{
			dataset: model.DatasetOutboxAck,
			key:     "prop-000000000042",
			csv: "proposal_id,status,external_document_number,external_revision,external_fiscal_year,external_posting_date,idempotency_key,run_id,posted_at,attempts,http_status,idempotency_replay,error,reason\n" +
				"prop-000000000042,posted,AP-2026-0004311,1,2026,2026-01-31,blp:acme-ch:prop-000000000042:9f21aa,run_7f3c1a,2026-01-31T09:15:00Z,1,201,false,,\n",
			xml: `<records><record><proposal_id>prop-000000000042</proposal_id><status>posted</status>` +
				`<external_document_number>AP-2026-0004311</external_document_number><external_revision>1</external_revision>` +
				`<external_fiscal_year>2026</external_fiscal_year><external_posting_date>2026-01-31</external_posting_date>` +
				`<idempotency_key>blp:acme-ch:prop-000000000042:9f21aa</idempotency_key><run_id>run_7f3c1a</run_id>` +
				`<posted_at>2026-01-31T09:15:00Z</posted_at><attempts>1</attempts><http_status>201</http_status>` +
				`<idempotency_replay>false</idempotency_replay><error></error><reason></reason></record></records>`,
			js: `[{"proposal_id":"prop-000000000042","status":"posted","external_document_number":"AP-2026-0004311",` +
				`"external_revision":1,"external_fiscal_year":2026,"external_posting_date":"2026-01-31",` +
				`"idempotency_key":"blp:acme-ch:prop-000000000042:9f21aa","run_id":"run_7f3c1a",` +
				`"posted_at":"2026-01-31T09:15:00Z","attempts":1,"http_status":201,"idempotency_replay":false,` +
				`"error":"","reason":""}]`,
		},
	}

	for _, tc := range tests {
		t.Run(string(tc.dataset), func(t *testing.T) {
			opt := importer.Options{Filename: "f", Dataset: string(tc.dataset)}
			results := map[string]*importer.Result{
				FormatCSV:  parse(t, NewCSV(), tc.csv, opt),
				FormatXML:  parse(t, NewXML(), tc.xml, opt),
				FormatJSON: parse(t, NewJSON(), tc.js, opt),
			}
			var reference string
			for _, id := range []string{FormatCSV, FormatJSON, FormatXML} {
				r := results[id]
				if !r.Accepted {
					t.Fatalf("%s: not accepted, diagnostics %v", id, r.Diagnostics)
				}
				if len(r.Documents) != 1 {
					t.Fatalf("%s: %d documents, want 1", id, len(r.Documents))
				}
				if r.Documents[0].Key != tc.key {
					t.Errorf("%s: key = %q, want %q", id, r.Documents[0].Key, tc.key)
				}
				body := string(r.Documents[0].Payload)
				if reference == "" {
					reference = body
					continue
				}
				if body != reference {
					t.Errorf("%s produced a different canonical payload than the earlier formats:\n %s\n %s", id, body, reference)
				}
			}
		})
	}
}

func TestInvoiceRoundTripsWithEmbeddedLines(t *testing.T) {
	// The invoice is the one dataset whose CSV and JSON shapes differ: a CSV
	// cell cannot hold a list, so a CSV sender delivers the lines as an
	// invoice_line file. What must agree is the content, not the framing.
	opt := importer.Options{Filename: "i", Dataset: "invoice"}
	js := `[{"supplier_number":"0000105","supplier_invoice_number":"0004711","company_code":"CH10",
	  "document_type":"RE","document_date":"2026-01-15","receipt_date":"2026-01-18",
	  "po_number":"4500001234/00010","currency":"CHF",
	  "gross_amount":{"amount_minor":134563,"currency":"CHF","scale":2},
	  "vat_amount":"100.83","vat_code":"V81","payment_terms_days":30,
	  "discount_raw":"2.00","discount_days":10,"cost_center":"0012340","text":"Wartung Presse 3",
	  "lines":[{"line_no":"00010","gl_account":"0006400","cost_center":"0012350","quantity":"12.000",
	            "uom":"EA","unit_price":"45.5000","line_amount":"546.00","tax_code":"V81"}]}]`
	xm := `<records><record>
	 <supplier_number>0000105</supplier_number><supplier_invoice_number>0004711</supplier_invoice_number>
	 <company_code>CH10</company_code><document_type>RE</document_type>
	 <document_date>2026-01-15</document_date><receipt_date>2026-01-18</receipt_date>
	 <po_number>4500001234/00010</po_number><currency>CHF</currency>
	 <gross_amount><amount_minor>134563</amount_minor><currency>CHF</currency><scale>2</scale></gross_amount>
	 <vat_amount>100.83</vat_amount><vat_code>V81</vat_code><payment_terms_days>30</payment_terms_days>
	 <discount_raw>2.00</discount_raw><discount_days>10</discount_days>
	 <cost_center>0012340</cost_center><text>Wartung Presse 3</text>
	 <lines><line_no>00010</line_no><gl_account>0006400</gl_account><cost_center>0012350</cost_center>
	   <quantity>12.000</quantity><uom>EA</uom><unit_price>45.5000</unit_price>
	   <line_amount>546.00</line_amount><tax_code>V81</tax_code></lines>
	</record></records>`

	rj := parse(t, NewJSON(), js, opt)
	rx := parse(t, NewXML(), xm, opt)
	for id, r := range map[string]*importer.Result{FormatJSON: rj, FormatXML: rx} {
		if !r.Accepted {
			t.Fatalf("%s: not accepted: %v", id, r.Diagnostics)
		}
		if r.Totals.ComputedLines != 1 {
			t.Errorf("%s: computed lines = %d, want 1", id, r.Totals.ComputedLines)
		}
	}
	if string(rj.Documents[0].Payload) != string(rx.Documents[0].Payload) {
		t.Errorf("JSON and XML produced different invoices:\n %s\n %s", rj.Documents[0].Payload, rx.Documents[0].Payload)
	}
	p := payload(t, rj, model.InvoiceKey("0000105", "0004711"))
	if p["gross_amount"] != "1345.63" {
		t.Errorf("gross_amount = %v, want the exact decimal 1345.63", p["gross_amount"])
	}
	if p["discount_raw"] != "2.00" {
		t.Errorf("discount_raw = %v, want the source value verbatim", p["discount_raw"])
	}
}

func TestSingleEmbeddedLineIsStillAList(t *testing.T) {
	// An XML invoice with exactly one <lines> element is a one-line invoice, not
	// an invoice whose lines field is an object. A parser that decides by shape
	// gets every single-line invoice in a file wrong.
	r := parse(t, NewXML(), `<records><record><supplier_number>0000105</supplier_number>
	 <supplier_invoice_number>0004711</supplier_invoice_number><company_code>CH10</company_code>
	 <document_type>RE</document_type><document_date>2026-01-15</document_date><currency>CHF</currency>
	 <gross_amount>100.00</gross_amount><vat_amount>0.00</vat_amount>
	 <lines><line_no>00010</line_no><gl_account>0006400</gl_account><cost_center>0012350</cost_center>
	  <quantity>1.000</quantity><uom>EA</uom><unit_price>100.0000</unit_price><line_amount>100.00</line_amount></lines>
	</record></records>`, importer.Options{Dataset: "invoice"})
	if !r.Accepted {
		t.Fatalf("not accepted: %v", r.Diagnostics)
	}
	if r.Totals.ComputedLines != 1 {
		t.Fatalf("computed lines = %d, want 1", r.Totals.ComputedLines)
	}
}

func TestFieldsIsCopiedAndComplete(t *testing.T) {
	f := Fields(model.DatasetSupplier)
	if len(f) == 0 {
		t.Fatal("Fields returned nothing for supplier")
	}
	f[0] = "tampered"
	if Fields(model.DatasetSupplier)[0] == "tampered" {
		t.Error("Fields returned the table's own slice; a caller mutated it")
	}
	if Fields(model.Dataset("creditors")) != nil {
		t.Error("Fields answered for a dataset that does not exist")
	}
	// "lines" is a field of the JSON and XML invoice shapes and has no CSV
	// column: a cell cannot hold a list.
	if !contains(Fields(model.DatasetInvoice), "lines") {
		t.Error("the invoice field table does not carry the embedded line list")
	}
}

func TestCanonicalFieldNamesIsSortedAndCoversEveryDataset(t *testing.T) {
	all := CanonicalFieldNames()
	if !sort.StringsAreSorted(all) {
		t.Errorf("CanonicalFieldNames is not sorted: %v", all)
	}
	for _, ds := range model.Datasets() {
		for _, f := range Fields(ds) {
			if !contains(all, f) {
				t.Errorf("%s.%s is missing from CanonicalFieldNames, so a header naming only that column is not detected", ds, f)
			}
		}
	}
}

func TestDetectRejectsTheOtherFormatsShapes(t *testing.T) {
	// The negative direction of detection, asserted here as well as in the
	// framework's cross-format test, because this is where a change to one
	// detector is made.
	samples := map[string]string{
		FormatCSV:  "supplier_number,name,currency\n0000417,A,CHF\n",
		FormatXML:  `<records><record><supplier_number>0000417</supplier_number></record></records>`,
		FormatJSON: `[{"supplier_number":"0000417"}]`,
	}
	legacy := "VORLAUF;2.1;0100;3101260612;CHF;SUBSG01;BLP;P\r\nKOPF;0004711;0000105;RE;150126;;;CHF;1'345.63;100.83\r\n"
	formats := map[string]importer.Format{FormatCSV: NewCSV(), FormatXML: NewXML(), FormatJSON: NewJSON()}

	for id, f := range formats {
		for sampleID, body := range samples {
			got := f.Detect(importer.Head([]byte(body)), importer.Options{})
			if want := id == sampleID; got != want {
				t.Errorf("%s.Detect(%s sample) = %v, want %v", id, sampleID, got, want)
			}
		}
		if f.Detect(importer.Head([]byte(legacy)), importer.Options{Dialect: httpx.CSVDialect{Delimiter: ";"}}) {
			t.Errorf("%s detected a legacy KRED-EXP export; a Vorlaufsatz is not a header row", id)
		}
		if f.Detect(nil, importer.Options{}) {
			t.Errorf("%s detected an empty file", id)
		}
	}
}
