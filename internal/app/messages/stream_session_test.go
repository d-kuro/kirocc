package messages

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionResponseWriter struct {
	mu       sync.Mutex
	header   http.Header
	writes   chan string
	writeErr error
	flushErr error
	count    int
}

func newSessionResponseWriter() *sessionResponseWriter {
	return &sessionResponseWriter{
		header: make(http.Header),
		writes: make(chan string, 256),
	}
}

func (w *sessionResponseWriter) Header() http.Header { return w.header }

func (w *sessionResponseWriter) WriteHeader(int) {}

func (w *sessionResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.count++
	err := w.writeErr
	w.mu.Unlock()
	if err != nil {
		return 0, err
	}
	w.writes <- string(append([]byte(nil), p...))
	return len(p), nil
}

func (w *sessionResponseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushErr
}

func (w *sessionResponseWriter) setWriteError(err error) {
	w.mu.Lock()
	w.writeErr = err
	w.mu.Unlock()
}

func (w *sessionResponseWriter) setFlushError(err error) {
	w.mu.Lock()
	w.flushErr = err
	w.mu.Unlock()
}

func (w *sessionResponseWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count
}

func receiveSessionWrite(t *testing.T, writes <-chan string) string {
	t.Helper()
	select {
	case got := <-writes:
		return got
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for downstream write")
		return ""
	}
}

func TestStreamSession_HeartbeatThenPromotedResponse(t *testing.T) {
	w := newSessionResponseWriter()
	session := newStreamSession(t.Context(), w, 10*time.Millisecond)
	session.Header().Set("Content-Type", "text/event-stream")
	session.Start()
	t.Cleanup(session.Stop)

	if got := receiveSessionWrite(t, w.writes); got != keepAliveComment {
		t.Fatalf("first write = %q, want %q", got, keepAliveComment)
	}
	if !session.Committed() || session.IsPromoted() {
		t.Fatalf("state = committed %v, promoted %v; want true, false", session.Committed(), session.IsPromoted())
	}

	const semantic = "event: message_start\ndata: {}\n\n"
	if _, err := session.Write([]byte(semantic)); err != nil {
		t.Fatalf("Write semantic event: %v", err)
	}
	if err := session.Promote(); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := receiveSessionWrite(t, w.writes); got != semantic {
		t.Fatalf("promoted write = %q, want %q", got, semantic)
	}
}

func TestStreamSession_FinalErrorAfterKeepAliveIsSSE(t *testing.T) {
	w := newSessionResponseWriter()
	session := newStreamSession(t.Context(), w, 0)
	session.Header().Set("Content-Type", "text/event-stream")

	if err := session.WriteKeepAlive(); err != nil {
		t.Fatalf("WriteKeepAlive: %v", err)
	}
	_ = receiveSessionWrite(t, w.writes)

	if err := session.WriteFinalError(newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream failed"), nil); err != nil {
		t.Fatalf("WriteFinalError: %v", err)
	}
	got := receiveSessionWrite(t, w.writes)
	if !strings.HasPrefix(got, "event: error\n") || !strings.Contains(got, `"type":"api_error"`) {
		t.Fatalf("final error is not SSE: %q", got)
	}
	if strings.HasPrefix(got, `{"type":"error"`) {
		t.Fatalf("JSON error was injected into committed SSE: %q", got)
	}
}

func TestStreamSession_FinalErrorBeforeCommitIsJSON(t *testing.T) {
	w := newSessionResponseWriter()
	session := newStreamSession(t.Context(), w, 0)

	if err := session.WriteFinalError(newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream failed"), nil); err != nil {
		t.Fatalf("WriteFinalError: %v", err)
	}
	got := receiveSessionWrite(t, w.writes)
	if !strings.HasPrefix(got, "{") || !strings.Contains(got, `"type":"error"`) || !strings.Contains(got, `"type":"api_error"`) {
		t.Fatalf("final error is not JSON: %q", got)
	}
	if contentType := w.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
}

func TestStreamSession_HeartbeatDisabled(t *testing.T) {
	w := newSessionResponseWriter()
	session := newStreamSession(t.Context(), w, 0)
	session.Start()
	t.Cleanup(session.Stop)

	select {
	case got := <-w.writes:
		t.Fatalf("disabled heartbeat wrote %q", got)
	case <-time.After(30 * time.Millisecond):
	}
	if session.Committed() {
		t.Fatal("disabled heartbeat committed the response")
	}
}

