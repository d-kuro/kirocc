package server

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// errorClient always returns an error from GenerateAssistantResponse.
type errorClient struct {
	err error
}

func (c *errorClient) GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
	return nil, c.err
}

type headerMultiResponseClient struct {
	responses    [][]any
	headers      []http.Header
	promptTokens int
	callCount    int
}

func (c *headerMultiResponseClient) GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
	idx := c.callCount
	if idx >= len(c.responses) {
		idx = len(c.responses) - 1
	}
	c.callCount++
	body := buildEventStream(c.responses[idx]...)
	header := http.Header{}
	if idx < len(c.headers) {
		header = c.headers[idx].Clone()
	}
	return &kiroclient.Response{StatusCode: 200, Body: body, Header: header, PromptTokens: c.promptTokens}, nil
}

func TestE2E_InvalidStateEvent_PreStream(t *testing.T) {
	p1 := mustJSON(map[string]string{
		"reason":  "CONTENT_LENGTH_EXCEEDS_THRESHOLD",
		"message": "Too long",
	})
	client := &capturingClient{events: []any{"invalidStateEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 400 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, body)
	}
}

func TestE2E_InvalidStateEvent_MidStream(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "partial"})
	p2 := mustJSON(map[string]string{"message": "limit exceeded"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1, "invalidStateEvent", p2}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"invalid_state"`) {
		t.Fatalf("missing error event in SSE: %s", body)
	}
}

func TestE2E_InvalidStateEvent_MidStream_ErrorEvent(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "partial"})
	p2 := mustJSON(map[string]string{"message": "throttled"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1, "invalidStateEvent", p2}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	sseBody := string(body)
	if !strings.Contains(sseBody, "event: error") {
		t.Fatalf("missing error event: %s", sseBody)
	}
}

func TestE2E_EmptyMessages(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"user","content":""},
			{"role":"assistant","content":""},
			{"role":"user","content":"hi"}
		],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	if client.captured == nil {
		t.Fatal("payload not captured")
	}
	// Verify history was built (empty content is allowed — v2 captures show
	// tool-result continuations use content="" in real kiro-cli).
	if len(client.captured.ConversationState.History) == 0 {
		t.Fatal("expected non-empty history")
	}
}

func TestE2E_TokenUsage_CacheFields(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	meta := mustJSON(map[string]any{
		"tokenUsage": map[string]any{
			"uncachedInputTokens":   50,
			"outputTokens":          20,
			"totalTokens":           120,
			"cacheReadInputTokens":  40,
			"cacheWriteInputTokens": 10,
		},
	})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1, "metadataEvent", meta}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	usage := result["usage"].(map[string]any)
	if usage["cache_read_input_tokens"] == nil {
		t.Fatal("missing cache_read_input_tokens")
	}
	if usage["cache_creation_input_tokens"] == nil {
		t.Fatal("missing cache_creation_input_tokens")
	}
	if int(usage["cache_read_input_tokens"].(float64)) != 40 {
		t.Fatalf("cache_read = %v", usage["cache_read_input_tokens"])
	}
}

func TestE2E_ToolDeduplication(t *testing.T) {
	tool1 := mustJSON(map[string]any{
		"name":      "read_file",
		"toolUseId": "tool_1",
		"input":     map[string]string{"path": "/tmp/a"},
		"stop":      true,
	})
	tool2 := mustJSON(map[string]any{
		"name":      "read_file",
		"toolUseId": "tool_1",
		"input":     map[string]string{"path": "/tmp/a"},
		"stop":      true,
	})
	client := &capturingClient{events: []any{"toolUseEvent", tool1, "toolUseEvent", tool2}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	content := result["content"].([]any)
	toolUseCount := 0
	for _, c := range content {
		block := c.(map[string]any)
		if block["type"] == "tool_use" {
			toolUseCount++
		}
	}
	if toolUseCount != 1 {
		t.Fatalf("tool_use count = %d, want 1 (dedup)", toolUseCount)
	}
}

func TestE2E_ToolInputMixed(t *testing.T) {
	// toolUseEvent with string input chunks (accumulated) then stop
	chunk1 := mustJSON(map[string]any{
		"name":      "write_file",
		"toolUseId": "tool_x",
		"input":     `{"path":`,
	})
	chunk2 := mustJSON(map[string]any{
		"input": `"/tmp/a"}`,
		"stop":  true,
	})
	client := &capturingClient{events: []any{"toolUseEvent", chunk1, "toolUseEvent", chunk2}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	content := result["content"].([]any)
	found := false
	for _, c := range content {
		block := c.(map[string]any)
		if block["type"] == "tool_use" {
			found = true
			if block["name"] != "write_file" {
				t.Fatalf("tool name = %v", block["name"])
			}
		}
	}
	if !found {
		t.Fatal("no tool_use block in response")
	}
}

func TestE2E_Truncation_Content(t *testing.T) {
	// Stream with text but no metadataEvent — response should still succeed.
	p1 := mustJSON(map[string]string{"content": "partial response"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	// Response should still contain the partial text
	content := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("empty content")
	}
	block := content[0].(map[string]any)
	if block["text"] != "partial response" {
		t.Fatalf("text = %v", block["text"])
	}
}

func TestE2E_PreCountedTokens_NonStreaming(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "hello"})
	client := &capturingClient{
		events:       []any{"assistantResponseEvent", p1},
		promptTokens: 500,
	}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	usage := result["usage"].(map[string]any)
	if int(usage["input_tokens"].(float64)) != 500 {
		t.Fatalf("input_tokens = %v, want 500", usage["input_tokens"])
	}
}

func TestE2E_PreCountedTokens_Streaming(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "hello"})
	client := &capturingClient{
		events:       []any{"assistantResponseEvent", p1},
		promptTokens: 750,
	}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)

	body, _ := io.ReadAll(resp.Body)
	sseBody := string(body)
	if !strings.Contains(sseBody, `"input_tokens":750`) {
		t.Fatalf("expected input_tokens:750 in SSE stream, got: %s", sseBody)
	}
}

func TestE2E_PreCountedTokens_ZeroFallback(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "hello"})
	client := &capturingClient{
		events:       []any{"assistantResponseEvent", p1},
		promptTokens: 0, // simulates tokencount failure
	}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	usage := result["usage"].(map[string]any)
	if int(usage["input_tokens"].(float64)) != 0 {
		t.Fatalf("input_tokens = %v, want 0 (fallback)", usage["input_tokens"])
	}
}

func TestE2E_MetadataOverridesPreCounted(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "hello"})
	meta := mustJSON(map[string]any{
		"tokenUsage": map[string]any{
			"uncachedInputTokens": 100,
			"outputTokens":        50,
			"totalTokens":         150,
		},
	})
	client := &capturingClient{
		events:       []any{"assistantResponseEvent", p1, "metadataEvent", meta},
		promptTokens: 999, // should be overridden by metadata
	}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	usage := result["usage"].(map[string]any)
	if int(usage["input_tokens"].(float64)) != 100 {
		t.Fatalf("input_tokens = %v, want 100 (metadata should override pre-counted)", usage["input_tokens"])
	}
}

func TestE2E_ClientError_Returns502(t *testing.T) {
	// When the kiro client returns an error, server should return 502
	errClient := &errorClient{err: io.EOF}

	mgr := &mockAuthManager{
		creds: &auth.Credentials{
			AccessToken: "test-token",
			ProfileARN:  "arn:test",
			Region:      "us-east-1",
		},
	}
	s := New(mgr, "", errClient)
	srv := newTCP4TestServer(t, s.Handler())
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 502 {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestE2E_EmptyVisibleEndTurn_NonStreaming_RetrySucceeds(t *testing.T) {
	// First call: thinking-only via tags (empty visible end_turn).
	// Second call (retry): normal text response.
	thinkingOnly := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>Let me think</thinking>"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 5, "totalTokens": 15},
		}),
	}
	normalResponse := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "Here is the answer"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 10, "totalTokens": 20},
		}),
	}
	client := &multiResponseClient{responses: [][]any{thinkingOnly, normalResponse}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	content := result["content"].([]any)
	// Should contain the retry's text, not the thinking-only response.
	found := false
	for _, c := range content {
		block := c.(map[string]any)
		if block["type"] == "text" && block["text"] == "Here is the answer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected retry text in response, got content: %v", content)
	}
	// Should have been called twice.
	if client.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", client.callCount)
	}
}

func TestE2E_EmptyVisibleEndTurn_NonStreaming_RetryAlsoFails(t *testing.T) {
	// Both calls return thinking-only → should return 502.
	thinkingOnly := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>Let me think</thinking>"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 5, "totalTokens": 15},
		}),
	}
	client := &multiResponseClient{responses: [][]any{thinkingOnly, thinkingOnly}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 502)
	if client.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", client.callCount)
	}
}

func TestE2E_EmptyVisibleEndTurn_RetryClearsIDs(t *testing.T) {
	// Verify that retry clears ConversationID and AgentContinuationID.
	thinkingOnly := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>Let me think</thinking>"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 5, "totalTokens": 15},
		}),
	}
	normalResponse := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "Here is the answer"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 10, "totalTokens": 20},
		}),
	}
	client := &multiResponseClient{responses: [][]any{thinkingOnly, normalResponse}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	if client.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", client.callCount)
	}
	// Both payloads point to the same object (mutated in-place before retry),
	// so we verify the final state has cleared IDs.
	if client.payloads[1].ConversationState.ConversationID != "" {
		t.Fatalf("attempt-2 ConversationID should be empty, got %q", client.payloads[1].ConversationState.ConversationID)
	}
	if client.payloads[1].ConversationState.AgentContinuationID != "" {
		t.Fatalf("attempt-2 AgentContinuationID should be empty, got %q", client.payloads[1].ConversationState.AgentContinuationID)
	}
}

func TestE2E_EmptyVisibleEndTurn_Streaming_RetrySucceeds(t *testing.T) {
	// First call: thinking-only. Second call: normal text.
	thinkingOnly := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>Let me think</thinking>"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 5, "totalTokens": 15},
		}),
	}
	normalResponse := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "Streamed answer"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 10, "totalTokens": 20},
		}),
	}
	client := &multiResponseClient{responses: [][]any{thinkingOnly, normalResponse}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)

	body, _ := io.ReadAll(resp.Body)
	sseBody := string(body)
	// The retry response should contain the text.
	if !strings.Contains(sseBody, "Streamed answer") {
		t.Fatalf("expected retry text in SSE stream, got: %s", sseBody)
	}
	// The thinking-only first response should have been discarded (no thinking_delta in output).
	if strings.Contains(sseBody, "thinking_delta") {
		t.Fatalf("thinking from first attempt should have been discarded, got: %s", sseBody)
	}
	if client.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", client.callCount)
	}
}

func TestE2E_EmptyVisibleEndTurn_Streaming_SavesFailureCapture(t *testing.T) {
	logBuf := setupCaptureTest(t)

	thinkingOnly := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>Let me think</thinking>"}),
		"metadataEvent", mustJSON(map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 10, "outputTokens": 5, "totalTokens": 15},
		}),
	}
	client := &headerMultiResponseClient{
		responses: [][]any{thinkingOnly, thinkingOnly},
		headers: []http.Header{
			{"X-Amzn-RequestId": []string{"req-1"}},
			{"X-Amzn-RequestId": []string{"req-2"}},
		},
	}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4[1m]","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 502)

	// Parse log output and find capture records.
	logOutput := logBuf.String()
	var captureRecords []map[string]any
	for line := range strings.SplitSeq(logOutput, "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["body"] == "upstream failure capture" {
			captureRecords = append(captureRecords, rec)
		}
	}
	if len(captureRecords) != 2 {
		t.Fatalf("capture record count = %d, want 2\nlog output:\n%s", len(captureRecords), logOutput)
	}

	attrs, ok := captureRecords[0]["attributes"].(map[string]any)
	if !ok {
		t.Fatal("capture record missing attributes")
	}
	if attrs["reason"] != "empty_visible_end_turn" {
		t.Errorf("reason = %v, want empty_visible_end_turn", attrs["reason"])
	}
	// response_headers should contain req-1 for attempt 1.
	headers, ok := attrs["response_headers"].(map[string]any)
	if !ok {
		t.Fatalf("response_headers should be a map, got %T", attrs["response_headers"])
	}
	headerJSON, _ := json.Marshal(headers)
	if !strings.Contains(string(headerJSON), "req-1") {
		t.Errorf("response_headers should contain req-1, got: %s", headerJSON)
	}
	// events should contain assistantResponseEvent.
	events, ok := attrs["events"].([]any)
	if !ok || len(events) == 0 {
		t.Fatalf("events should be a non-empty array, got %T: %v", attrs["events"], attrs["events"])
	}
	eventsJSON, _ := json.Marshal(events)
	if !strings.Contains(string(eventsJSON), "assistantResponseEvent") {
		t.Errorf("events should contain assistantResponseEvent, got: %s", eventsJSON)
	}
	// request_body should be present and non-empty.
	if attrs["request_body"] == nil {
		t.Error("request_body should be present")
	}
	// Verify attempt 2 capture record has req-2 in response_headers.
	attrs2, ok := captureRecords[1]["attributes"].(map[string]any)
	if !ok {
		t.Fatal("capture record 2 missing attributes")
	}
	if attrs2["attempt"] != float64(2) {
		t.Errorf("attempt 2: attempt = %v, want 2", attrs2["attempt"])
	}
	headers2, ok := attrs2["response_headers"].(map[string]any)
	if !ok {
		t.Fatalf("attempt 2: response_headers should be a map, got %T", attrs2["response_headers"])
	}
	headerJSON2, _ := json.Marshal(headers2)
	if !strings.Contains(string(headerJSON2), "req-2") {
		t.Errorf("attempt 2: response_headers should contain req-2, got: %s", headerJSON2)
	}
}

func TestE2E_Success_DoesNotSaveFailureCapture(t *testing.T) {
	logBuf := setupCaptureTest(t)

	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}
	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	_, _ = io.ReadAll(resp.Body)

	if strings.Contains(logBuf.String(), "upstream failure capture") {
		t.Fatal("capture log should not appear on success")
	}
}
