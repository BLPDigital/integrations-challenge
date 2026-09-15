package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// The machine-readable codes of the canonical error body. The Governor owns the
// admission codes (simclock.CodeRateLimited, simclock.CodeQuarantined,
// simclock.CodeQuotaExhausted, simclock.CodeUnavailable) and they are
// re-exported here so a handler needs one import for the whole vocabulary.
const (
	// CodeRateLimited is 429: token bucket empty or the seed-keyed injector.
	CodeRateLimited = simclock.CodeRateLimited
	// CodeQuarantined is 403 after 20 consecutive 429s on one endpoint.
	CodeQuarantined = simclock.CodeQuarantined
	// CodeQuotaExhausted is 503 for the rest of the run, retriable false.
	CodeQuotaExhausted = simclock.CodeQuotaExhausted
	// CodeUnavailable is the injected, always retriable 503.
	CodeUnavailable = simclock.CodeUnavailable

	// CodeTokenExpired is 401 for a token past its request allowance or its
	// virtual deadline. Retriable: the client refreshes and retries.
	CodeTokenExpired = "TOKEN_EXPIRED"
	// CodeTokenMissing is 401 for an absent or malformed Authorization header.
	CodeTokenMissing = "TOKEN_MISSING"
	// CodeTokenInvalid is 401 for a bearer token this run never issued.
	CodeTokenInvalid = "TOKEN_INVALID"
	// CodeAdminTokenRequired is 403 on the admin surface without the admin
	// token. A connector token can never reach an admin path.
	CodeAdminTokenRequired = "ADMIN_TOKEN_REQUIRED"

	// CodeUnsupportedContentType is 415 for a request or response media type
	// outside the four supported wire formats.
	CodeUnsupportedContentType = "UNSUPPORTED_CONTENT_TYPE"
	// CodeMalformedBody is 400 for a body that is the right media type but the
	// wrong shape. Details carry a "reason" from the documented vocabulary in
	// negotiate.go.
	CodeMalformedBody = "MALFORMED_BODY"

	// CodeIdempotencyKeyRequired is 400 for a mutating versioned request with
	// no Idempotency-Key header.
	CodeIdempotencyKeyRequired = "IDEMPOTENCY_KEY_REQUIRED"
	// CodeIdempotencyKeyReused is 409 for a known key presented with a
	// different request body.
	CodeIdempotencyKeyReused = "IDEMPOTENCY_KEY_REUSED"
	// CodeChunkGap is 409 for a chunk ordinal that skips one, with the expected
	// and seen ordinals in Details.
	CodeChunkGap = "CHUNK_GAP"

	// CodeInvalidCursor is 400 for a cursor that fails its signature check, is
	// not base64url, or is not the canonical JSON shape.
	CodeInvalidCursor = "INVALID_CURSOR"
	// CodeInternal is 500 for a server-side fault. It is never a client's
	// fault and never retriable-false advice about the client's own request.
	CodeInternal = "INTERNAL"
)

// Headers written or read by this package.
const (
	// HeaderIdempotencyKey carries the client's idempotency key.
	HeaderIdempotencyKey = "Idempotency-Key"
	// HeaderIdempotentReplay is set to "true" on a replayed response.
	HeaderIdempotentReplay = "Idempotent-Replay"
	// HeaderAdminToken carries the admin token on the admin surface.
	HeaderAdminToken = "X-Admin-Token"
)

// Error is the canonical error body, byte-identical on both servers:
//
//	{"code":"RATE_LIMITED","message":"token bucket empty","retriable":true,
//	 "retry_after_hint_ms":40,"details":{}}
//
// Retriable is machine-readable and authoritative: a client that retries a
// retriable:false response is making a mistake the grader scores. It has no
// omitempty, so an error body can never be written without it. Details is
// materialized as {} when it is nil, for the same reason.
// RetryAfterHintMs is the one member that may be absent: it is omitted when it
// is zero, because a hint of zero is not a hint.
//
// Details values must be JSON-marshallable and deterministic; encoding/json
// sorts the keys, so the bytes do not depend on map iteration order. Nothing in
// Details may carry a value the client was supposed to compute: an error says
// what is wrong and why, never what the right answer would have been.
//
// Status is the HTTP status the code is documented with. It is not part of the
// body and exists so a handler can write a constructor's result with Fail
// instead of repeating the status at every call site.
type Error struct {
	Code             string         `json:"code"`
	Message          string         `json:"message"`
	Retriable        bool           `json:"retriable"`
	RetryAfterHintMs int64          `json:"retry_after_hint_ms,omitempty"`
	Details          map[string]any `json:"details"`
	Status           int            `json:"-"`
}

