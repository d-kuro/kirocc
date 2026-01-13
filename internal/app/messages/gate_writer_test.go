package messages

import (
	"net/http/httptest"
	"testing"
)

func TestGateWriter_Promote(t *testing.T) {
	w := httptest.NewRecorder()
	gw := NewGateWriter(w)

	// Write before promote — should buffer.
	_, _ = gw.Write([]byte("buffered"))
	if w.Body.Len() != 0 {
		t.Fatalf("expected no data written before promote, got %d bytes", w.Body.Len())
	}
	if gw.IsPromoted() {
		t.Fatal("should not be promoted yet")
	}

	// Promote — should flush buffer.
	gw.Promote()
	if !gw.IsPromoted() {
		t.Fatal("should be promoted after Promote()")
	}
	if w.Body.String() != "buffered" {
		t.Fatalf("expected 'buffered', got %q", w.Body.String())
	}

	// Write after promote — should go directly.
	_, _ = gw.Write([]byte(" direct"))
	if w.Body.String() != "buffered direct" {
		t.Fatalf("expected 'buffered direct', got %q", w.Body.String())
	}
}

func TestGateWriter_Discard(t *testing.T) {
	w := httptest.NewRecorder()
	gw := NewGateWriter(w)

	_, _ = gw.Write([]byte("to be discarded"))
	gw.Discard()

	if w.Body.Len() != 0 {
		t.Fatalf("expected no data after discard, got %d bytes", w.Body.Len())
	}
	if gw.IsPromoted() {
		t.Fatal("should not be promoted after discard")
	}
}

func TestGateWriter_DoublePromote(t *testing.T) {
	w := httptest.NewRecorder()
	gw := NewGateWriter(w)

	_, _ = gw.Write([]byte("data"))
	gw.Promote()
	gw.Promote() // second promote should be no-op

	if w.Body.String() != "data" {
		t.Fatalf("expected 'data', got %q", w.Body.String())
	}
}

func TestGateWriter_FlushBeforePromote(t *testing.T) {
	w := httptest.NewRecorder()
	gw := NewGateWriter(w)

	// Flush before promote should be no-op (not panic).
	gw.Flush()
	if w.Body.Len() != 0 {
		t.Fatal("flush before promote should not write anything")
	}
}

func TestGateWriter_Header(t *testing.T) {
	w := httptest.NewRecorder()
	gw := NewGateWriter(w)

	gw.Header().Set("Content-Type", "text/event-stream")
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatal("header should be set on underlying writer")
	}
}
