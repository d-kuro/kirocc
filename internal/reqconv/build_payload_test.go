package reqconv

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func buildPayloadForTest(req *anthropic.Request, profileARN, modelID, conversationID string) (*kiroproto.Payload, error) {
	p, _, err := BuildPayload(req, BuildOptions{
		ProfileARN:     profileARN,
		ModelID:        modelID,
		ConversationID: conversationID,
	})
	return p, err
}

func TestBuildPayload_SimpleMessage(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, err := buildPayloadForTest(req, "arn:test", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	cs := payload.ConversationState
	if cs.AgentTaskType != "vibe" {
		t.Fatalf("agentTaskType = %q", cs.AgentTaskType)
	}
	if cs.ChatTriggerType != "MANUAL" {
		t.Fatalf("chatTriggerType = %q", cs.ChatTriggerType)
	}
	if cs.CurrentMessage.UserInputMessage.Content != "Hello" {
		t.Fatalf("content = %q", cs.CurrentMessage.UserInputMessage.Content)
	}
	if cs.CurrentMessage.UserInputMessage.ModelID != "claude-sonnet-4.6" {
		t.Fatalf("modelId = %q", cs.CurrentMessage.UserInputMessage.ModelID)
	}
	if payload.ProfileARN != "arn:test" {
		t.Fatalf("profileArn = %q", payload.ProfileARN)
	}
	if len(cs.History) != 0 {
		t.Fatalf("history should be empty, got %d", len(cs.History))
	}
}

func TestBuildPayload_SystemPromptInHistory(t *testing.T) {
	req := &anthropic.Request{
		Model:  "claude-sonnet-4-6",
		System: anthropic.SystemPrompt{Text: "You are helpful."},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi"}},
			{Role: "user", Content: anthropic.MessageContent{Text: "How are you?"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	cs := payload.ConversationState
	// System prompt should be in history[0] as a separate entry, not prepended to first user message.
	if cs.CurrentMessage.UserInputMessage.Content != "How are you?" {
		t.Fatalf("currentMessage = %q", cs.CurrentMessage.UserInputMessage.Content)
	}
	// history: [0]=system user, [1]=synthetic ack, [2]=original user "Hello", [3]=original assistant "Hi"
	if len(cs.History) != 4 {
		t.Fatalf("history len = %d", len(cs.History))
	}
	h0 := cs.History[0].UserInputMessage
	if h0 == nil {
		t.Fatal("history[0] should be user message")
	}
	if h0.Content != "You are helpful." {
		t.Fatalf("history[0].content = %q", h0.Content)
	}
	h1 := cs.History[1].AssistantResponseMessage
	if h1 == nil {
		t.Fatal("history[1] should be synthetic assistant ack")
	}
	if h1.Content != syntheticAck {
		t.Fatalf("history[1].content = %q", h1.Content)
	}
	h2 := cs.History[2].UserInputMessage
	if h2 == nil {
		t.Fatal("history[2] should be user message")
	}
	if h2.Content != "Hello" {
		t.Fatalf("history[2].content = %q", h2.Content)
	}
}

func TestBuildPayload_SystemPromptNoHistory(t *testing.T) {
	req := &anthropic.Request{
		Model:  "claude-sonnet-4-6",
		System: anthropic.SystemPrompt{Text: "You are helpful."},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	// Even with single message, system prompt goes to history as separate entry (v2 behavior).
	cs := payload.ConversationState
	if cs.CurrentMessage.UserInputMessage.Content != "Hello" {
		t.Fatalf("content = %q", cs.CurrentMessage.UserInputMessage.Content)
	}
	if len(cs.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(cs.History))
	}
	if cs.History[0].UserInputMessage.Content != "You are helpful." {
		t.Fatalf("history[0] = %q", cs.History[0].UserInputMessage.Content)
	}
	if cs.History[1].AssistantResponseMessage == nil || cs.History[1].AssistantResponseMessage.Content != syntheticAck {
		t.Fatal("history[1] should be synthetic ack")
	}
}

func TestBuildPayload_LastAssistant(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi there"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	cs := payload.ConversationState
	if cs.CurrentMessage.UserInputMessage.Content != "Continue" {
		t.Fatalf("content = %q, want Continue", cs.CurrentMessage.UserInputMessage.Content)
	}
	if len(cs.History) != 2 {
		t.Fatalf("history len = %d", len(cs.History))
	}
}

func TestBuildPayload_ToolUseFlow(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Tools: []anthropic.Tool{
			{Name: "get_weather", Description: "Get weather", InputSchema: map[string]any{"type": "object"}},
		},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Weather?"}},
			{
				Role: "assistant",
				Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: "text", Text: "Checking."},
						{Type: "tool_use", ID: "toolu_01", Name: "get_weather", Input: map[string]any{"city": "Tokyo"}},
					},
				},
			},
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: "tool_result", ToolUseID: "toolu_01", Content: anthropic.MessageContent{Text: "Sunny"}},
					},
				},
			},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	cs := payload.ConversationState
	// currentMessage should have tool results.
	ctx := cs.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		t.Fatal("expected userInputMessageContext")
	}
	if len(ctx.ToolResults) != 1 {
		t.Fatalf("toolResults len = %d", len(ctx.ToolResults))
	}
	if ctx.ToolResults[0].ToolUseID != "toolu_01" {
		t.Fatalf("toolUseId = %q", ctx.ToolResults[0].ToolUseID)
	}
	if len(ctx.Tools) != 1 {
		t.Fatalf("tools len = %d", len(ctx.Tools))
	}
	if cs.CurrentMessage.UserInputMessage.Content != "" {
		t.Fatalf("currentMessage.content = %q, want empty", cs.CurrentMessage.UserInputMessage.Content)
	}
	// History should have assistant with toolUses.
	if len(cs.History) != 2 {
		t.Fatalf("history len = %d", len(cs.History))
	}
	arm := cs.History[1].AssistantResponseMessage
	if arm == nil {
		t.Fatal("history[1] should be assistant")
	}
	if len(arm.ToolUses) != 1 {
		t.Fatalf("toolUses len = %d", len(arm.ToolUses))
	}
}

