package erp

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/fatjonblp/coding_challange_integrations/internal/httpx"
)

// A TokenResponse is the answer of POST /erp/v1/auth/token. The two expiry
// fields are the whole truth about the token's lifetime: it dies after
// expires_after_requests authenticated requests or expires_after_virtual_ms
// virtual milliseconds, whichever comes first, and there is no wall clock
// anywhere in that sentence.
type TokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresAfterRequests  int    `json:"expires_after_requests"`
	ExpiresAfterVirtualMs int64  `json:"expires_after_virtual_ms"`
}

// tokenRequest is the client credentials grant body. JSON is the documented
// form; a form-encoded body is accepted too, because half the HTTP clients in
// the world post credentials that way and refusing them teaches nothing.
type tokenRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// handleAuthToken issues an access token from client credentials.
//
// Wrong or missing credentials are 401 TOKEN_INVALID, retriable false: repeating
// the same wrong secret cannot help. The comparison is constant time, and no
// error body ever echoes the secret it was given.
func (s *Server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	body, err := httpx.ReadBody(r)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	var req tokenRequest
	switch {
	case len(strings.TrimSpace(string(body))) == 0:
		// No body at all: fall through to the credential check, which fails.
	case strings.HasPrefix(strings.TrimSpace(string(body)), "{"):
		if err := json.Unmarshal(body, &req); err != nil {
			httpx.Fail(w, httpx.MalformedBody("json_invalid",
				"credentials must be a JSON object with client_id and client_secret"))
			return
		}
	default:
		form, err := url.ParseQuery(string(body))
		if err != nil {
			httpx.Fail(w, httpx.MalformedBody("form_invalid",
				"credentials must be a JSON object or a form-encoded body"))
			return
		}
		req.ClientID = form.Get("client_id")
		req.ClientSecret = form.Get("client_secret")
	}

	creds := s.Credentials()
	idOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.ClientID)), []byte(creds.ClientID)) == 1
	secretOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.ClientSecret)), []byte(creds.ClientSecret)) == 1
	if !idOK || !secretOK {
		httpx.Fail(w, &httpx.Error{
			Code:      httpx.CodeTokenInvalid,
			Message:   "client credentials not recognized",
			Retriable: false,
			Status:    http.StatusUnauthorized,
		})
		return
	}

	_, st := s.state()
	requests, virtualMs := st.gov.TokenTTL()
	writeJSON(w, http.StatusOK, TokenResponse{
		AccessToken:           st.gov.IssueToken(),
		ExpiresAfterRequests:  requests,
		ExpiresAfterVirtualMs: virtualMs,
	})
}
