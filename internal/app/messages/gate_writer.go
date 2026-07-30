package messages

import (
	"io"
	"net/http"
	"time"
)

// Header returns the underlying ResponseWriter's header map. Callers must set
// headers before Start, when the heartbeat can begin committing the response.
func (s *streamSession) Header() http.Header {
	return s.w.Header()
}

// SetSSEHeaders installs transport headers once under the same mutex used by
// heartbeat writes. Retry attempts therefore never mutate the header map after
// the response may have been committed.
func (s *streamSession) SetSSEHeaders() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.headersSet || s.committed || s.finalized {
		return
	}
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	s.headersSet = true
}

// WriteHeader defers the status until promotion while semantic events are
// buffered. Once promoted, it delegates to the real writer.
func (s *streamSession) WriteHeader(statusCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstErr != nil || s.stopped || s.finalized {
		return
	}
	if s.promoted {
		s.w.WriteHeader(statusCode)
		return
	}
	s.statusCode = statusCode
}

// Write buffers semantic SSE bytes before promotion and writes directly after
// promotion. Transport keep-alives bypass this method's buffer.
func (s *streamSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unavailableLocked(); err != nil {
		return 0, err
	}
	if !s.promoted {
		return s.buf.Write(p)
	}
	return s.writeSocketLocked(p)
}

// Flush implements http.Flusher. Pre-promotion semantic writes are not flushed.
// Flush errors are retained by Err so SSEWriter can observe them.
func (s *streamSession) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.promoted || s.firstErr != nil || s.stopped || s.finalized {
		return
	}
	_ = s.flushSocketLocked()
}

// Promote flushes buffered semantic events and switches to direct mode.
func (s *streamSession) Promote() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unavailableLocked(); err != nil {
		return err
	}
	if s.promoted {
		return s.firstErr
	}

	s.promoted = true
	hadOutput := s.statusCode != 0 || s.buf.Len() > 0
	if s.statusCode != 0 {
		s.w.WriteHeader(s.statusCode)
	}
	if s.buf.Len() > 0 {
		data := append([]byte(nil), s.buf.Bytes()...)
		if _, err := s.writeSocketLocked(data); err != nil {
			return err
		}
		s.buf.Reset()
	}
	if err := s.flushSocketLocked(); err != nil {
		return err
	}
	if hadOutput {
		s.committed = true
	}
	return nil
}

// Discard drops only buffered semantic data. A keep-alive may already have
// committed HTTP 200; that transport state intentionally remains unchanged.
func (s *streamSession) Discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promoted {
		return
	}
	s.buf.Reset()
	s.statusCode = 0
}

// IsPromoted reports whether semantic events are writing directly downstream.
func (s *streamSession) IsPromoted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promoted
}

// Committed reports whether bytes have reached the downstream response.
func (s *streamSession) Committed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed
}

// Err returns the first downstream write or flush error.
func (s *streamSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstErr
}

func (s *streamSession) writeSocketLocked(p []byte) (int, error) {
	s.armWriteDeadlineLocked()
	n, err := s.w.Write(p)
	if n > 0 {
		s.committed = true
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.storeErrorLocked(err)
		return n, s.firstErr
	}
	s.pendingFlush = true
	return n, nil
}

func (s *streamSession) flushSocketLocked() error {
	if s.firstErr != nil {
		return s.firstErr
	}
	s.armWriteDeadlineLocked()
	if err := s.rc.Flush(); err != nil {
		s.storeErrorLocked(err)
		return s.firstErr
	}
	if s.pendingFlush {
		s.pendingFlush = false
		s.lastActivity = time.Now()
		select {
		case s.activity <- struct{}{}:
		default:
		}
	}
	return nil
}

// armWriteDeadlineLocked bounds the next socket write/flush so a client that
// stops reading cannot block the session mutex (and Stop) indefinitely.
// Unsupported transports (http.ErrNotSupported) are ignored: recorder-style
// writers in tests and exotic wrappers simply keep the unbounded behavior.
func (s *streamSession) armWriteDeadlineLocked() {
	_ = s.rc.SetWriteDeadline(time.Now().Add(socketWriteTimeout))
}
