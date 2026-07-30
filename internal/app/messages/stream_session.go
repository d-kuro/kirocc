package messages

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const keepAliveComment = ": keep-alive\n\n"

// socketWriteTimeout bounds each downstream write/flush. Without it a client
// that stops reading (without closing) would block the session mutex forever,
// wedging Stop, Promote and WriteFinalError. The server deliberately sets no
// global WriteTimeout, so this per-write deadline is the only bound.
const socketWriteTimeout = 30 * time.Second

// streamSession owns every downstream write for one streaming /v1/messages
// request, including transparent retries and tool-search rounds.
type streamSession struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	ctx    context.Context
	cancel context.CancelFunc

	interval  time.Duration
	activity  chan struct{}
	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup

	mu           sync.Mutex
	buf          bytes.Buffer
	statusCode   int
	promoted     bool
	committed    bool
	pendingFlush bool
	firstErr     error
	lastActivity time.Time
	stopped      bool
	headersSet   bool
	finalizing   bool
	finalized    bool
}

func newStreamSession(parent context.Context, w http.ResponseWriter, interval time.Duration) *streamSession {
	ctx, cancel := context.WithCancel(parent)
	return &streamSession{
		w:        w,
		rc:       http.NewResponseController(w),
		ctx:      ctx,
		cancel:   cancel,
		interval: interval,
		activity: make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
	}
}

// Context returns the derived upstream context. A downstream transport failure
// cancels it immediately.
func (s *streamSession) Context() context.Context {
	return s.ctx
}

// Start begins idle heartbeat generation. It is idempotent and intentionally
// does nothing when the interval is zero.
func (s *streamSession) Start() {
	s.startOnce.Do(func() {
		if s.interval <= 0 {
			return
		}
		// The stopped-check and wg.Add must share one critical section:
		// otherwise Stop can observe wg counter zero, return, and only then
		// have Start launch a heartbeat that outlives Stop's join guarantee.
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopped {
			return
		}
		s.lastActivity = time.Now()
		s.wg.Add(1)
		go s.runHeartbeat()
	})
}

// Stop terminates and joins the heartbeat goroutine. Once it returns, the
// session cannot write additional downstream bytes.
func (s *streamSession) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		close(s.stopCh)
		s.cancel()
	})
	s.wg.Wait()
}

func (s *streamSession) runHeartbeat() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		if s.stopped || s.firstErr != nil || s.finalizing || s.finalized {
			s.mu.Unlock()
			return
		}
		wait := time.Until(s.lastActivity.Add(s.interval))
		s.mu.Unlock()
		if wait < 0 {
			wait = 0
		}

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			if err := s.WriteKeepAlive(); err != nil {
				return
			}
		case <-s.activity:
			stopAndDrainTimer(timer)
		case <-s.stopCh:
			stopAndDrainTimer(timer)
			return
		case <-s.ctx.Done():
			stopAndDrainTimer(timer)
			return
		}
	}
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

// WriteKeepAlive writes and flushes one bare SSE comment directly to the real
// socket without promoting or disturbing buffered semantic events.
func (s *streamSession) WriteKeepAlive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalizing || s.finalized {
		return context.Canceled
	}
	if err := s.unavailableLocked(); err != nil {
		return err
	}
	if _, err := s.writeSocketLocked([]byte(keepAliveComment)); err != nil {
		return err
	}
	return s.flushSocketLocked()
}

// WriteFinalError applies the committed × promoted decision table. The
// callback is used only for a promoted semantic stream so SSEWriter can close
// its active content block before emitting the error event.
func (s *streamSession) WriteFinalError(final streamFinalError, writePromoted func() error) error {
	s.mu.Lock()
	if err := s.unavailableLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.promoted {
		if writePromoted != nil {
			s.finalizing = true
			s.mu.Unlock()
			err := writePromoted()
			s.mu.Lock()
			s.finalizing = false
			s.finalized = true
			s.mu.Unlock()
			return err
		}
		err := s.writeDirectSSEErrorLocked(final.sseType, final.sseMessage)
		s.finalized = true
		s.mu.Unlock()
		return err
	}

	s.buf.Reset()
	s.statusCode = 0
	if s.committed {
		err := s.writeDirectSSEErrorLocked(final.sseType, final.sseMessage)
		s.finalized = true
		s.mu.Unlock()
		return err
	}

	body, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    final.jsonType,
			"message": final.jsonMessage,
		},
	})
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("marshal downstream error: %w", err)
	}
	body = append(body, '\n')
	s.w.Header().Set("Content-Type", "application/json")
	s.w.WriteHeader(final.status)
	_, writeErr := s.writeSocketLocked(body)
	s.finalized = true
	s.mu.Unlock()
	return writeErr
}

func (s *streamSession) writeDirectSSEErrorLocked(errType, message string) error {
	data, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
	if err != nil {
		return fmt.Errorf("marshal downstream SSE error: %w", err)
	}
	payload := []byte("event: error\ndata: ")
	payload = append(payload, data...)
	payload = append(payload, '\n', '\n')
	if _, err := s.writeSocketLocked(payload); err != nil {
		return err
	}
	return s.flushSocketLocked()
}

func (s *streamSession) unavailableLocked() error {
	if s.firstErr != nil {
		return s.firstErr
	}
	if s.stopped {
		return context.Canceled
	}
	if s.finalized {
		return context.Canceled
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *streamSession) storeErrorLocked(err error) {
	if err == nil || s.firstErr != nil {
		return
	}
	s.firstErr = err
	s.cancel()
}
