package messages

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"math"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/respconv"
)

// roundCredits rounds credit consumption to 3 decimals for human-readable
// log output and OTel attributes. Kiro reports raw values like 0.19354654693200665
// which are noisy at full precision; 0.001 (1 milli-credit) is the smallest
// unit that matters for display.
func roundCredits(c float64) float64 {
	return math.Round(c*1000) / 1000
}

const retryReasonEmptyVisibleEndTurn = "empty_visible_end_turn"

func (s *Service) handleStreamingResponse(ctx context.Context, w http.ResponseWriter, apiResp *kiroclient.Response, model string, contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int, capture *upstreamAttemptCapture, toolNameMap map[string]string) string {
	traceID, short := logging.TraceIDs(ctx)

	gw := NewGateWriter(w)
	sw := respconv.NewSSEWriter(ctx, gw, model, contextWindowSize, stopSequences, maxTokens, preCountedInputTokens)
	sw.OnVisibleOutput = func() { gw.Promote() }
	sw.SetToolNameMap(toolNameMap)

	var streamErr bool
	var localStop bool
	var invalidReason string
	var isException bool
	err := kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
		capture.recordEvent(e)
		if streamErr || localStop {
			return true
		}
		// Stop early if the client disconnected (write failed).
		if sw.WriteErr() {
			streamErr = true
			return true
		}
		if e.Type == kiroproto.EventInvalidState {
			invalidReason = e.InvalidStateReason
			slog.ErrorContext(ctx, "invalid state",
				"trace_id", short,
				"reason", e.InvalidStateReason,
				"message", e.ErrorMessage,
			)
		}
		if e.Type == kiroproto.EventException {
			isException = true
			slog.ErrorContext(ctx, "upstream exception",
				"trace_id", short,
				"reason", e.InvalidStateReason,
				"message", e.ErrorMessage,
			)
		}
		shouldStop := sw.HandleEvent(e)
		if sw.WriteErr() {
			streamErr = true
			return true
		}
		if !shouldStop {
			return false
		}
		// Distinguish adapter-side stop (Finish already called) from error.
		// Closing the upstream body immediately on localStop avoids paying
		// for tokens/credits the client will never receive.
		if sw.LocalStop() {
			localStop = true
			return true
		}
		streamErr = true
		return true
	})

	if streamErr && !sw.Started() {
		return handleUpstreamError(w, isException, invalidReason)
	}

	// If the stream started (thinking events) but GateWriter was never promoted
	// (no visible output reached the client), we can still discard and write error JSON.
	if streamErr && sw.Started() && !gw.IsPromoted() {
		gw.Discard()
		return handleUpstreamError(w, isException, invalidReason)
	}

	if err != nil {
		slog.ErrorContext(ctx, "stream error", "trace_id", short, "err", err)
		writeStreamingOrJSONError(gw, sw, w, http.StatusBadGateway, errTypeStreamError, "upstream stream error")
		return ""
	}

	if !streamErr && !localStop {
		sw.Finish()
	}

	// Detect empty visible end_turn (thinking-only response with no visible text).
	// If the GateWriter hasn't been promoted yet, we can safely discard and retry.
	if !streamErr && !localStop && sw.IsEmptyVisibleEndTurn() && !gw.IsPromoted() {
		gw.Discard()
		args := []any{
			"trace_id", short,
			"thinking_chars", sw.ThinkingLen(),
			"has_tool_use", false,
			"retry", true,
		}
		args = append(args, capture.logAttrs()...)
		slog.WarnContext(ctx, "empty visible end_turn detected", args...)
		if credits, ok := sw.Credits(); ok {
			logAbortedAttemptCredits(ctx, short, credits, retryReasonEmptyVisibleEndTurn)
		}
		return retryReasonEmptyVisibleEndTurn
	}

	// Log response completion (only on success).
	if !streamErr {
		slog.DebugContext(ctx, "client response headers",
			"trace_id", traceID,
			"session_id", logging.SessionIDFromContext(ctx),
			"headers", logging.SafeHeaders{H: gw.Header()},
		)
		inputTokens, outputTokens := sw.Usage()
		credits, hasCredits := sw.Credits()
		logResponseStats(ctx, short, inputTokens, outputTokens, sw.HasContextUsage(), sw.ContextUsagePercentage(), contextWindowSize, credits, hasCredits)
	}
	return ""
}

