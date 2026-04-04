package respconv

import (
	"encoding/json/v2"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/google/uuid"
)

// NonStreamingStats holds token usage and context info from a non-streaming response.
type NonStreamingStats struct {
	// Token usage.
	InputTokens  int
	OutputTokens int
	// Context usage from Kiro.
	HasContextUsage        bool
	ContextUsagePercentage float64
}

// NonStreamingAccumulator wraps responseAccumulator for incremental non-streaming processing.
type NonStreamingAccumulator struct {
	acc responseAccumulator
}

// NewNonStreamingAccumulator creates a new accumulator for non-streaming responses.
func NewNonStreamingAccumulator(contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int) *NonStreamingAccumulator {
	a := &NonStreamingAccumulator{}
	a.acc = newAccumulator(contextWindowSize, stopSequences, maxTokens, preCountedInputTokens)
	return a
}

// ProcessEvent processes a single event and returns the delta.
func (n *NonStreamingAccumulator) ProcessEvent(e kiroproto.Event) EventDelta {
	return n.acc.ProcessEvent(e)
}

// SetFilterToolName sets the tool name to filter from accumulator recording.
func (n *NonStreamingAccumulator) SetFilterToolName(name string) {
	n.acc.FilterToolName = name
}

// SetToolNameMap sets the short→original tool name map for response remapping.
func (n *NonStreamingAccumulator) SetToolNameMap(m map[string]string) {
	n.acc.toolNameMap = m
}

// BuildResponse builds the final Anthropic response from accumulated events.
func (n *NonStreamingAccumulator) BuildResponse(model string) (map[string]any, NonStreamingStats) {
	return buildResponseFromAcc(&n.acc, model)
}

// IsEmptyVisibleEndTurn reports whether the response had thinking but no visible text or tool use.
func (n *NonStreamingAccumulator) IsEmptyVisibleEndTurn() bool {
	return n.acc.IsEmptyVisibleEndTurn()
}

// ThinkingLen returns the length of accumulated thinking content.
func (n *NonStreamingAccumulator) ThinkingLen() int {
	return n.acc.ThinkingBuf.Len()
}

// BuildNonStreamingResponse builds a complete Anthropic response from buffered events.
func BuildNonStreamingResponse(events []kiroproto.Event, model string, contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int) (map[string]any, NonStreamingStats) {
	a := NewNonStreamingAccumulator(contextWindowSize, stopSequences, maxTokens, preCountedInputTokens)
	for _, e := range events {
		a.ProcessEvent(e)
	}
	return a.BuildResponse(model)
}

// buildResponseFromAcc builds the Anthropic response from a responseAccumulator.
func buildResponseFromAcc(acc *responseAccumulator, model string) (map[string]any, NonStreamingStats) {
	// 1. Finalize thinking tags and stop sequence buffers.
	acc.FinalizeStream()

	// Deduplicate tool calls.
	toolCalls := DeduplicateToolCalls(acc.ToolCalls)

	// Build content array: thinking → text → tool_use.
	content := []any{}
	if acc.ThinkingBuf.Len() > 0 {
		block := map[string]any{
			"type":     anthropic.BlockTypeThinking,
			"thinking": acc.ThinkingBuf.String(),
		}
		if acc.Signature != "" {
			block["signature"] = acc.Signature
		}
		content = append(content, block)
	}
	if acc.TextBuf.Len() > 0 {
		content = append(content, map[string]any{
			"type": anthropic.BlockTypeText,
			"text": acc.TextBuf.String(),
		})
	}
	// When ThinkingBuf has content but no text or tool use, we intentionally
	// skip injecting an empty text block. The caller detects this via
	// IsEmptyVisibleEndTurn and retries the request instead.
	for _, tc := range toolCalls {
		var input any
		if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
			input = map[string]any{}
		}
		content = append(content, map[string]any{
			"type":  anthropic.BlockTypeToolUse,
			"id":    tc.ID,
			"name":  tc.Name,
			"input": input,
		})
	}

	stopReason, stopSequence := acc.resolveStopReason()

	inputTokens, outputTokens := acc.resolvedUsage()
	usage := acc.UsageMap(inputTokens, outputTokens)

	stats := NonStreamingStats{
		InputTokens:            inputTokens,
		OutputTokens:           outputTokens,
		HasContextUsage:        acc.HasContextUsage,
		ContextUsagePercentage: acc.ContextUsagePercentage,
	}

	return map[string]any{
		"id":            "msg_" + uuid.New().String()[:24],
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   stopReason,
		"stop_sequence": stopSequence,
		"usage":         usage,
	}, stats
}
