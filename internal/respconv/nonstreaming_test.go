package respconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestBuildNonStreamingResponse_TextOnly(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "Hello"},
		{Type: "assistantResponseEvent", Content: "Hello world"},
		{Type: "metadataEvent", InputTokens: 10, OutputTokens: 5},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	content := resp["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello world" {
		t.Fatalf("unexpected block: %v", block)
	}
	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", resp["stop_reason"])
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != 10 || usage["output_tokens"] != 5 {
		t.Fatalf("usage = %v", usage)
	}
}

func TestBuildNonStreamingResponse_WithThinking(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "reasoningContentEvent", ThinkingText: "Let me think", Signature: "sig_123"},
		{Type: "assistantResponseEvent", Content: "Answer"},
		{Type: "metadataEvent", InputTokens: 100, OutputTokens: 50},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	content := resp["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2", len(content))
	}
	// First should be thinking.
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" {
		t.Fatalf("first block type = %v", thinking["type"])
	}
	if thinking["signature"] != "sig_123" {
		t.Fatalf("signature = %v", thinking["signature"])
	}
	// Second should be text.
	text := content[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "Answer" {
		t.Fatalf("text block = %v", text)
	}
}

func TestBuildNonStreamingResponse_WithToolUse(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "Checking."},
		{Type: "toolUseEvent", ToolStop: true, ToolUseID: "toolu_01", ToolName: "get_weather", ToolInput: `{"city":"Tokyo"}`},
		{Type: "metadataEvent", InputTokens: 50, OutputTokens: 20},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	content := resp["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len = %d", len(content))
	}
	toolUse := content[1].(map[string]any)
	if toolUse["type"] != "tool_use" {
		t.Fatalf("second block type = %v", toolUse["type"])
	}
	if resp["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v", resp["stop_reason"])
	}
}

func TestBuildNonStreamingResponse_CacheTokens(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "Hi"},
		{Type: "metadataEvent", InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 50, CacheWriteInputTokens: 20},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	usage := resp["usage"].(map[string]any)
	if usage["cache_read_input_tokens"] != 50 {
		t.Fatalf("cache_read = %v", usage["cache_read_input_tokens"])
	}
	if usage["cache_creation_input_tokens"] != 20 {
		t.Fatalf("cache_write = %v", usage["cache_creation_input_tokens"])
	}
}

func TestBuildNonStreamingResponse_MeteringFallback(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "Hi"},
		{Type: "meteringEvent", InputTokens: 30, OutputTokens: 10},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != 30 {
		t.Fatalf("input_tokens = %v", usage["input_tokens"])
	}
}

func TestBuildNonStreamingResponse_MetadataOverMetering(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "Hi"},
		{Type: "meteringEvent", InputTokens: 30, OutputTokens: 10},
		{Type: "metadataEvent", InputTokens: 100, OutputTokens: 50},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != 100 {
		t.Fatalf("should prefer metadataEvent, got %v", usage["input_tokens"])
	}
}

func TestBuildNonStreamingResponse_ThinkingOnly_ViaTags(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "<thinking>Let me reason through this</thinking>"},
		{Type: "metadataEvent", InputTokens: 10, OutputTokens: 5},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	content := resp["content"].([]any)
	// Thinking-only: no empty text block injected; only thinking block.
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1 (thinking only)", len(content))
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" {
		t.Fatalf("first block type = %v, want thinking", thinking["type"])
	}
	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v, want end_turn", resp["stop_reason"])
	}
}

func TestBuildNonStreamingResponse_ThinkingOnly_ViaReasoningEvent(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "reasoningContentEvent", ThinkingText: "Thinking...", Signature: "sig_x"},
		{Type: "metadataEvent", InputTokens: 10, OutputTokens: 5},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	content := resp["content"].([]any)
	// Thinking-only: no empty text block injected; only thinking block.
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1 (thinking only)", len(content))
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" {
		t.Fatalf("first block type = %v, want thinking", thinking["type"])
	}
}

func TestBuildNonStreamingResponse_ThinkingWithToolUse_NoTextInjection(t *testing.T) {
	events := []kiroproto.Event{
		{Type: "assistantResponseEvent", Content: "<thinking>Let me check</thinking>"},
		{Type: "toolUseEvent", ToolStop: true, ToolUseID: "t2", ToolName: "bash", ToolInput: `{"cmd":"ls"}`},
		{Type: "metadataEvent", InputTokens: 10, OutputTokens: 5},
	}
	resp, _ := BuildNonStreamingResponse(events, "claude-sonnet-4.6", 200000, nil, 0, 0)
	content := resp["content"].([]any)
	// Should have thinking + tool_use, no injected text.
	for _, c := range content {
		block := c.(map[string]any)
		if block["type"] == "text" {
			t.Fatalf("should not inject text block when tool_use is present, content: %v", content)
		}
	}
	if resp["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", resp["stop_reason"])
	}
}

func TestNewNonStreamingAccumulator_ProcessAndBuild(t *testing.T) {
	acc := NewNonStreamingAccumulator(200000, nil, 0, 0)
	acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	acc.ProcessEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello world"})
	acc.ProcessEvent(kiroproto.Event{Type: "metadataEvent", InputTokens: 10, OutputTokens: 5})

	resp, stats := acc.BuildResponse("claude-sonnet-4.6")
	content := resp["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello world" {
		t.Fatalf("unexpected block: %v", block)
	}
	if stats.InputTokens != 10 || stats.OutputTokens != 5 {
		t.Fatalf("usage = %d/%d", stats.InputTokens, stats.OutputTokens)
	}
}

func TestNonStreamingAccumulator_InvalidState(t *testing.T) {
	acc := NewNonStreamingAccumulator(200000, nil, 0, 0)
	d := acc.ProcessEvent(kiroproto.Event{
		Type:               "invalidStateEvent",
		InvalidStateReason: "CONTENT_LENGTH_EXCEEDS_THRESHOLD",
		ErrorMessage:       "Too long",
	})
	if !d.IsError {
		t.Fatal("expected IsError = true")
	}
}
