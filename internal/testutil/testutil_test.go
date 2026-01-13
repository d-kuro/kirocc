package testutil

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestBuildFrame_ParseStream(t *testing.T) {
	payload := []byte(`{"content":"ok"}`)
	var events []kiroproto.Event

	err := kiroproto.ParseStream(context.Background(), bytes.NewReader(BuildFrame("assistantResponseEvent", payload)), func(e kiroproto.Event) bool {
		events = append(events, e)
		return false
	})
	if err != nil {
		t.Fatalf("ParseStream() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Type != kiroproto.EventAssistantResponse {
		t.Fatalf("event type = %q, want %q", events[0].Type, kiroproto.EventAssistantResponse)
	}
	if events[0].Content != "ok" {
		t.Fatalf("content = %q, want %q", events[0].Content, "ok")
	}
}

func TestBuildFrameWithExtraHeaders_ParseStream(t *testing.T) {
	extra := []byte{3, 'f', 'o', 'o', 7, 0, 3, 'b', 'a', 'r'}
	payload := []byte(`{"content":"ok"}`)
	var events []kiroproto.Event

	err := kiroproto.ParseStream(context.Background(), bytes.NewReader(BuildFrameWithExtraHeaders(extra, "assistantResponseEvent", payload)), func(e kiroproto.Event) bool {
		events = append(events, e)
		return false
	})
	if err != nil {
		t.Fatalf("ParseStream() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Type != kiroproto.EventAssistantResponse {
		t.Fatalf("event type = %q, want %q", events[0].Type, kiroproto.EventAssistantResponse)
	}
	if events[0].Content != "ok" {
		t.Fatalf("content = %q, want %q", events[0].Content, "ok")
	}
}

func TestNewTCP4TestServer(t *testing.T) {
	srv := NewTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