func TestBuildPayload_NoThinkingXMLInjected(t *testing.T) {
	// Effort is now sent natively via additionalModelRequestFields; no XML tags
	// are ever injected into the user content, regardless of effort.
	req := &anthropic.Request{
		Model: "claude-opus-4-8",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Think about this."}},
		},
	}
	payload, _, err := BuildPayload(req, BuildOptions{ModelID: "claude-opus-4.8", ConversationID: "conv-test", Effort: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if contains(content, "<thinking_mode>") || contains(content, "<max_thinking_length>") {
		t.Fatalf("content must not contain thinking XML, got %q", content)
	}
	if content != "Think about this." {
		t.Fatalf("content = %q, want verbatim user text", content)
	}
}

func TestBuildPayload_EffortNative(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-opus-4-8",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, _, err := BuildPayload(req, BuildOptions{ModelID: "claude-opus-4.8", ConversationID: "conv-test", Effort: "max"})
	if err != nil {
		t.Fatal(err)
	}
	amrf := payload.AdditionalModelRequestFields
	if amrf == nil || amrf.OutputConfig == nil {
		t.Fatal("expected additionalModelRequestFields.output_config")
	}
	if amrf.OutputConfig.Effort != "max" {
		t.Fatalf("effort = %q, want max", amrf.OutputConfig.Effort)
	}
}

func TestBuildPayload_EffortOmittedWhenEmpty(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-opus-4-8",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-opus-4.8", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	if payload.AdditionalModelRequestFields != nil {
		t.Fatalf("additionalModelRequestFields should be nil when no effort, got %+v", payload.AdditionalModelRequestFields)
	}
}

func TestBuildPayload_EnvStateOnCurrentMessageOnly(t *testing.T) {
	req := &anthropic.Request{
		Model:  "claude-sonnet-4-6",
		System: anthropic.SystemPrompt{Text: "You are helpful.\n<env>\nWorking directory: /tmp/x\nPlatform: darwin\n</env>"},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi"}},
			{Role: "user", Content: anthropic.MessageContent{Text: "How are you?"}},
		},
	}
	payload, _, err := BuildPayload(req, BuildOptions{ModelID: "claude-sonnet-4.6", ConversationID: "conv-test"})
	if err != nil {
		t.Fatal(err)
	}

	// Current message carries envState (derived from the <env> block).
	curCtx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if curCtx == nil || curCtx.EnvState == nil {
		t.Fatal("current message should carry envState")
	}
	if curCtx.EnvState.OperatingSystem != "macos" || curCtx.EnvState.CurrentWorkingDirectory != "/tmp/x" {
		t.Fatalf("envState = %+v", curCtx.EnvState)
	}

	// No history entry carries envState.
	for i, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil && h.UserInputMessage.UserInputMessageContext != nil {
			if h.UserInputMessage.UserInputMessageContext.EnvState != nil {
				t.Fatalf("history[%d] should not carry envState", i)
			}
		}
	}
}

