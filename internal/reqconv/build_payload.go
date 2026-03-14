package reqconv

import (
	"fmt"
	"log/slog"
	"strings"

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
	EnvState       *kiroproto.EnvState
	ToolSearchCtx  *toolsearch.Context
}

// BuildPayload converts an Anthropic request into a Kiro API payload.
func BuildPayload(req *anthropic.Request, options BuildOptions) (*kiroproto.Payload, error) {
	// 1. Build system prompt and convert tools.
	systemPrompt, toolEntries, err := buildSystemAndTools(req, options.ToolSearchCtx)
	if err != nil {
		return nil, err
	}

	// 2. Normalize and split messages.
	hasTools := len(req.Tools) > 0
	if options.ToolSearchCtx != nil {
		hasTools = true
	}
	msgs := Normalize(req.Messages, hasTools)
	historyMsgs, lastMsg := splitMessages(msgs)

	// 3. Build history and place system prompt.
	history := buildHistory(historyMsgs, options.EnvState)
	history, lastContent := placeSystemPrompt(systemPrompt, history, ExtractTextContent(lastMsg.Content), options.EnvState)

	// 4. Build currentMessage.
	// Extract tool_use IDs from the preceding assistant message for reordering tool results.
	var precedingToolUseIDs []string
	if len(historyMsgs) > 0 {
		precedingToolUseIDs = extractToolUseIDs(historyMsgs[len(historyMsgs)-1])
	}
	userInputMessage := buildCurrentMessage(lastMsg, lastContent, options.ModelID, toolEntries, options.Thinking, options.ThinkingBudget, options.EnvState, precedingToolUseIDs)

	// 5. Apply cache points.
	ApplySystemCachePoints(req.System, history, &userInputMessage)

	// 6. Assemble payload.
	// Auto-generate conversationID if not provided.
	conversationID := options.ConversationID
	if conversationID == "" {
		conversationID = buildConversationID(options.ModelID, systemPrompt, req.Messages)
	}
	// Find the last user-initiated message content (not tool-result continuations)
	// to make agentContinuationID change per utterance but stable for continuations.
	lastUserContent := findLastUserUtterance(msgs)
	convState := kiroproto.ConversationState{
		ConversationID:  conversationID,
		ChatTriggerType: kiroproto.ChatTriggerTypeManual,
		AgentTaskType:   kiroproto.AgentTaskTypeVibe,
		CurrentMessage:  kiroproto.CurrentMessage{UserInputMessage: userInputMessage},
	}
	if len(history) > 0 {
		convState.History = history
	}
	convState.AgentContinuationID = buildAgentContinuationID(conversationID, lastUserContent)
	payload := &kiroproto.Payload{ConversationState: convState}
	if options.ProfileARN != "" {
		payload.ProfileARN = options.ProfileARN
	}
	return payload, nil
}

// buildSystemAndTools extracts the system prompt and converts tools.
func buildSystemAndTools(req *anthropic.Request, tsCtx *toolsearch.Context) (string, []kiroproto.ToolEntry, error) {
	systemPrompt := ExtractSystemPrompt(req.System)

	var toolEntries []kiroproto.ToolEntry
	if tsCtx != nil {
		// Tool search mode: convert only active tools, inject ToolSearch tool.
		var err error
		toolEntries, err = ConvertTools(tsCtx.ActiveTools)
		if err != nil {
			return "", nil, err
		}
		toolEntries = ApplyToolCachePoints(tsCtx.ActiveTools, toolEntries)
		toolEntries = append(toolEntries, toolsearch.KiroToolSearchEntry())
	} else if len(req.Tools) > 0 {
		var err error
		toolEntries, err = ConvertTools(req.Tools)
		if err != nil {
			return "", nil, err
		}
		toolEntries = ApplyToolCachePoints(req.Tools, toolEntries)
	}
	return systemPrompt, toolEntries, nil
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

// placeSystemPrompt inserts the system prompt as a dedicated history entry pair
// (user message + synthetic assistant ack), matching the v2 kiro-cli structure.
// v2 captures show this pair is present in every request, even the first one.
// Returns a new history slice (original is not mutated) and the updated lastContent.
func placeSystemPrompt(systemPrompt string, history []kiroproto.HistoryEntry, lastContent string, envState *kiroproto.EnvState) ([]kiroproto.HistoryEntry, string) {
	if systemPrompt == "" {
		return history, lastContent
	}
	// Always build the system prompt pair: user message + synthetic assistant ack.
	// v2 captures show envState is present on ALL user messages, including the system prompt entry.
	systemPair := []kiroproto.HistoryEntry{
		{UserInputMessage: &kiroproto.HistoryUserInputMessage{
			Content: systemPrompt,
			Origin:  kiroproto.OriginKiroCLI,
			UserInputMessageContext: &kiroproto.UserInputMessageContext{
				EnvState: envState,
			},
		}},
		{AssistantResponseMessage: &kiroproto.AssistantResponseMessage{
			Content: syntheticAck,
		}},
	}
	newHistory := make([]kiroproto.HistoryEntry, 0, len(systemPair)+len(history))
	newHistory = append(newHistory, systemPair...)
	newHistory = append(newHistory, history...)
	return newHistory, lastContent
}

// buildCurrentMessage constructs the Kiro UserInputMessage from the last Anthropic message.
func buildCurrentMessage(lastMsg anthropic.Message, lastContent, modelID string, toolEntries []kiroproto.ToolEntry, thinking bool, thinkingBudget int, envState *kiroproto.EnvState, precedingToolUseIDs []string) kiroproto.UserInputMessage {
	msg := kiroproto.UserInputMessage{
		Content: lastContent,
		ModelID: modelID,
		Origin:  kiroproto.OriginKiroCLI,
	}

	// Add tools, tool results, and env state to context.
	toolResults := ExtractToolResults(lastMsg.Content)
	toolResults = ReorderToolResults(toolResults, precedingToolUseIDs)
	ctx := &kiroproto.UserInputMessageContext{
		EnvState: envState,
	}
	if len(toolEntries) > 0 {
		ctx.Tools = toolEntries
	}
	if len(toolResults) > 0 {
		ctx.ToolResults = toolResults
	}
	msg.UserInputMessageContext = ctx

	// Match the observed kiro-cli continuation shape:
	// tool-result-only turns keep empty currentMessage.content instead of "Continue".
	if msg.Content == "" && len(toolResults) == 0 {
		msg.Content = syntheticContinue
	}

	// Add images.
	if images := ExtractImages(lastMsg.Content); len(images) > 0 {
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

// buildAgentContinuationID generates a deterministic agent continuation ID.
// v2 captures show this changes per user utterance but stays the same for
// tool-result continuations within the same utterance.
func buildAgentContinuationID(conversationID, lastUserContent string) string {
	if conversationID == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("agent-continuation:"+conversationID+":"+lastUserContent)).String()
}

// findLastUserUtterance returns the text content of the last user message that
// is a real utterance (has non-empty text content, not a tool-result-only continuation).
// This is used to make agentContinuationID change per utterance but stay stable
// for tool-result continuations within the same utterance.
func findLastUserUtterance(msgs []anthropic.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			text := ExtractTextContent(msgs[i].Content)
			if text != "" {
				return text
			}
		}
	}
	return ""
}

