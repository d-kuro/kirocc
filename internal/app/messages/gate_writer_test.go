package messages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamSession_Promote(t *testing.T) {
	w := httptest.NewRecorder()
	session := newStreamSession(context.Background(), w, 0)

	if _, err := session.Write([]byte("buffered")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected no data written before promote, got %d bytes", w.Body.Len())
	}
	if session.IsPromoted() {
		t.Fatal("should not be promoted yet")
	}

	if err := session.Promote(); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !session.IsPromoted() {
		t.Fatal("should be promoted after Promote()")
	}
	if !session.Committed() {
		t.Fatal("promoted bytes should commit the response")
	}
	if w.Body.String() != "buffered" {
		t.Fatalf("expected 'buffered', got %q", w.Body.String())
	}

	if _, err := session.Write([]byte(" direct")); err != nil {
		t.Fatalf("direct Write: %v", err)
	}
	if w.Body.String() != "buffered direct" {
		t.Fatalf("expected 'buffered direct', got %q", w.Body.String())
	}
}

func TestStreamSession_DiscardOnlyClearsSemanticBuffer(t *testing.T) {
	w := httptest.NewRecorder()
	session := newStreamSession(context.Background(), w, 0)

	if _, err := session.Write([]byte("to be discarded")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	session.Discard()

	if w.Body.Len() != 0 {
		t.Fatalf("expected no data after discard, got %d bytes", w.Body.Len())
	}
	if session.IsPromoted() {
		t.Fatal("should not be promoted after discard")
	}

	if err := session.WriteKeepAlive(); err != nil {
		t.Fatalf("WriteKeepAlive: %v", err)
	}
	if !session.Committed() || session.IsPromoted() {
		t.Fatalf("state after keep-alive = committed %v, promoted %v; want true, false", session.Committed(), session.IsPromoted())
	}
	if _, err := session.Write([]byte("second attempt")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	session.Discard()
	if got := w.Body.String(); got != keepAliveComment {
		t.Fatalf("Discard removed transport bytes: got %q, want %q", got, keepAliveComment)
	}
}

func TestStreamSession_DoublePromote(t *testing.T) {
	w := httptest.NewRecorder()
	session := newStreamSession(context.Background(), w, 0)

	_, _ = session.Write([]byte("data"))
	if err := session.Promote(); err != nil {
		t.Fatalf("first Promote: %v", err)
	}
	if err := session.Promote(); err != nil {
		t.Fatalf("second Promote: %v", err)
	}

	if w.Body.String() != "data" {
		t.Fatalf("expected 'data', got %q", w.Body.String())
	}
}

func TestStreamSession_FlushBeforePromote(t *testing.T) {
	w := httptest.NewRecorder()
	session := newStreamSession(context.Background(), w, 0)

	session.Flush()
	if w.Body.Len() != 0 {
		t.Fatal("flush before promote should not write anything")
	}
}

func TestStreamSession_Header(t *testing.T) {
	w := httptest.NewRecorder()
	session := newStreamSession(context.Background(), w, 0)

	session.Header().Set("Content-Type", "text/event-stream")
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("header should be set on underlying writer")
	}
}

func TestStreamSession_DeferredStatusIsWrittenOnPromote(t *testing.T) {
	w := httptest.NewRecorder()
	session := newStreamSession(context.Background(), w, 0)
	session.WriteHeader(http.StatusAccepted)
	_, _ = session.Write([]byte("semantic"))

	if err := session.Promote(); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}
