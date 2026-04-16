package respconv

import (
	"encoding/json/jsontext"
	"strings"
	"unicode/utf8"

	"github.com/d-kuro/kirocc/internal/kiroproto"
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
	// Cumulative text trackers for NormalizeChunk.
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
	thinkingTagInside bool   // currently inside <thinking> tags
	thinkingTagBuf    string // buffer for partial tag matching across chunk boundaries
	thinkingTagUsed   bool   // true if <thinking> tags were detected (guards against double-counting with reasoningContentEvent)
	// FilterToolName, when set, causes ProcessEvent to skip recording tool_use
	// events with this name in HasToolUse/ToolCalls (used by tool search orchestrator).
	FilterToolName string
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

// ProcessEvent processes a single Kiro event and returns the delta for this event.
func (a *responseAccumulator) ProcessEvent(e kiroproto.Event) EventDelta {
	var d EventDelta

	switch e.Type {
	case kiroproto.EventAssistantResponse:
		delta := NormalizeChunk(e.Content, a.lastContent)
		a.lastContent = e.Content
		if delta != "" && !a.LocalStop {
			textOut, thinkingOut := a.parseThinkingTags(delta)
			if thinkingOut != "" {
				a.accumulateThinking(thinkingOut, &d)
			}
			if textOut != "" {
				a.HasText = true
				// Apply stop sequence detection.
				if len(a.stopSequences) > 0 {
					textOut = a.applyStopSequenceFilter(textOut)
				}
				// Apply max_tokens budget enforcement.
				if textOut != "" && !a.LocalStop {
					textOut = a.applyMaxTokensBudget(textOut)
				}
				if textOut != "" {
					a.TextBuf.WriteString(textOut)
					d.TextDelta = textOut
				}
			}
			if a.LocalStop {
				d.StopSignal = true
				d.StopReason = a.StopReason
				d.StopSequence = a.StopSequence
			}
		}

	case kiroproto.EventReasoningContent:
		if e.Signature != "" {
			a.Signature = e.Signature
		}
		if e.RedactedContent != "" {
			d.RedactedContent = e.RedactedContent
			return d
		}
		// Guard against double-counting: if thinking tags were already parsed
		// from assistantResponseEvent, skip reasoningContentEvent thinking.
		if a.thinkingTagUsed {
			return d
		}
		delta := NormalizeChunk(e.ThinkingText, a.lastThinking)
		a.lastThinking = e.ThinkingText
		if delta != "" && !a.LocalStop {
			a.accumulateThinking(delta, &d)
		}

	case kiroproto.EventToolUse:
		if e.ToolStop && !a.LocalStop {
			// Restore original tool name if shortened.
			toolName := e.ToolName
			if mapped, ok := a.toolNameMap[toolName]; ok {
				toolName = mapped
			}
			// Skip recording filtered tools (e.g. internal ToolSearch).
			if a.FilterToolName != "" && e.ToolName == a.FilterToolName {
				d.ToolStop = true
				d.ToolUseID = e.ToolUseID
				d.ToolName = toolName
				d.ToolInput = e.ToolInput
				return d
			}
			a.HasToolUse = true
			tc := ToolCall{
				ID:    e.ToolUseID,
				Name:  toolName,
				Input: e.ToolInput,
			}
			if !jsontext.Value(tc.Input).IsValid() {
				a.ToolParseError = true
			}
			// Count tool input runes toward budget and enforce max_tokens.
			// Unlike text/thinking, tool input JSON cannot be truncated mid-stream
			// (would produce invalid JSON), so we check the budget inline instead
			// of using applyMaxTokensBudget which truncates at a rune boundary.
			toolRunes := utf8.RuneCountInString(e.ToolInput)
			a.outputRuneCount += toolRunes
			if a.maxTokensBudget > 0 && a.outputRuneCount/4 >= a.maxTokensBudget {
				a.LocalStop = true
				a.StopReason = StopReasonMaxTokens
			}
			a.ToolCalls = append(a.ToolCalls, tc)
			d.ToolStop = true
			d.ToolUseID = e.ToolUseID
			d.ToolName = toolName
			d.ToolInput = e.ToolInput
			if a.LocalStop {
				d.StopSignal = true
				d.StopReason = a.StopReason
			}
		}

	case kiroproto.EventMetadata:
		a.HasMetadata = true
		a.InputTokens = e.InputTokens
		a.OutputTokens = e.OutputTokens
		a.CacheReadInputTokens = e.CacheReadInputTokens
		a.CacheWriteInputTokens = e.CacheWriteInputTokens

	case kiroproto.EventMetering:
		if !a.HasMetadata {
			a.InputTokens = e.InputTokens
			a.OutputTokens = e.OutputTokens
		}

	case kiroproto.EventMessageMetadata:
		a.ConversationID = e.ConversationID

	case kiroproto.EventContextUsage:
		a.HasContextUsage = true
		a.ContextUsagePercentage = e.ContextUsagePercentage

	case kiroproto.EventInvalidState, kiroproto.EventException:
		d.IsError = true
		d.ErrorMessage = e.ErrorText()
	}

	return d
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

// estimatedOutputTokens returns an approximate output token count from accumulated text.
// Uses the incrementally tracked rune count with a heuristic of ~4 runes per token.
// Returns at least 1 for non-empty output to avoid reporting 0 tokens for short responses.
func (a *responseAccumulator) estimatedOutputTokens() int {
	if a.outputRuneCount == 0 {
		return 0
	}
	return max(1, a.outputRuneCount/4)
}

// resolvedUsage returns the best available input and output token counts.
// Priority: metadata/metering > pre-counted (tiktoken) > contextUsage estimate > 0.
func (a *responseAccumulator) resolvedUsage() (inputTokens, outputTokens int) {
	if a.HasMetadata || a.InputTokens > 0 || a.OutputTokens > 0 {
		return a.InputTokens, a.OutputTokens
	}
	if a.PreCountedInputTokens > 0 {
		return a.PreCountedInputTokens, a.estimatedOutputTokens()
	}
	if a.HasContextUsage && a.ContextWindowSize > 0 {
		pct := max(0, min(100, a.ContextUsagePercentage))
		estOutput := a.estimatedOutputTokens()
		total := int(pct / 100 * float64(a.ContextWindowSize))
		estInput := max(0, total-estOutput)
		return estInput, estOutput
	}
	return 0, 0
}

// UsageMap builds an Anthropic-compatible usage map from the given token counts.
func (a *responseAccumulator) UsageMap(inputTokens, outputTokens int) map[string]any {
	return map[string]any{
		"input_tokens":                inputTokens,
		"output_tokens":               outputTokens,
		"cache_read_input_tokens":     a.CacheReadInputTokens,
		"cache_creation_input_tokens": a.CacheWriteInputTokens,
	}
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

// applyStopSequenceFilter checks for stop sequences in the pending buffer + new delta.
// Returns the text to emit. If a stop sequence is found, sets LocalStop and truncates.
func (a *responseAccumulator) applyStopSequenceFilter(delta string) string {
	a.stopSeqPending += delta

	// Check for full match.
	for _, s := range a.stopSequences {
		if idx := strings.Index(a.stopSeqPending, s); idx >= 0 {
			emit := a.stopSeqPending[:idx]
			a.stopSeqPending = ""
			a.LocalStop = true
			a.StopReason = StopReasonStopSequence
			a.StopSequence = s
			return emit
		}
	}

	// Keep a trailing suffix that could be a partial match prefix.
	// Use rune-aware splitting to avoid cutting multi-byte UTF-8 characters.
	runeCount := utf8.RuneCountInString(a.stopSeqPending)
	if runeCount <= a.stopSeqMaxKeep {
		return ""
	}
	// Find the byte offset after (runeCount - stopSeqMaxKeep) runes.
	splitAt := 0
	for range runeCount - a.stopSeqMaxKeep {
		_, size := utf8.DecodeRuneInString(a.stopSeqPending[splitAt:])
		splitAt += size
	}
	emit := a.stopSeqPending[:splitAt]
	a.stopSeqPending = a.stopSeqPending[splitAt:]
	return emit
}

// flushStopSeqPending returns any remaining text in the stop sequence pending buffer.
// Called when the stream ends without a stop sequence match.
func (a *responseAccumulator) flushStopSeqPending() string {
	out := a.stopSeqPending
	a.stopSeqPending = ""
	return out
}

// resolveStopReason returns the stop_reason and stop_sequence for the Anthropic response.
func (a *responseAccumulator) resolveStopReason() (stopReason string, stopSequence any) {
	stopReason = StopReasonEndTurn
	if a.StopReason != "" {
		stopReason = a.StopReason
		if a.StopReason == StopReasonStopSequence {
			return stopReason, a.StopSequence
		}
		return stopReason, nil
	}
	if a.HasToolUse {
		return StopReasonToolUse, nil
	}
	return stopReason, nil
}

// initStopSequences sets the stop sequences and precomputes maxKeep (in runes).
// Empty strings are filtered out since strings.Index(x, "") == 0 is always true.
func (a *responseAccumulator) initStopSequences(seqs []string) {
	a.stopSequences = nil
	a.stopSeqMaxKeep = 0
	for _, s := range seqs {
		if s == "" {
			continue
		}
		a.stopSequences = append(a.stopSequences, s)
		runeLen := utf8.RuneCountInString(s)
		if runeLen-1 > a.stopSeqMaxKeep {
			a.stopSeqMaxKeep = runeLen - 1
		}
	}
}

// applyMaxTokensBudget checks whether adding delta would exceed the max_tokens budget.
// Uses cumulative rune count / 4 as a token estimate. Returns the (possibly truncated) delta.
// If the budget is exceeded, sets LocalStop and StopReason.
func (a *responseAccumulator) applyMaxTokensBudget(delta string) string {
	if a.maxTokensBudget <= 0 {
		a.outputRuneCount += utf8.RuneCountInString(delta)
		return delta
	}
	runesInDelta := utf8.RuneCountInString(delta)
	newTotal := a.outputRuneCount + runesInDelta
	if newTotal/4 < a.maxTokensBudget {
		a.outputRuneCount = newTotal
		return delta
	}
	// Budget exceeded — find the cutoff point.
	// Allow up to (maxTokensBudget * 4 - outputRuneCount) more runes.
	remaining := a.maxTokensBudget*4 - a.outputRuneCount
	if remaining <= 0 {
		a.LocalStop = true
		a.StopReason = StopReasonMaxTokens
		return ""
	}
	// Truncate delta to remaining runes.
	count := 0
	for i := range delta {
		count++
		if count > remaining {
			a.outputRuneCount += remaining
			a.LocalStop = true
			a.StopReason = StopReasonMaxTokens
			return delta[:i]
		}
	}
	// All runes fit (edge case: exactly at boundary).
	a.outputRuneCount += runesInDelta
	if a.outputRuneCount/4 >= a.maxTokensBudget {
		a.LocalStop = true
		a.StopReason = StopReasonMaxTokens
	}
	return delta
}

// thinkingOpenTag and thinkingCloseTag are the XML tags used for prompt-injected thinking.
const (
	thinkingOpenTag  = "<thinking>"
	thinkingCloseTag = "</thinking>"
)

// parseThinkingTags parses <thinking>...</thinking> tags from streaming text.
// Returns (textOut, thinkingOut) — the portions to route to text and thinking buffers.
// Handles tags split across chunk boundaries via thinkingTagBuf.
func (a *responseAccumulator) parseThinkingTags(delta string) (textOut, thinkingOut string) {
	// Fast path: no buffered partial tag, not inside thinking, and no tag in delta.
	if a.thinkingTagBuf == "" && !a.thinkingTagInside && !strings.Contains(delta, "<") {
		return delta, ""
	}

	a.thinkingTagBuf += delta

	var textBuilder, thinkBuilder strings.Builder

	for len(a.thinkingTagBuf) > 0 {
		if a.thinkingTagInside {
			// Look for </thinking> close tag.
			idx := strings.Index(a.thinkingTagBuf, thinkingCloseTag)
			if idx >= 0 {
				// Found close tag — emit thinking content before it.
				thinkBuilder.WriteString(a.thinkingTagBuf[:idx])
				a.thinkingTagBuf = a.thinkingTagBuf[idx+len(thinkingCloseTag):]
				a.thinkingTagInside = false
				continue
			}
			// No close tag found. Check if the tail could be a partial </thinking>.
			keep := partialTagSuffix(a.thinkingTagBuf, thinkingCloseTag)
			if keep > 0 {
				// Emit everything except the potential partial match.
				thinkBuilder.WriteString(a.thinkingTagBuf[:len(a.thinkingTagBuf)-keep])
				a.thinkingTagBuf = a.thinkingTagBuf[len(a.thinkingTagBuf)-keep:]
			} else {
				// No partial match — emit all as thinking.
				thinkBuilder.WriteString(a.thinkingTagBuf)
				a.thinkingTagBuf = ""
			}
			break
		}

		// Outside thinking — look for <thinking> open tag.
		idx := strings.Index(a.thinkingTagBuf, thinkingOpenTag)
		if idx >= 0 {
			// Found open tag — emit text before it.
			textBuilder.WriteString(a.thinkingTagBuf[:idx])
			a.thinkingTagBuf = a.thinkingTagBuf[idx+len(thinkingOpenTag):]
			a.thinkingTagInside = true
			a.thinkingTagUsed = true
			continue
		}
		// No open tag found. Check if the tail could be a partial <thinking>.
		keep := partialTagSuffix(a.thinkingTagBuf, thinkingOpenTag)
		if keep > 0 {
			textBuilder.WriteString(a.thinkingTagBuf[:len(a.thinkingTagBuf)-keep])
			a.thinkingTagBuf = a.thinkingTagBuf[len(a.thinkingTagBuf)-keep:]
		} else {
			textBuilder.WriteString(a.thinkingTagBuf)
			a.thinkingTagBuf = ""
		}
		break
	}

	return textBuilder.String(), thinkBuilder.String()
}

// finalizeThinkingTags flushes any remaining content in the thinking tag buffer.
// Must be called before flushStopSeqPending at stream end.
func (a *responseAccumulator) finalizeThinkingTags() (textOut, thinkingOut string) {
	if a.thinkingTagBuf == "" {
		return "", ""
	}
	remaining := a.thinkingTagBuf
	a.thinkingTagBuf = ""
	if a.thinkingTagInside {
		return "", remaining
	}
	return remaining, ""
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

// partialTagSuffix returns the length of the longest suffix of s that is a prefix of tag.
// Returns 0 if no suffix of s matches a prefix of tag.
func partialTagSuffix(s, tag string) int {
	maxLen := min(len(tag)-1, len(s))
	for n := maxLen; n > 0; n-- {
		if s[len(s)-n:] == tag[:n] {
			return n
		}
	}
	return 0
}
