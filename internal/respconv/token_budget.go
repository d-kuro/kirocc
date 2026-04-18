package respconv

import "unicode/utf8"

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
