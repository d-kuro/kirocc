package server

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/models"
	tu "github.com/d-kuro/kirocc/internal/testutil"
)

// mockAuthManager implements TokenGetter for tests.
type mockAuthManager struct {
	creds *auth.Credentials
	err   error
}

func (m *mockAuthManager) GetToken(ctx context.Context) (*auth.Credentials, error) {
	return m.creds, m.err
}

// mockKiroClient implements kiroclient.Client for tests.
type mockKiroClient struct {
	handler func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error)
}

func (m *mockKiroClient) GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
	return m.handler(ctx, token, payload, region)
}

// buildEventStream builds a binary event stream body from event type/payload pairs.
func buildEventStream(events ...any) io.ReadCloser {
	var buf bytes.Buffer
	for i := 0; i < len(events); i += 2 {
		eventType := events[i].(string)
		payload := events[i+1].([]byte)
		buf.Write(tu.BuildFrame(eventType, payload))
	}
	return io.NopCloser(&buf)
}

func newTestServer(t *testing.T, apiKey string, client kiroclient.Client) *httptest.Server {
	t.Helper()
	mgr := &mockAuthManager{
		creds: &auth.Credentials{
			AccessToken: "test-token",
			ProfileARN:  "arn:test",
			Region:      "us-east-1",
		},
	}
	if client == nil {
		client = &mockKiroClient{
			handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
				p, _ := json.Marshal(map[string]string{"content": "ok"})
				body := buildEventStream("assistantResponseEvent", p)
				return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
			},
		}
	}
	s := New(mgr, apiKey, client)
	return newTCP4TestServer(t, s.Handler())
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestGetModels(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	data := result["data"].([]any)
	if len(data) == 0 {
		t.Fatal("empty data")
	}
}

func TestGetModels_ContainsDefaultModel(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	data := result["data"].([]any)
	found := false
	for _, item := range data {
		m := item.(map[string]any)
		if m["id"] == models.DefaultModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default model %q not found", models.DefaultModel)
	}
}

func TestCountTokens(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/v1/messages/count_tokens",
		"application/json",
		strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hello"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	tokens, ok := result["input_tokens"].(float64)
	if !ok || tokens <= 0 {
		t.Fatalf("input_tokens = %v, want > 0", result["input_tokens"])
	}
}

func TestCountTokens_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/messages/count_tokens")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestCountTokens_InvalidJSON(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader("bad"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCountTokens_EmptyMessages(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages/count_tokens", "application/json",
		strings.NewReader(`{"model":"claude-sonnet-4","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEventLoggingBatch(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/event_logging/batch", "application/json", strings.NewReader(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAPIKeyAuth_Missing(t *testing.T) {
	srv := newTestServer(t, "secret", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAPIKeyAuth_Valid(t *testing.T) {
	srv := newTestServer(t, "secret", nil)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAPIKeyAuth_Invalid(t *testing.T) {
	srv := newTestServer(t, "secret", nil)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAPIKeyAuth_SkippedForHealth(t *testing.T) {
	srv := newTestServer(t, "secret", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestCORSHeaders(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	req, _ := http.NewRequest("OPTIONS", srv.URL+"/v1/messages", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:3000")
	}
}

func TestPostMessages_InvalidJSON(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader("bad"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPostMessages_EmptyMessages(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPostMessages_AuthError(t *testing.T) {
	mgr := &mockAuthManager{err: io.EOF}
	client := &mockKiroClient{handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
		return nil, io.EOF
	}}
	s := New(mgr, "", client)
	srv := newTCP4TestServer(t, s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 200 {
		t.Fatal("expected non-200 when auth fails")
	}
}

func TestPostMessages_NonStreaming(t *testing.T) {
	p1, _ := json.Marshal(map[string]string{"content": "Hello!"})
	p2, _ := json.Marshal(map[string]any{"usage": map[string]any{"inputTokens": 10, "outputTokens": 5}})

	client := &mockKiroClient{handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
		body := buildEventStream("assistantResponseEvent", p1, "meteringEvent", p2)
		return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	}}

	srv := newTestServer(t, "", client)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	if result["type"] != "message" {
		t.Fatalf("type = %v", result["type"])
	}
	content := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("empty content")
	}
	block := content[0].(map[string]any)
	if block["text"] != "Hello!" {
		t.Fatalf("text = %v", block["text"])
	}
}

func TestPostMessages_Streaming(t *testing.T) {
	p1, _ := json.Marshal(map[string]string{"content": "Hello"})
	p2, _ := json.Marshal(map[string]string{"content": "Hello world"})

	client := &mockKiroClient{handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
		body := buildEventStream("assistantResponseEvent", p1, "assistantResponseEvent", p2)
		return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	}}

	srv := newTestServer(t, "", client)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	sseBody := string(body)
	for _, want := range []string{"event: message_start", "event: content_block_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(sseBody, want) {
			t.Errorf("missing %q in SSE body", want)
		}
	}
}

func TestPostMessages_InvalidState_PreStream(t *testing.T) {
	p1, _ := json.Marshal(map[string]string{
		"reason":  "CONTENT_LENGTH_EXCEEDS_THRESHOLD",
		"message": "Too long",
	})

	client := &mockKiroClient{handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
		body := buildEventStream("invalidStateEvent", p1)
		return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	}}

	srv := newTestServer(t, "", client)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 400 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, body)
	}
}

func TestPostMessages_InvalidState_MidStream(t *testing.T) {
	p1, _ := json.Marshal(map[string]string{"content": "partial"})
	p2, _ := json.Marshal(map[string]string{"message": "limit exceeded"})

	client := &mockKiroClient{handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
		body := buildEventStream("assistantResponseEvent", p1, "invalidStateEvent", p2)
		return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	}}

	srv := newTestServer(t, "", client)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"invalid_state"`) {
		t.Fatalf("missing error event in SSE: %s", body)
	}
}

func TestPostMessages_PayloadPassthrough(t *testing.T) {
	var capturedPayload *kiroproto.Payload
	client := &mockKiroClient{handler: func(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
		capturedPayload = payload
		p, _ := json.Marshal(map[string]string{"content": "ok"})
		body := buildEventStream("assistantResponseEvent", p)
		return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	}}

	srv := newTestServer(t, "", client)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hello"}],"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if capturedPayload == nil {
		t.Fatal("payload not captured")
	}
	if capturedPayload.ConversationState.AgentTaskType != "vibe" {
		t.Fatalf("agentTaskType = %q", capturedPayload.ConversationState.AgentTaskType)
	}
	if capturedPayload.ConversationState.ChatTriggerType != "MANUAL" {
		t.Fatalf("chatTriggerType = %q", capturedPayload.ConversationState.ChatTriggerType)
	}
}

func TestPostMessages_MethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, "", nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestIsLocalhostOrigin(t *testing.T) {
	tests := []struct {
		origin string
		want   bool
	}{
		{"http://localhost:3000", true},
		{"https://localhost:8080", true},
		{"http://127.0.0.1:3000", true},
		{"https://127.0.0.1:443", true},
		{"http://[::1]:3000", true},
		{"", false},
		{"http://example.com", false},
		// Malformed origins that prefix matching would incorrectly accept:
		{"http://localhost:evil.com", false},
		{"http://localhost:3000/path@evil.com", false},
		// No port:
		{"http://localhost", true},
		{"http://127.0.0.1", true},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			got := isLocalhostOrigin(tt.origin)
			if got != tt.want {
				t.Errorf("isLocalhostOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}
