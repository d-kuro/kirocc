package reqconv

import (
	"log/slog"
	"slices"
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

// extractReplayableRedactedThinking returns the redacted reasoning blob to
// replay for the in-flight tool round, or "" when no blob is attributable to
// it.
//
// A blob is attributed to the nearest tool-call block (tool_use or
// server_tool_use) after it, falling back to the nearest one before it — this
// covers both kirocc's emission order (blob after its tool_use) and the
// Anthropic replay convention (thinking blocks first). Blobs attributed to a
// server_tool_use belong to an internal tool-search round whose reasoning the
// backend has already consumed; replaying them against the final tool round
// would be rejected, so they are skipped. When several blobs attribute to the
// answered round, the last one wins (blobs are never concatenated — joining
// base64 blobs would corrupt them).
func extractReplayableRedactedThinking(msg anthropic.Message, answeredIDs []string) string {
	if msg.Content.IsString() {
		return ""
	}
	blocks := msg.Content.Blocks
	answersCurrentRound := func(b anthropic.ContentBlock) bool {
		return b.IsToolUse() && slices.Contains(answeredIDs, b.ID)
	}
	isToolCall := func(b anthropic.ContentBlock) bool {
		return b.IsToolUse() || b.Type == anthropic.BlockTypeServerToolUse
	}
	var data string
	for i, b := range blocks {
		if b.Type != anthropic.BlockTypeRedactedThinking || b.Data == "" {
			continue
		}
		attributed := false
		foundNext := false
		for j := i + 1; j < len(blocks); j++ {
			if isToolCall(blocks[j]) {
				attributed = answersCurrentRound(blocks[j])
				foundNext = true
				break
			}
		}
		if !foundNext {
			for j := i - 1; j >= 0; j-- {
				if isToolCall(blocks[j]) {
					attributed = answersCurrentRound(blocks[j])
					break
				}
			}
		}
		if attributed {
			data = b.Data
		}
	}
	return data
}

// answersToolUses reports whether any of resultIDs answers one of the given
// tool uses — i.e. the assistant's tool round is the one currently being
// continued.
func answersToolUses(toolUses []kiroproto.HistoryToolUse, resultIDs []string) bool {
	for _, tu := range toolUses {
		if slices.Contains(resultIDs, tu.ToolUseID) {
			return true
		}
	}
	return false
}

// buildHistory converts normalized Anthropic messages to Kiro history entries.
//
// currentToolResultIDs are the tool_use IDs referenced by the current (last)
// user message's tool_result blocks. A redacted reasoning blob is replayed
// only on the trailing assistant message whose tool_use IDs those results
// answer — captures show completed turns omit reasoningContent entirely.
func buildHistory(msgs []anthropic.Message, nameMap *ToolNameMap, currentToolResultIDs []string) []kiroproto.HistoryEntry {
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

				// Replay the redacted reasoning blob only when this assistant's
				// tool round is still in flight (the current user message
				// answers its tool_use IDs) and it is the last assistant entry.
				if i == len(msgs)-1 && answersToolUses(allToolUses, currentToolResultIDs) {
					if data := extractReplayableRedactedThinking(msg, currentToolResultIDs); data != "" {
						arm.ReasoningContent = &kiroproto.ReasoningContent{RedactedContent: data}
					}
				}
			}

			history = append(history, kiroproto.HistoryEntry{AssistantResponseMessage: arm})
		}
	}
	return history
}
