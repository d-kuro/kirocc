package reqconv

import (
	"fmt"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/toolsearch"
	"github.com/google/uuid"
)

// defaultThinkingBudget is the default thinking token budget (medium).
const defaultThinkingBudget = anthropic.ThinkingBudgetMedium

// BuildOptions controls how an Anthropic request is mapped to a Kiro payload.
type BuildOptions struct {
	ProfileARN     string
	ModelID        string
	ConversationID string
	Thinking       bool
	ThinkingBudget int
	ToolSearchCtx  *toolsearch.Context
}

// BuildPayload converts an Anthropic request into a Kiro API payload.
func BuildPayload(req *anthropic.Request, options BuildOptions) (*kiroproto.Payload, *ToolNameMap, error) {
	nameMap := NewToolNameMap()

	// 1. Build system prompt and convert tools.
	systemPrompt, toolEntries := buildSystemAndTools(req, options.ToolSearchCtx, nameMap)

	// 2. Normalize and split messages.
	hasTools := len(req.Tools) > 0
	if options.ToolSearchCtx != nil {
		hasTools = true
	}
	msgs := Normalize(req.Messages, hasTools)
	historyMsgs, lastMsg := splitMessages(msgs)

	// 3. Build history and place system prompt.
	history := buildHistory(historyMsgs, nameMap)
	history, lastContent := placeSystemPrompt(systemPrompt, history, ExtractTextContent(lastMsg.Content))

	// 4. Build currentMessage.
	// Extract tool_use IDs from the preceding assistant message for reordering tool results.
	var precedingToolUseIDs []string
	if len(historyMsgs) > 0 {
		precedingToolUseIDs = extractToolUseIDs(historyMsgs[len(historyMsgs)-1])
	}
	userInputMessage := buildCurrentMessage(lastMsg, lastContent, options.ModelID, toolEntries, options.Thinking, options.ThinkingBudget, precedingToolUseIDs)

	convState := kiroproto.ConversationState{
		ConversationID:  options.ConversationID,
		ChatTriggerType: kiroproto.ChatTriggerTypeManual,
		AgentTaskType:   kiroproto.AgentTaskTypeVibe,
		CurrentMessage:  kiroproto.CurrentMessage{UserInputMessage: userInputMessage},
	}
	if len(history) > 0 {
		convState.History = history
	}
	payload := &kiroproto.Payload{ConversationState: convState}
	if options.ProfileARN != "" {
		payload.ProfileARN = options.ProfileARN
	}
	return payload, nameMap, nil
}

// buildSystemAndTools extracts the system prompt and converts tools.
func buildSystemAndTools(req *anthropic.Request, tsCtx *toolsearch.Context, nameMap *ToolNameMap) (string, []kiroproto.ToolEntry) {
	systemPrompt := ExtractSystemPrompt(req.System)

	var toolEntries []kiroproto.ToolEntry
	if tsCtx != nil {
		// Tool search mode: convert only active tools, inject ToolSearch tool.
		toolEntries = ConvertTools(tsCtx.ActiveTools, nameMap)
		toolEntries = ApplyToolCachePoints(tsCtx.ActiveTools, toolEntries)
		toolEntries = append(toolEntries, toolsearch.KiroToolSearchEntry())
	} else if len(req.Tools) > 0 {
		toolEntries = ConvertTools(req.Tools, nameMap)
		toolEntries = ApplyToolCachePoints(req.Tools, toolEntries)
	}
	return systemPrompt, toolEntries
}

// splitMessages splits normalized messages into history messages and the last message.
// If the last message is from the assistant, all messages go to history and a
// synthetic "Continue" user message is returned.
func splitMessages(msgs []anthropic.Message) (history []anthropic.Message, last anthropic.Message) {
	if len(msgs) == 0 {
		return nil, anthropic.Message{}
	}
	if msgs[len(msgs)-1].Role == "assistant" {
		return msgs, anthropic.Message{
			Role:    "user",
			Content: anthropic.MessageContent{Text: syntheticContinue},
		}
	}
	return msgs[:len(msgs)-1], msgs[len(msgs)-1]
}

