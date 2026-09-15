package simclock

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// TokenStatus is the outcome of Governor.ValidateToken.
type TokenStatus int

// The token validation outcomes.
const (
	// TokenInvalid means the token was never issued by this Governor. Tokens
	// are per-run and in memory, so a token from an earlier run is invalid.
	TokenInvalid TokenStatus = iota
	// TokenExpired means the token was issued but has spent its request
	// allowance or passed its virtual deadline. The handler answers
	// 401 TOKEN_EXPIRED with retriable true.
	TokenExpired
	// TokenOK means the token is valid and this use has been counted against
	// its request allowance.
	TokenOK
)

// String returns the lowercase name of the status, for logs and test failures.
func (s TokenStatus) String() string {
	switch s {
	case TokenInvalid:
		return "invalid"
	case TokenExpired:
		return "expired"
	case TokenOK:
		return "ok"
	}
	return "unknown(" + strconv.Itoa(int(s)) + ")"
}

// accessToken is the server-side state of one issued token.
type accessToken struct {
	requests    int64 // validated uses so far
	deadline100 int64 // virtual deadline in vms100; 0 means no time limit
	expired     bool  // latched, so expiry never un-happens
}

// IssueToken mints an access token and returns its opaque value. The value is
// sha256 over ("simclock/token", seed, issue counter), hex, truncated, so it is
// derived deterministically from the seed and is never random: two runs of the
// same scenario issue byte-identical tokens, which is what makes a transcript
// diffable. Tokens live in memory for the run only.
//
// The token's deadline is the current virtual clock plus
// Config.TokenVirtualTTLVms, and its allowance is Config.TokenRequestTTL
// validated requests. IssueToken does not itself account for the auth request:
// the handler charges that through Admit with ERPAuthToken or TwinAuthToken.
func (g *Governor) IssueToken() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.issuedTokenCount++
	sum := sha256.Sum256([]byte("simclock/token|" +
		strconv.FormatInt(g.cfg.Seed, 10) + "|" +
		strconv.FormatInt(g.issuedTokenCount, 10)))
	value := "tok_" + hex.EncodeToString(sum[:])[:40]
	t := &accessToken{}
	if g.cfg.TokenVirtualTTLVms > 0 {
		t.deadline100 = g.clock.Now100() + g.cfg.TokenVirtualTTLVms*Vms100PerVms
	}
	g.accessTokens[value] = t
	return value
}

// ValidateToken checks value and, when it is valid, counts this use against the
// token's request allowance. It returns TokenInvalid for a token this Governor
// never issued, TokenExpired once the allowance is spent or the virtual deadline
// has passed, and TokenOK otherwise. Expiry latches: an expired token stays
// expired even though the virtual clock cannot move backwards anyway.
//
// The allowance is inclusive: with Config.TokenRequestTTL of 250 the 250th
// validation returns TokenOK and the 251st returns TokenExpired.
func (g *Governor) ValidateToken(value string) TokenStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.accessTokens[value]
	if !ok {
		return TokenInvalid
	}
	if t.expired {
		return TokenExpired
	}
	if t.deadline100 > 0 && g.clock.Now100() > t.deadline100 {
		t.expired = true
		return TokenExpired
	}
	t.requests++
	if g.cfg.TokenRequestTTL > 0 && t.requests > int64(g.cfg.TokenRequestTTL) {
		t.expired = true
		return TokenExpired
	}
	return TokenOK
}

// TokenTTL returns the advertised token lifetime: the request allowance and the
// virtual-millisecond lifetime, for the expires_after_requests and
// expires_after_virtual_ms fields of the auth response.
func (g *Governor) TokenTTL() (requests int, virtualMs int64) {
	return g.cfg.TokenRequestTTL, g.cfg.TokenVirtualTTLVms
}
