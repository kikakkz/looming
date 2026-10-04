// SPDX-License-Identifier: Apache-2.0

package app

import "net/http"

// CapturingWriter passes everything through untouched while teeing the
// response transcript up to cap bytes (gateway-l1 storm Q3: truncation
// beats back-pressure). SSE framing flows through byte-identical.
type CapturingWriter struct {
	http.ResponseWriter
	buf    []byte
	cap    int
	status int
	cut    bool
}

// WrapResponse captures up to capBytes of the response body.
func WrapResponse(w http.ResponseWriter, capBytes int) *CapturingWriter {
	return &CapturingWriter{ResponseWriter: w, cap: capBytes}
}

func (c *CapturingWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *CapturingWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if remaining := c.cap - len(c.buf); remaining >= len(p) {
		c.buf = append(c.buf, p...)
	} else {
		if remaining > 0 {
			c.buf = append(c.buf, p[:remaining]...)
		}
		c.cut = true
	}
	return c.ResponseWriter.Write(p)
}

// Transcript returns the captured bytes and whether the cap cut them.
func (c *CapturingWriter) Transcript() (body []byte, truncated bool) {
	return c.buf, c.cut
}

// Status returns the response status (200 if WriteHeader never ran).
func (c *CapturingWriter) Status() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// Flush implements http.Flusher so streaming responses keep flushing
// through the recorder.
func (c *CapturingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap for http.ResponseController (Go 1.20+).
func (c *CapturingWriter) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}
