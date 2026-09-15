// Package helpers is zero-signal plumbing for the BLP integrations exercise.
//
// Copy it, rewrite it, or ignore it. None of it is graded, and it is not part of
// the module's own build: it lives under examples/ so it never affects the
// protected tree or `make check`.
//
// The signal in this exercise is not writing a retry loop. It is knowing that
// retriable false means never, that an injected fault succeeds on the retry of
// the SAME logical request, and that a token dies after a published number of
// requests. All three are in docs/spec.md.
package helpers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// APIError is the canonical error body both servers answer with.
type APIError struct {
	Status           int            `json:"-"`
	Code             string         `json:"code"`
	Message          string         `json:"message"`
	Retriable        bool           `json:"retriable"`
	RetryAfterHintMs int64          `json:"retry_after_hint_ms"`
	Details          map[string]any `json:"details"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// MaxAttempts is the cap the published fault model needs: an injected fault
// succeeds on the retry of the same logical request, so a third attempt is
// already generous and a fourth is a storm.
const MaxAttempts = 3

// Client is one authenticated HTTP surface: the ERP or the twin.
type Client struct {
	Base         string
	ClientID     string
	ClientSecret string
	TokenPath    string
	HTTP         *http.Client

	token    string
	Requests int
}

// Authenticate fetches a token.
func (c *Client) Authenticate() error {
	body, err := json.Marshal(map[string]string{
		"client_id": c.ClientID, "client_secret": c.ClientSecret,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.Base+c.TokenPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.Requests++
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp.StatusCode, raw)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	c.token = out.AccessToken
	return nil
}

// Do performs one request, refreshing the token on a 401 and retrying only what
// the server marked retriable.
//
// A 207 is returned to the caller rather than treated as a failure: it is the
// per-item answer of a batch, and dropping it loses the outcome of every item.
func (c *Client) Do(method, path string, body []byte, header map[string]string) (int, http.Header, []byte, error) {
	if c.token == "" {
		if err := c.Authenticate(); err != nil {
			return 0, nil, nil, err
		}
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequest(method, c.Base+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := c.client().Do(req)
		if err != nil {
			if attempt < MaxAttempts {
				continue
			}
			return 0, nil, nil, err
		}
		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.Requests++
		if readErr != nil {
			return resp.StatusCode, resp.Header, nil, readErr
		}
		if resp.StatusCode < 300 || resp.StatusCode == http.StatusMultiStatus {
			return resp.StatusCode, resp.Header, raw, nil
		}
		apiErr := decodeAPIError(resp.StatusCode, raw)
		if resp.StatusCode == http.StatusUnauthorized && attempt < MaxAttempts {
			if err := c.Authenticate(); err != nil {
				return resp.StatusCode, resp.Header, raw, err
			}
			continue
		}
		if apiErr.Retriable && attempt < MaxAttempts {
			continue
		}
		return resp.StatusCode, resp.Header, raw, apiErr
	}
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func decodeAPIError(status int, raw []byte) *APIError {
	out := &APIError{Status: status, Code: fmt.Sprintf("HTTP_%d", status)}
	_ = json.Unmarshal(raw, out)
	out.Status = status
	if out.Code == "" {
		out.Code = fmt.Sprintf("HTTP_%d", status)
	}
	return out
}

// IdempotencyKey is the conforming recipe from docs/spec.md.
//
// The asserted property is that the key is a pure function of the proposal and
// stable across runs. The run id must NOT be in here: a resumed run would post
// everything a second time, and the ERP counts every such attempt.
func IdempotencyKey(tenant, proposalID, contentHash string) string {
	if len(contentHash) > 16 {
		contentHash = contentHash[:16]
	}
	return strings.Join([]string{"blp", tenant, proposalID, contentHash}, ":")
}

// SHA256File is the checksum the manifest must carry per file.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PublishBatch performs the atomic drop protocol: write into a staging directory
// whose name starts with a dot, fsync every file, then one rename.
//
// The leading dot is what makes it atomic in practice: the twin's scanner ignores
// it, so a half-written batch is invisible rather than half-read.
func PublishBatch(inboxRoot, batchID string, write func(dir string) error) (string, error) {
	incoming := filepath.Join(inboxRoot, "incoming")
	staging := filepath.Join(incoming, ".staging-"+batchID)
	final := filepath.Join(incoming, batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	if err := write(staging); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		f, err := os.Open(filepath.Join(staging, e.Name()))
		if err != nil {
			return "", err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	if err := os.Rename(staging, final); err != nil {
		return "", err
	}
	return final, nil
}
