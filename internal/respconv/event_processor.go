package respconv

import (
	"encoding/json/jsontext"
	"unicode/utf8"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// ProcessEvent processes a single Kiro event and returns the delta for this event.
func (a *responseAccumulator) ProcessEvent(e kiroproto.Event) EventDelta {
	var d EventDelta

	switch e.Type {
	case kiroproto.EventAssistantResponse:
		delta := ComputeDelta(e.Content, a.lastContent)
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
		if a.suppressReasoningContent {
			return d
		}
		delta := ComputeDelta(e.ThinkingText, a.lastThinking)
		a.lastThinking = e.ThinkingText
		if delta != "" && !a.LocalStop {
			a.accumulateThinking(delta, &d)
		}

	case kiroproto.EventToolUse:
		a.processToolUseEvent(e, &d)

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
	// Skip recording filtered tools (e.g. internal ToolSearch).
	if a.DropToolName != "" && e.ToolName == a.DropToolName {
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
