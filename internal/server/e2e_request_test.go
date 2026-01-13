package server

import (
	"io"
	"strings"
	"testing"
)

func TestE2E_ToolUseFlow(t *testing.T) {
	toolEvent := mustJSON(map[string]any{
		"name":      "get_weather",
		"toolUseId": "tool_1",
		"input":     map[string]string{"city": "Tokyo"},
		"stop":      true,
	})
	client := &capturingClient{events: []any{"toolUseEvent", toolEvent}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"user","content":"What is the weather?"},
			{"role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"get_weather","input":{"city":"Tokyo"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"Sunny, 25C"}]}
		],
		"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	ctx := client.captured.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		t.Fatal("no context")
	}
	if len(ctx.ToolResults) == 0 {
		t.Fatal("no tool results")
	}
	if ctx.ToolResults[0].ToolUseID != "tool_1" {
		t.Fatalf("toolUseId = %q", ctx.ToolResults[0].ToolUseID)
	}
}

func TestE2E_ToolResultOnly(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"I will help"}
		],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	content := client.captured.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "Continue") {
		t.Fatalf("expected Continue, got %q", content)
	}
}

func TestE2E_ThinkingMode_XMLInjection(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "result"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4[1m]","messages":[{"role":"user","content":"think about this"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	content := client.captured.ConversationState.CurrentMessage.UserInputMessage.Content
	// Should contain XML thinking tags (prompt injection approach).
	if !strings.Contains(content, "<thinking_mode>enabled</thinking_mode>") {
		t.Fatalf("should contain thinking_mode tag: %q", content)
	}
	if !strings.Contains(content, "<max_thinking_length>") {
		t.Fatalf("should contain max_thinking_length tag: %q", content)
	}
	// Original user text should still be present.
	if !strings.Contains(content, "think about this") {
		t.Fatalf("should contain original user text: %q", content)
	}
	// Should NOT have thinking tool in tools array.
	ctx := client.captured.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil {
		for _, te := range ctx.Tools {
			if te.ToolSpecification != nil && te.ToolSpecification.Name == "thinking" {
				t.Fatal("thinking tool should not be present (using prompt injection)")
			}
		}
	}
}

func TestE2E_ThinkingMode_Native(t *testing.T) {
	p1 := mustJSON(map[string]any{"text": "Let me think...", "signature": "sig123"})
	p2 := mustJSON(map[string]string{"content": "The answer is 42"})
	client := &capturingClient{events: []any{"reasoningContentEvent", p1, "assistantResponseEvent", p2}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	sseBody := string(body)
	if !strings.Contains(sseBody, `"thinking"`) {
		t.Error("missing thinking block type")
	}
	if !strings.Contains(sseBody, `"thinking_delta"`) {
		t.Error("missing thinking_delta")
	}
	if !strings.Contains(sseBody, `"sig123"`) {
		t.Error("missing signature in SSE")
	}
}

func TestE2E_SystemPromptPlacement(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"system":"You are helpful.",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":"reply"},
			{"role":"user","content":"second"}
		],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireCaptured(t, client)

	history := client.captured.ConversationState.History
	if len(history) < 3 {
		t.Fatalf("expected at least 3 history entries, got %d", len(history))
	}
	// history[0] = system prompt user, history[1] = synthetic ack, history[2] = original "first"
	firstUser := history[0].UserInputMessage
	if firstUser == nil {
		t.Fatal("first history entry is not user")
	}
	if !strings.Contains(firstUser.Content, "You are helpful.") {
		t.Fatalf("system not in first history user: %q", firstUser.Content)
	}
	origUser := history[2].UserInputMessage
	if origUser == nil {
		t.Fatal("history[2] should be user")
	}
	if !strings.Contains(origUser.Content, "first") {
		t.Fatalf("original content missing: %q", origUser.Content)
	}
}

func TestE2E_SystemPromptArray_CacheControl(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"system":[{"type":"text","text":"System A","cache_control":{"type":"ephemeral"}},{"type":"text","text":"System B"}],
		"messages":[{"role":"user","content":"hi"}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireCaptured(t, client)

	// System prompt is now in history[0], not currentMessage
	cs := client.captured.ConversationState
	if len(cs.History) < 1 {
		t.Fatal("expected history entries")
	}
	sysContent := cs.History[0].UserInputMessage.Content
	if !strings.Contains(sysContent, "System A") || !strings.Contains(sysContent, "System B") {
		t.Fatalf("system blocks missing from history[0]: %q", sysContent)
	}
}

func TestE2E_MultiTurnConversation(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"user","content":"turn 1"},
			{"role":"assistant","content":"reply 1"},
			{"role":"user","content":"turn 2"},
			{"role":"assistant","content":"reply 2"},
			{"role":"user","content":"turn 3"}
		],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireCaptured(t, client)

	history := client.captured.ConversationState.History
	if len(history) != 4 {
		t.Fatalf("history len = %d, want 4", len(history))
	}
	current := client.captured.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(current, "turn 3") {
		t.Fatalf("currentMessage = %q", current)
	}
}

