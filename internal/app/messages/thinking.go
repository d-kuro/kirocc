package messages

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/logging"
)

// formatContextWindow renders a context window size as a human label like
// "200k" or "1M" for log output.
func formatContextWindow(size int) string {
	if size >= 1_000_000 {
		return fmt.Sprintf("%dM", size/1_000_000)
	}
	return fmt.Sprintf("%dk", size/1000)
}

// resolveThinkingBudget returns the explicit budget_tokens on the request, or
// derives one from the effort level. Unknown effort levels warn and fall back
// to medium. Returns 0 only when the request has no thinking config.
func resolveThinkingBudget(ctx context.Context, req *anthropic.Request) int {
	if req.Thinking == nil {
		return 0
	}
	if req.Thinking.BudgetTokens > 0 {
		return req.Thinking.BudgetTokens
	}
	effort := req.Effort()
	switch effort {
	case anthropic.EffortMax:
		return anthropic.ThinkingBudgetMax
	case anthropic.EffortXHigh:
		return anthropic.ThinkingBudgetXHigh
	case anthropic.EffortHigh:
		return anthropic.ThinkingBudgetHigh
	case anthropic.EffortLow:
		return anthropic.ThinkingBudgetLow
	case anthropic.EffortMedium, "":
		return anthropic.ThinkingBudgetMedium
	default:
		_, short := logging.TraceIDs(ctx)
		slog.WarnContext(ctx, "unknown effort level, falling back to medium",
			"trace_id", short, "effort", effort)
		return anthropic.ThinkingBudgetMedium
	}
}
