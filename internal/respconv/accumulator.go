package respconv

import (
	"strings"
)

// Adapter-side stop reason constants.
const (
	StopReasonStopSequence = "stop_sequence"
	StopReasonMaxTokens    = "max_tokens"
	StopReasonEndTurn      = "end_turn"
	StopReasonToolUse      = "tool_use"
)

// EventDelta holds the per-event delta information produced by responseAccumulator.
type EventDelta struct {
	TextDelta       string
	ThinkingDelta   string
	RedactedContent string
	ToolStop        bool
	ToolUseID       string
	ToolName        string
	ToolInput       string
	IsError         bool
	ErrorMessage    string
	// Stop signal fields — set when adapter-side stop is triggered.
	StopSignal   bool
	StopReason   string // StopReasonStopSequence or StopReasonMaxTokens
	StopSequence string // matched stop sequence (only for stop_sequence)
}

// responseAccumulator tracks shared state across streaming and non-streaming response processing.
type responseAccumulator struct {
	// Cumulative text trackers for ComputeDelta.
	lastContent  string
	lastThinking string
	// Accumulated full text (for non-streaming).
	TextBuf     strings.Builder
	ThinkingBuf strings.Builder
	// Tool calls.
	ToolCalls      []ToolCall
	HasToolUse     bool
	ToolParseError bool
	// Token usage.
	HasMetadata           bool
	InputTokens           int
	OutputTokens          int
	CacheReadInputTokens  int
	CacheWriteInputTokens int
	// Context usage from contextUsageEvent.
	HasContextUsage        bool
	ContextUsagePercentage float64
	ContextWindowSize      int // set externally by SSEWriter / BuildNonStreamingResponse
	// Credit usage from meteringEvent.
	HasCredits bool
	Credits    float64
	// Signature from reasoningContentEvent.
	Signature string
	// Conversation metadata.
	ConversationID string
	// Text presence.
	HasText bool
	// Adapter-side stop tracking.
	LocalStop    bool
	StopReason   string // StopReasonStopSequence or StopReasonMaxTokens
	StopSequence string // matched stop sequence value
	// Stop sequence detection.
	stopSequences  []string
	stopSeqMaxKeep int    // precomputed max(len(s)-1) across stop sequences
	stopSeqPending string // trailing buffer for cross-chunk boundary matching
	// Pre-counted input tokens from tiktoken (set before streaming starts).
	PreCountedInputTokens int
	// Max tokens budget enforcement.
	maxTokensBudget int // 0 = no enforcement
	outputRuneCount int // cumulative rune count across all output content
	// Thinking tag parser state.
	thinkingTagInside        bool   // currently inside <thinking> tags
	thinkingTagBuf           string // buffer for partial tag matching across chunk boundaries
	suppressReasoningContent bool   // true if <thinking> tags were detected (guards against double-counting with reasoningContentEvent)
	// DropToolName, when set, causes ProcessEvent to skip recording tool_use
	// events with this name in HasToolUse/ToolCalls (used by tool search orchestrator).
	DropToolName string
	// toolNameMap maps shortened tool names back to originals (short→original).
	// When set, tool names from Kiro responses are restored before emitting to the client.
	toolNameMap map[string]string
}

// newAccumulator creates a responseAccumulator with common initialization.
func newAccumulator(contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int) responseAccumulator {
	acc := responseAccumulator{
		ContextWindowSize:     contextWindowSize,
		maxTokensBudget:       maxTokens,
		PreCountedInputTokens: preCountedInputTokens,
	}
	acc.initStopSequences(stopSequences)
	return acc
}

// IsEmptyVisibleEndTurn reports whether the response completed with thinking
// content but no visible text and no tool use. This indicates the upstream model
// produced only a thinking block and the client would see an empty response.
func (a *responseAccumulator) IsEmptyVisibleEndTurn() bool {
	if a.LocalStop {
		return false // stop_sequence/max_tokens are not empty-response cases
	}
	return a.ThinkingBuf.Len() > 0 && a.TextBuf.Len() == 0 && !a.HasToolUse
}

// accumulateThinking applies max_tokens budget to thinking content and writes to ThinkingBuf.
// Sets ThinkingDelta and StopSignal on the delta as appropriate.
func (a *responseAccumulator) accumulateThinking(thought string, d *EventDelta) {
	thought = a.applyMaxTokensBudget(thought)
	if thought != "" {
		a.ThinkingBuf.WriteString(thought)
		d.ThinkingDelta = thought
	}
	if a.LocalStop {
		d.StopSignal = true
		d.StopReason = a.StopReason
	}
}

// FinalizeStream flushes thinking tags and stop sequence buffers, routing any
// remaining content to the appropriate accumulator buffers. Returns the text
// and thinking deltas to emit. This consolidates the finalize logic shared by
// streaming and non-streaming paths.
func (a *responseAccumulator) FinalizeStream() (textDelta, thinkingDelta string) {
	// 1. Finalize thinking tags.
	if textOut, thinkingOut := a.finalizeThinkingTags(); textOut != "" || thinkingOut != "" {
		if thinkingOut != "" {
			d := EventDelta{}
			a.accumulateThinking(thinkingOut, &d)
			thinkingDelta = d.ThinkingDelta
		}
		if textOut != "" {
			a.HasText = true
			if len(a.stopSequences) > 0 {
				textOut = a.applyStopSequenceFilter(textOut)
			}
			if textOut != "" && !a.LocalStop {
				textOut = a.applyMaxTokensBudget(textOut)
			}
			if textOut != "" {
				a.TextBuf.WriteString(textOut)
				textDelta = textOut
			}
		}
	}

	// 2. Flush stop sequence pending buffer.
	if remaining := a.flushStopSeqPending(); remaining != "" && !a.LocalStop {
		remaining = a.applyMaxTokensBudget(remaining)
		if remaining != "" {
			a.TextBuf.WriteString(remaining)
			textDelta += remaining
		}
	}

	return textDelta, thinkingDelta
}