func TestBuildPayload_EnvStateOmittedWhenNil(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	// No tools, no tool results, no envState → no userInputMessageContext at all.
	if payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext != nil {
		t.Fatal("userInputMessageContext should be nil when nothing to attach")
	}
}

// TestBuildPayload_WireFieldOrder pins the JSON field order: additionalModelRequestFields
// after profileArn at the root, and envState before tools within
// userInputMessageContext — matching the captured kiro-cli 2.5.1 wire format.
func TestBuildPayload_WireFieldOrder(t *testing.T) {
	req := &anthropic.Request{
		Model:  "claude-opus-4-8",
		System: anthropic.SystemPrompt{Text: "<env>\nWorking directory: /tmp/x\nPlatform: darwin\n</env>"},
		Tools: []anthropic.Tool{
			{Name: "get_weather", Description: "Get weather", InputSchema: map[string]any{"type": "object"}},
		},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, _, err := BuildPayload(req, BuildOptions{ProfileARN: "arn:test", ModelID: "claude-opus-4.8", ConversationID: "conv-test", Effort: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	profileIdx := indexOf(s, `"profileArn"`)
	amrfIdx := indexOf(s, `"additionalModelRequestFields"`)
	if profileIdx < 0 || amrfIdx < 0 {
		t.Fatalf("missing root fields in %s", s)
	}
	if profileIdx > amrfIdx {
		t.Fatalf("profileArn must come before additionalModelRequestFields; got %s", s)
	}

	envIdx := indexOf(s, `"envState"`)
	toolsIdx := indexOf(s, `"tools"`)
	if envIdx < 0 || toolsIdx < 0 {
		t.Fatalf("missing envState/tools in %s", s)
	}
	if envIdx > toolsIdx {
		t.Fatalf("envState must come before tools; got %s", s)
	}
}

func indexOf(haystack, needle string) int {
	return strings.Index(haystack, needle)
}

func TestBuildPayload_EmptyProfileARN(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	if payload.ProfileARN != "" {
		t.Fatalf("profileArn should be empty, got %q", payload.ProfileARN)
	}
	// Verify JSON omits profileArn.
	data, _ := json.Marshal(payload)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if _, ok := m["profileArn"]; ok {
		t.Fatal("profileArn should be omitted in JSON")
	}
}

func TestBuildPayload_ThinkingInHistory(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "thinking", Thinking: "Let me reason about this"},
					{Type: "text", Text: "Here is my answer"},
					{Type: "tool_use", ID: "tool_1", Name: "bash", Input: map[string]any{"cmd": "ls"}},
				},
			}},
			{Role: "user", Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "tool_1", Content: anthropic.MessageContent{Text: "file.txt"}},
					{Type: "text", Text: "What next?"},
				},
			}},
		},
		Tools: []anthropic.Tool{
			{Name: "bash", Description: "Run bash", InputSchema: map[string]any{"type": "object"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}

	history := payload.ConversationState.History
	// History: [user, assistant]
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}

	// v2 captures show thinking blocks are NOT included in history toolUses.
	// Only regular tool_use blocks should be present.
	arm := history[1].AssistantResponseMessage
	if arm == nil {
		t.Fatal("history[1] should be assistant")
	}
	if len(arm.ToolUses) != 1 {
		t.Fatalf("toolUses len = %d, want 1 (only regular tool)", len(arm.ToolUses))
	}
	if arm.ToolUses[0].Name != "bash" {
		t.Fatalf("toolUse name = %q, want bash", arm.ToolUses[0].Name)
	}

	// Current message should NOT have thinking tool results (kiro-cli doesn't send them).
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		t.Fatal("expected context")
	}
	// Only the regular tool result should be present.
	if len(ctx.ToolResults) != 1 {
		t.Fatalf("toolResults len = %d, want 1 (only regular tool result)", len(ctx.ToolResults))
	}
	if ctx.ToolResults[0].ToolUseID != "tool_1" {
		t.Fatalf("toolResult ID = %q, want tool_1", ctx.ToolResults[0].ToolUseID)
	}
}

