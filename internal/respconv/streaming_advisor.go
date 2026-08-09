package respconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
)

// AdvisorIteration is one advisor consultation's usage entry, reported in
// usage.iterations[] alongside the executor's top-level counts.
type AdvisorIteration struct {
	Model        string
	InputTokens  int
	OutputTokens int
}

// IterationMaps renders advisor iterations as Anthropic usage.iterations[]
// entries. Returns nil when there were no consultations so the key is omitted.
func IterationMaps(iters []AdvisorIteration) []map[string]any {
	if len(iters) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(iters))
	for _, it := range iters {
		out = append(out, map[string]any{
			"type":          "advisor_message",
			"model":         it.Model,
			"input_tokens":  it.InputTokens,
			"output_tokens": it.OutputTokens,
		})
	}
	return out
}

// AdvisorResultBlock builds an advisor_tool_result content block carrying the
// advisor's guidance as a plaintext advisor_result. The proxy sees the advisor
// response in the clear, so it never emits advisor_redacted_result — forging
// encrypted content the real API could not decrypt would break clients that
// round-trip it.
func AdvisorResultBlock(toolUseID, text, stopReason string) map[string]any {
	return map[string]any{
		"type":        anthropic.BlockTypeAdvisorToolResult,
		"tool_use_id": toolUseID,
		"content": map[string]any{
			"type":        anthropic.BlockTypeAdvisorResult,
			"text":        text,
			"stop_reason": stopReason,
		},
	}
}

// AdvisorErrorBlock builds an advisor_tool_result content block carrying an
// advisor_tool_result_error. Advisor failures surface as tool results, never
// as HTTP errors — the executor is expected to continue without the advice.
func AdvisorErrorBlock(toolUseID, errorCode string) map[string]any {
	return map[string]any{
		"type":        anthropic.BlockTypeAdvisorToolResult,
		"tool_use_id": toolUseID,
		"content": map[string]any{
			"type":       anthropic.BlockTypeAdvisorResultError,
			"error_code": errorCode,
		},
	}
}

// WriteAdvisorResult writes an advisor_tool_result content block carrying the
// advisor's guidance.
func (s *SSEWriter) WriteAdvisorResult(toolUseID, text, stopReason string) {
	s.writeBlock(AdvisorResultBlock(toolUseID, text, stopReason), nil)
}

// WriteAdvisorError writes an advisor_tool_result content block carrying an
// advisor error code.
func (s *SSEWriter) WriteAdvisorError(toolUseID, errorCode string) {
	s.writeBlock(AdvisorErrorBlock(toolUseID, errorCode), nil)
}

// AddAdvisorIteration records one advisor consultation's usage for the final
// message_delta. Stored on the writer, not the accumulator, so it survives
// per-round ResetAccumulator calls.
func (s *SSEWriter) AddAdvisorIteration(model string, inputTokens, outputTokens int) {
	s.advisorIterations = append(s.advisorIterations, AdvisorIteration{
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	})
}
