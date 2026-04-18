package reqconv

import (
	"log/slog"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/google/uuid"
)

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
func buildHistory(msgs []anthropic.Message, nameMap *ToolNameMap) []kiroproto.HistoryEntry {
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
			if len(toolResults) > 0 {
				userMsg.UserInputMessageContext = &kiroproto.UserInputMessageContext{
					ToolResults: toolResults,
				}
			}
			history = append(history, kiroproto.HistoryEntry{UserInputMessage: userMsg})

		case "assistant":
			content := ExtractTextContent(msg.Content)
			// Generate a deterministic messageId from content + toolUseIDs.
			// v3 captures show messageId must be stable across requests for the same
			// assistant history entry. Using SHA1-based UUID ensures this.
			allToolUses := ExtractToolUses(msg.Content)
			for i := range allToolUses {
				allToolUses[i].Name = nameMap.Shorten(allToolUses[i].Name)
			}
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
