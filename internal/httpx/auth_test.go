package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"", ""},
		{"Bearer tok_abc", "tok_abc"},
		{"bearer tok_abc", "tok_abc"},
		{"BEARER  tok_abc ", "tok_abc"},
		{"Basic dXNlcjpwdw==", ""},
		{"tok_abc", ""},
		{"Bearer", ""},
	}
	for _, tc := range tests {
		t.Run(tc.header, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/x", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := BearerToken(r); got != tc.want {
				t.Errorf("BearerToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestBearerMiddleware(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 1, TokenRequestTTL: 2})
	token := gov.IssueToken()
	h := &okHandler{}
	mw := Bearer(gov)(h)

	// Two valid uses, then the allowance is spent.
	tests := []struct {
		name       string
		token      string
		wantStatus int
		wantCode   string
		retriable  bool
	}{
		{"first use", token, 200, "", false},
		{"second use", token, 200, "", false},
		{"allowance spent", token, 401, CodeTokenExpired, true},
		{"unknown token", "tok_nope", 401, CodeTokenInvalid, false},
		{"no header", "", 401, CodeTokenMissing, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/outbox/proposals", nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, r)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantCode == "" {
				return
			}
			var body Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body: %v", err)
			}
			if body.Code != tc.wantCode || body.Retriable != tc.retriable {
				t.Fatalf("body = %+v", body)
			}
		})
	}
	if h.calls != 2 {
		t.Fatalf("handler calls = %d, want 2", h.calls)
	}
}

func TestBearerExemptions(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 1, TokenRequestTTL: 1})
	h := &okHandler{}
	mw := Bearer(gov)(h)
	for _, path := range []string{
		"/v1/auth/token",
		"/erp/v1/auth/token",
		"/admin/v1/metrics",
		"/erp-admin/v1/reset",
	} {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		if rec.Code != 200 {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
	}
	if h.calls != 4 {
		t.Fatalf("handler calls = %d, want 4", h.calls)
	}
}

func TestBearerWithoutGovernorIsAPassthrough(t *testing.T) {
	h := &okHandler{}
	mw := Bearer(nil)(h)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/x", nil))
	if rec.Code != 200 || h.calls != 1 {
		t.Fatalf("status %d, calls %d", rec.Code, h.calls)
	}
}

func TestAdminTokenCheck(t *testing.T) {
	tests := []struct {
		name  string
		want  string
		given string
		ok    bool
	}{
		{"match", "adm_secret", "adm_secret", true},
		{"absent", "adm_secret", "", false},
		{"wrong", "adm_secret", "adm_other", false},
		{"prefix only", "adm_secret", "adm_", false},
		{"unconfigured fails closed", "", "", false},
		{"unconfigured rejects anything", "", "anything", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/admin/v1/metrics", nil)
			if tc.given != "" {
				r.Header.Set(HeaderAdminToken, tc.given)
			}
			if got := CheckAdminToken(r, tc.want); got != tc.ok {
				t.Errorf("CheckAdminToken = %v, want %v", got, tc.ok)
			}
			rec := httptest.NewRecorder()
			if got := RequireAdminToken(rec, r, tc.want); got != tc.ok {
				t.Errorf("RequireAdminToken = %v, want %v", got, tc.ok)
			}
			if tc.ok {
				return
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			var body Error
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body: %v", err)
			}
			if body.Code != CodeAdminTokenRequired || body.Retriable {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestAdminSurfaceRejectsAConnectorToken(t *testing.T) {
	gov := simclock.New(simclock.Config{Seed: 1, TokenRequestTTL: 250})
	connectorToken := gov.IssueToken()
	h := &okHandler{}
	mw := AdminToken("adm_secret")(h)

	r := httptest.NewRequest("GET", "/admin/v1/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+connectorToken)
	// A connector may also try the admin header with its own token.
	r.Header.Set(HeaderAdminToken, connectorToken)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if h.calls != 0 {
		t.Fatal("a connector token reached the admin surface")
	}

	r = httptest.NewRequest("GET", "/admin/v1/metrics", nil)
	r.Header.Set(HeaderAdminToken, "adm_secret")
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, r)
	if rec.Code != 200 || h.calls != 1 {
		t.Fatalf("status %d, calls %d", rec.Code, h.calls)
	}
}
