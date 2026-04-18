package respconv

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