// Error implements error. The message is "CODE: message"; a nil receiver is
// rendered rather than panicking, because an error value reaches a log line on
// paths where a nil check would be noise.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

// WithDetail sets one Details entry and returns e. It mutates the receiver, so
// it is meant to be chained onto a constructor result and never applied to a
// shared value.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any, 1)
	}
	e.Details[key] = value
	return e
}

// AsError converts err to the canonical error body. An *Error is returned as
// is; anything else becomes CodeInternal with status 500 and err.Error() as the
// message, so callers must never wrap a credential or a bearer token into an
// error they hand to this package. A nil err yields a generic internal error
// rather than a nil result.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e
	}
	msg := "internal error"
	if err != nil {
		msg = err.Error()
	}
	return &Error{Code: CodeInternal, Message: msg, Status: http.StatusInternalServerError}
}

// WriteError writes err as the canonical error body with the given status. The
// body is always application/json regardless of what the request asked for: an
// error body is diagnostic plumbing, not negotiated content, and a client that
// cannot parse JSON still gets the status. A 429 additionally carries
// Retry-After in whole seconds, rounded up, minimum 1, as the build spec fixes.
//
// Details is materialized as {} when absent, so the retriable and details keys
// are present on every error body ever written.
func WriteError(w http.ResponseWriter, status int, err error) {
	e := AsError(err)
	body := *e
	if body.Details == nil {
		body.Details = map[string]any{}
	}
	buf, mErr := json.Marshal(&body)
	if mErr != nil {
		// Details held something unmarshallable. Fall back to a body that is
		// always valid rather than to an empty response.
		buf, _ = json.Marshal(&Error{
			Code:      CodeInternal,
			Message:   "error body not serializable: " + mErr.Error(),
			Retriable: false,
			Details:   map[string]any{},
		})
	}
	h := w.Header()
	h.Set("Content-Type", MediaJSON)
	if status == http.StatusTooManyRequests {
		h.Set("Retry-After", strconv.FormatInt(retryAfterSeconds(body.RetryAfterHintMs), 10))
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf)
	_, _ = w.Write([]byte("\n"))
}

// Fail writes err with the status its own Error.Status names, or 500 when it
// names none.
func Fail(w http.ResponseWriter, err error) {
	e := AsError(err)
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	WriteError(w, status, e)
}

// retryAfterSeconds converts a millisecond hint to whole seconds, rounded up,
// with a floor of 1: Retry-After has second granularity and a zero would invite
// a hot retry loop.
func retryAfterSeconds(hintMs int64) int64 {
	if hintMs <= 1000 {
		return 1
	}
	return (hintMs + 999) / 1000
}

// RateLimited is 429 RATE_LIMITED with the exact refill interval as the hint.
func RateLimited(retryAfterHintMs int64) *Error {
	return &Error{
		Code:             CodeRateLimited,
		Message:          "token bucket empty",
		Retriable:        true,
		RetryAfterHintMs: retryAfterHintMs,
		Status:           http.StatusTooManyRequests,
	}
}

// Quarantined is 403 CLIENT_QUARANTINED, not retriable: the client earned it by
// ignoring 20 consecutive rate-limit responses on one endpoint.
func Quarantined() *Error {
	return &Error{
		Code:      CodeQuarantined,
		Message:   "client quarantined after consecutive rate limits",
		Retriable: false,
		Status:    http.StatusForbidden,
	}
}

// QuotaExhausted is 503 QUOTA_EXHAUSTED, not retriable, for the rest of the run.
func QuotaExhausted() *Error {
	return &Error{
		Code:      CodeQuotaExhausted,
		Message:   "request quota exhausted for this run",
		Retriable: false,
		Status:    http.StatusServiceUnavailable,
	}
}

// Unavailable is the injected 503, always retriable, and always succeeds on the
// next attempt with the same idempotency key.
func Unavailable() *Error {
	return &Error{
		Code:      CodeUnavailable,
		Message:   "service temporarily unavailable",
		Retriable: true,
		Status:    http.StatusServiceUnavailable,
	}
}

