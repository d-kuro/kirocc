package messages

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/models"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/respconv"
	"github.com/d-kuro/kirocc/internal/toolsearch"
	"github.com/google/uuid"
)

const maxToolSearchRounds = 3

// maxServerToolRounds caps the combined inner loop across all server-side
// tools (tool search + advisor) so a pathological executor cannot ping-pong
// between them indefinitely.
const maxServerToolRounds = 6

// roundTotals accumulates per-round usage across tool-search rounds and folds
// in the current (partial) round when a final summary is needed.
type roundTotals struct {
	inputTokens  int
	outputTokens int
	credits      float64
	hasCredits   bool
}

// addCompleted accumulates a round whose accumulator is about to be reset.
func (t *roundTotals) addCompleted(in, out int, credits float64, hasCredits bool) {
	t.inputTokens += in
	t.outputTokens += out
	if hasCredits {
		t.credits += credits
		t.hasCredits = true
	}
}

// withCurrent returns the totals folded together with the current round's stats.
func (t roundTotals) withCurrent(in, out int, credits float64, hasCredits bool) (totalIn, totalOut int, totalCredits float64, totalHasCredits bool) {
	totalIn = t.inputTokens + in
	totalOut = t.outputTokens + out
	totalCredits = t.credits + credits
	totalHasCredits = t.hasCredits || hasCredits
	return
}

// creditsWith returns cumulative credits including the current round; the bool
// is true if any meteringEvent was observed across all rounds so far.
func (t roundTotals) creditsWith(credits float64, hasCredits bool) (float64, bool) {
	return t.credits + credits, t.hasCredits || hasCredits
}

// serverToolOrchestrator manages the inner loop for server-side tools
// (tool search and advisor). Either context may be nil; at least one is set.
type serverToolOrchestrator struct {
	service           *Service
	tsCtx             *toolsearch.Context
	advCtx            *advisor.Context
	req               *anthropic.Request
	creds             *auth.Credentials
	buildOpts         reqconv.BuildOptions
	contextWindowSize int
	responseModel     string
	// dropNames are the synthetic Kiro tool names this orchestrator intercepts.
	// Fixed once at construction: the active contexts never change afterwards.
	dropNames []string
}

// dropToolNames returns the synthetic Kiro tool names the orchestrator
// intercepts; they must never be recorded as client-visible tool calls.
// Fixed for the orchestrator's lifetime, so it is computed once in
// newServerToolOrchestrator.
func (o *serverToolOrchestrator) dropToolNames() []string {
	return o.dropNames
}

// intercepts reports whether a Kiro tool_use with this name belongs to the
// orchestrator rather than the client. Reads the same list the accumulator
// filters on, so the two views of "which tools are ours" cannot diverge.
func (o *serverToolOrchestrator) intercepts(toolName string) bool {
	return slices.Contains(o.dropNames, toolName)
}

// newServerToolUseID mints a client-visible server_tool_use block ID.
func newServerToolUseID() string {
	return "srvtoolu_" + uuid.New().String()[:24]
}

// maxRounds returns the inner-loop round cap: the historical tool-search cap
// when only tool search is active, the combined cap when advisor is present.
func (o *serverToolOrchestrator) maxRounds() int {
	if o.advCtx != nil {
		return maxServerToolRounds
	}
	return maxToolSearchRounds
}

// run dispatches to the streaming or non-streaming implementation based on
// req.Stream. Returns retryReasonEmptyVisibleEndTurn when the upstream
// produced thinking-only output and the call site should retry.
func (o *serverToolOrchestrator) run(ctx context.Context, w http.ResponseWriter, session *streamSession) string {
	if o.req.Stream {
		return o.handleStreaming(ctx, session)
	}
	return o.handleNonStreaming(ctx, w)
}

