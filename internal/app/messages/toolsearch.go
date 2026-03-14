package messages

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/respconv"
	"github.com/d-kuro/kirocc/internal/toolsearch"
	"github.com/google/uuid"
)

const maxToolSearchRounds = 3

// toolSearchOrchestrator manages the inner loop for tool search.
type toolSearchOrchestrator struct {
	service           *Service
	tsCtx             *toolsearch.Context
	req               *anthropic.Request
	creds             *auth.Credentials
	buildOpts         reqconv.BuildOptions
	contextWindowSize int
}

func (o *toolSearchOrchestrator) handleStreaming(ctx context.Context, w http.ResponseWriter) string {
	short := logging.ShortTraceID(logging.TraceIDFromContext(ctx))

	gw := NewGateWriter(w)
	sw := respconv.NewSSEWriter(ctx, gw, o.buildOpts.ModelID, o.contextWindowSize, o.req.StopSequences, o.req.MaxTokens, 0)
	sw.OnVisibleOutput = func() { gw.Promote() }

	msgs := slices.Clone(o.req.Messages)

	var cumulativeInputTokens, cumulativeOutputTokens int

	for round := range maxToolSearchRounds {
		payload, err := o.buildPayload(msgs)
		if err != nil {
			slog.WarnContext(ctx, "tool search payload build error", "trace_id", short, "err", err)
			writeStreamingOrJSONError(gw, sw, w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
			return ""
		}

		apiResp, err := o.service.client.GenerateAssistantResponse(ctx, o.creds.AccessToken, payload, o.creds.Region)
		if err != nil {
			slog.ErrorContext(ctx, "kiro api error", "trace_id", short, "round", round+1, "err", err)
			writeStreamingOrJSONError(gw, sw, w, http.StatusBadGateway, errTypeAPI, "upstream API error")
			return ""
		}

		if round > 0 {
			// Accumulate usage from previous round before resetting.
			in, out := sw.Usage()
			cumulativeInputTokens += in
			cumulativeOutputTokens += out
			sw.ResetAccumulator(o.contextWindowSize, o.req.StopSequences, o.req.MaxTokens, 0)
		}
		sw.SetFilterToolName(toolsearch.KiroToolSearchName)

		var foundToolSearch bool
		var toolSearchInput string
		var streamErr, localStop bool

		err = kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
			if streamErr || localStop || foundToolSearch {
				return true
			}
			if sw.WriteErr() {
				streamErr = true
				return true
			}
			if e.Type == kiroproto.EventToolUse && e.ToolStop && e.ToolName == toolsearch.KiroToolSearchName {
				foundToolSearch = true
				toolSearchInput = e.ToolInput
				return true
			}
			shouldStop := sw.HandleEvent(e)
			if sw.WriteErr() {
				streamErr = true
				return true
			}
			if !shouldStop {
				return false
			}
			if sw.LocalStop() {
				localStop = true
				return true
			}
			streamErr = true
			return true
		})
		_ = apiResp.Body.Close()

		if err != nil && !foundToolSearch {
			slog.ErrorContext(ctx, "stream error", "trace_id", short, "round", round+1, "err", err)
			writeStreamingOrJSONError(gw, sw, w, http.StatusBadGateway, errTypeAPI, "upstream stream error")
			return ""
		}

		if !foundToolSearch {
			if !streamErr && !localStop {
				sw.Finish()
			}
			// Detect empty visible end_turn (thinking-only response) and signal retry.
			if !streamErr && !localStop && sw.IsEmptyVisibleEndTurn() && !gw.IsPromoted() {
				gw.Discard()
				slog.WarnContext(ctx, "empty visible end_turn detected in tool search", "trace_id", short)
				return retryReasonEmptyVisibleEndTurn
			}
			if streamErr && !gw.IsPromoted() {
				gw.Discard()
				WriteErrorJSON(w, http.StatusBadGateway, errTypeAPI, "upstream stream error")
			}
			if !streamErr {
				inputTokens, outputTokens := sw.Usage()
				logResponseStats(ctx, short, inputTokens+cumulativeInputTokens, outputTokens+cumulativeOutputTokens, sw.HasContextUsage(), sw.ContextUsagePercentage(), o.contextWindowSize)
			}
			return ""
		}

		// ToolSearch detected — execute search and emit SSE blocks.
		query, maxResults := parseToolSearchInput(toolSearchInput)
		srvToolUseID := "srvtoolu_" + uuid.New().String()[:24]
		searchInput := buildSearchInput(query, maxResults)

		inputBytes, _ := json.Marshal(searchInput)
		sw.WriteServerToolUse(srvToolUseID, o.tsCtx.SearchToolName, string(inputBytes))

		results, searchErr := o.executeSearch(ctx, short, round, query, maxResults)
		if searchErr != nil {
			sw.WriteToolSearchError(srvToolUseID, toolsearch.ErrorCode(searchErr))
		} else {
			sw.WriteToolSearchResult(srvToolUseID, results)
		}

		msgs = o.appendSearchMessages(msgs, srvToolUseID, searchInput, results, searchErr)
	}

	// Max rounds reached without normal completion.
	slog.WarnContext(ctx, "tool search max rounds reached", "trace_id", short, "max_rounds", maxToolSearchRounds)
	sw.Finish()
	inputTokens, outputTokens := sw.Usage()
	logResponseStats(ctx, short, inputTokens+cumulativeInputTokens, outputTokens+cumulativeOutputTokens, sw.HasContextUsage(), sw.ContextUsagePercentage(), o.contextWindowSize)
	return ""
}

