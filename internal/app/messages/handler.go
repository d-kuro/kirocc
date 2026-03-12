package messages

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/models"
	"github.com/d-kuro/kirocc/internal/reqconv"
)

func (s *Service) HandleMessages(w http.ResponseWriter, r *http.Request) {
	traceID := logging.TraceIDFromContext(r.Context())
	short := logging.ShortTraceID(traceID)

	req, err := parseAndValidateRequest(w, r)
	if err != nil {
		slog.WarnContext(r.Context(), "invalid request",
			"trace_id", short, "err", err)
		WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
		return
	}

	slog.DebugContext(r.Context(), "client request headers",
		"trace_id", short,
		"headers", logging.SafeHeaders{H: r.Header},
	)

	creds, err := s.auth.GetToken(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "auth error",
			"trace_id", short, "err", err)
		WriteErrorJSON(w, http.StatusUnauthorized, ErrTypeAuthentication, "authentication failed")
		return
	}

	kiroModel, thinking, contextWindowSize := models.Resolve(req.Model, hasContext1MBeta(r.Header))

	// Also check the request's thinking field (Anthropic API standard).
	if req.IsThinkingEnabled() {
		thinking = true
	}

	contextWindow := fmt.Sprintf("%dk", contextWindowSize/1000)
	if contextWindowSize >= 1_000_000 {
		contextWindow = fmt.Sprintf("%dM", contextWindowSize/1_000_000)
	}
	thinkingLog := any(thinking)
	if thinking && req.Thinking != nil && req.Thinking.ReasoningEffort != "" {
		thinkingLog = req.Thinking.ReasoningEffort
	}
	slog.InfoContext(r.Context(), "--> POST /v1/messages",
		"trace_id", short,
		"model", kiroModel,
		"thinking", thinkingLog,
		"stream", req.Stream,
		"context_window", contextWindow,
	)

	thinkingBudget := 0
	if req.Thinking != nil {
		thinkingBudget = req.Thinking.BudgetTokens
		// When budget_tokens is not explicitly set, derive from reasoning_effort.
		if thinkingBudget <= 0 {
			switch req.Thinking.ReasoningEffort {
			case anthropic.ReasoningEffortHigh:
				thinkingBudget = anthropic.ThinkingBudgetHigh
			case anthropic.ReasoningEffortLow:
				thinkingBudget = anthropic.ThinkingBudgetLow
			default: // "medium" or unset
				thinkingBudget = anthropic.ThinkingBudgetMedium
			}
		}
	}

	payload, err := reqconv.BuildPayload(req, reqconv.BuildOptions{ProfileARN: creds.ProfileARN, ModelID: kiroModel, Thinking: thinking, ThinkingBudget: thinkingBudget, EnvState: s.envState})
	if err != nil {
		slog.WarnContext(r.Context(), "payload build error",
			"trace_id", short, "err", err)
		WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
		return
	}

	retryReason := s.callAndHandle(r.Context(), w, req, payload, creds, kiroModel, contextWindowSize, thinking, 1)
	if retryReason == "" {
		return
	}

	// Handle empty visible end_turn: retry with the same payload.
	// The thinking-only response is typically a transient upstream issue,
	// so we retry as-is without disabling thinking.
	if retryReason == retryReasonEmptyVisibleEndTurn {
		slog.WarnContext(r.Context(), "retrying after empty visible end_turn",
			"trace_id", short,
			"reason", retryReason,
		)
		// Clear IDs to break out of stuck conversation state.
		payload.ConversationState.ConversationID = ""
		payload.ConversationState.AgentContinuationID = ""
		reason := s.callAndHandle(r.Context(), w, req, payload, creds, kiroModel, contextWindowSize, thinking, 2)
		if reason == "" {
			return
		}
		if reason == retryReasonEmptyVisibleEndTurn {
			slog.ErrorContext(r.Context(), "retry also returned empty visible end_turn",
				"trace_id", short, "reason", reason)
			WriteErrorJSON(w, http.StatusBadGateway, errTypeAPI, "upstream returned empty response")
			return
		}
		// Retry returned a different error (e.g. invalid state) — report it as-is.
		slog.ErrorContext(r.Context(), "retry failed with different reason",
			"trace_id", short, "reason", reason)
		WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, "invalid state: "+reason)
		return
	}

	// Retry once with cleared conversation ID.
	slog.WarnContext(r.Context(), "retrying with cleared conversation ID",
		"trace_id", short,
		"reason", retryReason,
	)
	payload.ConversationState.ConversationID = ""
	payload.ConversationState.AgentContinuationID = ""
	if reason := s.callAndHandle(r.Context(), w, req, payload, creds, kiroModel, contextWindowSize, thinking, 2); reason != "" {
		WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, "invalid state: "+reason)
	}
}

// callAndHandle calls the Kiro API and handles the response.
// Returns a non-empty reason string if the request failed with a retryable invalidStateEvent
// before the stream started (i.e., no bytes written to w yet). Returns "" on success or non-retryable error.
func (s *Service) callAndHandle(ctx context.Context, w http.ResponseWriter, req *anthropic.Request, payload *kiroproto.Payload, creds *auth.Credentials, model string, contextWindowSize int, thinking bool, attempt int) string {
	short := logging.ShortTraceID(logging.TraceIDFromContext(ctx))
	capture := newUpstreamAttemptCapture(ctx, payload, model, thinking, req.Stream, attempt)

	apiResp, err := s.client.GenerateAssistantResponse(ctx, creds.AccessToken, payload, creds.Region)
	if err != nil {
		slog.ErrorContext(ctx, "kiro api error",
			"trace_id", short, "err", err)
		WriteErrorJSON(w, http.StatusBadGateway, errTypeAPI, "upstream API error")
		return ""
	}
	body := apiResp.Body
	defer func() { _ = body.Close() }()
	if capture != nil {
		capture.setResponseHeaders(apiResp.Header)
	}

	var reason string
	if req.Stream {
		reason = s.handleStreamingResponse(ctx, w, apiResp, model, contextWindowSize, req.StopSequences, req.MaxTokens, apiResp.PromptTokens, capture)
	} else {
		reason = s.handleNonStreamingResponse(ctx, w, apiResp, model, contextWindowSize, req.StopSequences, req.MaxTokens, apiResp.PromptTokens, capture)
	}
	if reason == retryReasonEmptyVisibleEndTurn {
		capture.logCapture(ctx, reason)
	}
	return reason
}

// hasContext1MBeta checks if the Anthropic-Beta header contains a context-1m flag.
func hasContext1MBeta(h http.Header) bool {
	for _, v := range h["Anthropic-Beta"] {
		for beta := range strings.SplitSeq(v, ",") {
			if strings.HasPrefix(strings.TrimSpace(beta), "context-1m") {
				return true
			}
		}
	}
	return false
}