func (o *serverToolOrchestrator) handleStreaming(ctx context.Context, session *streamSession) string {
	_, short := logging.TraceIDs(ctx)

	sw := respconv.NewSSEWriter(ctx, session, o.responseModel, o.contextWindowSize, o.req.StopSequences, o.req.MaxTokens, 0)
	sw.OnVisibleOutput = session.Promote
	sw.SetDrainOnStop(models.IsReasoningModel(o.buildOpts.ModelID))
	// Loop-invariant: ResetAccumulator preserves the drop set across rounds.
	sw.SetDropToolNames(o.dropToolNames()...)

	msgs := slices.Clone(o.req.Messages)

	var totals roundTotals
	// endTurn is set when a round must be the last one (client tool calls were
	// emitted alongside an intercepted server tool); distinguishes a normal
	// close from hitting the round cap.
	var endTurn bool

	for round := range o.maxRounds() {
		payload, nameMap, err := o.buildPayload(msgs)
		if err != nil {
			slog.WarnContext(ctx, "tool search payload build error", "trace_id", short, "err", err)
			final := newStreamFinalError(http.StatusBadRequest, errTypeInvalidRequest, err.Error())
			_ = session.WriteFinalError(final, func() error {
				return sw.WriteError(errTypeInvalidRequest, err.Error())
			})
			return ""
		}
		sw.SetToolNameMap(nameMap.ReverseMap())

		apiResp, err := o.service.client.GenerateAssistantResponse(ctx, o.creds.AccessToken, payload, o.creds.Region)
		if err != nil {
			logUpstreamError(ctx, short, err, "round", round+1)
			final := newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream API error")
			_ = session.WriteFinalError(final, func() error {
				return sw.WriteError(errTypeAPI, "upstream API error")
			})
			return ""
		}
		session.Start()

		if round > 0 {
			// Accumulate usage from previous round before resetting.
			in, out := sw.Usage()
			credits, hasCredits := sw.Credits()
			totals.addCompleted(in, out, credits, hasCredits)
			sw.ResetAccumulator(o.contextWindowSize, o.req.StopSequences, o.req.MaxTokens, 0)
		}

		var interceptedName string
		var interceptedInput string
		var streamErr, localStop bool
		var invalidReason string
		var isException bool
		var upstreamMessage string

		err = kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
			if streamErr || localStop {
				return true
			}
			// After a server-tool frame is observed, keep parsing so subsequent
			// meteringEvent / contextUsageEvent frames flow into the accumulator.
			// Upstream errors in the tail must still abort the round.
			if interceptedName != "" {
				if e.Type == kiroproto.EventException || e.Type == kiroproto.EventInvalidState {
					isException = e.Type == kiroproto.EventException
					invalidReason = e.InvalidStateReason
					upstreamMessage = e.ErrorText()
					streamErr = true
					return true
				}
				// Kiro can emit further tool calls after the intercepted one.
				// A second server-tool call is dropped (the executor can repeat
				// it next round); client tool calls flow through HandleEvent so
				// their tool_use blocks are emitted, not lost.
				if e.Type == kiroproto.EventToolUse {
					if e.ToolStop && o.intercepts(e.ToolName) {
						return false
					}
					if sw.HandleEvent(e) {
						localStop = sw.LocalStop()
						streamErr = !localStop
						return true
					}
					return false
				}
				sw.RecordTail(e)
				return false
			}
			if sw.WriteErr() != nil || session.Err() != nil {
				streamErr = true
				return true
			}
			if e.Type == kiroproto.EventToolUse && e.ToolStop && o.intercepts(e.ToolName) {
				interceptedName = e.ToolName
				interceptedInput = e.ToolInput
				return false
			}
			if e.Type == kiroproto.EventException || e.Type == kiroproto.EventInvalidState {
				isException = e.Type == kiroproto.EventException
				invalidReason = e.InvalidStateReason
				upstreamMessage = e.ErrorText()
			}
			shouldStop := sw.HandleEvent(e)
			if sw.WriteErr() != nil || session.Err() != nil {
				streamErr = true
				return true
			}
			if !shouldStop {
				return false
			}
			// Upstream error frames terminate as errors even when a local
			// stop is already latched (e.g. an exception arriving mid-drain
			// after a GPT tool-use max_tokens stop).
			if e.Type == kiroproto.EventException || e.Type == kiroproto.EventInvalidState {
				streamErr = true
				return true
			}
			if sw.LocalStop() {
				localStop = true
				return true
			}
			streamErr = true
			return true
		})
		_ = apiResp.Body.Close()

		if sw.WriteErr() != nil || session.Err() != nil || ctx.Err() != nil {
			return ""
		}

		// A parse error or upstream error frame in the tail must abort the
		// orchestrator even when the server-tool frame was already observed.
		if streamErr {
			classification := classifyUpstreamError(isException, invalidReason, upstreamMessage)
			_ = session.WriteFinalError(classification.final, func() error {
				return sw.WriteError(classification.final.sseType, classification.final.sseMessage)
			})
			return ""
		}
		if err != nil {
			slog.ErrorContext(ctx, "stream error", "trace_id", short, "round", round+1, "err", err)
			final := newStreamFinalError(http.StatusBadGateway, errTypeStreamError, "upstream stream error")
			_ = session.WriteFinalError(final, func() error {
				return sw.WriteError(errTypeStreamError, "upstream stream error")
			})
			return ""
		}

		if interceptedName == "" {
			// streamErr was already handled above; only success/localStop reach here.
			if !localStop {
				if err := sw.Finish(); err != nil {
					return ""
				}
			}
			// Detect empty visible end_turn (thinking-only response) and signal retry.
			if !localStop && sw.IsEmptyVisibleEndTurn() && !session.IsPromoted() {
				session.Discard()
				slog.WarnContext(ctx, "empty visible end_turn detected in server tool loop", "trace_id", short)
				if credits, ok := totals.creditsWith(sw.Credits()); ok {
					logAbortedAttemptCredits(ctx, short, credits, retryReasonEmptyVisibleEndTurn)
				}
				return retryReasonEmptyVisibleEndTurn
			}
			if !session.IsPromoted() {
				if err := session.Promote(); err != nil {
					return ""
				}
			}
			in, out := sw.Usage()
			credits, hasCredits := sw.Credits()
			totalIn, totalOut, totalCredits, totalHasCredits := totals.withCurrent(in, out, credits, hasCredits)
			logResponseStats(ctx, short, totalIn, totalOut, sw.HasContextUsage(), sw.ContextUsagePercentage(), o.contextWindowSize, totalCredits, totalHasCredits)
			return ""
		}

		if interceptedName == advisor.KiroToolName {
			// Advisor detected — run the subcall and emit SSE blocks.
			srvToolUseID := newServerToolUseID()
			sw.WriteServerToolUse(srvToolUseID, o.advCtx.ToolName, "{}")

			outcome := o.consultAdvisor(ctx, short, round, msgs)
			// Advisor tokens are reported separately in usage.iterations[],
			// but its credits are real spend and fold into the round totals.
			totals.addCompleted(0, 0, outcome.credits, outcome.hasCredits)
			if outcome.errorCode != "" {
				sw.WriteAdvisorError(srvToolUseID, outcome.errorCode)
			} else {
				sw.WriteAdvisorResult(srvToolUseID, outcome.text, outcome.stopReason)
				sw.AddAdvisorIteration(o.advCtx.ResponseModel, outcome.inputTokens, outcome.outputTokens)
			}
			if sw.WriteErr() != nil || session.Err() != nil {
				return ""
			}

			msgs = o.appendAdvisorMessages(msgs, srvToolUseID, outcome, sw.RedactedContents())
		} else {
			// ToolSearch detected — execute search and emit SSE blocks.
			query, maxResults, parseErr := parseToolSearchInput(interceptedInput)
			if parseErr != nil {
				slog.WarnContext(ctx, "tool search input parse error", "trace_id", short, "err", parseErr)
				final := newStreamFinalError(http.StatusBadRequest, errTypeInvalidRequest, parseErr.Error())
				_ = session.WriteFinalError(final, func() error {
					return sw.WriteError(errTypeInvalidRequest, parseErr.Error())
				})
				return ""
			}
			srvToolUseID := newServerToolUseID()
			searchInput := buildSearchInput(query, maxResults)

			inputBytes, _ := json.Marshal(searchInput)
			sw.WriteServerToolUse(srvToolUseID, o.tsCtx.SearchToolName, string(inputBytes))

			results, searchErr := o.executeSearch(ctx, short, round, query, maxResults)
			if searchErr != nil {
				sw.WriteToolSearchError(srvToolUseID, toolsearch.ErrorCode(searchErr))
			} else {
				sw.WriteToolSearchResult(srvToolUseID, results)
			}
			if sw.WriteErr() != nil || session.Err() != nil {
				return ""
			}

			msgs = o.appendSearchMessages(msgs, srvToolUseID, searchInput, results, searchErr, nameMap, sw.RedactedContents())
		}

		if sw.HasToolUse() {
			// The same turn carries client tool calls; their tool_use blocks
			// were emitted above and only the client can answer them. Close the
			// message — the server tool's result is already in the stream and
			// returns via history on the next turn.
			endTurn = true
			break
		}
	}

	if !endTurn {
		// Max rounds reached without normal completion.
		slog.WarnContext(ctx, "server tool max rounds reached", "trace_id", short, "max_rounds", o.maxRounds())
	}
	if err := sw.Finish(); err != nil {
		return ""
	}
	if !session.IsPromoted() {
		if err := session.Promote(); err != nil {
			return ""
		}
	}
	in, out := sw.Usage()
	credits, hasCredits := sw.Credits()
	totalIn, totalOut, totalCredits, totalHasCredits := totals.withCurrent(in, out, credits, hasCredits)
	logResponseStats(ctx, short, totalIn, totalOut, sw.HasContextUsage(), sw.ContextUsagePercentage(), o.contextWindowSize, totalCredits, totalHasCredits)
	return ""
}

