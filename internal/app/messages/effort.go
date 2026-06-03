package messages

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/models"
)

// formatContextWindow renders a context window size as a human label like
// "200k" or "1M" for log output.
func formatContextWindow(size int) string {
	if size >= 1_000_000 {
		return fmt.Sprintf("%dM", size/1_000_000)
	}
	return fmt.Sprintf("%dk", size/1000)
}

// defaultThinkingEffort is the native effort level sent when a request enables
// reasoning (thinking.type, the [1m] suffix, or the context-1m header) without
// an explicit output_config.effort. kiro-cli 2.5.1 expresses all reasoning depth
// through effort, so this carries the "thinking on" intent to the backend. It
// matches the medium tier the old thinking-XML path defaulted to.
const defaultThinkingEffort = models.EffortMedium

// resolveEffort maps the request's reasoning intent to a native effort level the
// resolved Kiro model accepts. Precedence:
//
//  1. An explicit, recognized output_config.effort wins (validated/clamped to
//     the model's enum; xhigh on a 4-value model clamps to max).
//  2. Otherwise, if reasoning is enabled via thinking/[1m]/context-1m, fall back
//     to defaultThinkingEffort so the intent still reaches the backend natively.
//  3. Otherwise (and for unrecognized or unsupported effort), return "" so
//     additionalModelRequestFields is omitted.
//
// An explicit but unrecognized effort is dropped without invoking the thinking
// fallback — the client asked for something specific that we couldn't honor, so
// we don't silently substitute a guess.
func resolveEffort(ctx context.Context, kiroModel string, req *anthropic.Request, thinking bool) string {
	_, short := logging.TraceIDs(ctx)
	requested := req.Effort()

	if requested != "" {
		resolved := models.ResolveEffort(kiroModel, requested)
		if resolved != requested {
			switch resolved {
			case "":
				slog.WarnContext(ctx, "effort not honored, dropping",
					"trace_id", short, "model", kiroModel, "requested_effort", requested)
			default:
				slog.WarnContext(ctx, "requested effort not supported by model, downgrading",
					"trace_id", short, "model", kiroModel, "requested_effort", requested, "effort", resolved)
			}
		}
		return resolved
	}

	if thinking {
		resolved := models.ResolveEffort(kiroModel, defaultThinkingEffort)
		if resolved != "" {
			slog.DebugContext(ctx, "thinking enabled without effort, using default effort",
				"trace_id", short, "model", kiroModel, "effort", resolved)
		}
		return resolved
	}

	return ""
}