func TestBuildPayload_ThinkingPendingToCurrentMessage(t *testing.T) {
	// Last assistant has thinking, next user is currentMessage.
	// Thinking tool results should NOT be sent (kiro-cli behavior).
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "thinking", Thinking: "Deep thought"},
					{Type: "text", Text: "Answer"},
				},
			}},
			{Role: "user", Content: anthropic.MessageContent{Text: "Follow up"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}

	// v2 captures show thinking blocks are NOT included in history toolUses.
	// Assistant with only thinking + text should have no toolUses.
	arm := payload.ConversationState.History[1].AssistantResponseMessage
	if arm == nil {
		t.Fatal("history[1] should be assistant")
	}
	if len(arm.ToolUses) != 0 {
		t.Fatalf("expected no toolUses (thinking excluded), got %+v", arm.ToolUses)
	}

	// Current message should NOT have thinking tool results.
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil && len(ctx.ToolResults) > 0 {
		t.Fatalf("toolResults should be empty (no thinking results), got %d", len(ctx.ToolResults))
	}
}

func TestBuildPayload_UsesProvidedConversationID(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	p, _, err := BuildPayload(req, BuildOptions{ModelID: "claude-sonnet-4.6", ConversationID: "session-abc-123"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ConversationState.ConversationID != "session-abc-123" {
		t.Fatalf("got %q; want %q", p.ConversationState.ConversationID, "session-abc-123")
	}
}

func TestBuildPayload_EmptyConversationID(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}
	p, _, _ := BuildPayload(req, BuildOptions{ModelID: "claude-sonnet-4.6"})
	if p.ConversationState.ConversationID != "" {
		t.Fatalf("got %q; want empty", p.ConversationState.ConversationID)
	}
}

func TestBuildPayload_Doc09_FullExample(t *testing.T) {
	// Reproduce the full conversion example from doc 09.
	req := &anthropic.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 8096,
		Stream:    true,
		System: anthropic.SystemPrompt{
			Blocks: []anthropic.SystemBlock{
				{Type: "text", Text: "You are a helpful coding assistant.", CacheControl: &anthropic.CacheControl{Type: "ephemeral"}},
			},
		},
		Tools: []anthropic.Tool{
			{
				Name:        "get_weather",
				Description: "Get current weather for a city",
				InputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"city": map[string]any{"type": "string"}},
					"required":             []any{"city"},
					"additionalProperties": false,
				},
			},
		},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "What's the weather in Tokyo and New York?"}},
			{
				Role: "assistant",
				Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: "text", Text: "I'll check both cities for you."},
						{Type: "tool_use", ID: "toolu_01A", Name: "get_weather", Input: map[string]any{"city": "Tokyo"}},
						{Type: "tool_use", ID: "toolu_02B", Name: "get_weather", Input: map[string]any{"city": "New York"}},
					},
				},
			},
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: "tool_result", ToolUseID: "toolu_01A", Content: anthropic.MessageContent{Text: "Sunny, 28°C"}},
						{Type: "tool_result", ToolUseID: "toolu_02B", Content: anthropic.MessageContent{Text: ""}, IsError: true},
					},
				},
			},
		},
	}

	payload, err := buildPayloadForTest(req, "arn:aws:codewhisperer:us-east-1:123456789:profile/example", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}

	cs := payload.ConversationState
	// agentTaskType
	if cs.AgentTaskType != "vibe" {
		t.Fatalf("agentTaskType = %q", cs.AgentTaskType)
	}
	// tool_result-only continuation should keep empty currentMessage.content.
	if cs.CurrentMessage.UserInputMessage.Content != "" {
		t.Fatalf("currentMessage.content = %q, want empty", cs.CurrentMessage.UserInputMessage.Content)
	}
	// Tool results
	ctx := cs.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) != 2 {
		t.Fatalf("expected 2 tool results")
	}
	if ctx.ToolResults[0].Status != "success" {
		t.Fatalf("first result status = %q", ctx.ToolResults[0].Status)
	}
	if ctx.ToolResults[1].Status != "error" {
		t.Fatalf("second result status = %q", ctx.ToolResults[1].Status)
	}
	if ctx.ToolResults[1].Content[0].JSON["stdout"] != "(empty result)" {
		t.Fatalf("empty tool result = %v", ctx.ToolResults[1].Content[0].JSON)
	}
	// History: system prompt as separate entry + synthetic ack + original messages
	if len(cs.History) != 4 {
		t.Fatalf("history len = %d", len(cs.History))
	}
	h0 := cs.History[0].UserInputMessage
	if h0 == nil {
		t.Fatal("history[0] should be user")
	}
	if h0.Content != "You are a helpful coding assistant." {
		t.Fatalf("history[0].content = %q", h0.Content)
	}
	h1 := cs.History[1].AssistantResponseMessage
	if h1 == nil || h1.Content != syntheticAck {
		t.Fatal("history[1] should be synthetic ack")
	}
	h2 := cs.History[2].UserInputMessage
	if h2 == nil {
		t.Fatal("history[2] should be user")
	}
	if h2.Content != "What's the weather in Tokyo and New York?" {
		t.Fatalf("history[2].content = %q", h2.Content)
	}
	// Assistant history with tool uses (now at index 3)
	h3 := cs.History[3].AssistantResponseMessage
	if h3 == nil {
		t.Fatal("history[3] should be assistant")
	}
	if len(h3.ToolUses) != 2 {
		t.Fatalf("toolUses len = %d", len(h3.ToolUses))
	}
	// Schema sanitization: additionalProperties removed
	toolSpec := ctx.Tools[0].ToolSpecification
	if toolSpec == nil {
		t.Fatal("expected tool specification")
	}
	schema := toolSpec.InputSchema.JSON
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatal("additionalProperties should be removed")
	}
	// profileArn
	if payload.ProfileARN != "arn:aws:codewhisperer:us-east-1:123456789:profile/example" {
		t.Fatalf("profileArn = %q", payload.ProfileARN)
	}
}

