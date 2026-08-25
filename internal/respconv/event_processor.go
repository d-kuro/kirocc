package respconv

import (
	"encoding/json/jsontext"
	"math"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// ProcessEvent processes a single Kiro event and returns the delta for this event.
func (a *responseAccumulator) ProcessEvent(e kiroproto.Event) EventDelta {
	var d EventDelta

	switch e.Type {
	case kiroproto.EventAssistantResponse:
		if e.Content != "" && !a.LocalStop {
			textOut, thinkingOut := a.parseThinkingTags(e.Content)
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
			a.accumulateRedacted(e.RedactedContent, &d)
			return d
		}
		// Guard against double-counting: if thinking tags were already parsed
		// from assistantResponseEvent, skip reasoningContentEvent thinking.
		if a.suppressReasoningContent {
			return d
		}
		if e.ThinkingText != "" && !a.LocalStop {
			a.accumulateThinking(e.ThinkingText, &d)
		}

	case kiroproto.EventToolUse:
		a.processToolUseEvent(e, &d)

	case kiroproto.EventMetadata:
		// Kiro may emit metadataEvent without tokenUsage. Treat an all-zero
		// event as missing usage rather than authoritative data; otherwise it
		// would erase metering counts and suppress the pre-count/context fallback.
		if hasUsableTokenCounts(e.InputTokens, e.OutputTokens) {
			a.HasMetadata = true
			a.InputTokens = max(0, e.InputTokens)
			a.OutputTokens = max(0, e.OutputTokens)
			a.CacheReadInputTokens = max(0, e.CacheReadInputTokens)
			a.CacheWriteInputTokens = max(0, e.CacheWriteInputTokens)
		} else {
			// Cache creation may be reported independently of the primary counts.
			// Preserve it without marking the metadata as usable token usage.
			a.CacheReadInputTokens = max(a.CacheReadInputTokens, e.CacheReadInputTokens)
			a.CacheWriteInputTokens = max(a.CacheWriteInputTokens, e.CacheWriteInputTokens)
		}

	case kiroproto.EventMetering:
		// Reject NaN/Inf/negative so a malformed upstream payload never
		// poisons the cumulative log/span values; downstream callers see
		// HasCredits=false, the same as "no meteringEvent received".
		if !math.IsNaN(e.Credits) && !math.IsInf(e.Credits, 0) && e.Credits >= 0 {
			a.HasCredits = true
			a.Credits = e.Credits
		}
		if !a.HasMetadata && hasUsableTokenCounts(e.InputTokens, e.OutputTokens) {
			a.InputTokens = max(0, e.InputTokens)
			a.OutputTokens = max(0, e.OutputTokens)
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

// processToolUseEvent handles kiroproto.EventToolUse, recording or filtering the tool call
// and updating the output budget.
func (a *responseAccumulator) processToolUseEvent(e kiroproto.Event, d *EventDelta) {
	if !e.ToolStop || a.LocalStop {
		return
	}
	// Restore original tool name if shortened.
	toolName := e.ToolName
	if mapped, ok := a.toolNameMap[toolName]; ok {
		toolName = mapped
	}
	// Skip recording filtered tools (e.g. internal ToolSearch / advisor).
	if _, drop := a.dropToolNames[e.ToolName]; drop {
		d.ToolStop = true
		d.ToolUseID = e.ToolUseID
		d.ToolName = toolName
		d.ToolInput = e.ToolInput
		return
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
	a.accumulateOpaqueOutput(e.ToolInput)
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
