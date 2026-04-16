package kiroclient

import (
	"context"
	"encoding/json/v2"
	"errors"
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
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
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
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
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
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
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
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
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
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
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

func TestReadLimitedBody_Truncates(t *testing.T) {
	big := strings.Repeat("x", 2000)
	got := readLimitedBody(io.NopCloser(strings.NewReader(big)), 1024)
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

// TestHTTPClient_NonEventStreamContentType verifies that a 200 response with a
// non-eventstream Content-Type (e.g. application/json error envelope returned
// by Kiro under throttling or internal errors) is surfaced as an error instead
// of being passed to the frame parser, which would otherwise fail with a
// confusing "reading prelude" message.
func TestHTTPClient_NonEventStreamContentType(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"__type":"ThrottlingException","message":"Rate exceeded"}`))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error for non-eventstream Content-Type")
	}

	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError, got %T: %v", err, err)
	}
	if ue.Status != http.StatusOK {
		t.Errorf("Status = %d, want %d", ue.Status, http.StatusOK)
	}
	if ue.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want %q", ue.ContentType, "application/json")
	}
	if ue.Exception != "ThrottlingException" {
		t.Errorf("Exception = %q, want %q", ue.Exception, "ThrottlingException")
	}
	if !strings.Contains(ue.Body, "Rate exceeded") {
		t.Errorf("Body should contain upstream message, got %q", ue.Body)
	}
}

// TestHTTPClient_NonEventStreamThrottlingRetries verifies that a 200 response
// with an AWS-style ThrottlingException JSON body triggers retry rather than
// failing immediately.
func TestHTTPClient_NonEventStreamThrottlingRetries(t *testing.T) {
	var callCount atomic.Int32
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"__type":"ThrottlingException","message":"Rate exceeded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	resp, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	_ = resp.Body.Close()
	if callCount.Load() != 3 {
		t.Fatalf("expected 3 calls after 2 throttles, got %d", callCount.Load())
	}
}

func TestParseAWSExceptionType(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"throttling", `{"__type":"ThrottlingException","message":"..."}`, "ThrottlingException"},
		{"shape prefix", `{"__type":"com.amazon.coral.service#ThrottlingException"}`, "ThrottlingException"},
		{"type field", `{"type":"InternalServerException"}`, "InternalServerException"},
		{"code field", `{"code":"ServiceUnavailableException"}`, "ServiceUnavailableException"},
		{"empty", ``, ""},
		{"invalid", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAWSExceptionType(tc.body)
			if got != tc.want {
				t.Errorf("parseAWSExceptionType(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestIsRetryableAWSException(t *testing.T) {
	retryable := []string{
		"ThrottlingException",
		"TooManyRequestsException",
		"ServiceUnavailableException",
		"InternalServerException",
		"InternalFailureException",
		"InternalServerError",
	}
	for _, e := range retryable {
		if !isRetryableAWSException(e) {
			t.Errorf("%q should be retryable", e)
		}
	}
	nonRetryable := []string{"", "ValidationException", "AccessDeniedException", "ResourceNotFoundException"}
	for _, e := range nonRetryable {
		if isRetryableAWSException(e) {
			t.Errorf("%q should NOT be retryable", e)
		}
	}
}

func TestIsEventStreamContentType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"application/vnd.amazon.eventstream", true},
		{"application/vnd.amazon.eventstream; charset=utf-8", true},
		{"application/vnd.amazon.eventstream;charset=utf-8", true},
		{"Application/VND.Amazon.EventStream", true},
		{"application/json", false},
		{"application/json; charset=utf-8", false},
		{"", false},
		{"text/plain", false},
		{"application/vnd.amazon.eventstreamx", false}, // no separator
	}
	for _, tc := range cases {
		if got := isEventStreamContentType(tc.in); got != tc.want {
			t.Errorf("isEventStreamContentType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestUpstreamError_429 verifies that 429 errors return *UpstreamError.
func TestUpstreamError_429(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"slow down"}`))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error")
	}

	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError, got %T: %v", err, err)
	}
	if ue.Status != http.StatusTooManyRequests {
		t.Errorf("Status = %d, want %d", ue.Status, http.StatusTooManyRequests)
	}
	if !strings.Contains(ue.Body, "slow down") {
		t.Errorf("Body = %q, want to contain %q", ue.Body, "slow down")
	}
}

// TestUpstreamError_400 verifies that 400 errors return *UpstreamError.
func TestUpstreamError_400(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request body"))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error")
	}

	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError, got %T: %v", err, err)
	}
	if ue.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", ue.Status, http.StatusBadRequest)
	}
	if ue.Body != "bad request body" {
		t.Errorf("Body = %q, want %q", ue.Body, "bad request body")
	}
}

// TestUpstreamError_XAmznErrorTypeHeader verifies that X-Amzn-ErrorType header
// is used as fallback when __type is absent from the body.
func TestUpstreamError_XAmznErrorTypeHeader(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-ErrorType", "ValidationException:http://example.com")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"invalid"}`))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL))
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	_, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err == nil {
		t.Fatal("expected error")
	}

	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UpstreamError, got %T: %v", err, err)
	}
	if ue.Exception != "ValidationException" {
		t.Errorf("Exception = %q, want %q", ue.Exception, "ValidationException")
	}
}

// TestHTTPClient_BodyReadIdleTimeout verifies that when the Kiro API returns
// 200 with eventstream headers but then stops sending data, the idle watchdog
// fires and surfaces an error through ParseStream.
func TestHTTPClient_BodyReadIdleTimeout(t *testing.T) {
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		// Flush headers, then hang — never send body bytes.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // hold connection until test client disconnects
	}))
	defer srv.Close()

	c := NewHTTPClient(
		WithBaseURL(srv.URL),
		WithBodyReadIdleTimeout(50*time.Millisecond),
	)
	payload := &kiroproto.Payload{ConversationState: kiroproto.ConversationState{AgentTaskType: "vibe", ChatTriggerType: "MANUAL", CurrentMessage: kiroproto.CurrentMessage{UserInputMessage: kiroproto.UserInputMessage{Content: "Hi"}}}}
	resp, err := c.GenerateAssistantResponse(context.Background(), "tok", payload, "us-east-1")
	if err != nil {
		t.Fatalf("GenerateAssistantResponse should succeed (200), got error: %v", err)
	}

	// Feed the body through ParseStream — the real production path.
	// ParseStream uses bufio.Reader + io.ReadFull internally.
	parseErr := kiroproto.ParseStream(context.Background(), resp.Body, func(e kiroproto.Event) bool {
		t.Errorf("unexpected event: %+v", e)
		return true
	})
	_ = resp.Body.Close()

	if parseErr == nil {
		t.Fatal("expected ParseStream to fail with idle timeout, got nil")
	}
	if !errors.Is(parseErr, ErrBodyReadIdle) {
		t.Fatalf("expected ErrBodyReadIdle, got: %v", parseErr)
	}
}

func TestNormalizeAWSExceptionType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "ThrottlingException", "ThrottlingException"},
		{"hash prefix", "com.amazon.coral.service#ThrottlingException", "ThrottlingException"},
		{"colon suffix", "ValidationException:http://example.com", "ValidationException"},
		{"both hash and colon", "ns#ThrottlingException:hostname", "ThrottlingException"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeAWSExceptionType(tc.in)
			if got != tc.want {
				t.Errorf("normalizeAWSExceptionType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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