func TestStreamSession_KeepAliveFailureCancelsUpstreamAndSuppressesFinalError(t *testing.T) {
	disconnectErr := errors.New("client disconnected")
	w := newSessionResponseWriter()
	w.setWriteError(disconnectErr)
	session := newStreamSession(t.Context(), w, time.Millisecond)
	session.Start()
	t.Cleanup(session.Stop)

	select {
	case <-session.Context().Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("upstream context was not canceled after keep-alive failure")
	}
	if !errors.Is(session.Err(), disconnectErr) {
		t.Fatalf("Err() = %v, want %v", session.Err(), disconnectErr)
	}
	writesBefore := w.writeCount()
	if err := session.WriteFinalError(newStreamFinalError(http.StatusBadGateway, errTypeAPI, "must not be written"), nil); !errors.Is(err, disconnectErr) {
		t.Fatalf("WriteFinalError err = %v, want %v", err, disconnectErr)
	}
	if got := w.writeCount(); got != writesBefore {
		t.Fatalf("writes after disconnect = %d, want %d", got, writesBefore)
	}
}

func TestStreamSession_StoresFirstFlushError(t *testing.T) {
	firstErr := errors.New("flush failed")
	secondErr := errors.New("write failed")
	w := newSessionResponseWriter()
	w.setFlushError(firstErr)
	session := newStreamSession(t.Context(), w, 0)

	if err := session.WriteKeepAlive(); !errors.Is(err, firstErr) {
		t.Fatalf("WriteKeepAlive err = %v, want %v", err, firstErr)
	}
	w.setWriteError(secondErr)
	if err := session.WriteKeepAlive(); !errors.Is(err, firstErr) {
		t.Fatalf("second WriteKeepAlive err = %v, want first error %v", err, firstErr)
	}
	if !errors.Is(session.Err(), firstErr) {
		t.Fatalf("Err() = %v, want first error %v", session.Err(), firstErr)
	}
	select {
	case <-session.Context().Done():
	default:
		t.Fatal("flush failure did not cancel upstream context")
	}
}

func TestStreamSession_PromoteReturnsFlushError(t *testing.T) {
	wantErr := errors.New("promote flush failed")
	w := newSessionResponseWriter()
	w.setFlushError(wantErr)
	session := newStreamSession(t.Context(), w, 0)
	if _, err := session.Write([]byte("semantic")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := session.Promote(); !errors.Is(err, wantErr) {
		t.Fatalf("Promote() err = %v, want %v", err, wantErr)
	}
	if !errors.Is(session.Err(), wantErr) {
		t.Fatalf("Err() = %v, want %v", session.Err(), wantErr)
	}
}

func TestStreamSession_StopJoinsHeartbeat(t *testing.T) {
	w := newSessionResponseWriter()
	session := newStreamSession(context.Background(), w, time.Millisecond)
	session.Start()
	_ = receiveSessionWrite(t, w.writes)

	session.Stop()
	writesAfterStop := w.writeCount()
	select {
	case got := <-w.writes:
		t.Fatalf("heartbeat wrote %q after Stop returned", got)
	case <-time.After(5 * time.Millisecond):
	}
	if got := w.writeCount(); got != writesAfterStop {
		t.Fatalf("write count after Stop = %d, want %d", got, writesAfterStop)
	}
}

func TestStreamSession_HeartbeatAndPromoteAreRaceSafe(t *testing.T) {
	for range 100 {
		w := newSessionResponseWriter()
		session := newStreamSession(context.Background(), w, 0)
		if _, err := session.Write([]byte("semantic")); err != nil {
			t.Fatalf("Write: %v", err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = session.WriteKeepAlive()
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = session.Promote()
		}()
		close(start)
		wg.Wait()

		if err := session.Err(); err != nil {
			t.Fatalf("session Err: %v", err)
		}
		if !session.Committed() || !session.IsPromoted() {
			t.Fatalf("state = committed %v, promoted %v; want true, true", session.Committed(), session.IsPromoted())
		}
	}
}

func TestStreamSession_HeartbeatAndFinalErrorCannotMixJSONAndSSE(t *testing.T) {
	for range 100 {
		w := newSessionResponseWriter()
		session := newStreamSession(context.Background(), w, 0)
		session.SetSSEHeaders()
		final := newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream failed")

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = session.WriteKeepAlive()
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = session.WriteFinalError(final, nil)
		}()
		close(start)
		wg.Wait()

		var body strings.Builder
		for {
			select {
			case chunk := <-w.writes:
				body.WriteString(chunk)
			default:
				goto drained
			}
		}
	drained:
		got := body.String()
		switch {
		case strings.HasPrefix(got, "{"):
			if strings.Contains(got, keepAliveComment) || strings.Contains(got, "event: error") {
				t.Fatalf("heartbeat/SSE appended to JSON final error: %q", got)
			}
		case strings.HasPrefix(got, keepAliveComment):
			if !strings.Contains(got, "event: error") || strings.Contains(got, "\n{\"type\":\"error\"") {
				t.Fatalf("committed SSE contains invalid final error: %q", got)
			}
		default:
			t.Fatalf("unexpected downstream output: %q", got)
		}
	}
}