func TestE2E_ImageInput(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "I see an image"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}},
			{"type":"text","text":"describe this"}
		]}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	images := client.captured.ConversationState.CurrentMessage.UserInputMessage.Images
	if len(images) == 0 {
		t.Fatal("no images in payload")
	}
	if images[0].Format != "png" {
		t.Fatalf("format = %q, want png", images[0].Format)
	}
}

func TestE2E_ImageURL_Skip(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":"https://example.com/img.png"}},
			{"type":"text","text":"describe"}
		]}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	images := client.captured.ConversationState.CurrentMessage.UserInputMessage.Images
	if len(images) != 0 {
		t.Fatalf("URL image should be skipped, got %d images", len(images))
	}
}

func TestE2E_CacheControl(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"cached msg","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":"reply"},
			{"role":"user","content":"next"}
		],
		"tools":[{"name":"my_tool","description":"A tool","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	ctx := client.captured.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		t.Fatal("no context")
	}
	hasCachePoint := false
	for _, te := range ctx.Tools {
		if te.CachePoint != nil {
			hasCachePoint = true
			break
		}
	}
	if !hasCachePoint {
		t.Fatal("no cache point in tools")
	}
}

func TestE2E_LongToolDescription(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	longDesc := strings.Repeat("x", 50001)
	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"big_tool","description":"` + longDesc + `","input_schema":{"type":"object"}}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	tools := client.captured.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(tools) == 0 {
		t.Fatal("expected tools in payload")
	}
	if tools[0].ToolSpecification.Description != longDesc {
		t.Fatal("long description should be kept as-is in tool spec")
	}
}

func TestE2E_SchemaSanitization(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"my_tool","description":"A tool","input_schema":{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false}}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	ctx := client.captured.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.Tools) == 0 {
		t.Fatal("no tools")
	}
	spec := ctx.Tools[0].ToolSpecification
	if spec == nil {
		t.Fatal("no tool specification")
	}
	if _, ok := spec.InputSchema.JSON["additionalProperties"]; ok {
		t.Fatal("additionalProperties should be removed")
	}
}

func TestE2E_ToolNameValidation(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	longName := strings.Repeat("a", 65)
	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"` + longName + `","description":"A tool","input_schema":{"type":"object"}}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 400)
}

func TestE2E_MessageNormalization(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"user","content":"msg1"},
			{"role":"user","content":"msg2"},
			{"role":"assistant","content":"reply"},
			{"role":"user","content":"msg3"}
		],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireCaptured(t, client)

	history := client.captured.ConversationState.History
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	first := history[0].UserInputMessage
	if first == nil {
		t.Fatal("first entry not user")
	}
	if !strings.Contains(first.Content, "msg1") || !strings.Contains(first.Content, "msg2") {
		t.Fatalf("merged content = %q", first.Content)
	}
}

func TestE2E_ToolReferenceBlockSkipped(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":[
			{"type":"tool_reference","tool_name":"Bash"},
			{"type":"text","text":"use this tool"}
		]}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	content := client.captured.ConversationState.CurrentMessage.UserInputMessage.Content
	if strings.Contains(content, "tool_reference") {
		t.Fatalf("tool_reference should be skipped, got: %q", content)
	}
	if !strings.Contains(content, "use this tool") {
		t.Fatalf("text content missing: %q", content)
	}
}

func TestE2E_UnknownContentBlock(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[{"role":"user","content":[
			{"type":"future_block"},
			{"type":"text","text":"hello"}
		]}],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	content := client.captured.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "[future_block]") {
		t.Fatalf("unknown block not converted: %q", content)
	}
}

func TestE2E_KiroConstraints(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	reqBody := `{
		"model":"claude-sonnet-4",
		"messages":[
			{"role":"assistant","content":"I start"},
			{"role":"user","content":"hello"}
		],
		"stream":false
	}`
	resp := postMessages(t, srv.URL, reqBody)
	defer func() { _ = resp.Body.Close() }()

	requireStatus(t, resp, 200)
	requireCaptured(t, client)

	history := client.captured.ConversationState.History
	if len(history) > 0 && history[0].UserInputMessage == nil {
		t.Fatal("first history entry should be user")
	}
}