func (s *Service) handleNonStreamingResponse(ctx context.Context, w http.ResponseWriter, apiResp *kiroclient.Response, model string, contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int, capture *upstreamAttemptCapture, toolNameMap map[string]string) string {
	traceID, short := logging.TraceIDs(ctx)
	acc := respconv.NewNonStreamingAccumulator(contextWindowSize, stopSequences, maxTokens, preCountedInputTokens)
	acc.SetToolNameMap(toolNameMap)

	var invalidReason string
	var hasError bool
	var isException bool
	err := kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
		capture.recordEvent(e)
		d := acc.ProcessEvent(e)
		if d.IsError {
			hasError = true
			switch e.Type {
			case kiroproto.EventException:
				isException = true
				slog.ErrorContext(ctx, "upstream exception",
					"trace_id", short,
					"reason", e.InvalidStateReason,
					"message", e.ErrorMessage,
				)
			case kiroproto.EventInvalidState:
				invalidReason = e.InvalidStateReason
				slog.ErrorContext(ctx, "invalid state",
					"trace_id", short,
					"reason", e.InvalidStateReason,
					"message", e.ErrorMessage,
				)
			}
			return true // stop parsing
		}
		return false
	})
	if err != nil {
		slog.ErrorContext(ctx, "stream parse error", "trace_id", short, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream stream error")
		return ""
	}

	if hasError {
		return handleUpstreamError(w, isException, invalidReason)
	}

	resp, stats := acc.BuildResponse(model)

	// Detect empty visible end_turn (thinking-only response with no visible text).
	if acc.IsEmptyVisibleEndTurn() {
		args := []any{
			"trace_id", short,
			"thinking_chars", acc.ThinkingLen(),
			"has_tool_use", false,
			"retry", true,
		}
		args = append(args, capture.logAttrs()...)
		slog.WarnContext(ctx, "empty visible end_turn detected", args...)
		if credits, ok := acc.Credits(); ok {
			logAbortedAttemptCredits(ctx, short, credits, retryReasonEmptyVisibleEndTurn)
		}
		return retryReasonEmptyVisibleEndTurn
	}

	w.Header().Set("Content-Type", "application/json")
	slog.DebugContext(ctx, "client response headers",
		"trace_id", traceID,
		"session_id", logging.SessionIDFromContext(ctx),
		"headers", logging.SafeHeaders{H: w.Header()},
	)
	if err := json.MarshalWrite(w, resp); err != nil {
		slog.ErrorContext(ctx, "write non-streaming response failed", "err", err)
		return ""
	}
	_, _ = w.Write([]byte("\n"))

	logResponseStats(ctx, short, stats.InputTokens, stats.OutputTokens, stats.HasContextUsage, stats.ContextUsagePercentage, contextWindowSize, stats.Credits, stats.HasCredits)
	return ""
}

// logResponseStats logs response completion and warns on context limit exceeded.
func logResponseStats(ctx context.Context, short string, inputTokens, outputTokens int, hasContextUsage bool, contextUsagePct float64, contextWindowSize int, credits float64, hasCredits bool) {
	hasUsage := inputTokens > 0 || outputTokens > 0 || hasContextUsage
	pct := resolveContextPercent(contextUsagePct, hasContextUsage, inputTokens, contextWindowSize)
	contextUsage := "unknown"
	if hasUsage {
		contextUsage = fmt.Sprintf("%.1fk(%.1f%%)", float64(inputTokens)/1000, pct)
	}
	args := []any{
		"trace_id", short,
		"session_id", logging.ShortID(logging.SessionIDFromContext(ctx)),
		"status", 200,
		"input_tokens", inputTokens,
		"output_tokens", outputTokens,
		"context_usage", contextUsage,
	}
	if hasCredits {
		rounded := roundCredits(credits)
		trace.SpanFromContext(ctx).SetAttributes(attribute.Float64("kiro.credits", rounded))
		args = append(args, "credits", rounded)
	}
	slog.InfoContext(ctx, "<-- POST /v1/messages", args...)
	if hasUsage && pct >= 100 {
		slog.WarnContext(ctx, "context limit exceeded",
			"trace_id", short,
			"context_usage", fmt.Sprintf("%.1fk(%.1f%%)", float64(inputTokens)/1000, pct),
		)
	}
}

// logAbortedAttemptCredits logs the credits consumed by an upstream attempt
// that the proxy decided to abandon (e.g. empty-visible end_turn that triggers
// retry). The successful retry's credits flow through logResponseStats normally,
// so this avoids under-reporting cumulative credit consumption.
func logAbortedAttemptCredits(ctx context.Context, short string, credits float64, reason string) {
	rounded := roundCredits(credits)
	trace.SpanFromContext(ctx).SetAttributes(attribute.Float64("kiro.credits.aborted_attempt", rounded))
	slog.InfoContext(ctx, "upstream attempt credits (aborted)",
		"trace_id", short,
		"credits", rounded,
		"reason", reason,
	)
}

// resolveContextPercent returns the context usage percentage, falling back to
// an estimate from inputTokens/windowSize when the reported value is not available.
func resolveContextPercent(reported float64, hasContextUsage bool, inputTokens, windowSize int) float64 {
	if hasContextUsage || windowSize == 0 {
		return reported
	}
	return float64(inputTokens) * 100 / float64(windowSize)
}
