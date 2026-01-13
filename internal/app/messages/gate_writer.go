package messages

import (
	"bytes"
	"net/http"
)

// GateWriter wraps an http.ResponseWriter and buffers all writes until Promote
// is called. This allows the server to discard buffered SSE events (e.g. on
// empty visible end_turn) and retry the upstream request transparently.
type GateWriter struct {
	w          http.ResponseWriter
	buf        bytes.Buffer
	promoted   bool
	statusCode int // deferred status code (0 = not set)
}

// NewGateWriter creates a GateWriter that buffers writes to w.
func NewGateWriter(w http.ResponseWriter) *GateWriter {
	return &GateWriter{w: w}
}

// Header returns the underlying ResponseWriter's header map.
func (g *GateWriter) Header() http.Header {
	return g.w.Header()
}

// WriteHeader captures the status code. In buffered mode it defers the actual
// call until Promote; in promoted mode it delegates immediately.
func (g *GateWriter) WriteHeader(statusCode int) {
	if g.promoted {
		g.w.WriteHeader(statusCode)
		return
	}
	g.statusCode = statusCode
}

// Write buffers data before promotion, or writes directly after.
func (g *GateWriter) Write(p []byte) (int, error) {
	if g.promoted {
		return g.w.Write(p)
	}
	return g.buf.Write(p)
}

// Flush delegates to the underlying Flusher if promoted.
func (g *GateWriter) Flush() {
	if !g.promoted {
		return
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Promote flushes the buffer to the real ResponseWriter and switches to
// direct-write mode. All subsequent Write calls go straight through.
func (g *GateWriter) Promote() {
	if g.promoted {
		return
	}
	g.promoted = true
	if g.statusCode != 0 {
		g.w.WriteHeader(g.statusCode)
	}
	if g.buf.Len() > 0 {
		_, _ = g.w.Write(g.buf.Bytes())
		g.buf.Reset()
	}
	if f, ok := g.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Discard drops the buffered data without writing it to the client.
// Must only be called before Promote.
func (g *GateWriter) Discard() {
	g.buf.Reset()
	g.statusCode = 0
}

// IsPromoted reports whether the writer has been promoted to direct mode.
func (g *GateWriter) IsPromoted() bool {
	return g.promoted
}
