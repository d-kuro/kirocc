package kiroclient

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d-kuro/kirocc/internal/kiroproto"
	tu "github.com/d-kuro/kirocc/internal/testutil"
)

// newTCP4TestServer creates an httptest.Server bound to tcp4 to avoid IPv6 bind failures in sandboxed environments.
func newTCP4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return tu.NewTCP4TestServer(t, handler)
}

func TestHTTPClient_Success(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != amzTarget {
			t.Errorf("X-Amz-Target = %q", r.Header.Get("X-Amz-Target"))
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing Bearer token")
		}
		if r.Header.Get("Content-Type") != "application/x-amz-json-1.0" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("stream-body"))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{
		ConversationState: kiroproto.ConversationState{
			ChatTriggerType: "MANUAL",
			AgentTaskType:   "vibe",
			CurrentMessage: kiroproto.CurrentMessage{
				UserInputMessage: kiroproto.UserInputMessage{Content: "Hello"},
			},
		},
	}
	resp, err := c.GenerateAssistantResponse(context.Background(), "test-token", payload, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "stream-body" {
		t.Fatalf("body = %q", body)
	}
}

func TestHTTPClient_CorrectHeaders(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Amz-Target") != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" {
			t.Errorf("wrong X-Amz-Target: %q", r.Header.Get("X-Amz-Target"))
		}
		if r.Header.Get("Accept") != "*/*" {
			t.Errorf("wrong Accept: %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	resp, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestHTTPClient_PayloadSerialization(t *testing.T) {
	var captured []byte
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{
		ConversationState: kiroproto.ConversationState{
			AgentTaskType:   "vibe",
			ChatTriggerType: "MANUAL",
			CurrentMessage:  kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hello", ModelID: "claude-sonnet-4.6", Origin: "AI_EDITOR"}},
		},
		ProfileARN: "arn:test",
	}
	resp, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	var m map[string]any
	if err := json.Unmarshal(captured, &m); err != nil {
		t.Fatal(err)
	}
	cs := m["conversationState"].(map[string]any)
	if cs["agentTaskType"] != "vibe" {
		t.Fatalf("agentTaskType = %v", cs["agentTaskType"])
	}
}

func TestHTTPClient_Retry403_WithRefresh(t *testing.T) {
	var callCount atomic.Int32
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-token" {
			t.Errorf("expected new token, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewHTTPClient(
		WithBaseURL(srv.URL),
		WithTokenRefresher(func(ctx context.Context) (string, error) {
			return "new-token", nil
		}),
	)
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	resp, err := c.GenerateAssistantResponse(context.Background(), "old-token", payload, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if callCount.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount.Load())
	}
}

func TestHTTPClient_Retry429(t *testing.T) {
	var callCount atomic.Int32
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	c.httpClient.Timeout = 30 * time.Second
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	resp, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if callCount.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", callCount.Load())
	}
}

func TestHTTPClient_403_NoRefresher(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPClient_400_NoRetry(t *testing.T) {
	var callCount atomic.Int32
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if callCount.Load() != 1 {
		t.Fatalf("400 should not retry, got %d calls", callCount.Load())
	}
}

func TestHTTPClient_EndpointURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		region  string
		want    string
	}{
		{"region-based", "", "us-west-2", "https://q.us-west-2.amazonaws.com/"},
		{"override", "http://localhost:8080", "us-west-2", "http://localhost:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []HTTPClientOption
			if tt.baseURL != "" {
				opts = append(opts, WithBaseURL(tt.baseURL))
			}
			c := NewHTTPClient(opts...)
			if got := c.endpointURL(tt.region); got != tt.want {
				t.Errorf("endpointURL(%q) = %q, want %q", tt.region, got, tt.want)
			}
		})
	}
}

func TestBackoffDelay_Values(t *testing.T) {
	cases := []struct {
		attempt int
		base    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
	}
	for _, tc := range cases {
		// Run multiple times to account for jitter.
		for range 20 {
			got := backoffDelay(tc.attempt)
			lo := tc.base * 3 / 4 // -25%
			hi := tc.base * 5 / 4 // +25%
			if got < lo || got > hi {
				t.Errorf("backoffDelay(%d) = %v, want in [%v, %v]", tc.attempt, got, lo, hi)
			}
		}
	}
}

func TestReadErrorBody_Truncates(t *testing.T) {
	big := strings.Repeat("x", 2000)
	got := readErrorBody(io.NopCloser(strings.NewReader(big)))
	if len(got) > 1024 {
		t.Fatalf("expected ≤1024 bytes, got %d", len(got))
	}
}

func TestHTTPClient_NoGlobalTimeout(t *testing.T) {
	// Client.Timeout must be 0 (no timeout) so that long-running streaming
	// responses are not cut off. Timeouts should be context-based.
	c := NewHTTPClient()
	if c.httpClient.Timeout != 0 {
		t.Fatalf("httpClient.Timeout = %v, want 0 (context-based)", c.httpClient.Timeout)
	}
}

func TestHTTPClient_ContextTimeout(t *testing.T) {
	// Verify that context cancellation properly stops the request.
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // Simulate slow response
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(ctx, "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error from context timeout")
	}
}

func TestRetryWait_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := retryWait(ctx, 1*time.Second)
	if err == nil {
		t.Fatal("expected context error")
	}
}
