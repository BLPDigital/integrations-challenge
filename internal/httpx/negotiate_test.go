package httpx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureRecords is the round-trip fixture. Every value is a string, because
// CSV and XML are string-typed on the wire and the canonical AP model keeps
// keys and amounts in strings for exactly that reason. It contains the nasty
// cases: significant leading zeros, the delimiter, the quote, an embedded line
// break, CP1252-range accents, an ampersand and an empty value.
func fixtureRecords() []map[string]string {
	return []map[string]string{
		{
			"supplier_number": "0000417",
			"name":            "Muster AG",
			"gross_amount":    "1250.00",
			"document_date":   "2026-03-29",
			"note":            `a;b,c "quoted" & <tagged>`,
			"crlf_note":       "before\r\nafter",
			"iban":            "",
		},
		{
			"supplier_number": "0000418",
			"name":            "Zürcher & Söhne",
			"gross_amount":    "-1234.56",
			"document_date":   "2026-02-01",
			"note":            "line one\nline two\twith tab",
			"crlf_note":       "bare\rcarriage",
			"iban":            "CH93****2957",
		},
		{
			"supplier_number": "0000419",
			"name":            "Ünïcodé Ltd",
			"gross_amount":    "0.05",
			"document_date":   "2026-12-31",
			"note":            "trailing delimiter;",
			"crlf_note":       "",
			"iban":            "CH55****0001",
		},
	}
}

// exoticDialect is a CSV dialect that differs from the canonical one in every
// dimension the manifest can declare.
func exoticDialect() CSVDialect {
	return CSVDialect{
		Delimiter:          ";",
		Quote:              "'",
		Escape:             "double",
		LineEnding:         "CRLF",
		DecimalSeparator:   ",",
		ThousandsSeparator: "'",
		NullToken:          "NULL",
		Trim:               "both",
		DateFormat:         "DD.MM.YYYY",
	}
}

