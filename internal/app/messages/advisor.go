package messages

import (
	"context"
	"log/slog"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/respconv"
)

// advisorOutcome is the result of one advisor consultation. Exactly one of
// errorCode or text is meaningful: a non-empty errorCode means the
// consultation failed and must surface as advisor_tool_result_error.
type advisorOutcome struct {
	errorCode    string
	text         string
	stopReason   string
	inputTokens  int
	outputTokens int
	// emptyResponse marks a clean upstream stream that carried no assistant
	// text. It is retryable, unlike a genuine upstream error.
	emptyResponse bool
	// credits carries the subcall's meteringEvent so the round totals (and the
	// kiro.credits span attribute) include what the advisor consultation cost.
	credits    float64
	hasCredits bool
}

// advisorEmptyRetries is how many extra attempts a clean-but-empty advisor
// response earns. Kiro intermittently answers an advisor subcall with a
// well-formed stream carrying no assistant text; replaying the identical payload
// then succeeds, so a single empty response is a transient failure rather than a
// verdict. Bounded at one retry: a genuinely unanswerable prompt must not double
// the latency of every consultation.
const advisorEmptyRetries = 1

// consultAdvisor performs one advisor consultation: preflight checks, then a
// dedicated upstream request to the advisor model carrying the conversation so
// far. Failures never abort the client request — they come back as an advisor
// error code the executor can read and route around.
//
// Every outcome is logged as a "<-- advisor" response paired with the
// "--> advisor" request line, mirroring the executor's own INFO pair so an
// advisor subcall is as traceable as the request that triggered it.
func (o *serverToolOrchestrator) consultAdvisor(ctx context.Context, short string, round int, msgs []anthropic.Message) advisorOutcome {
	if o.advCtx.PreflightError != "" {
		outcome := advisorOutcome{errorCode: o.advCtx.PreflightError}
		logAdvisorResponse(ctx, short, o.advCtx, round, outcome)
		return outcome
	}
	// One consultation consumes one use regardless of how many attempts it
	// takes: the retry answers the client's single request, it is not a second
	// consultation.
	if !o.advCtx.Consume() {
		outcome := advisorOutcome{errorCode: anthropic.AdvisorErrorMaxUsesExceeded}
		logAdvisorResponse(ctx, short, o.advCtx, round, outcome)
		return outcome
	}

	var outcome advisorOutcome
	for attempt := range advisorEmptyRetries + 1 {
		outcome = o.runAdvisorSubcall(ctx, short, round, attempt, msgs)
		if !outcome.emptyResponse {
			break
		}
		if attempt < advisorEmptyRetries {
			slog.WarnContext(ctx, "retrying advisor subcall after empty response",
				"trace_id", short, "round", round+1, "attempt", attempt+1)
		}
	}
	logAdvisorResponse(ctx, short, o.advCtx, round, outcome)
	return outcome
}

// runAdvisorSubcall issues one upstream attempt. Preflight checks and use
// accounting belong to consultAdvisor, which may call this more than once for a
// single consultation.
func (o *serverToolOrchestrator) runAdvisorSubcall(ctx context.Context, short string, round, attempt int, msgs []anthropic.Message) advisorOutcome {
	payload, err := o.buildAdvisorPayload(msgs)
	if err != nil {
		slog.WarnContext(ctx, "advisor payload build error", "trace_id", short, "round", round+1, "err", err)
		return advisorOutcome{errorCode: anthropic.AdvisorErrorPromptTooLong}
	}
	logAdvisorRequest(ctx, short, o.advCtx, round, attempt, len(msgs))
	slog.DebugContext(ctx, "advisor request body",
		"trace_id", short, "round", round+1, "request_body", marshalRaw(payload),
	)

	apiResp, err := o.service.client.GenerateAssistantResponse(ctx, o.creds.AccessToken, payload, o.creds.Region)
	if err != nil {
		logUpstreamError(ctx, short, err, "round", round+1, "advisor", true)
		return advisorOutcome{errorCode: anthropic.AdvisorErrorUnavailable}
	}
	defer func() { _ = apiResp.Body.Close() }()

	// The advisor has no tools, so the response is thinking + text only.
	// MaxTokens from the tool definition is enforced by the accumulator budget,
	// which yields a genuine max_tokens stop reason when hit. PromptTokens is
	// the pre-counted input fallback for when upstream omits tokenUsage.
	acc := respconv.NewNonStreamingAccumulator(o.contextWindowSize, nil, o.advCtx.MaxTokens, apiResp.PromptTokens)
	var hasError bool
	err = kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
		if d := acc.ProcessEvent(e); d.IsError {
			hasError = true
			return true
		}
		return false
	})
	if err != nil || hasError {
		slog.WarnContext(ctx, "advisor upstream error", "trace_id", short, "round", round+1, "err", err)
		return advisorOutcome{errorCode: anthropic.AdvisorErrorUnavailable}
	}

	text, stopReason, stats := acc.FinalizeText()
	if text == "" {
		// The stream parsed cleanly (no error frame, no parse error), so the
		// advisor produced reasoning-only or nothing at all. Report which, so a
		// recurring unavailable is diagnosable without DEBUG logging.
		slog.WarnContext(ctx, "advisor returned no visible text",
			"trace_id", short,
			"round", round+1,
			"attempt", attempt+1,
			"stop_reason", stopReason,
			"thinking_len", acc.ThinkingLen(),
			"redacted_blobs", len(acc.RedactedContents()),
			"output_tokens", stats.OutputTokens,
		)
		return advisorOutcome{
			errorCode:     anthropic.AdvisorErrorUnavailable,
			emptyResponse: true,
			credits:       stats.Credits,
			hasCredits:    stats.HasCredits,
		}
	}

	slog.DebugContext(ctx, "advisor response body",
		"trace_id", short, "round", round+1, "advice", text,
	)
	return advisorOutcome{
		text:         text,
		stopReason:   stopReason,
		inputTokens:  stats.InputTokens,
		outputTokens: stats.OutputTokens,
		credits:      stats.Credits,
		hasCredits:   stats.HasCredits,
	}
}

