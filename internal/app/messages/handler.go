package messages

import (
	"context"
	"log/slog"
	"net/http"

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

	req, err := parseAndValidateRequest(ctx, w, r)
	if err != nil {
		slog.WarnContext(ctx, "invalid request", "trace_id", short, "err", err)
		httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
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

	s.logRequest(ctx, short, ccSessionID, kiroModel, contextWindowSize, req, thinking)

	thinkingBudget := resolveThinkingBudget(ctx, req)

	// Tool search short-circuits to the orchestrator, which has its own retry loop.
	if tsCtx := toolsearch.NewContext(req.Tools); tsCtx != nil {
		refs := reqconv.ExtractToolReferences(req.Messages)
		tsCtx.PromoteTools(refs)
		slog.InfoContext(ctx, "tool search enabled",
			"trace_id", short,
			"search_type", tsCtx.SearchType,
			"deferred_tools", len(tsCtx.DeferredTools),
			"active_tools", len(tsCtx.ActiveTools),
		)
		s.runToolSearch(ctx, w, req, creds, tsCtx, kiroModel, anthropicModel, contextWindowSize, thinking, thinkingBudget, ccSessionID, short)
		return
	}

	payload, nameMap, err := reqconv.BuildPayload(req, reqconv.BuildOptions{
		ProfileARN:     creds.ProfileARN,
		ModelID:        kiroModel,
		ConversationID: ccSessionID,
		Thinking:       thinking,
		ThinkingBudget: thinkingBudget,
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
func (s *Service) logRequest(ctx context.Context, short, ccSessionID, kiroModel string, contextWindowSize int, req *anthropic.Request, thinking bool) {
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
	)
}

// runToolSearch wires up the orchestrator and retries once on empty-visible end_turn.
func (s *Service) runToolSearch(ctx context.Context, w http.ResponseWriter, req *anthropic.Request, creds *auth.Credentials, tsCtx *toolsearch.Context, kiroModel, responseModel string, contextWindowSize int, thinking bool, thinkingBudget int, ccSessionID, short string) {
	orch := &toolSearchOrchestrator{
		service: s,
		tsCtx:   tsCtx,
		req:     req,
		creds:   creds,
		buildOpts: reqconv.BuildOptions{
			ProfileARN:     creds.ProfileARN,
			ModelID:        kiroModel,
			ConversationID: ccSessionID,
			Thinking:       thinking,
			ThinkingBudget: thinkingBudget,
			ToolSearchCtx:  tsCtx,
		},
		contextWindowSize: contextWindowSize,
		responseModel:     responseModel,
	}
	reason := orch.run(ctx, w)
	if reason != retryReasonEmptyVisibleEndTurn {
		return
	}
	slog.WarnContext(ctx, "retrying tool search after empty visible end_turn", "trace_id", short)
	if r2 := orch.run(ctx, w); r2 == retryReasonEmptyVisibleEndTurn {
		slog.ErrorContext(ctx, "tool search retry also returned empty visible end_turn", "trace_id", short)
		httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream returned empty response")
	}
}