func (o *serverToolOrchestrator) handleNonStreaming(ctx context.Context, w http.ResponseWriter) string {
	_, short := logging.TraceIDs(ctx)

	msgs := slices.Clone(o.req.Messages)

	var orderedBlocks []any
	var totals roundTotals
	var iterations []respconv.AdvisorIteration
	var lastStopReason string
	var lastStopSequence any

	var normalExit bool
	dropNames := o.dropToolNames()

	for round := range o.maxRounds() {
		payload, nameMap, err := o.buildPayload(msgs)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
			return ""
		}

		apiResp, err := o.service.client.GenerateAssistantResponse(ctx, o.creds.AccessToken, payload, o.creds.Region)
		if err != nil {
			logUpstreamError(ctx, short, err, "round", round+1)
			httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream API error")
			return ""
		}

		acc := respconv.NewNonStreamingAccumulator(o.contextWindowSize, o.req.StopSequences, o.req.MaxTokens, 0)
		acc.SetDropToolNames(dropNames...)
		acc.SetToolNameMap(nameMap.ReverseMap())

		var hasError bool
		var interceptedName string
		var interceptedInput string
		err = kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
			d := acc.ProcessEvent(e)
			if d.IsError {
				hasError = true
				return true
			}
			// Detect the filtered server-tool tool_use via EventDelta; client
			// tool calls are recorded by the accumulator as usual and surface
			// through acc.HasToolUse().
			if d.ToolStop && interceptedName == "" && o.intercepts(d.ToolName) {
				interceptedName = d.ToolName
				interceptedInput = d.ToolInput
			}
			return false
		})
		_ = apiResp.Body.Close()

		// A parse error or upstream error frame aborts the round even when the
		// server-tool frame was already observed — mirroring the streaming
		// path, which never runs an intercepted tool after a tail error.
		if err != nil || hasError {
			httpx.WriteError(w, http.StatusBadGateway, errTypeAPI, "upstream error")
			return ""
		}

		resp, stats := acc.BuildResponse(o.responseModel)
		totals.addCompleted(stats.InputTokens, stats.OutputTokens, stats.Credits, stats.HasCredits)
		lastStopReason, _ = resp["stop_reason"].(string)
		lastStopSequence = resp["stop_sequence"]

		// Extract content blocks (intercepted server tools won't appear here
		// since they're filtered).
		content, _ := resp["content"].([]any)
		orderedBlocks = append(orderedBlocks, content...)

		if interceptedName == "" {
			// Detect empty visible end_turn (thinking-only response) and signal retry.
			if acc.IsEmptyVisibleEndTurn() {
				slog.WarnContext(ctx, "empty visible end_turn detected in server tool loop", "trace_id", short)
				if totals.hasCredits {
					logAbortedAttemptCredits(ctx, short, totals.credits, retryReasonEmptyVisibleEndTurn)
				}
				return retryReasonEmptyVisibleEndTurn
			}
			normalExit = true
			break
		}

		if interceptedName == advisor.KiroToolName {
			// Advisor detected — run the subcall and append blocks.
			srvToolUseID := newServerToolUseID()
			outcome := o.consultAdvisor(ctx, short, round, msgs)
			// Advisor tokens are reported separately in usage.iterations[],
			// but its credits are real spend and fold into the round totals.
			totals.addCompleted(0, 0, outcome.credits, outcome.hasCredits)

			orderedBlocks = append(orderedBlocks, respconv.ServerToolUseBlock(srvToolUseID, o.advCtx.ToolName, nil))
			if outcome.errorCode != "" {
				orderedBlocks = append(orderedBlocks, respconv.AdvisorErrorBlock(srvToolUseID, outcome.errorCode))
			} else {
				orderedBlocks = append(orderedBlocks, respconv.AdvisorResultBlock(srvToolUseID, outcome.text, outcome.stopReason))
				iterations = append(iterations, respconv.AdvisorIteration{
					Model:        o.advCtx.ResponseModel,
					InputTokens:  outcome.inputTokens,
					OutputTokens: outcome.outputTokens,
				})
			}

			msgs = o.appendAdvisorMessages(msgs, srvToolUseID, outcome, acc.RedactedContents())
		} else {
			// Execute search.
			query, maxResults, parseErr := parseToolSearchInput(interceptedInput)
			if parseErr != nil {
				slog.WarnContext(ctx, "tool search input parse error", "trace_id", short, "err", parseErr)
				httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, parseErr.Error())
				return ""
			}

			srvToolUseID := newServerToolUseID()
			results, searchErr := o.executeSearch(ctx, short, round, query, maxResults)

			// Add server_tool_use + tool_search_tool_result blocks.
			searchInput := buildSearchInput(query, maxResults)
			orderedBlocks = append(orderedBlocks, respconv.ServerToolUseBlock(srvToolUseID, o.tsCtx.SearchToolName, searchInput))
			if searchErr != nil {
				orderedBlocks = append(orderedBlocks, respconv.ToolSearchErrorBlock(srvToolUseID, toolsearch.ErrorCode(searchErr)))
			} else {
				orderedBlocks = append(orderedBlocks, respconv.ToolSearchResultBlock(srvToolUseID, results))
			}

			msgs = o.appendSearchMessages(msgs, srvToolUseID, searchInput, results, searchErr, nameMap, acc.RedactedContents())
		}

		if acc.HasToolUse() {
			// Client tool calls were recorded in this round's content and only
			// the client can answer them; close the message. The server tool's
			// result returns via history on the next turn.
			normalExit = true
			break
		}
	}

	// Max rounds reached without normal completion.
	if !normalExit {
		slog.WarnContext(ctx, "server tool max rounds reached", "trace_id", short, "max_rounds", o.maxRounds())
	}

	// Build final response.
	usage := map[string]any{
		"input_tokens":                totals.inputTokens,
		"output_tokens":               totals.outputTokens,
		"cache_read_input_tokens":     0,
		"cache_creation_input_tokens": 0,
	}
	if iters := respconv.IterationMaps(iterations); iters != nil {
		usage["iterations"] = iters
	}
	finalResp := map[string]any{
		"id":            "msg_" + uuid.New().String()[:24],
		"type":          "message",
		"role":          "assistant",
		"content":       orderedBlocks,
		"model":         o.responseModel,
		"stop_reason":   lastStopReason,
		"stop_sequence": lastStopSequence,
		"usage":         usage,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, finalResp); err != nil {
		slog.ErrorContext(ctx, "write non-streaming response failed", "err", err)
	}
	_, _ = w.Write([]byte("\n"))

	logResponseStats(ctx, short, totals.inputTokens, totals.outputTokens, false, 0, o.contextWindowSize, totals.credits, totals.hasCredits)
	return ""
}