func TestBuildPayload_NoContextWhenNoToolsOrResults(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi"}},
			{Role: "user", Content: anthropic.MessageContent{Text: "How are you?"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	h0 := payload.ConversationState.History[0].UserInputMessage
	if h0.UserInputMessageContext != nil {
		t.Fatal("history user message should not have UserInputMessageContext when no tools or toolResults")
	}
}

func TestBuildPayload_ToolResultsInHistory(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Tools: []anthropic.Tool{
			{Name: "bash", Description: "Run bash", InputSchema: map[string]any{"type": "object"}},
		},
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_use", ID: "tool_1", Name: "bash", Input: map[string]any{"cmd": "ls"}},
				},
			}},
			{Role: "user", Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "tool_1", Content: anthropic.MessageContent{Text: "file.txt"}},
				},
			}},
			{Role: "assistant", Content: anthropic.MessageContent{Text: "Done"}},
			{Role: "user", Content: anthropic.MessageContent{Text: "Thanks"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	// history[2] is the user message with tool_result — should have ToolResults.
	h2 := payload.ConversationState.History[2].UserInputMessage
	if h2 == nil {
		t.Fatal("history[2] should be user message")
	}
	if h2.UserInputMessageContext == nil {
		t.Fatal("history[2] should have UserInputMessageContext for toolResults")
	}
	if len(h2.UserInputMessageContext.ToolResults) != 1 {
		t.Fatalf("toolResults len = %d, want 1", len(h2.UserInputMessageContext.ToolResults))
	}
}

func TestBuildPayload_AssistantMessageID(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
			{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi"}},
			{Role: "user", Content: anthropic.MessageContent{Text: "How are you?"}},
		},
	}
	payload, err := buildPayloadForTest(req, "", "claude-sonnet-4.6", "conv-test")
	if err != nil {
		t.Fatal(err)
	}
	arm := payload.ConversationState.History[1].AssistantResponseMessage
	if arm == nil {
		t.Fatal("history[1] should be assistant")
	}
	if arm.MessageID == "" {
		t.Fatal("assistant history message should have a non-empty messageId")
	}
}

