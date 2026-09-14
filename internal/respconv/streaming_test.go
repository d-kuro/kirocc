package respconv

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestSSEWriter_TextOnly(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: " world"})
	_ = sw.Finish()

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
	// Exactly one delta per frame — a duplicated or re-sent frame would
	// produce extra text_delta events (issue #112 regression guard).
	if n := strings.Count(body, `"text_delta"`); n != 2 {
		t.Fatalf("text_delta count = %d, want 2", n)
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
	sw.HandleEvent(kiroproto.Event{Type: "reasoningContentEvent", ThinkingText: " think"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Answer"})
	_ = sw.Finish()

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
	_ = sw.Finish()

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

func TestSSEWriter_InvalidState_MidStreamLeavesErrorOutputToCaller(t *testing.T) {
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
	if strings.Contains(body, "event: error") {
		t.Fatal("SSEWriter must leave final error classification/output to the stream session")
	}
}

type failingSSEWriter struct {
	header http.Header
	err    error
}

func (w *failingSSEWriter) Header() http.Header { return w.header }
func (w *failingSSEWriter) WriteHeader(int)     {}
func (w *failingSSEWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestSSEWriter_StoresConcreteWriteError(t *testing.T) {
	wantErr := errors.New("downstream write failed")
	w := &failingSSEWriter{header: make(http.Header), err: wantErr}
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	if !errors.Is(sw.WriteErr(), wantErr) {
		t.Fatalf("WriteErr() = %v, want %v", sw.WriteErr(), wantErr)
	}
}

func TestSSEWriter_StoresPromotionError(t *testing.T) {
	wantErr := errors.New("promote flush failed")
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)
	sw.OnVisibleOutput = func() error { return wantErr }

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hello"})
	if !errors.Is(sw.WriteErr(), wantErr) {
		t.Fatalf("WriteErr() = %v, want %v", sw.WriteErr(), wantErr)
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
	_ = sw.Finish()

	input, output := sw.Usage()
	if input != 100 || output != 50 {
		t.Fatalf("Usage() = (%d, %d), want (100, 50)", input, output)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"output_tokens":50`) {
		t.Fatal("missing output_tokens in message_delta")
	}
}

func TestSSEWriter_Credits(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	if _, ok := sw.Credits(); ok {
		t.Fatal("expected no credits before any meteringEvent")
	}

	sw.HandleEvent(kiroproto.Event{Type: "meteringEvent", Credits: 0.5417})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Hi"})
	_ = sw.Finish()

	credits, ok := sw.Credits()
	if !ok {
		t.Fatal("expected credits to be present after meteringEvent")
	}
	if credits != 0.5417 {
		t.Fatalf("Credits() = %v, want 0.5417", credits)
	}
}

func TestSSEWriter_RecordTail(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)
	bodyBefore := w.Body.Len()

	// RecordTail should ingest credit/context info but not write SSE output.
	sw.RecordTail(kiroproto.Event{Type: "meteringEvent", Credits: 0.99})
	sw.RecordTail(kiroproto.Event{Type: "contextUsageEvent", ContextUsagePercentage: 12.5})
	// A non-tail event must be ignored entirely (not routed to acc, no SSE).
	sw.RecordTail(kiroproto.Event{Type: "assistantResponseEvent", Content: "should-not-appear"})

	if w.Body.Len() != bodyBefore {
		t.Fatalf("RecordTail wrote SSE bytes (%d → %d)", bodyBefore, w.Body.Len())
	}
	credits, ok := sw.Credits()
	if !ok || credits != 0.99 {
		t.Fatalf("Credits = (%v, %v), want (0.99, true)", credits, ok)
	}
	if !sw.HasContextUsage() || sw.ContextUsagePercentage() != 12.5 {
		t.Fatalf("ContextUsage = (%v, %v), want (12.5, true)", sw.ContextUsagePercentage(), sw.HasContextUsage())
	}
}

func TestSSEWriter_RedactedContent(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "reasoningContentEvent", RedactedContent: "base64data"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "Answer"})
	_ = sw.Finish()

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
	_ = sw.Finish()

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
	_ = sw.Finish()

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
	_ = sw.Finish()

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
	_ = sw.Finish()

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
	_ = sw.Finish()

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
	_ = sw.Finish()

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

func TestSSEWriter_RecordTail_RedactedContent(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "gpt-5.6-sol", 272000, nil, 0, 100)
	bodyBefore := w.Body.Len()
	redacted := strings.Repeat("A", 400)

	// Trailing redacted blob after the tool-search frame must be accumulated
	// for next-round replay, without writing anything to the SSE stream.
	sw.RecordTail(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: redacted})

	if w.Body.Len() != bodyBefore {
		t.Fatalf("RecordTail wrote SSE bytes (%d → %d)", bodyBefore, w.Body.Len())
	}
	got := sw.RedactedContents()
	if len(got) != 1 || got[0] != redacted {
		t.Fatalf("RedactedContents() = %v, want one 400-rune blob", got)
	}
	inputTokens, outputTokens := sw.Usage()
	if inputTokens != 100 || outputTokens < 1 {
		t.Fatalf("Usage() = (%d, %d), want (100, >=1)", inputTokens, outputTokens)
	}
}

func TestSSEWriter_MaxTokensWithToolUse_DrainsTrailingBlob(t *testing.T) {
	w := httptest.NewRecorder()
	// budget 1 token = 4 runes; the tool input exceeds it → LocalStop.
	sw := NewSSEWriter(context.Background(), w, "gpt-5.6-sol", 272000, nil, 1, 0)
	sw.SetDrainOnStop(true)

	stopped := sw.HandleEvent(kiroproto.Event{Type: "toolUseEvent", ToolStop: true, ToolUseID: "call_1", ToolName: "read", ToolInput: `{"path":"/tmp/somewhere"}`})
	if stopped {
		t.Fatal("HandleEvent must not terminate on max_tokens when a completed tool call exists (drain trailing blob)")
	}
	if !sw.LocalStop() {
		t.Fatal("expected LocalStop after budget exceeded")
	}
	// Trailing blob arrives after the tool_use frame — must still be emitted.
	sw.HandleEvent(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: "late-blob"})
	_ = sw.Finish()

	body := w.Body.String()
	if !strings.Contains(body, `"redacted_thinking"`) || !strings.Contains(body, `"late-blob"`) {
		t.Fatalf("missing trailing redacted_thinking block: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("stop_reason should stay max_tokens: %s", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("missing message_stop: %s", body)
	}
}

func TestSSEWriter_MaxTokensTextOnly_StopsImmediately(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 1, 0)

	stopped := sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "0123456789abcdef"})
	if !stopped {
		t.Fatal("text-only max_tokens must terminate the stream immediately")
	}
	if !strings.Contains(w.Body.String(), "message_stop") {
		t.Fatal("Finish must have been called on immediate stop")
	}
}

func TestSSEWriter_MaxTokensWithToolUse_NoDrainStopsImmediately(t *testing.T) {
	// Claude models never emit a trailing reasoning blob: a completed tool
	// call must not keep the stream draining when drainOnStop is unset.
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 1, 0)

	stopped := sw.HandleEvent(kiroproto.Event{Type: "toolUseEvent", ToolStop: true, ToolUseID: "toolu_1", ToolName: "read", ToolInput: `{"path":"/tmp/somewhere"}`})
	if !stopped {
		t.Fatal("max_tokens with tool use must terminate immediately without drainOnStop")
	}
	if !strings.Contains(w.Body.String(), "message_stop") {
		t.Fatal("Finish must have been called on immediate stop")
	}
}

func TestSSEWriter_MaxTokensRedactedOnly_StopsImmediately(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "gpt-5.6-sol", 272000, nil, 2, 0)

	stopped := sw.HandleEvent(kiroproto.Event{
		Type:            kiroproto.EventReasoningContent,
		RedactedContent: strings.Repeat("A", 8),
	})

	if !stopped {
		t.Fatal("redacted-only max_tokens must terminate the stream immediately")
	}
	body := w.Body.String()
	if !strings.Contains(body, `"redacted_thinking"`) {
		t.Fatalf("missing redacted_thinking block: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("missing max_tokens stop reason: %s", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("missing message_stop: %s", body)
	}
}

// sseBlockOrder returns the content-block types in emission order, plus the
// content_block_start indices, so a test can assert both ordering and that
// dropping a block left no gap in the index sequence.
func sseBlockOrder(t *testing.T, body string) (types []string, indices []int) {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var e struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue
		}
		if e.Type != "content_block_start" {
			continue
		}
		types = append(types, e.ContentBlock.Type)
		indices = append(indices, e.Index)
	}
	return types, indices
}

func assertContiguousIndices(t *testing.T, indices []int) {
	t.Helper()
	for i, got := range indices {
		if got != i {
			t.Fatalf("content_block_start indices = %v, want 0..%d contiguous", indices, len(indices)-1)
		}
	}
}

// Kiro's `auto` router trails its reasoning blob after the text block. Claude
// Code's final-result extraction keeps only text that follows the last thinking
// block, so emitting that blob empties `claude -p`. The blob has nothing to
// replay in a round with no tool call, so it must be dropped.
func TestSSEWriter_AutoTrailingRedacted_DroppedSoTextSurvives(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "auto", 1000000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "OK"})
	sw.HandleEvent(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: "blob-trailing"})
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	body := w.Body.String()
	if strings.Contains(body, "redacted_thinking") {
		t.Fatalf("trailing blob must not reach the client: %s", body)
	}
	if strings.Contains(body, "blob-trailing") {
		t.Fatalf("blob payload leaked: %s", body)
	}
	if !strings.Contains(body, `"text":"OK"`) && !strings.Contains(body, `"text_delta","text":"OK"`) {
		t.Fatalf("answer text missing: %s", body)
	}
	types, indices := sseBlockOrder(t, body)
	if len(types) != 1 || types[0] != "text" {
		t.Fatalf("blocks = %v, want exactly one text block", types)
	}
	assertContiguousIndices(t, indices)
}

// A blob that genuinely precedes the text is the Anthropic convention and is
// kept where it arrived — dropping it would lose reasoning the client may echo.
func TestSSEWriter_LeadingRedacted_KeptBeforeText(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "auto", 1000000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: "blob-leading"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "OK"})
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "blob-leading") {
		t.Fatalf("leading blob must be preserved: %s", body)
	}
	types, indices := sseBlockOrder(t, body)
	if len(types) != 2 || types[0] != "redacted_thinking" || types[1] != "text" {
		t.Fatalf("blocks = %v, want [redacted_thinking text]", types)
	}
	assertContiguousIndices(t, indices)
}

// An interior blob keeps its position: text after it proves it was not
// trailing, and in a reasoning-model round that position is what attributes the
// blob to a tool call. Only a blob left pending at the end of the round is
// reconsidered. Real `auto` output never takes this shape — it sends one text
// block then one trailing blob, even for a long multi-paragraph answer.
func TestSSEWriter_InteriorRedacted_KeepsPosition(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "gpt-5.6-sol", 272000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: "HEAD"})
	sw.HandleEvent(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: "blob-mid"})
	sw.HandleEvent(kiroproto.Event{Type: "assistantResponseEvent", Content: " TAIL"})
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "blob-mid") {
		t.Fatalf("interior blob must keep its position: %s", body)
	}
	if !strings.Contains(body, "HEAD") || !strings.Contains(body, "TAIL") {
		t.Fatalf("both halves of the answer must be present: %s", body)
	}
	types, indices := sseBlockOrder(t, body)
	if len(types) != 3 || types[0] != "text" || types[1] != "redacted_thinking" || types[2] != "text" {
		t.Fatalf("blocks = %v, want [text redacted_thinking text]", types)
	}
	assertContiguousIndices(t, indices)
}

// A round carrying a tool call still needs its blob: buildHistory replays it as
// ReasoningContent for the in-flight round (the GPT 5.6 drain path).
func TestSSEWriter_TrailingRedactedWithToolUse_Kept(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "gpt-5.6-sol", 272000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: "toolUseEvent", ToolStop: true, ToolUseID: "toolu_1", ToolName: "read", ToolInput: `{"path":"/tmp"}`})
	sw.HandleEvent(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: "blob-replayable"})
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, "blob-replayable") {
		t.Fatalf("a tool round's blob must be kept for replay: %s", body)
	}
	types, indices := sseBlockOrder(t, body)
	if len(types) != 2 || types[0] != "tool_use" || types[1] != "redacted_thinking" {
		t.Fatalf("blocks = %v, want [tool_use redacted_thinking]", types)
	}
	assertContiguousIndices(t, indices)
}

// A reasoning-only round has nothing else to show, so the blob is still
// emitted rather than leaving the client with empty content. The retryable
// variant of this shape is caught by IsEmptyVisibleEndTurn instead.
func TestSSEWriter_RedactedOnly_StillEmitted(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "auto", 1000000, nil, 0, 0)

	sw.HandleEvent(kiroproto.Event{Type: kiroproto.EventReasoningContent, RedactedContent: "blob-only"})
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if body := w.Body.String(); !strings.Contains(body, "blob-only") {
		t.Fatalf("reasoning-only round must not send empty content: %s", body)
	}
	if !sw.IsEmptyVisibleEndTurn() {
		t.Fatal("reasoning-only round must still be flagged for retry")
	}
}
