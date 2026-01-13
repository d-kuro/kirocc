package respconv

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestSSEWriter_TextOnly(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello world"})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, "event: message_start\n") {
		t.Fatal("missing event: message_start prefix")
	}
	if !strings.Contains(body, "event: content_block_start\n") {
		t.Fatal("missing event: content_block_start prefix")
	}
	if !strings.Contains(body, `"text_delta"`) {
		t.Fatal("missing text_delta")
	}
	if !strings.Contains(body, `"text":"Hello"`) {
		t.Fatal("missing first delta")
	}
	if !strings.Contains(body, `"text":" world"`) {
		t.Fatal("missing second delta")
	}
	if !strings.Contains(body, "event: message_stop\n") {
		t.Fatal("missing event: message_stop")
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatal("missing end_turn stop_reason")
	}
}

func TestSSEWriter_ThinkingWithSignature(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Let me", Signature: "sig_abc123"})
	sw.HandleEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Let me think"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Answer"})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"signature":"sig_abc123"`) {
		t.Fatal("missing signature in thinking block start")
	}
	if !strings.Contains(body, `"thinking_delta"`) {
		t.Fatal("missing thinking_delta")
	}
	if !strings.Contains(body, `"thinking":"Let me"`) {
		t.Fatal("missing first thinking delta")
	}
	if !strings.Contains(body, `"thinking":" think"`) {
		t.Fatal("missing second thinking delta")
	}
}

func TestSSEWriter_ToolUse(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Checking."})
	sw.HandleEvent(kiroproto.Event{
		Type: "toolUseEvent", ToolStop: true,
		ToolUseID: "toolu_01", ToolName: "get_weather", ToolInput: `{"city":"Tokyo"}`,
	})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatal("missing tool_use stop_reason")
	}
	if !strings.Contains(body, `"name":"get_weather"`) {
		t.Fatal("missing tool name")
	}
	if !strings.Contains(body, `"input_json_delta"`) {
		t.Fatal("missing input_json_delta")
	}
}

func TestSSEWriter_InvalidState_PreStream(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	isError := sw.HandleEvent(kiroproto.Event{
		Type: "invalidStateEvent", InvalidStateReason: "CONTENT_LENGTH_EXCEEDS_THRESHOLD",
		ErrorMessage: "Too long",
	})
	if !isError {
		t.Fatal("expected error return")
	}
	if sw.Started() {
		t.Fatal("should not have started stream")
	}
}

func TestSSEWriter_InvalidState_MidStream(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	isError := sw.HandleEvent(kiroproto.Event{
		Type: "invalidStateEvent", ErrorMessage: "Error occurred",
	})
	if !isError {
		t.Fatal("expected error return")
	}
	body := w.Body.String()
	if !strings.Contains(body, `"invalid_state"`) {
		t.Fatal("missing error event in stream")
	}
}

func TestSSEWriter_MetadataEvent(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{
		Type: "metadataEvent", InputTokens: 100, OutputTokens: 50,
		CacheReadInputTokens: 20, CacheWriteInputTokens: 10,
	})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hi"})
	sw.Finish()

	input, output := sw.Usage()
	if input != 100 || output != 50 {
		t.Fatalf("Usage() = (%d, %d), want (100, 50)", input, output)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"output_tokens":50`) {
		t.Fatal("missing output_tokens in message_delta")
	}
}

func TestSSEWriter_RedactedContent(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "reasoningContentEvent", RedactedContent: "base64data"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Answer"})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"redacted_thinking"`) {
		t.Fatal("missing redacted_thinking block")
	}
	if !strings.Contains(body, `"base64data"`) {
		t.Fatal("missing redacted content data")
	}
}

func TestSSEWriter_NoOpEvents(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	// These should not cause any output or errors.
	sw.HandleEvent(kiroproto.Event{Type: "followupPromptEvent"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hi"})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"Hi"`) {
		t.Fatal("text should still work after no-op events")
	}
}

func TestSSEWriter_ThinkingViaTags(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	// Thinking via tags in assistant response event.
	sw.HandleEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>Step 1: analyze the problem</thinking>The answer is 42",
	})
	sw.Finish()

	body := w.Body.String()
	// Should have thinking block.
	if !strings.Contains(body, `"thinking"`) {
		t.Fatal("missing thinking block type")
	}
	if !strings.Contains(body, `"thinking_delta"`) {
		t.Fatal("missing thinking_delta")
	}
	if !strings.Contains(body, `"thinking":"Step 1: analyze the problem"`) {
		t.Fatalf("missing thinking content in body: %s", body)
	}
	// Should have text block.
	if !strings.Contains(body, `"text_delta"`) {
		t.Fatal("missing text_delta")
	}
	if !strings.Contains(body, `"text":"The answer is 42"`) {
		t.Fatalf("missing text content in body: %s", body)
	}
	// Stop reason should be end_turn.
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected end_turn stop_reason, body: %s", body)
	}
}

func TestSSEWriter_ThinkingOnly_ViaTags(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	// Only thinking tags — no visible text.
	sw.HandleEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>Let me reason through this</thinking>",
	})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"thinking_delta"`) {
		t.Fatal("missing thinking_delta")
	}
	// Empty text block should NOT be injected; instead IsEmptyVisibleEndTurn should be true.
	if strings.Contains(body, `"type":"text"`) {
		t.Fatalf("should not inject text block for thinking-only response, body: %s", body)
	}
	if !sw.IsEmptyVisibleEndTurn() {
		t.Fatal("expected IsEmptyVisibleEndTurn() = true")
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected end_turn stop_reason, body: %s", body)
	}
}

func TestSSEWriter_ThinkingOnly_ViaReasoningEvent(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	// Only a reasoning content event — no text, no regular tool.
	sw.HandleEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: "Thinking...", Signature: "sig_x"})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"thinking_delta"`) {
		t.Fatal("missing thinking_delta")
	}
	if strings.Contains(body, `"type":"text"`) {
		t.Fatalf("should not inject text block for thinking-only response, body: %s", body)
	}
	if !sw.IsEmptyVisibleEndTurn() {
		t.Fatal("expected IsEmptyVisibleEndTurn() = true")
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected end_turn stop_reason, body: %s", body)
	}
}

func TestSSEWriter_ThinkingWithToolUse_NoTextInjection(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	// Thinking via tags + regular tool — should NOT inject empty text block.
	sw.HandleEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>Let me check</thinking>",
	})
	sw.HandleEvent(kiroproto.Event{
		Type: "toolUseEvent", ToolStop: true,
		ToolUseID: "t2", ToolName: "bash",
		ToolInput: `{"cmd":"ls"}`,
	})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected tool_use stop_reason, body: %s", body)
	}
	// Count text block starts — should only have thinking and tool_use, no injected text.
	if strings.Count(body, `"type":"text"`) > 0 {
		t.Fatalf("should not inject text block when tool_use is present, body: %s", body)
	}
}

func TestSSEWriter_ThinkingViaTags_WithRegularTool(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	// Thinking via tags.
	sw.HandleEvent(kiroproto.Event{
		Type:    "assistantResponseEvent",
		Content: "<thinking>Let me check</thinking>",
	})
	// Regular tool.
	sw.HandleEvent(kiroproto.Event{
		Type: "toolUseEvent", ToolStop: true,
		ToolUseID: "t2", ToolName: "bash",
		ToolInput: `{"cmd":"ls"}`,
	})
	sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"thinking_delta"`) {
		t.Fatal("missing thinking_delta")
	}
	if !strings.Contains(body, `"name":"bash"`) {
		t.Fatal("missing regular tool")
	}
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatal("expected tool_use stop_reason")
	}
}
