package httpx

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/simclock"
)

// BearerToken returns the token from an Authorization: Bearer header, or "" when
// the header is absent or is not a bearer credential. The scheme is matched
// case-insensitively, as RFC 7235 requires; the token itself is verbatim.
func BearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(v) < len(scheme) || !strings.EqualFold(v[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(v[len(scheme):])
}

// Bearer returns middleware enforcing the Governor's token lifecycle on the
// versioned surface:
//
//   - no bearer token: 401 TOKEN_MISSING, retriable false,
//   - a token this run never issued: 401 TOKEN_INVALID, retriable false,
//   - a token past its request allowance or its virtual deadline:
//     401 TOKEN_EXPIRED, retriable true, because refreshing and retrying is the
//     correct client behavior and the grader scores it.
//
// A valid use is counted against the token's allowance, so token expiry is a
// deterministic function of how many authenticated requests the client made.
//
// Exempt: admin paths, which carry the admin token instead, and the token
// endpoints themselves, which are what a client calls when it has no token. The
// exemption is by path so that one middleware can wrap a whole mux.
//
// Bearer belongs inside [Guard], never outside it: the 401 it writes is a
// non-admin request and must cost its quota unit like any other. See the Guard
// doc comment for the required order.
func Bearer(gov *simclock.Governor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if gov == nil || IsAdminPath(r.URL.Path) || strings.HasSuffix(r.URL.Path, "/auth/token") {
				next.ServeHTTP(w, r)
				return
			}
			token := BearerToken(r)
			if token == "" {
				Fail(w, TokenMissing())
				return
			}
			switch gov.ValidateToken(token) {
			case simclock.TokenOK:
				next.ServeHTTP(w, r)
			case simclock.TokenExpired:
				Fail(w, TokenExpired())
			default:
				Fail(w, TokenInvalid())
			}
		})
	}
}

// CheckAdminToken reports whether r carries the expected admin token in
// X-Admin-Token, comparing in constant time. An empty want rejects everything:
// the admin surface fails closed, and a server that means its admin surface to
// be open simply does not install the check.
//
// The admin token is a separate credential from the connector's bearer token and
// is never issued to a connector, so no connector token can reach an admin path.
func CheckAdminToken(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	got := r.Header.Get(HeaderAdminToken)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// RequireAdminToken writes 403 ADMIN_TOKEN_REQUIRED and returns false when r
// does not carry the admin token. A handler uses it as a guard clause:
//
//	if !httpx.RequireAdminToken(w, r, srv.adminToken) {
//	    return
//	}
func RequireAdminToken(w http.ResponseWriter, r *http.Request, want string) bool {
	if CheckAdminToken(r, want) {
		return true
	}
	Fail(w, AdminTokenRequired())
	return false
}

// AdminToken returns middleware wrapping an admin surface with
// RequireAdminToken.
func AdminToken(want string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !RequireAdminToken(w, r, want) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