// encodeFixture renders records in f, through the same writer the servers use.
func encodeFixture(t *testing.T, f Format, dialect CSVDialect, records []map[string]string) []byte {
	t.Helper()
	raws := make([]json.RawMessage, 0, len(records))
	for _, rec := range records {
		buf, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		raws = append(raws, buf)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/records", nil)
	r.Header.Set("Accept", f.MediaType())
	r = r.WithContext(WithCSVDialect(r.Context(), dialect))
	if err := WriteNegotiated(rec, r, map[string]any{"records": raws}); err != nil {
		t.Fatalf("WriteNegotiated(%s): %v", f, err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != f.MediaType() {
		t.Fatalf("content type = %q, want %q", ct, f.MediaType())
	}
	return rec.Body.Bytes()
}

// decodeToMaps decodes a body in f into comparable string maps.
func decodeToMaps(t *testing.T, f Format, dialect CSVDialect, body []byte) []map[string]string {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/ingest/batches/b1/records", bytes.NewReader(body))
	r.Header.Set("Content-Type", f.MediaType())
	r = r.WithContext(WithCSVDialect(r.Context(), dialect))
	raws, err := DecodeRecords(r, "supplier")
	if err != nil {
		t.Fatalf("DecodeRecords(%s): %v\nbody:\n%s", f, err, body)
	}
	out := make([]map[string]string, 0, len(raws))
	for _, raw := range raws {
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("record %s is not an object of strings: %v", raw, err)
		}
		out = append(out, m)
	}
	return out
}

func TestEveryFormatCombinationRoundTrips(t *testing.T) {
	dialects := map[string]CSVDialect{
		"canonical": {},
		"exotic":    exoticDialect(),
	}
	want := fixtureRecords()
	for _, dialectName := range []string{"canonical", "exotic"} {
		dialect := dialects[dialectName]
		for _, in := range Formats() {
			for _, out := range Formats() {
				t.Run(dialectName+"/"+string(in)+"_to_"+string(out), func(t *testing.T) {
					// Produce a body in the input format, decode it, re-emit
					// it in the output format and decode again. Both legs go
					// through the real negotiation path.
					inBody := encodeFixture(t, in, dialect, want)
					got := decodeToMaps(t, in, dialect, inBody)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("leg 1 (%s): got %#v, want %#v", in, got, want)
					}
					outBody := encodeFixture(t, out, dialect, got)
					got2 := decodeToMaps(t, out, dialect, outBody)
					if !reflect.DeepEqual(got2, want) {
						t.Fatalf("leg 2 (%s -> %s): got %#v, want %#v", in, out, got2, want)
					}
				})
			}
		}
	}
}

func TestDecodeRecordsJSONShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"array", `[{"a":"1"},{"a":"2"}]`, []string{`{"a":"1"}`, `{"a":"2"}`}},
		{"envelope", `{"records":[{"a":"1"}],"next_cursor":"x"}`, []string{`{"a":"1"}`}},
		{"single object", `{"a":"1"}`, []string{`{"a":"1"}`}},
		{"empty array", `[]`, nil},
		{"empty envelope", `{"records":[]}`, nil},
		{"whitespace is compacted", "[ { \"a\" : \"1\" } ]", []string{`{"a":"1"}`}},
		{"empty body", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", MediaJSON)
			got, err := DecodeRecords(r, "supplier")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d records, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if string(got[i]) != tc.want[i] {
					t.Errorf("record %d = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDecodeRecordsNDJSON(t *testing.T) {
	body := "{\"a\":\"1\"}\r\n\n   \n{\"a\":\"2\"}\n"
	r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(body))
	r.Header.Set("Content-Type", MediaNDJSON)
	got, err := DecodeRecords(r, "supplier")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || string(got[0]) != `{"a":"1"}` || string(got[1]) != `{"a":"2"}` {
		t.Fatalf("got %v", got)
	}
}

func TestDecodeRecordsErrors(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		dialect     CSVDialect
		wantCode    string
		wantStatus  int
		wantReason  string
	}{
		{"unsupported type", "text/yaml", "a: 1", CSVDialect{}, CodeUnsupportedContentType, 415, ""},
		{"json scalar", MediaJSON, `42`, CSVDialect{}, CodeMalformedBody, 400, "json_not_an_object"},
		{"json broken", MediaJSON, `[{"a":}]`, CSVDialect{}, CodeMalformedBody, 400, "json_invalid"},
		{"json records not array", MediaJSON, `{"records":{"a":"1"}}`, CSVDialect{}, CodeMalformedBody, 400, "json_records_not_an_array"},
		{"ndjson broken", MediaNDJSON, "{\"a\":\"1\"}\n{oops}\n", CSVDialect{}, CodeMalformedBody, 400, "ndjson_invalid"},
		{"invalid utf8", MediaCSV, "a\n\xff\n", CSVDialect{}, CodeMalformedBody, 400, "invalid_utf8"},
		{"csv empty body", MediaCSV, "", CSVDialect{}, CodeMalformedBody, 400, "csv_header_missing"},
		{"csv field count", MediaCSV, "a,b\n1\n", CSVDialect{}, CodeMalformedBody, 400, "csv_field_count_mismatch"},
		{"csv empty column", MediaCSV, "a,\n1,2\n", CSVDialect{}, CodeMalformedBody, 400, "csv_empty_column_name"},
		{"csv duplicate column", MediaCSV, "a,a\n1,2\n", CSVDialect{}, CodeMalformedBody, 400, "csv_duplicate_column"},
		{"csv unterminated quote", MediaCSV, "a\n\"oops\n", CSVDialect{}, CodeMalformedBody, 400, "csv_unterminated_quote"},
		{"csv trailing data", MediaCSV, "a\n\"x\"y\n", CSVDialect{}, CodeMalformedBody, 400, "csv_quote_trailing_data"},
		{"csv bare quote", MediaCSV, "a\nx\"y\n", CSVDialect{}, CodeMalformedBody, 400, "csv_bare_quote"},
		{"csv header forbidden off", MediaCSV, "a\n1\n", CSVDialect{Header: boolPtr(false)}, CodeMalformedBody, 400, "csv_header_required"},
		{"csv bad escape", MediaCSV, "a\n1\n", CSVDialect{Escape: "backslash"}, CodeMalformedBody, 400, "csv_dialect_unsupported"},
		{"csv bad delimiter", MediaCSV, "a\n1\n", CSVDialect{Delimiter: ";;"}, CodeMalformedBody, 400, "csv_dialect_unsupported"},
		{"csv bad date format", MediaCSV, "a\n1\n", CSVDialect{DateFormat: "TTMMJJ"}, CodeMalformedBody, 400, "csv_dialect_unsupported"},
		{"xml bad root", MediaXML, "<rows><record><a>1</a></record></rows>", CSVDialect{}, CodeMalformedBody, 400, "xml_unexpected_root"},
		{"xml not well formed", MediaXML, "<records><record><a>1</a>", CSVDialect{}, CodeMalformedBody, 400, "xml_not_well_formed"},
		{"xml nested field", MediaXML, "<records><record><a><b>1</b></a></record></records>", CSVDialect{}, CodeMalformedBody, 400, "xml_nested_field"},
		{"xml duplicate field", MediaXML, "<records><record><a>1</a><a>2</a></record></records>", CSVDialect{}, CodeMalformedBody, 400, "xml_duplicate_field"},
		{"xml stray text", MediaXML, "<records>oops<record><a>1</a></record></records>", CSVDialect{}, CodeMalformedBody, 400, "xml_unexpected_text"},
		{"xml deep element", MediaXML, "<records><record><a>1</a></record><record><b><c/></b></record></records>", CSVDialect{}, CodeMalformedBody, 400, "xml_nested_field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			r = r.WithContext(WithCSVDialect(r.Context(), tc.dialect))
			_, err := DecodeRecords(r, "supplier")
			if err == nil {
				t.Fatal("want an error, got none")
			}
			e := AsError(err)
			if e.Code != tc.wantCode || e.Status != tc.wantStatus {
				t.Fatalf("error = %+v, want code %s status %d", e, tc.wantCode, tc.wantStatus)
			}
			if e.Details["dataset"] != "supplier" {
				t.Errorf("details lack the dataset: %+v", e.Details)
			}
			if tc.wantReason != "" && e.Details["reason"] != tc.wantReason {
				t.Errorf("reason = %v, want %s", e.Details["reason"], tc.wantReason)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

func TestDecodeRecordsBodyTooLarge(t *testing.T) {
	body := bytes.Repeat([]byte("x"), MaxRequestBodyBytes+1)
	r := httptest.NewRequest("POST", "/v1/x", bytes.NewReader(body))
	r.Header.Set("Content-Type", MediaNDJSON)
	_, err := DecodeRecords(r, "supplier")
	if err == nil {
		t.Fatal("want an error, got none")
	}
	e := AsError(err)
	if e.Status != http.StatusRequestEntityTooLarge || e.Details["reason"] != "body_too_large" {
		t.Fatalf("error = %+v", e)
	}
}

func TestReadBodyLeavesBodyReadable(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(`{"a":"1"}`))
	r.Header.Set("Content-Type", MediaJSON)
	first, err := ReadBody(r)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if BodyHash(first) != BodyHash([]byte(`{"a":"1"}`)) {
		t.Error("body hash differs from the source")
	}
	recs, err := DecodeRecords(r, "supplier")
	if err != nil {
		t.Fatalf("DecodeRecords after ReadBody: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
}

func TestNullRoundTrip(t *testing.T) {
	// A JSON null survives NDJSON and XML, and survives CSV when the dialect
	// declares a null token.
	tests := []struct {
		name    string
		format  Format
		dialect CSVDialect
		want    string
	}{
		{"ndjson", FormatNDJSON, CSVDialect{}, `{"a":null}`},
		{"xml", FormatXML, CSVDialect{}, `{"a":null}`},
		{"csv with null token", FormatCSV, CSVDialect{NullToken: "NULL"}, `{"a":null}`},
		{"csv without null token", FormatCSV, CSVDialect{}, `{"a":""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/v1/x", nil)
			r.Header.Set("Accept", tc.format.MediaType())
			r = r.WithContext(WithCSVDialect(r.Context(), tc.dialect))
			payload := map[string]any{"records": []json.RawMessage{json.RawMessage(`{"a":null}`)}}
			if err := WriteNegotiated(rec, r, payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			in := httptest.NewRequest("POST", "/v1/x", bytes.NewReader(rec.Body.Bytes()))
			in.Header.Set("Content-Type", tc.format.MediaType())
			in = in.WithContext(WithCSVDialect(in.Context(), tc.dialect))
			got, err := DecodeRecords(in, "supplier")
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got) != 1 || string(got[0]) != tc.want {
				t.Fatalf("got %v, want %s (body %q)", got, tc.want, rec.Body.String())
			}
		})
	}
}

func TestNonStringJSONValuesAreStringified(t *testing.T) {
	// Numbers keep their exact literal, never a float64 round trip, and bools
	// become their text form. This is the documented cost of the string-typed
	// formats and the reason the canonical model keeps money in strings.
	payload := map[string]any{"records": []json.RawMessage{
		json.RawMessage(`{"amount":1250.00,"big":12345678901234567890,"flag":true,"nested":{"b":1},"list":[1,2]}`),
	}}
	for _, f := range []Format{FormatXML, FormatCSV} {
		t.Run(string(f), func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/v1/x", nil)
			r.Header.Set("Accept", f.MediaType())
			if err := WriteNegotiated(rec, r, payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			in := httptest.NewRequest("POST", "/v1/x", bytes.NewReader(rec.Body.Bytes()))
			in.Header.Set("Content-Type", f.MediaType())
			got, err := DecodeRecords(in, "supplier")
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := `{"amount":"1250.00","big":"12345678901234567890","flag":"true","list":"[1,2]","nested":"{\"b\":1}"}`
			if len(got) != 1 || string(got[0]) != want {
				t.Fatalf("got %s\nwant %s", got[0], want)
			}
		})
	}
}

func TestWriteNegotiatedJSONIsVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("Accept", MediaJSON)
	payload := map[string]any{
		"records":        []json.RawMessage{json.RawMessage(`{"name":"A & B <c>"}`)},
		"next_cursor":    "eyJk",
		"max_change_seq": 118422,
		"returned":       1,
		"has_more":       true,
	}
	if err := WriteNegotiated(rec, r, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := strings.TrimSpace(rec.Body.String())
	want := `{"has_more":true,"max_change_seq":118422,"next_cursor":"eyJk","records":[{"name":"A & B <c>"}],"returned":1}`
	if got != want {
		t.Fatalf("body = %s\nwant %s", got, want)
	}
	if rec.Header().Get("Vary") != "Accept" {
		t.Error("Vary: Accept is missing")
	}
	if rec.Header().Get("X-Envelope-Next-Cursor") != "" {
		t.Error("json responses must not need envelope headers")
	}
}

func TestWriteNegotiatedEnvelopeHeaders(t *testing.T) {
	payload := map[string]any{
		"records":        []json.RawMessage{json.RawMessage(`{"a":"1"}`)},
		"next_cursor":    "eyJkYXRhc2V0",
		"max_change_seq": 118422,
		"has_more":       true,
		"nested":         map[string]string{"skipped": "yes"},
		"unsafe":         "line\nbreak",
	}
	for _, f := range []Format{FormatNDJSON, FormatXML, FormatCSV} {
		t.Run(string(f), func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/v1/x", nil)
			r.Header.Set("Accept", f.MediaType())
			if err := WriteNegotiated(rec, r, payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			h := rec.Header()
			if got := h.Get("X-Envelope-Next-Cursor"); got != "eyJkYXRhc2V0" {
				t.Errorf("next cursor header = %q", got)
			}
			if got := h.Get("X-Envelope-Max-Change-Seq"); got != "118422" {
				t.Errorf("max change seq header = %q", got)
			}
			if got := h.Get("X-Envelope-Has-More"); got != "true" {
				t.Errorf("has more header = %q", got)
			}
			if got := h.Get("X-Envelope-Nested"); got != "" {
				t.Errorf("nested member leaked into a header: %q", got)
			}
			if got := h.Get("X-Envelope-Unsafe"); got != "" {
				t.Errorf("unsafe member leaked into a header: %q", got)
			}
		})
	}
}

func TestWriteNegotiatedUnsupportedAcceptIs415(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("Accept", "text/yaml")
	err := WriteNegotiated(rec, r, map[string]any{"records": []json.RawMessage{}})
	if err == nil {
		t.Fatal("want an error")
	}
	if rec.Code != 415 {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if e := AsError(err); e.Code != CodeUnsupportedContentType {
		t.Fatalf("error = %+v", e)
	}
}

func TestWriteNegotiatedCSVColumnsAndEmpty(t *testing.T) {
	dialect := CSVDialect{Columns: []string{"b", "a"}}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("Accept", MediaCSV)
	r = r.WithContext(WithCSVDialect(r.Context(), dialect))
	payload := map[string]any{"records": []json.RawMessage{
		json.RawMessage(`{"a":"1","b":"2","dropped":"3"}`),
	}}
	if err := WriteNegotiated(rec, r, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, want := rec.Body.String(), "b,a\n2,1\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	// No records and no declared columns: an empty body, because a header row
	// cannot be invented.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("Accept", MediaCSV)
	if err := WriteNegotiated(rec, r, map[string]any{"records": []json.RawMessage{}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

func TestWriteNegotiatedSingleObjectIsOneRecord(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/v1/x", nil)
	r.Header.Set("Accept", MediaXML)
	if err := WriteNegotiated(rec, r, map[string]string{"a": "1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := "<records><record><a>1</a></record></records>"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestWriteNegotiatedStatusIsHonoured(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/x", nil)
	r.Header.Set("Accept", MediaJSON)
	if err := WriteNegotiatedStatus(rec, r, http.StatusMultiStatus, map[string]any{"records": []json.RawMessage{}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207", rec.Code)
	}
}

func TestXMLEnvelopeMetadataIsIgnoredOnDecode(t *testing.T) {
	body := `<records><meta><next_cursor>x</next_cursor></meta><record><a>1</a></record></records>`
	r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(body))
	r.Header.Set("Content-Type", MediaXML)
	got, err := DecodeRecords(r, "supplier")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || string(got[0]) != `{"a":"1"}` {
		t.Fatalf("got %v", got)
	}
}

func TestXMLNamespacePrefixesAreIgnored(t *testing.T) {
	body := `<ns2:records xmlns:ns2="urn:x"><ns2:record><ns2:a>1</ns2:a></ns2:record></ns2:records>`
	r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(body))
	r.Header.Set("Content-Type", MediaXML)
	got, err := DecodeRecords(r, "supplier")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || string(got[0]) != `{"a":"1"}` {
		t.Fatalf("got %v", got)
	}
}

func TestUnionColumnsIsSortedAndDeterministic(t *testing.T) {
	recs := [][]recordField{
		{{Name: "z"}, {Name: "a"}},
		{{Name: "m"}, {Name: "a"}},
	}
	want := []string{"a", "m", "z"}
	for i := 0; i < 50; i++ {
		got := unionColumns(recs)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: got %v, want %v", i, got, want)
		}
		if !sort.StringsAreSorted(got) {
			t.Fatalf("not sorted: %v", got)
		}
	}
}

func TestFormatString(t *testing.T) {
	for _, f := range Formats() {
		if f.String() != string(f) {
			t.Errorf("%q.String() = %q", f, f.String())
		}
	}
}

func TestWriteNegotiatedRejectsUnusableShapes(t *testing.T) {
	tests := []struct {
		name    string
		format  Format
		payload any
	}{
		{"scalar payload in csv", FormatCSV, 42},
		{"scalar payload in xml", FormatXML, "text"},
		{"field name is not an xml name", FormatXML, map[string]string{"amount(chf)": "1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/v1/x", nil)
			r.Header.Set("Accept", tc.format.MediaType())
			err := WriteNegotiated(rec, r, tc.payload)
			if err == nil {
				t.Fatalf("want an error, got body %q", rec.Body.String())
			}
			if e := AsError(err); e.Code != CodeInternal || e.Status != http.StatusInternalServerError {
				t.Fatalf("error = %+v", e)
			}
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
		})
	}
}

func TestControlCharactersAreEscapedInJSON(t *testing.T) {
	// A CSV cell may carry a control character; the JSON record built from it
	// must stay valid JSON.
	body := "a\n\"x\x01y\"\n"
	r := httptest.NewRequest("POST", "/v1/x", strings.NewReader(body))
	r.Header.Set("Content-Type", MediaCSV)
	got, err := DecodeRecords(r, "supplier")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || string(got[0]) != `{"a":"x\u0001y"}` {
		t.Fatalf("got %s", got[0])
	}
	if !json.Valid(got[0]) {
		t.Fatalf("record is not valid json: %s", got[0])
	}
}
