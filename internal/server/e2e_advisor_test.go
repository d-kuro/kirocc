package server

import (
	"io"
	"strings"
	"testing"
)

const advisorNonStreamingRequest = `{
	"model":"claude-sonnet-4-6",
	"max_tokens":1024,
	"messages":[{"role":"user","content":"refactor this module"}],
	"tools":[
		{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5"},
		{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}
	]
}`

// advisorCallEvents is an executor round that calls the synthetic advisor tool.
func advisorCallEvents() []any {
	return []any{
		"toolUseEvent", mustJSON(map[string]any{
			"name": "advisor", "toolUseId": "advisor-call-1", "input": `{}`, "stop": true,
		}),
	}
}

func advisorAdviceEvents(text string) []any {
	return []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": text}),
		"metadataEvent", mustJSON(map[string]any{"tokenUsage": map[string]any{"uncachedInputTokens": 1200, "outputTokens": 340}}),
	}
}

// The full advisor loop: executor calls advisor → proxy consults the advisor
// model → executor completes with the advice in context. The client sees
// server_tool_use + advisor_tool_result + final text, and the advisor round's
// tokens land in usage.iterations[].
func TestE2E_Advisor_NonStreaming(t *testing.T) {
	client := &multiResponseClient{responses: [][]any{
		advisorCallEvents(),
		advisorAdviceEvents("Start with the contract layer."),
		textEvents("done per the advice"),
	}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, advisorNonStreamingRequest)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	result := decodeResponse(t, resp)

	if client.callCount != 3 {
		t.Fatalf("upstream calls = %d, want 3 (executor, advisor, executor)", client.callCount)
	}

	// The advisor subcall must use the advisor model, carry no tools, and no
	// conversation ID.
	advisorPayload := client.payloads[1]
	if got := advisorPayload.ConversationState.CurrentMessage.UserInputMessage.ModelID; got != "claude-opus-5" {
		t.Errorf("advisor subcall model = %q, want claude-opus-5", got)
	}
	if advisorPayload.ConversationState.ConversationID != "" {
		t.Errorf("advisor subcall conversationId = %q, want empty", advisorPayload.ConversationState.ConversationID)
	}
	if mctx := advisorPayload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext; mctx != nil && len(mctx.Tools) > 0 {
		t.Errorf("advisor subcall carries %d tools, want none", len(mctx.Tools))
	}

	// Block sequence: server_tool_use(advisor) → advisor_tool_result → text.
	content, _ := result["content"].([]any)
	var types []string
	for _, b := range content {
		block := b.(map[string]any)
		types = append(types, block["type"].(string))
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "server_tool_use,advisor_tool_result") {
		t.Fatalf("content types = %v, want server_tool_use followed by advisor_tool_result", types)
	}
	var advisorResult map[string]any
	for _, b := range content {
		block := b.(map[string]any)
		if block["type"] == "advisor_tool_result" {
			advisorResult = block
		}
	}
	inner, _ := advisorResult["content"].(map[string]any)
	if inner["type"] != "advisor_result" {
		t.Fatalf("advisor result content = %+v, want advisor_result", inner)
	}
	if inner["text"] != "Start with the contract layer." {
		t.Errorf("advice text = %q", inner["text"])
	}

	// usage.iterations[] carries the advisor round.
	usage, _ := result["usage"].(map[string]any)
	iters, _ := usage["iterations"].([]any)
	if len(iters) != 1 {
		t.Fatalf("iterations = %+v, want 1 entry", usage["iterations"])
	}
	iter := iters[0].(map[string]any)
	if iter["type"] != "advisor_message" || iter["model"] != "claude-opus-5" {
		t.Errorf("iteration = %+v", iter)
	}

	// The advisor round's second executor payload must carry the advice as a
	// tool result answering the synthetic tool call.
	execPayload := client.payloads[2]
	histText := historyText(execPayload)
	if !strings.Contains(histText, "Start with the contract layer.") {
		t.Errorf("second executor round history lacks the advice:\n%s", histText)
	}
}

