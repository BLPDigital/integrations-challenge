package web

import (
	"bytes"
	"net/http"
)

// A capture is a minimal in-memory [http.ResponseWriter]. It exists so
// [AdminSource] can call the ERP's own admin handlers in process without
// importing net/http/httptest into non-test code and without opening a socket to
// the server the UI is already inside.
//
// It records the first status written, which is the status the caller saw, and
// buffers the whole body, which for an admin response is bounded by the retention
// caps of the request log and the idempotency store.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

// Header returns the response header map.
func (c *capture) Header() http.Header { return c.header }

// WriteHeader records the first status written.
func (c *capture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

// Write appends to the body, recording an implicit 200 on the first byte.
func (c *capture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}
