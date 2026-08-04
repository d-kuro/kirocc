package respconv

import (
	"unicode/utf8"
)

// accumulateRedacted records one opaque reasoning block and applies the same
// output accounting used for non-truncatable tool input.
func (a *responseAccumulator) accumulateRedacted(data string, delta *EventDelta) {
	if data == "" {
		return
	}
	a.RedactedContents = append(a.RedactedContents, data)
	a.accumulateOpaqueOutput(data)
	delta.RedactedContent = data
	if a.LocalStop {
		delta.StopSignal = true
		delta.StopReason = a.StopReason
	}
}

// accumulateOpaqueOutput counts output that cannot be truncated without
// corrupting its wire representation, such as JSON tool input or base64
// reasoning content. The stop reason is first-wins: once a local stop is
// latched (e.g. stop_sequence), later budget overruns must not overwrite it.
func (a *responseAccumulator) accumulateOpaqueOutput(content string) {
	a.outputRuneCount += utf8.RuneCountInString(content)
	if !a.LocalStop && a.maxTokensBudget > 0 && a.outputRuneCount/4 >= a.maxTokensBudget {
		a.LocalStop = true
		a.StopReason = StopReasonMaxTokens
	}
}