// An unresolvable advisor model must yield advisor_tool_result_error with
// model_not_found — never an HTTP error, and no advisor upstream call.
func TestE2E_Advisor_ModelNotFound(t *testing.T) {
	req := strings.Replace(advisorNonStreamingRequest, `"model":"claude-opus-5"`, `"model":"claude-nonexistent-99"`, 1)
	client := &multiResponseClient{responses: [][]any{
		advisorCallEvents(),
		textEvents("proceeding without advice"),
	}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, req)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	result := decodeResponse(t, resp)

	if client.callCount != 2 {
		t.Fatalf("upstream calls = %d, want 2 (no advisor subcall)", client.callCount)
	}
	content, _ := result["content"].([]any)
	var errorCode string
	for _, b := range content {
		block := b.(map[string]any)
		if block["type"] == "advisor_tool_result" {
			inner, _ := block["content"].(map[string]any)
			if inner["type"] == "advisor_tool_result_error" {
				errorCode, _ = inner["error_code"].(string)
			}
		}
	}
	if errorCode != "model_not_found" {
		t.Fatalf("error_code = %q, want model_not_found\ncontent: %+v", errorCode, content)
	}
	usage, _ := result["usage"].(map[string]any)
	if _, has := usage["iterations"]; has {
		t.Errorf("failed consultation must not produce iterations: %+v", usage)
	}
}

// Exceeding max_uses yields max_uses_exceeded for the excess consultation.
func TestE2E_Advisor_MaxUsesExceeded(t *testing.T) {
	req := strings.Replace(advisorNonStreamingRequest,
		`"model":"claude-opus-5"`,
		`"model":"claude-opus-5","max_uses":1`, 1)
	client := &multiResponseClient{responses: [][]any{
		advisorCallEvents(),
		advisorAdviceEvents("first advice"),
		advisorCallEvents(),
		textEvents("giving up on more advice"),
	}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, req)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	result := decodeResponse(t, resp)

	content, _ := result["content"].([]any)
	var errorCodes []string
	var resultCount int
	for _, b := range content {
		block := b.(map[string]any)
		if block["type"] != "advisor_tool_result" {
			continue
		}
		inner, _ := block["content"].(map[string]any)
		switch inner["type"] {
		case "advisor_result":
			resultCount++
		case "advisor_tool_result_error":
			code, _ := inner["error_code"].(string)
			errorCodes = append(errorCodes, code)
		}
	}
	if resultCount != 1 {
		t.Errorf("advisor_result count = %d, want 1", resultCount)
	}
	if len(errorCodes) != 1 || errorCodes[0] != "max_uses_exceeded" {
		t.Errorf("error codes = %v, want [max_uses_exceeded]", errorCodes)
	}
}

// Streaming: the advisor blocks are emitted as SSE content blocks and the
// final message_delta carries usage.iterations[].
func TestE2E_Advisor_Streaming(t *testing.T) {
	req := strings.Replace(advisorNonStreamingRequest, `"max_tokens":1024`, `"max_tokens":1024,"stream":true`, 1)
	client := &multiResponseClient{responses: [][]any{
		advisorCallEvents(),
		advisorAdviceEvents("Streamed advice."),
		textEvents("done"),
	}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, req)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	sse := string(body)
	for _, want := range []string{
		`"type":"server_tool_use"`,
		`"name":"advisor"`,
		`"type":"advisor_tool_result"`,
		`"type":"advisor_result"`,
		`"text":"Streamed advice."`,
		`"type":"advisor_message"`,
		`"model":"claude-opus-5"`,
		"event: message_stop",
	} {
		if !strings.Contains(sse, want) {
			t.Errorf("SSE stream missing %s", want)
		}
	}
	if strings.Contains(sse, "encrypted_content") {
		t.Errorf("SSE stream contains forged encrypted_content")
	}
}

// A client tool call arriving after the advisor call in the same turn must
// not be dropped: its tool_use block reaches the client and the turn ends so
// the client can answer it.
func TestE2E_Advisor_ClientToolUseAfterAdvisorIsNotLost(t *testing.T) {
	req := strings.Replace(advisorNonStreamingRequest, `"max_tokens":1024`, `"max_tokens":1024,"stream":true`, 1)
	client := &multiResponseClient{responses: [][]any{
		{
			"toolUseEvent", mustJSON(map[string]any{
				"name": "advisor", "toolUseId": "advisor-call-1", "input": `{}`, "stop": true,
			}),
			"toolUseEvent", mustJSON(map[string]any{
				"name": "Read", "toolUseId": "read-call-1", "input": `{"path":"main.go"}`, "stop": true,
			}),
		},
		advisorAdviceEvents("advice for the mixed turn"),
	}}
	srv := newE2EServerWithClient(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, req)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	sse := string(body)

	// The client tool call must be present, the turn must close, and the
	// executor loop must not have run a third upstream round.
	for _, want := range []string{
		`"name":"Read"`,
		`"id":"read-call-1"`,
		`"type":"advisor_tool_result"`,
		"event: message_stop",
	} {
		if !strings.Contains(sse, want) {
			t.Errorf("SSE stream missing %s:\n%s", want, sse)
		}
	}
	if client.callCount != 2 {
		t.Errorf("upstream calls = %d, want 2 (executor + advisor, no extra round)", client.callCount)
	}
}