// syntheticAck is the synthetic assistant acknowledgment that kiro-cli always
// inserts after the system prompt in history. v2 captures confirm this is present
// in every request.
const syntheticAck = "I will fully incorporate this information when generating my responses, and explicitly acknowledge relevant parts of the summary when answering questions."

// syntheticAckMessageID is a deterministic UUID for the synthetic ack, computed once since the input is constant.
var syntheticAckMessageID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("synthetic-ack:"+syntheticAck)).String()

// placeSystemPrompt inserts the system prompt as a dedicated history entry pair
// (user message + synthetic assistant ack), matching the v2 kiro-cli structure.
// v2 captures show this pair is present in every request, even the first one.
// Returns a new history slice (original is not mutated) and the updated lastContent.
func placeSystemPrompt(systemPrompt string, history []kiroproto.HistoryEntry, lastContent string) ([]kiroproto.HistoryEntry, string) {
	if systemPrompt == "" {
		return history, lastContent
	}
	// Always build the system prompt pair: user message + synthetic assistant ack.
	systemPair := []kiroproto.HistoryEntry{
		{UserInputMessage: &kiroproto.HistoryUserInputMessage{
			Content: systemPrompt,
			Origin:  kiroproto.OriginKiroCLI,
		}},
		{AssistantResponseMessage: &kiroproto.AssistantResponseMessage{
			MessageID: syntheticAckMessageID,
			Content:   syntheticAck,
		}},
	}
	newHistory := make([]kiroproto.HistoryEntry, 0, len(systemPair)+len(history))
	newHistory = append(newHistory, systemPair...)
	newHistory = append(newHistory, history...)
	return newHistory, lastContent
}

// buildCurrentMessage constructs the Kiro UserInputMessage from the last Anthropic message.
func buildCurrentMessage(lastMsg anthropic.Message, lastContent, modelID string, toolEntries []kiroproto.ToolEntry, thinking bool, thinkingBudget int, precedingToolUseIDs []string) kiroproto.UserInputMessage {
	msg := kiroproto.UserInputMessage{
		Content: lastContent,
		ModelID: modelID,
		Origin:  kiroproto.OriginKiroCLI,
	}

	// Single-pass scan of lastMsg.Content to extract both tool_results and images.
	toolResults, images := scanCurrentMessage(lastMsg.Content)
	toolResults = ReorderToolResults(toolResults, precedingToolUseIDs)
	if len(toolEntries) > 0 || len(toolResults) > 0 {
		ctx := &kiroproto.UserInputMessageContext{}
		if len(toolEntries) > 0 {
			ctx.Tools = toolEntries
		}
		if len(toolResults) > 0 {
			ctx.ToolResults = toolResults
		}
		msg.UserInputMessageContext = ctx
	}

	// Match the observed kiro-cli continuation shape:
	// tool-result-only turns keep empty currentMessage.content instead of "Continue".
	if msg.Content == "" && len(toolResults) == 0 {
		msg.Content = syntheticContinue
	}

	if len(images) > 0 {
		msg.Images = images
	}

	// Inject thinking mode XML tags after all content decisions are finalized.
	// Skip injection for tool-result-only continuations (content="" with toolResults)
	// to preserve the empty content shape that upstream expects.
	if thinking && (msg.Content != "" || len(toolResults) == 0) {
		budget := thinkingBudget
		if budget <= 0 {
			budget = defaultThinkingBudget
		}
		prefix := fmt.Sprintf("<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>%d</max_thinking_length>", budget)
		if msg.Content != "" {
			msg.Content = prefix + "\n\n" + msg.Content
		} else {
			msg.Content = prefix
		}
	}

	return msg
}
