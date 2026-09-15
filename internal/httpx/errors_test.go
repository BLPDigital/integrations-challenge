package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorConstructors(t *testing.T) {
	tests := []struct {
		name      string
		err       *Error
		code      string
		status    int
		retriable bool
		hint      int64
	}{
		{"rate limited", RateLimited(40), CodeRateLimited, 429, true, 40},
		{"quarantined", Quarantined(), CodeQuarantined, 403, false, 0},
		{"quota exhausted", QuotaExhausted(), CodeQuotaExhausted, 503, false, 0},
		{"unavailable", Unavailable(), CodeUnavailable, 503, true, 0},
		{"token expired", TokenExpired(), CodeTokenExpired, 401, true, 0},
		{"token missing", TokenMissing(), CodeTokenMissing, 401, false, 0},
		{"token invalid", TokenInvalid(), CodeTokenInvalid, 401, false, 0},
		{"admin token", AdminTokenRequired(), CodeAdminTokenRequired, 403, false, 0},
		{"unsupported", UnsupportedContentType("text/yaml"), CodeUnsupportedContentType, 415, false, 0},
		{"malformed", MalformedBody("json_invalid", "bad"), CodeMalformedBody, 400, false, 0},
		{"idem required", IdempotencyKeyRequired(), CodeIdempotencyKeyRequired, 400, false, 0},
		{"idem reused", IdempotencyKeyReused(), CodeIdempotencyKeyReused, 409, false, 0},
		{"chunk gap", ChunkGap(3, 5), CodeChunkGap, 409, false, 0},
		{"cursor", InvalidCursor(), CodeInvalidCursor, 400, false, 0},
		{"internal", Internal("boom"), CodeInternal, 500, true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Code != tc.code {
				t.Errorf("code = %q, want %q", tc.err.Code, tc.code)
			}
			if tc.err.Status != tc.status {
				t.Errorf("status = %d, want %d", tc.err.Status, tc.status)
			}
			if tc.err.Retriable != tc.retriable {
				t.Errorf("retriable = %v, want %v", tc.err.Retriable, tc.retriable)
			}
			if tc.err.RetryAfterHintMs != tc.hint {
				t.Errorf("hint = %d, want %d", tc.err.RetryAfterHintMs, tc.hint)
			}
			if tc.err.Message == "" {
				t.Error("message is empty")
			}
			// Every error body must be writable and must carry retriable.
			rec := httptest.NewRecorder()
			Fail(rec, tc.err)
			if rec.Code != tc.status {
				t.Errorf("written status = %d, want %d", rec.Code, tc.status)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not json: %v (%s)", err, rec.Body.String())
			}
			for _, key := range []string{"code", "message", "retriable", "details"} {
				if _, ok := body[key]; !ok {
					t.Errorf("body has no %q member: %s", key, rec.Body.String())
				}
			}
		})
	}
}

func TestWriteErrorRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		status int
		hint   int64
		want   string
	}{
		{"429 short hint", 429, 40, "1"},
		{"429 no hint", 429, 0, "1"},
		{"429 long hint", 429, 2500, "3"},
		{"503 carries none", 503, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tc.status, &Error{Code: "X", Message: "m", RetryAfterHintMs: tc.hint})
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Errorf("Retry-After = %q, want %q", got, tc.want)
			}
			if ct := rec.Header().Get("Content-Type"); ct != MediaJSON {
				t.Errorf("content type = %q, want %q", ct, MediaJSON)
			}
		})
	}
}

func TestWriteErrorAlwaysCarriesRetriableAndDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 400, &Error{Code: "X", Message: "m"})
	got := strings.TrimSpace(rec.Body.String())
	want := `{"code":"X","message":"m","retriable":false,"details":{}}`
	if got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestAsError(t *testing.T) {
	e := RateLimited(40)
	if got := AsError(e); got != e {
		t.Errorf("AsError lost the *Error")
	}
	wrapped := &wrapErr{inner: e}
	if got := AsError(wrapped); got != e {
		t.Errorf("AsError did not unwrap")
	}
	plain := AsError(errors.New("boom"))
	if plain.Code != CodeInternal || plain.Status != http.StatusInternalServerError {
		t.Errorf("plain error = %+v", plain)
	}
	if nilErr := AsError(nil); nilErr.Code != CodeInternal {
		t.Errorf("nil error = %+v", nilErr)
	}
	var typed *Error
	if got := typed.Error(); got != "<nil>" {
		t.Errorf("nil receiver Error() = %q", got)
	}
}

type wrapErr struct{ inner error }

func (w *wrapErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrapErr) Unwrap() error { return w.inner }

func TestErrorDetails(t *testing.T) {
	e := ChunkGap(3, 5)
	if e.Details["expected"] != int64(3) || e.Details["seen"] != int64(5) {
		t.Fatalf("details = %+v", e.Details)
	}
	rec := httptest.NewRecorder()
	Fail(rec, e)
	got := strings.TrimSpace(rec.Body.String())
	want := `{"code":"CHUNK_GAP","message":"chunk ordinal out of sequence","retriable":false,"details":{"expected":3,"seen":5}}`
	if got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestFailWithoutStatusIs500(t *testing.T) {
	rec := httptest.NewRecorder()
	Fail(rec, &Error{Code: "X", Message: "m"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
