package messages

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
)

// invocation bundles everything callAndHandle needs for one upstream attempt.
// Replaces the former 11-argument callAndHandle signature.
type invocation struct {
	req               *anthropic.Request
	payload           *kiroproto.Payload
	creds             *auth.Credentials
	model             string
	responseModel     string
	contextWindowSize int
	thinking          bool
	toolNameMap       map[string]string
}

// callAndHandle performs one upstream call for the invocation and streams or
// buffers the response to w. Returns a non-empty reason if the request failed
// before semantic output was promoted. Transport keep-alives do not prevent
// that transparent retry.
func (s *Service) callAndHandle(ctx context.Context, w http.ResponseWriter, session *streamSession, inv *invocation, attempt int) string {
	_, short := logging.TraceIDs(ctx)
	capture := newUpstreamAttemptCapture(ctx, s.captureEnabled, inv.payload, inv.model, inv.thinking, inv.req.Stream, attempt)

	apiResp, err := s.client.GenerateAssistantResponse(ctx, inv.creds.AccessToken, inv.payload, inv.creds.Region)
	if err != nil {
		logUpstreamError(ctx, short, err)
		if session != nil {
			_ = session.WriteFinalError(newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream API error"), nil)
		} else {
			httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream API error")
		}
		return ""
	}
	body := apiResp.Body
	defer func() { _ = body.Close() }()
	if capture != nil {
		capture.setResponseHeaders(apiResp.Header)
	}

	var reason string
	if inv.req.Stream {
		reason = s.handleStreamingResponse(ctx, session, apiResp, inv.responseModel, inv.contextWindowSize, inv.req.StopSequences, inv.req.MaxTokens, apiResp.PromptTokens, capture, inv.toolNameMap)
	} else {
		reason = s.handleNonStreamingResponse(ctx, w, apiResp, inv.responseModel, inv.contextWindowSize, inv.req.StopSequences, inv.req.MaxTokens, apiResp.PromptTokens, capture, inv.toolNameMap)
	}
	if reason == retryReasonEmptyVisibleEndTurn {
		capture.logCapture(ctx, reason)
	}
	return reason
}

// executeWithRetry runs the invocation and handles retryable invalidStateEvent
// responses by clearing ConversationID and attempting once more. Terminal error
// responses are written to w and the function returns.
func (s *Service) executeWithRetry(ctx context.Context, w http.ResponseWriter, inv *invocation) {
	if inv.req.Stream {
		session := newStreamSession(ctx, w, s.keepAliveInterval)
		defer session.Stop()
		s.executeRetry(session.Context(), session, session, inv)
		return
	}
	s.executeRetry(ctx, w, nil, inv)
}

func (s *Service) executeRetry(ctx context.Context, w http.ResponseWriter, session *streamSession, inv *invocation) {
	_, short := logging.TraceIDs(ctx)

	reason := s.callAndHandle(ctx, w, session, inv, 1)
	if reason == "" {
		return
	}

	slog.WarnContext(ctx, "retrying upstream request",
		"trace_id", short,
		"reason", reason,
	)
	// Clear conversation ID to break out of stuck state (empty_visible_end_turn
	// or retryable invalidStateEvent like CONTENT_LENGTH_EXCEEDS_THRESHOLD).
	inv.payload.ConversationState.ConversationID = ""

	reason2 := s.callAndHandle(ctx, w, session, inv, 2)
	if reason2 == "" {
		return
	}
	if reason2 == retryReasonEmptyVisibleEndTurn {
		slog.ErrorContext(ctx, "retry also returned empty visible end_turn",
			"trace_id", short, "reason", reason2)
		if session != nil {
			_ = session.WriteFinalError(newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream returned empty response"), nil)
		} else {
			httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream returned empty response")
		}
		return
	}
	// Retry ended with a different (final) error — report it as invalid state.
	slog.ErrorContext(ctx, "retry failed",
		"trace_id", short, "first_reason", reason, "second_reason", reason2)
	if session != nil {
		_ = session.WriteFinalError(newStreamFinalError(http.StatusBadRequest, errTypeInvalidRequest, "invalid state: "+reason2), nil)
	} else {
		httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, "invalid state: "+reason2)
	}
}