func TestPlaceSystemPrompt_InjectsIntoHistory(t *testing.T) {
	history := []kiroproto.HistoryEntry{
		{UserInputMessage: &kiroproto.HistoryUserInputMessage{Content: "hello"}},
	}
	newHistory, last := placeSystemPrompt("System", history, "current")
	if last != "current" {
		t.Fatalf("lastContent changed: %q", last)
	}
	// Should have: [0]=system user, [1]=synthetic ack, [2]=original "hello"
	if len(newHistory) != 3 {
		t.Fatalf("newHistory len = %d, want 3", len(newHistory))
	}
	if newHistory[0].UserInputMessage.Content != "System" {
		t.Fatalf("history[0] = %q", newHistory[0].UserInputMessage.Content)
	}
	if newHistory[1].AssistantResponseMessage == nil || newHistory[1].AssistantResponseMessage.Content != syntheticAck {
		t.Fatal("history[1] should be synthetic ack")
	}
	if newHistory[1].AssistantResponseMessage.MessageID == "" {
		t.Fatal("synthetic ack should have a non-empty MessageID")
	}
	if newHistory[2].UserInputMessage.Content != "hello" {
		t.Fatalf("history[2] = %q", newHistory[2].UserInputMessage.Content)
	}
	// Original slice must NOT be mutated.
	if history[0].UserInputMessage.Content != "hello" {
		t.Fatal("original history was mutated")
	}
}

func TestPlaceSystemPrompt_NoHistory(t *testing.T) {
	newHistory, last := placeSystemPrompt("System", nil, "current")
	if last != "current" {
		t.Fatalf("last = %q", last)
	}
	// Even with no history, system prompt pair is created.
	if len(newHistory) != 2 {
		t.Fatalf("history len = %d, want 2", len(newHistory))
	}
	if newHistory[0].UserInputMessage.Content != "System" {
		t.Fatalf("history[0] = %q", newHistory[0].UserInputMessage.Content)
	}
	if newHistory[1].AssistantResponseMessage == nil || newHistory[1].AssistantResponseMessage.Content != syntheticAck {
		t.Fatal("history[1] should be synthetic ack")
	}
	if newHistory[1].AssistantResponseMessage.MessageID == "" {
		t.Fatal("synthetic ack should have a non-empty MessageID")
	}
}

func TestPlaceSystemPrompt_EmptySystem(t *testing.T) {
	history := []kiroproto.HistoryEntry{
		{UserInputMessage: &kiroproto.HistoryUserInputMessage{Content: "hello"}},
	}
	newHistory, last := placeSystemPrompt("", history, "current")
	if last != "current" {
		t.Fatalf("last = %q", last)
	}
	if newHistory[0].UserInputMessage.Content != "hello" {
		t.Fatalf("history[0] = %q", newHistory[0].UserInputMessage.Content)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