// logAdvisorRequest logs the outbound advisor subcall, mirroring the
// "--> POST /v1/messages" line the executor's own request gets. trace_id and
// session_id come from the originating client request's context, so an advisor
// subcall correlates with the turn that triggered it. attempt is reported only
// on a retry, so the common line stays uncluttered.
func logAdvisorRequest(ctx context.Context, short string, advCtx *advisor.Context, round, attempt, messages int) {
	args := []any{
		"trace_id", short,
		"session_id", logging.ShortID(logging.SessionIDFromContext(ctx)),
		"model", advCtx.KiroModel,
		"round", round + 1,
		"messages", messages,
		"max_uses", advCtx.MaxUses,
	}
	if attempt > 0 {
		args = append(args, "attempt", attempt+1)
	}
	slog.InfoContext(ctx, "--> advisor", args...)
}

// logAdvisorResponse logs the advisor subcall result at the same granularity as
// "<-- POST /v1/messages": status, model, tokens and credits, or the error code
// when the consultation failed.
func logAdvisorResponse(ctx context.Context, short string, advCtx *advisor.Context, round int, outcome advisorOutcome) {
	// An unresolved advisor model has no ResponseModel; naming what the client
	// asked for is what makes a model_not_found line actionable.
	model := advCtx.ResponseModel
	if model == "" {
		model = advCtx.RequestedModel
	}
	args := []any{
		"trace_id", short,
		"session_id", logging.ShortID(logging.SessionIDFromContext(ctx)),
		"model", model,
		"round", round + 1,
		"uses", advCtx.Uses(),
		"max_uses", advCtx.MaxUses,
	}
	if outcome.errorCode != "" {
		args = append(args, "status", outcome.errorCode, "error_code", outcome.errorCode)
	} else {
		args = append(args,
			"status", 200,
			"input_tokens", outcome.inputTokens,
			"output_tokens", outcome.outputTokens,
			"stop_reason", outcome.stopReason,
		)
	}
	if outcome.hasCredits {
		args = append(args, "credits", roundCredits(outcome.credits))
	}
	slog.InfoContext(ctx, "<-- advisor", args...)
}

// buildAdvisorPayload builds the advisor subcall payload: the conversation so
// far plus the advisor instruction, no tools, the strictly resolved advisor
// model, and no conversation ID (the subcall must not pollute the executor's
// Kiro conversation state).
//
// The instruction is separated from the conversation by an assistant turn.
// Appending it directly would let normalization merge it into the preceding
// user message — mid-session that message is the tool output the executor just
// received, and an advisor model reading the instruction as a trailing fragment
// of tool output treats it as untrusted embedded content and answers with
// nothing, which surfaces to the client as advisor error "unavailable".
func (o *serverToolOrchestrator) buildAdvisorPayload(msgs []anthropic.Message) (*kiroproto.Payload, error) {
	subReq := *o.req
	subReq.Tools = nil
	subReq.Messages = append(append([]anthropic.Message{}, msgs...),
		anthropic.Message{
			Role:    "assistant",
			Content: anthropic.MessageContent{Text: advisor.SubcallHandoff},
		},
		anthropic.Message{
			Role:    "user",
			Content: anthropic.MessageContent{Text: advisor.SubcallPrompt},
		},
	)
	opts := reqconv.BuildOptions{
		ProfileARN: o.buildOpts.ProfileARN,
		ModelID:    o.advCtx.KiroModel,
	}
	payload, _, err := reqconv.BuildPayload(&subReq, opts)
	return payload, err
}

// appendAdvisorMessages records the advisor round in the working conversation.
// The result text matches what reqconv.ServerToolResultText produces when the
// same round is replayed from client-supplied history, so the executor sees an
// identical rendering either way.
func (o *serverToolOrchestrator) appendAdvisorMessages(msgs []anthropic.Message, srvToolUseID string, outcome advisorOutcome, redacted []string) []anthropic.Message {
	var resultText string
	var isError bool
	if outcome.errorCode != "" {
		isError = true
		resultText = reqconv.ServerToolResultText(anthropic.ContentBlock{
			Type: anthropic.BlockTypeAdvisorToolResult,
			Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
				{Type: anthropic.BlockTypeAdvisorResultError, ErrorCode: outcome.errorCode},
			}},
		})
	} else {
		resultText = outcome.text
	}
	return appendServerToolRound(msgs, srvToolUseID, advisor.KiroToolName, map[string]any{}, resultText, isError, redacted)
}
