package messages

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/models"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/toolsearch"
)

const headerCCSessionID = "X-Claude-Code-Session-Id"

func (s *Service) HandleMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	traceID, short := logging.TraceIDs(ctx)

	req, bodyBytes, err := s.parseAndValidateRequest(ctx, w, r)
	if err != nil {
		slog.WarnContext(ctx, "invalid request", "trace_id", short, "body_bytes", bodyBytes, "err", err)
		writeRequestError(w, err)
		return
	}

	ccSessionID := r.Header.Get(headerCCSessionID)
	if ccSessionID == "" {
		httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, "missing "+headerCCSessionID+" header")
		return
	}
	ctx = logging.WithSessionID(ctx, ccSessionID)
	r = r.WithContext(ctx)

	slog.DebugContext(ctx, "client request headers",
		"trace_id", traceID,
		"session_id", ccSessionID,
		"headers", logging.SafeHeaders{H: r.Header},
	)

	creds, err := s.auth.GetToken(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "auth error", "trace_id", short, "err", err)
		httpx.WriteError(w, http.StatusUnauthorized, ErrTypeAuthentication, "authentication failed")
		return
	}

	kiroModel, thinking, contextWindowSize, anthropicModel := models.Resolve(req.Model, anthropic.HasContext1MBeta(r.Header))
	if req.IsThinkingEnabled() {
		thinking = true
	}

	s.logRequest(ctx, short, ccSessionID, kiroModel, contextWindowSize, req, thinking, bodyBytes)

	effort := resolveEffort(ctx, kiroModel, req, thinking)

	// Server-side tools (tool search, advisor) short-circuit to the
	// orchestrator, which has its own retry loop.
	tsCtx, advCtx := newServerToolContexts(req)
	if tsCtx != nil {
		slog.InfoContext(ctx, "tool search enabled",
			"trace_id", short,
			"search_type", tsCtx.SearchType,
			"deferred_tools", len(tsCtx.DeferredTools),
			"active_tools", len(tsCtx.ActiveTools),
		)
	}
	if tsCtx != nil || advCtx != nil {
		s.runServerTools(ctx, w, req, creds, tsCtx, advCtx, kiroModel, anthropicModel, contextWindowSize, effort, ccSessionID, short)
		return
	}

	payload, nameMap, err := reqconv.BuildPayload(req, reqconv.BuildOptions{
		ProfileARN:     creds.ProfileARN,
		ModelID:        kiroModel,
		ConversationID: ccSessionID,
		Effort:         effort,
	})
	if err != nil {
		slog.WarnContext(ctx, "payload build error", "trace_id", short, "err", err)
		httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
		return
	}

	s.executeWithRetry(ctx, w, &invocation{
		req:               req,
		payload:           payload,
		creds:             creds,
		model:             kiroModel,
		responseModel:     anthropicModel,
		contextWindowSize: contextWindowSize,
		thinking:          thinking,
		toolNameMap:       nameMap.ReverseMap(),
	})
}

// logRequest emits the "--> POST /v1/messages" info log summarizing the call.
func (s *Service) logRequest(ctx context.Context, short, ccSessionID, kiroModel string, contextWindowSize int, req *anthropic.Request, thinking bool, bodyBytes int64) {
	var thinkingLog any = false
	if thinking {
		if effort := req.Effort(); effort != "" {
			thinkingLog = effort
		} else {
			thinkingLog = "enabled"
		}
	}
	slog.InfoContext(ctx, "--> POST /v1/messages",
		"trace_id", short,
		"session_id", logging.ShortID(ccSessionID),
		"model", kiroModel,
		"thinking", thinkingLog,
		"stream", req.Stream,
		"context_window", formatContextWindow(contextWindowSize),
		"body_bytes", bodyBytes,
	)
}

// newServerToolContexts builds the per-request server-side tool contexts.
// Both the live send path and count_tokens call this so the payload they build
// carries an identical tool set — a token count derived from a different tool
// list would under-report by the size of the tool schemas.
func newServerToolContexts(req *anthropic.Request) (*toolsearch.Context, *advisor.Context) {
	tsCtx := toolsearch.NewContext(req.Tools)
	if tsCtx != nil {
		tsCtx.PromoteTools(reqconv.ExtractToolReferences(req.Messages))
	}
	// models.ResolveKnown is strict by design: an unknown advisor model must
	// yield model_not_found, never a silent downgrade to a default model.
	return tsCtx, advisor.NewContext(req.Tools, models.ResolveKnown)
}

// runServerTools wires up the orchestrator and retries once on empty-visible end_turn.
func (s *Service) runServerTools(ctx context.Context, w http.ResponseWriter, req *anthropic.Request, creds *auth.Credentials, tsCtx *toolsearch.Context, advCtx *advisor.Context, kiroModel, responseModel string, contextWindowSize int, effort string, ccSessionID, short string) {
	var dropNames []string
	if tsCtx != nil {
		dropNames = append(dropNames, toolsearch.KiroToolSearchName)
	}
	if advCtx != nil {
		dropNames = append(dropNames, advisor.KiroToolName)
	}
	orch := &serverToolOrchestrator{
		service: s,
		tsCtx:   tsCtx,
		advCtx:  advCtx,
		req:     req,
		creds:   creds,
		buildOpts: reqconv.BuildOptions{
			ProfileARN:     creds.ProfileARN,
			ModelID:        kiroModel,
			ConversationID: ccSessionID,
			Effort:         effort,
			ToolSearchCtx:  tsCtx,
			AdvisorCtx:     advCtx,
		},
		contextWindowSize: contextWindowSize,
		responseModel:     responseModel,
		dropNames:         dropNames,
	}
	if req.Stream {
		session := newStreamSession(ctx, w, s.keepAliveInterval)
		defer session.Stop()
		s.runServerToolsWithRetry(session.Context(), session, session, orch, short)
		return
	}
	s.runServerToolsWithRetry(ctx, w, nil, orch, short)
}

func (s *Service) runServerToolsWithRetry(ctx context.Context, w http.ResponseWriter, session *streamSession, orch *serverToolOrchestrator, short string) {
	reason := orch.run(ctx, w, session)
	if reason != retryReasonEmptyVisibleEndTurn {
		return
	}
	slog.WarnContext(ctx, "retrying server tool loop after empty visible end_turn", "trace_id", short)
	if r2 := orch.run(ctx, w, session); r2 == retryReasonEmptyVisibleEndTurn {
		slog.ErrorContext(ctx, "server tool retry also returned empty visible end_turn", "trace_id", short)
		if session != nil {
			_ = session.WriteFinalError(newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream returned empty response"), nil)
		} else {
			httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream returned empty response")
		}
	}
}
