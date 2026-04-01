package messages

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"

	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
)

var upstreamCaptureEnabled = func(ctx context.Context) bool {
	return slog.Default().Enabled(ctx, slog.LevelDebug)
}

// ConfigureCaptureForTesting overrides capture settings and returns a restore function.
func ConfigureCaptureForTesting(enabled func(context.Context) bool) func() {
	oldEnabled := upstreamCaptureEnabled
	if enabled != nil {
		upstreamCaptureEnabled = enabled
	}
	return func() {
		upstreamCaptureEnabled = oldEnabled
	}
}

type upstreamCapturedEvent struct {
	Seq                    int     `json:"seq"`
	Type                   string  `json:"type"`
	Content                string  `json:"content,omitempty"`
	ThinkingText           string  `json:"thinking_text,omitempty"`
	ToolName               string  `json:"tool_name,omitempty"`
	ToolUseID              string  `json:"tool_use_id,omitempty"`
	ToolInput              string  `json:"tool_input,omitempty"`
	ToolStop               bool    `json:"tool_stop,omitzero"`
	Credits                float64 `json:"credits,omitzero"`
	InputTokens            int     `json:"input_tokens,omitzero"`
	OutputTokens           int     `json:"output_tokens,omitzero"`
	UncachedInputTokens    int     `json:"uncached_input_tokens,omitzero"`
	CacheReadInputTokens   int     `json:"cache_read_input_tokens,omitzero"`
	CacheWriteInputTokens  int     `json:"cache_write_input_tokens,omitzero"`
	TotalTokens            int     `json:"total_tokens,omitzero"`
	Signature              string  `json:"signature,omitempty"`
	RedactedContent        string  `json:"redacted_content,omitempty"`
	ContextUsagePercentage float64 `json:"context_usage_percentage,omitzero"`
	ConversationID         string  `json:"conversation_id,omitempty"`
	UtteranceID            string  `json:"utterance_id,omitempty"`
	InvalidStateReason     string  `json:"invalid_state_reason,omitempty"`
	ErrorMessage           string  `json:"error_message,omitempty"`
}

type upstreamAttemptCapture struct {
	traceID             string
	attempt             int
	model               string
	thinking            bool
	stream              bool
	conversationID      string
	agentContinuationID string
	currentContentLen   int
	toolCount           int
	toolResultCount     int
	requestBody         []byte
	responseHeader      http.Header
	events              []upstreamCapturedEvent

	assistantResponseEvents int
	reasoningEvents         int
	toolUseEventCount       int
	toolUseNames            map[string]int
	hasVisibleText          bool
	hasRealToolUse          bool
}

func newUpstreamAttemptCapture(ctx context.Context, payload *kiroproto.Payload, model string, thinking, stream bool, attempt int) *upstreamAttemptCapture {
	if !upstreamCaptureEnabled(ctx) {
		return nil
	}

	traceID := logging.TraceIDFromContext(ctx)

	body, err := json.Marshal(payload)
	if err != nil {
		slog.WarnContext(ctx, "upstream capture request marshal failed",
			"trace_id", logging.ShortID(traceID),
			"attempt", attempt,
			"err", err,
		)
	}

	current := payload.ConversationState.CurrentMessage.UserInputMessage
	agentContinuationID := payload.ConversationState.AgentContinuationID
	currentContentLen := len(current.Content)
	var toolCount int
	var toolResultCount int
	if current.UserInputMessageContext != nil {
		toolCount = len(current.UserInputMessageContext.Tools)
		toolResultCount = len(current.UserInputMessageContext.ToolResults)
	}
	return &upstreamAttemptCapture{
		traceID:             traceID,
		attempt:             attempt,
		model:               model,
		thinking:            thinking,
		stream:              stream,
		conversationID:      payload.ConversationState.ConversationID,
		agentContinuationID: agentContinuationID,
		currentContentLen:   currentContentLen,
		toolCount:           toolCount,
		toolResultCount:     toolResultCount,
		requestBody:         body,
		toolUseNames:        make(map[string]int),
	}
}