// extractToolUseIDs returns the IDs of all tool_use blocks in a message's content.
func extractToolUseIDs(msg anthropic.Message) []string {
	if msg.Content.IsString() {
		return nil
	}
	var ids []string
	for _, b := range msg.Content.Blocks {
		if b.IsToolUse() {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

// buildHistory converts normalized Anthropic messages to Kiro history entries.
func buildHistory(msgs []anthropic.Message, envState *kiroproto.EnvState) []kiroproto.HistoryEntry {
	var history []kiroproto.HistoryEntry

	for i, msg := range msgs {
		switch msg.Role {
		case "user":
			content := ExtractTextContent(msg.Content)
			userMsg := &kiroproto.HistoryUserInputMessage{
				Content: content,
				Origin:  kiroproto.OriginKiroCLI,
			}
			// Warn if images are present in history — Kiro history type does not support images.
			if images := ExtractImages(msg.Content); len(images) > 0 {
				slog.Warn("images in history messages are not supported and will be dropped", "image_count", len(images))
			}
			toolResults := ExtractToolResults(msg.Content)
			// Reorder tool results to match the preceding assistant's tool_use order.
			if len(toolResults) > 1 && i > 0 && msgs[i-1].Role == "assistant" {
				toolResults = ReorderToolResults(toolResults, extractToolUseIDs(msgs[i-1]))
			}
			if envState != nil || len(toolResults) > 0 {
				ctx := &kiroproto.UserInputMessageContext{EnvState: envState}
				if len(toolResults) > 0 {
					ctx.ToolResults = toolResults
				}
				userMsg.UserInputMessageContext = ctx
			}
			history = append(history, kiroproto.HistoryEntry{UserInputMessage: userMsg})

		case "assistant":
			content := ExtractTextContent(msg.Content)
			// Generate a deterministic messageId from content + toolUseIDs.
			// v3 captures show messageId must be stable across requests for the same
			// assistant history entry. Using SHA1-based UUID ensures this.
			allToolUses := ExtractToolUses(msg.Content)
			var idSeedBuilder strings.Builder
			idSeedBuilder.WriteString("assistant-msg:")
			idSeedBuilder.WriteString(content)
			for _, tu := range allToolUses {
				idSeedBuilder.WriteByte(':')
				idSeedBuilder.WriteString(tu.ToolUseID)
			}
			arm := &kiroproto.AssistantResponseMessage{
				MessageID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(idSeedBuilder.String())).String(),
				Content:   content,
			}

			// v2 captures show thinking blocks are NOT included in history toolUses.
			// Only real tool_use blocks are included.
			if len(allToolUses) > 0 {
				arm.ToolUses = allToolUses
			}

			history = append(history, kiroproto.HistoryEntry{AssistantResponseMessage: arm})
		}
	}
	return history
}

// buildConversationID generates a deterministic conversation ID from the model,
// system prompt, and first user message text. Same inputs always produce the same ID,
// so the conversation ID stays stable across turns within the same conversation.
// Falls back to a random UUID if no user message text is found.
func buildConversationID(modelID, systemPrompt string, msgs []anthropic.Message) string {
	anchor := firstUserMessageText(msgs)
	if anchor == "" {
		return uuid.New().String()
	}
	seed := modelID + "\n" + systemPrompt + "\n" + anchor
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}

func firstUserMessageText(msgs []anthropic.Message) string {
	for _, m := range msgs {
		if m.Role == "user" {
			if t := ExtractTextContent(m.Content); t != "" {
				return t
			}
		}
	}
	return ""
}