// executeSearch runs the tool search, promotes results, and logs.
func (o *serverToolOrchestrator) executeSearch(ctx context.Context, short string, round int, query string, maxResults int) ([]string, error) {
	results, err := toolsearch.Search(query, o.tsCtx.DeferredTools, o.tsCtx.SearchType, maxResults)
	if err == nil {
		o.tsCtx.PromoteTools(results)
	}
	slog.InfoContext(ctx, "tool search executed",
		"trace_id", short, "round", round+1, "query", query, "results", results,
	)
	return results, err
}

// appendSearchMessages appends the server_tool_use + tool_result messages to the conversation.
// The result text is rendered by reqconv.ServerToolResultText, the same function
// that renders this round when it is replayed from client-supplied history, so
// the executor sees identical text either way. Tool names are shortened via
// nameMap so Kiro sees names matching the tool schema.
func (o *serverToolOrchestrator) appendSearchMessages(msgs []anthropic.Message, srvToolUseID string, searchInput map[string]any, results []string, searchErr error, nameMap *reqconv.ToolNameMap, redacted []string) []anthropic.Message {
	var inner anthropic.ContentBlock
	isError := searchErr != nil
	if isError {
		inner = anthropic.ContentBlock{
			Type:      anthropic.BlockTypeToolSearchResultError,
			ErrorCode: toolsearch.ErrorCode(searchErr),
		}
	} else {
		refs := make([]anthropic.ContentBlock, 0, len(results))
		for _, name := range results {
			refs = append(refs, anthropic.ContentBlock{
				Type:     anthropic.BlockTypeToolReference,
				ToolName: nameMap.Shorten(name),
			})
		}
		inner = anthropic.ContentBlock{
			Type:           anthropic.BlockTypeToolSearchSearchResult,
			ToolReferences: refs,
		}
	}
	resultText := reqconv.ServerToolResultText(anthropic.ContentBlock{
		Type:    anthropic.BlockTypeToolSearchToolResult,
		Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{inner}},
	})
	return appendServerToolRound(msgs, srvToolUseID, toolsearch.KiroToolSearchName, searchInput, resultText, isError, redacted)
}