func (o *toolSearchOrchestrator) handleNonStreaming(ctx context.Context, w http.ResponseWriter) string {
	short := logging.ShortTraceID(logging.TraceIDFromContext(ctx))

	msgs := slices.Clone(o.req.Messages)

	var orderedBlocks []any
	var totalInputTokens, totalOutputTokens int
	var lastStopReason string
	var lastStopSequence any

	var normalExit bool

	for round := range maxToolSearchRounds {
		payload, err := o.buildPayload(msgs)
		if err != nil {
			WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
			return ""
		}

		apiResp, err := o.service.client.GenerateAssistantResponse(ctx, o.creds.AccessToken, payload, o.creds.Region)
		if err != nil {
			slog.ErrorContext(ctx, "kiro api error", "trace_id", short, "round", round+1, "err", err)
			WriteErrorJSON(w, http.StatusBadGateway, errTypeAPI, "upstream API error")
			return ""
		}

		acc := respconv.NewNonStreamingAccumulator(o.contextWindowSize, o.req.StopSequences, o.req.MaxTokens, 0)
		acc.SetFilterToolName(toolsearch.KiroToolSearchName)

		var hasError bool
		var foundToolSearch bool
		var nsToolSearchInput string
		err = kiroproto.ParseStream(ctx, apiResp.Body, func(e kiroproto.Event) bool {
			d := acc.ProcessEvent(e)
			if d.IsError {
				hasError = true
				return true
			}
			// Detect filtered ToolSearch tool_use via EventDelta.
			if d.ToolStop && d.ToolName == toolsearch.KiroToolSearchName {
				foundToolSearch = true
				nsToolSearchInput = d.ToolInput
			}
			return false
		})
		_ = apiResp.Body.Close()

		if (err != nil || hasError) && !foundToolSearch {
			WriteErrorJSON(w, http.StatusBadGateway, errTypeAPI, "upstream error")
			return ""
		}

		resp, stats := acc.BuildResponse(o.buildOpts.ModelID)
		totalInputTokens += stats.InputTokens
		totalOutputTokens += stats.OutputTokens
		lastStopReason, _ = resp["stop_reason"].(string)
		lastStopSequence = resp["stop_sequence"]

		// Extract content blocks (ToolSearch won't appear here since it's filtered).
		content, _ := resp["content"].([]any)
		orderedBlocks = append(orderedBlocks, content...)

		if !foundToolSearch {
			// Detect empty visible end_turn (thinking-only response) and signal retry.
			if acc.IsEmptyVisibleEndTurn() {
				slog.WarnContext(ctx, "empty visible end_turn detected in tool search", "trace_id", short)
				return retryReasonEmptyVisibleEndTurn
			}
			normalExit = true
			break
		}

		// Execute search.
		query, maxResults := parseToolSearchInput(nsToolSearchInput)

		srvToolUseID := "srvtoolu_" + uuid.New().String()[:24]
		results, searchErr := o.executeSearch(ctx, short, round, query, maxResults)

		// Add server_tool_use block.
		searchInput := buildSearchInput(query, maxResults)
		orderedBlocks = append(orderedBlocks, map[string]any{
			"type":  anthropic.BlockTypeServerToolUse,
			"id":    srvToolUseID,
			"name":  o.tsCtx.SearchToolName,
			"input": searchInput,
		})

		// Add tool_search_tool_result block.
		if searchErr != nil {
			orderedBlocks = append(orderedBlocks, map[string]any{
				"type":        anthropic.BlockTypeToolSearchToolResult,
				"tool_use_id": srvToolUseID,
				"content": map[string]any{
					"type":       anthropic.BlockTypeToolSearchResultError,
					"error_code": toolsearch.ErrorCode(searchErr),
				},
			})
		} else {
			orderedBlocks = append(orderedBlocks, map[string]any{
				"type":        anthropic.BlockTypeToolSearchToolResult,
				"tool_use_id": srvToolUseID,
				"content": map[string]any{
					"type":            anthropic.BlockTypeToolSearchSearchResult,
					"tool_references": toolsearch.ToolRefMaps(results),
				},
			})
		}

		msgs = o.appendSearchMessages(msgs, srvToolUseID, searchInput, results, searchErr)
	}

	// Max rounds reached without normal completion.
	if !normalExit {
		slog.WarnContext(ctx, "tool search max rounds reached", "trace_id", short, "max_rounds", maxToolSearchRounds)
	}

	// Build final response.
	finalResp := map[string]any{
		"id":            "msg_" + uuid.New().String()[:24],
		"type":          "message",
		"role":          "assistant",
		"content":       orderedBlocks,
		"model":         o.buildOpts.ModelID,
		"stop_reason":   lastStopReason,
		"stop_sequence": lastStopSequence,
		"usage": map[string]any{
			"input_tokens":                totalInputTokens,
			"output_tokens":               totalOutputTokens,
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 0,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, finalResp); err != nil {
		slog.ErrorContext(ctx, "write non-streaming response failed", "err", err)
	}
	_, _ = w.Write([]byte("\n"))

	logResponseStats(ctx, short, totalInputTokens, totalOutputTokens, false, 0, o.contextWindowSize)
	return ""
}

// executeSearch runs the tool search, promotes results, and logs.
func (o *toolSearchOrchestrator) executeSearch(ctx context.Context, short string, round int, query string, maxResults int) ([]string, error) {
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
// On error, the tool_result contains the error message instead of tool references.
func (o *toolSearchOrchestrator) appendSearchMessages(msgs []anthropic.Message, srvToolUseID string, searchInput map[string]any, results []string, searchErr error) []anthropic.Message {
	var resultContent anthropic.MessageContent
	var isError bool
	if searchErr != nil {
		isError = true
		resultContent = anthropic.MessageContent{Text: "tool search error: " + toolsearch.ErrorCode(searchErr)}
	} else {
		// Use text content so Kiro's tool_result conversion preserves the result.
		// tool_reference blocks would be dropped by extractToolResultContentText.
		resultContent = anthropic.MessageContent{Text: "Found tools: " + strings.Join(results, ", ")}
	}
	return append(msgs,
		anthropic.Message{
			Role: "assistant",
			Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
				{Type: anthropic.BlockTypeServerToolUse, ID: srvToolUseID, Name: toolsearch.KiroToolSearchName, Input: searchInput},
			}},
		},
		anthropic.Message{
			Role: "user",
			Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
				{Type: anthropic.BlockTypeToolResult, ToolUseID: srvToolUseID, Content: resultContent, IsError: isError},
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

func (o *toolSearchOrchestrator) buildPayload(msgs []anthropic.Message) (*kiroproto.Payload, error) {
	tmpReq := *o.req
	tmpReq.Messages = msgs
	return reqconv.BuildPayload(&tmpReq, o.buildOpts)
}

// writeStreamingOrJSONError writes an error via SSE if the stream is promoted, otherwise via JSON.
func writeStreamingOrJSONError(gw *GateWriter, sw *respconv.SSEWriter, w http.ResponseWriter, status int, errType, message string) {
	if sw.Started() && gw.IsPromoted() {
		sw.WriteError(errType, message)
		return
	}
	if !gw.IsPromoted() {
		gw.Discard()
	}
	WriteErrorJSON(w, status, errType, message)
}

// parseToolSearchInput extracts query and max_results from the ToolSearch tool input JSON.
func parseToolSearchInput(input string) (query string, maxResults int) {
	var parsed struct {
		Query      string  `json:"query"`
		MaxResults float64 `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return input, 0
	}
	return parsed.Query, int(parsed.MaxResults)
}