// TokenExpired is 401 TOKEN_EXPIRED, retriable: refresh the token and retry.
func TokenExpired() *Error {
	return &Error{
		Code:      CodeTokenExpired,
		Message:   "access token expired",
		Retriable: true,
		Status:    http.StatusUnauthorized,
	}
}

// TokenMissing is 401 TOKEN_MISSING for an absent or malformed Authorization
// header. Not retriable: repeating the same unauthenticated request cannot help.
func TokenMissing() *Error {
	return &Error{
		Code:      CodeTokenMissing,
		Message:   "bearer token required",
		Retriable: false,
		Status:    http.StatusUnauthorized,
	}
}

// TokenInvalid is 401 TOKEN_INVALID for a token this run never issued. Not
// retriable with the same token; the client must obtain a new one.
func TokenInvalid() *Error {
	return &Error{
		Code:      CodeTokenInvalid,
		Message:   "access token not recognized",
		Retriable: false,
		Status:    http.StatusUnauthorized,
	}
}

// AdminTokenRequired is 403 ADMIN_TOKEN_REQUIRED. It is deliberately the same
// answer for an absent and for a wrong admin token, so the admin surface leaks
// nothing to a connector token.
func AdminTokenRequired() *Error {
	return &Error{
		Code:      CodeAdminTokenRequired,
		Message:   "admin token required",
		Retriable: false,
		Status:    http.StatusForbidden,
	}
}

// UnsupportedContentType is 415 UNSUPPORTED_CONTENT_TYPE. mediaType is echoed
// in Details as the offending type, verbatim and untrusted.
func UnsupportedContentType(mediaType string) *Error {
	e := &Error{
		Code:      CodeUnsupportedContentType,
		Message:   "unsupported media type",
		Retriable: false,
		Status:    http.StatusUnsupportedMediaType,
	}
	return e.WithDetail("media_type", mediaType)
}

// MalformedBody is 400 MALFORMED_BODY with a machine-readable reason from the
// vocabulary documented on DecodeRecords.
func MalformedBody(reason, message string) *Error {
	e := &Error{
		Code:      CodeMalformedBody,
		Message:   message,
		Retriable: false,
		Status:    http.StatusBadRequest,
	}
	return e.WithDetail("reason", reason)
}

// IdempotencyKeyRequired is 400 IDEMPOTENCY_KEY_REQUIRED.
func IdempotencyKeyRequired() *Error {
	e := &Error{
		Code:      CodeIdempotencyKeyRequired,
		Message:   "Idempotency-Key header required on this request",
		Retriable: false,
		Status:    http.StatusBadRequest,
	}
	return e.WithDetail("header", HeaderIdempotencyKey)
}

// IdempotencyKeyReused is 409 IDEMPOTENCY_KEY_REUSED: a known key presented
// with a different request body. Not retriable: the client must pick a key that
// is a pure function of the payload.
func IdempotencyKeyReused() *Error {
	return &Error{
		Code:      CodeIdempotencyKeyReused,
		Message:   "idempotency key already used with a different request body",
		Retriable: false,
		Status:    http.StatusConflict,
	}
}

// ChunkGap is 409 CHUNK_GAP carrying the expected and the seen chunk ordinal.
// Not retriable as sent: the client must send the missing ordinal first.
func ChunkGap(expected, seen int64) *Error {
	e := &Error{
		Code:      CodeChunkGap,
		Message:   "chunk ordinal out of sequence",
		Retriable: false,
		Status:    http.StatusConflict,
	}
	e.WithDetail("expected", expected)
	return e.WithDetail("seen", seen)
}

// InvalidCursor is 400 INVALID_CURSOR. The reason is coarse on purpose: a
// tamper-evident cursor that explained exactly which byte failed would be a
// forging oracle.
func InvalidCursor() *Error {
	return &Error{
		Code:      CodeInvalidCursor,
		Message:   "cursor is not a cursor this server issued",
		Retriable: false,
		Status:    http.StatusBadRequest,
	}
}

// Internal is 500 INTERNAL, retriable, for a server-side fault.
func Internal(message string) *Error {
	return &Error{
		Code:      CodeInternal,
		Message:   message,
		Retriable: true,
		Status:    http.StatusInternalServerError,
	}
}