// appendServerToolRound records one intercepted server-tool round in the
// working conversation so the next executor round sees the call answered:
// assistant(server_tool_use) then user(tool_result). redacted carries the
// round's trailing reasoning blobs (GPT 5.6); they are replayed as
// redacted_thinking blocks so buildHistory can attach the blob to the
// in-flight tool round in the next request.
func appendServerToolRound(msgs []anthropic.Message, srvToolUseID, toolName string, input map[string]any, resultText string, isError bool, redacted []string) []anthropic.Message {
	assistantBlocks := make([]anthropic.ContentBlock, 0, len(redacted)+1)
	for _, data := range redacted {
		assistantBlocks = append(assistantBlocks, anthropic.ContentBlock{Type: anthropic.BlockTypeRedactedThinking, Data: data})
	}
	assistantBlocks = append(assistantBlocks, anthropic.ContentBlock{Type: anthropic.BlockTypeServerToolUse, ID: srvToolUseID, Name: toolName, Input: input})
	return append(msgs,
		anthropic.Message{
			Role:    "assistant",
			Content: anthropic.MessageContent{Blocks: assistantBlocks},
		},
		anthropic.Message{
			Role: "user",
			Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
				{Type: anthropic.BlockTypeToolResult, ToolUseID: srvToolUseID, Content: anthropic.MessageContent{Text: resultText}, IsError: isError},
			}},
		},
	)
}

// buildSearchInput constructs the input map for a ToolSearch tool_use.
func buildSearchInput(query string, maxResults int) map[string]any {
	input := map[string]any{"query": query}
	if maxResults > 0 {
		input["max_results"] = maxResults
	}
	return input
}

func (o *serverToolOrchestrator) buildPayload(msgs []anthropic.Message) (*kiroproto.Payload, *reqconv.ToolNameMap, error) {
	tmpReq := *o.req
	tmpReq.Messages = msgs
	return reqconv.BuildPayload(&tmpReq, o.buildOpts)
}

// parseToolSearchInput extracts query and max_results from the ToolSearch tool input JSON.
// Returns an error if the input is not valid JSON.
func parseToolSearchInput(input string) (query string, maxResults int, err error) {
	var parsed struct {
		Query      string  `json:"query"`
		MaxResults float64 `json:"max_results"`
	}
	if uerr := json.Unmarshal([]byte(input), &parsed); uerr != nil {
		return "", 0, fmt.Errorf("parse tool_search input: %w", uerr)
	}
	return parsed.Query, int(parsed.MaxResults), nil
}
