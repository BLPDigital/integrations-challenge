package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestFormat(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        Format
		wantCode    string
	}{
		{"absent", "", FormatJSON, ""},
		{"json", "application/json", FormatJSON, ""},
		{"json with charset", "application/json; charset=utf-8", FormatJSON, ""},
		{"json uppercase", "APPLICATION/JSON", FormatJSON, ""},
		{"ndjson", "application/x-ndjson", FormatNDJSON, ""},
		{"ndjson alias", "application/ndjson", FormatNDJSON, ""},
		{"xml", "application/xml", FormatXML, ""},
		{"text xml alias", "text/xml; charset=UTF-8", FormatXML, ""},
		{"csv", "text/csv", FormatCSV, ""},
		{"csv alias", "application/csv", FormatCSV, ""},
		{"yaml", "text/yaml", "", CodeUnsupportedContentType},
		{"form", "application/x-www-form-urlencoded", "", CodeUnsupportedContentType},
		{"latin1 charset", "text/csv; charset=ISO-8859-1", "", CodeUnsupportedContentType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/ingest/batches", strings.NewReader("{}"))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			} else {
				r.Header.Del("Content-Type")
			}
			got, err := RequestFormat(r)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatalf("want error %s, got format %q", tc.wantCode, got)
				}
				e := AsError(err)
				if e.Code != tc.wantCode || e.Status != 415 {
					t.Fatalf("error = %+v", e)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("format = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResponseFormat(t *testing.T) {
	tests := []struct {
		name        string
		accept      string
		contentType string
		want        Format
		wantCode    string
	}{
		{"absent mirrors json", "", "application/json", FormatJSON, ""},
		{"absent mirrors csv", "", "text/csv", FormatCSV, ""},
		{"absent no content type", "", "", FormatJSON, ""},
		{"explicit ndjson", "application/x-ndjson", "application/json", FormatNDJSON, ""},
		{"wildcard mirrors request", "*/*", "application/xml", FormatXML, ""},
		{"first supported wins", "text/yaml, text/csv", "application/json", FormatCSV, ""},
		{"quality order", "text/csv;q=0.2, application/xml;q=0.9", "application/json", FormatXML, ""},
		{"equal quality keeps client order", "application/xml;q=0.5, text/csv;q=0.5", "application/json", FormatXML, ""},
		{"zero quality is a refusal", "application/xml;q=0, text/csv", "application/json", FormatCSV, ""},
		{"wildcard beats low quality", "application/xml;q=0.1, */*", "text/csv", FormatCSV, ""},
		{"browser accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "application/json", FormatXML, ""},
		{"unsupported only", "text/yaml, application/pdf", "application/json", "", CodeUnsupportedContentType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/outbox/proposals", nil)
			r.Header.Del("Content-Type")
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			if tc.accept != "" {
				r.Header.Set("Accept", tc.accept)
			}
			got, err := ResponseFormat(r)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatalf("want error %s, got %q", tc.wantCode, got)
				}
				if e := AsError(err); e.Code != tc.wantCode || e.Status != 415 {
					t.Fatalf("error = %+v", e)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("format = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAcceptQuality(t *testing.T) {
	tests := []struct {
		params string
		want   int64
	}{
		{"", 1000},
		{"q=1", 1000},
		{"q=1.0", 1000},
		{"q=0", 0},
		{"q=0.5", 500},
		{"q=0.05", 50},
		{"q=0.001", 1},
		{"q=0.8", 800},
		{" q=0.8 ", 800},
		{"charset=utf-8;q=0.3", 300},
		{"q=bogus", 1000},
		{"q=2", 1000},
	}
	for _, tc := range tests {
		t.Run(tc.params, func(t *testing.T) {
			if got := acceptQuality(tc.params); got != tc.want {
				t.Errorf("acceptQuality(%q) = %d, want %d", tc.params, got, tc.want)
			}
		})
	}
}

func TestFormatMediaTypeRoundTrip(t *testing.T) {
	for _, f := range Formats() {
		mt := f.MediaType()
		got, ok := FormatForMediaType(mt)
		if !ok || got != f {
			t.Errorf("%s: media type %q maps back to %q (ok=%v)", f, mt, got, ok)
		}
	}
	if _, ok := FormatForMediaType("text/yaml"); ok {
		t.Error("text/yaml is supported")
	}
	if got := Format("").MediaType(); got != "" {
		t.Errorf("zero format media type = %q", got)
	}
}
