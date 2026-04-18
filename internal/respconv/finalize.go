package respconv

// finalResult bundles the Anthropic-compatible stop reason and usage payload
// derived from an accumulator. Both streaming and non-streaming paths emit
// identical values here; callers just pack them into different wire formats.
type finalResult struct {
	StopReason   string
	StopSequence any
	InputTokens  int
	OutputTokens int
	Usage        map[string]any
}

// finalizeResult consolidates the FinalizeStream → resolveStopReason →
// resolvedUsage → UsageMap pipeline shared by streaming.Finish and
// nonstreaming.buildResponseFromAcc. FinalizeStream mutates the accumulator,
// so this must be called exactly once per stream.
func finalizeResult(a *responseAccumulator) (textDelta, thinkingDelta string, r finalResult) {
	textDelta, thinkingDelta = a.FinalizeStream()
	r.StopReason, r.StopSequence = a.resolveStopReason()
	r.InputTokens, r.OutputTokens = a.resolvedUsage()
	r.Usage = a.UsageMap(r.InputTokens, r.OutputTokens)
	return
}