func (c *upstreamAttemptCapture) shortTraceID() string {
	return logging.ShortID(c.traceID)
}

func (c *upstreamAttemptCapture) setResponseHeaders(h http.Header) {
	if c == nil {
		return
	}
	if h == nil {
		c.responseHeader = http.Header{}
		return
	}
	c.responseHeader = h.Clone()
}

func (c *upstreamAttemptCapture) recordEvent(e kiroproto.Event) {
	if c == nil {
		return
	}

	evt := upstreamCapturedEvent{
		Seq:                    len(c.events) + 1,
		Type:                   e.Type,
		Content:                e.Content,
		ThinkingText:           e.ThinkingText,
		ToolName:               e.ToolName,
		ToolUseID:              e.ToolUseID,
		ToolInput:              e.ToolInput,
		ToolStop:               e.ToolStop,
		Credits:                e.Credits,
		InputTokens:            e.InputTokens,
		OutputTokens:           e.OutputTokens,
		UncachedInputTokens:    e.UncachedInputTokens,
		CacheReadInputTokens:   e.CacheReadInputTokens,
		CacheWriteInputTokens:  e.CacheWriteInputTokens,
		TotalTokens:            e.TotalTokens,
		Signature:              e.Signature,
		RedactedContent:        e.RedactedContent,
		ContextUsagePercentage: e.ContextUsagePercentage,
		ConversationID:         e.ConversationID,
		UtteranceID:            e.UtteranceID,
		InvalidStateReason:     e.InvalidStateReason,
		ErrorMessage:           e.ErrorMessage,
	}
	c.events = append(c.events, evt)

	switch e.Type {
	case kiroproto.EventAssistantResponse:
		c.assistantResponseEvents++
		if e.Content != "" {
			c.hasVisibleText = true
		}
	case kiroproto.EventReasoningContent:
		c.reasoningEvents++
	case kiroproto.EventToolUse:
		c.toolUseEventCount++
		if e.ToolName != "" {
			c.toolUseNames[e.ToolName]++
			if e.ToolName != kiroproto.ThinkingToolName {
				c.hasRealToolUse = true
			}
		}
	}
}

func (c *upstreamAttemptCapture) toolUseNameList() []string {
	if c == nil || len(c.toolUseNames) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(c.toolUseNames))
}

func (c *upstreamAttemptCapture) logAttrs() []any {
	if c == nil {
		return nil
	}
	return []any{
		"assistant_response_events", c.assistantResponseEvents,
		"agent_continuation_id", c.agentContinuationID,
		"current_content_len", c.currentContentLen,
		"tool_count", c.toolCount,
		"tool_result_count", c.toolResultCount,
		"reasoning_events", c.reasoningEvents,
		"tool_use_event_count", c.toolUseEventCount,
		"tool_use_names", c.toolUseNameList(),
		"has_visible_text", c.hasVisibleText,
		"has_real_tool_use", c.hasRealToolUse,
	}
}

func (c *upstreamAttemptCapture) logCapture(ctx context.Context, reason string) {
	if c == nil {
		return
	}
	args := []any{
		"trace_id", c.shortTraceID(),
		"attempt", c.attempt,
		"reason", reason,
		"model", c.model,
		"thinking", c.thinking,
		"stream", c.stream,
		"conversation_id", c.conversationID,
	}
	args = append(args, c.logAttrs()...)
	args = append(args,
		"request_body", jsontext.Value(c.requestBody),
		"response_headers", marshalRaw(c.responseHeader),
		"events", marshalRaw(c.events),
	)
	slog.WarnContext(ctx, "upstream failure capture", args...)
}

func marshalRaw(v any) jsontext.Value {
	b, err := json.Marshal(v)
	if err != nil {
		return jsontext.Value(fmt.Sprintf("%q", fmt.Sprint(v)))
	}
	return b
}
