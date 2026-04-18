package reqconv

import (
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

const (
	syntheticEmpty    = "(empty)"
	syntheticContinue = "Continue"
)

// Normalize runs the normalization pipeline on messages. Ordering matters:
// tool content handling runs first (it inspects original block shapes), then
// role normalization, merging, and alternation fixups.
func Normalize(msgs []anthropic.Message, hasTools bool) []anthropic.Message {
	msgs = textualizeToolContent(msgs, hasTools)
	msgs = normalizeRoles(msgs)
	msgs = mergeAdjacentSameRole(msgs)
	msgs = ensureStartsWithUser(msgs)
	msgs = ensureAlternatingRoles(msgs)
	return msgs
}

// textualizeToolContent handles tool content based on whether tools are defined.
func textualizeToolContent(msgs []anthropic.Message, hasTools bool) []anthropic.Message {
	if !hasTools {
		return textualizeAllToolContent(msgs)
	}
	return textualizeOrphanToolResults(msgs)
}

// textualizeAllToolContent converts all tool_use and tool_result blocks to text
// when no tools are defined in the request.
func textualizeAllToolContent(msgs []anthropic.Message) []anthropic.Message {
	result := make([]anthropic.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Content.IsString() {
			result = append(result, msg)
			continue
		}
		var newBlocks []anthropic.ContentBlock
		for _, b := range msg.Content.Blocks {
			switch b.Type {
			case anthropic.BlockTypeToolUse, anthropic.BlockTypeServerToolUse:
				inputJSON, _ := json.Marshal(b.Input)
				text := fmt.Sprintf("[Tool: %s (%s)]\n%s", b.Name, b.ID, string(inputJSON))
				newBlocks = append(newBlocks, anthropic.ContentBlock{Type: anthropic.BlockTypeText, Text: text})
			case anthropic.BlockTypeToolResult, anthropic.BlockTypeToolSearchToolResult:
				content := extractToolResultContentText(b)
				text := fmt.Sprintf("[Tool Result (%s)]\n%s", b.ToolUseID, content)
				newBlocks = append(newBlocks, anthropic.ContentBlock{Type: anthropic.BlockTypeText, Text: text})
			default:
				newBlocks = append(newBlocks, b)
			}
		}
		result = append(result, anthropic.Message{
			Role:    msg.Role,
			Content: anthropic.MessageContent{Blocks: newBlocks},
		})
	}
	return result
}

// textualizeOrphanToolResults converts tool_result blocks to text when
// the preceding assistant message doesn't have a matching tool_use.
func textualizeOrphanToolResults(msgs []anthropic.Message) []anthropic.Message {
	result := make([]anthropic.Message, 0, len(msgs))
	for i, msg := range msgs {
		if msg.Role != "user" || msg.Content.IsString() {
			result = append(result, msg)
			continue
		}
		// Collect tool_use IDs from the preceding assistant message.
		assistantToolIDs := make(map[string]struct{})
		if i > 0 && msgs[i-1].Role == "assistant" && !msgs[i-1].Content.IsString() {
			for _, b := range msgs[i-1].Content.Blocks {
				if b.IsToolUse() {
					assistantToolIDs[b.ID] = struct{}{}
				}
			}
		}

		var newBlocks []anthropic.ContentBlock
		for _, b := range msg.Content.Blocks {
			if b.IsToolResult() {
				if _, ok := assistantToolIDs[b.ToolUseID]; !ok {
					content := extractToolResultContentText(b)
					text := fmt.Sprintf("[Tool Result (%s)]\n%s", b.ToolUseID, content)
					newBlocks = append(newBlocks, anthropic.ContentBlock{Type: anthropic.BlockTypeText, Text: text})
					continue
				}
			}
			newBlocks = append(newBlocks, b)
		}
		result = append(result, anthropic.Message{
			Role:    msg.Role,
			Content: anthropic.MessageContent{Blocks: newBlocks},
		})
	}
	return result
}

// extractToolResultContentText gets the text content from a tool_result block.
func extractToolResultContentText(b anthropic.ContentBlock) string {
	if b.Content.IsString() {
		return b.Content.Text
	}
	var parts []string
	for _, cb := range b.Content.Blocks {
		switch {
		case cb.Type == anthropic.BlockTypeText:
			parts = append(parts, cb.Text)
		case cb.Type == anthropic.BlockTypeToolSearchSearchResult || len(cb.ToolReferences) > 0:
			// Preserve tool_references from tool_search_tool_result content.
			for _, ref := range cb.ToolReferences {
				if ref.ToolName != "" {
					parts = append(parts, "tool_reference: "+ref.ToolName)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// mergeAdjacentSameRole merges runs of consecutive same-role plain-text messages
// into a single message. Each run is joined with "\n" using one strings.Builder,
// so the cost is O(total characters) rather than O(n²) from repeated concatenation.
func mergeAdjacentSameRole(msgs []anthropic.Message) []anthropic.Message {
	if len(msgs) == 0 {
		return msgs
	}
	result := make([]anthropic.Message, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		// Find the end of a mergeable run starting at i.
		j := i + 1
		if isPlainTextContent(msgs[i].Content) {
			for j < len(msgs) && msgs[j].Role == msgs[i].Role && isPlainTextContent(msgs[j].Content) {
				j++
			}
		}
		if j == i+1 {
			result = append(result, msgs[i])
			i = j
			continue
		}
		var b strings.Builder
		for k := i; k < j; k++ {
			if k > i {
				b.WriteByte('\n')
			}
			b.WriteString(ExtractTextContent(msgs[k].Content))
		}
		result = append(result, anthropic.Message{
			Role:    msgs[i].Role,
			Content: anthropic.MessageContent{Text: b.String()},
		})
		i = j
	}
	return result
}

// isPlainTextContent reports whether content is a plain string or only text blocks.
func isPlainTextContent(content anthropic.MessageContent) bool {
	if content.IsString() {
		return true
	}
	for _, b := range content.Blocks {
		if b.Type != anthropic.BlockTypeText {
			return false
		}
	}
	return true
}

// ensureStartsWithUser prepends a synthetic "(empty)" user message
// if the first message is not from the user.
func ensureStartsWithUser(msgs []anthropic.Message) []anthropic.Message {
	if len(msgs) == 0 {
		return msgs
	}
	if msgs[0].Role == "user" {
		return msgs
	}
	synthetic := anthropic.Message{
		Role:    "user",
		Content: anthropic.MessageContent{Text: syntheticEmpty},
	}
	return append([]anthropic.Message{synthetic}, msgs...)
}

// normalizeRoles converts non-standard roles (e.g., "developer") to "user".
// Returns a new slice if any mutation is needed; otherwise returns the original.
func normalizeRoles(msgs []anthropic.Message) []anthropic.Message {
	var result []anthropic.Message
	for i, msg := range msgs {
		if msg.Role != "user" && msg.Role != "assistant" {
			if result == nil {
				result = make([]anthropic.Message, len(msgs))
				copy(result, msgs)
			}
			result[i].Role = "user"
		}
	}
	if result != nil {
		return result
	}
	return msgs
}

// ensureAlternatingRoles inserts synthetic "(empty)" messages between
// consecutive messages with the same role.
func ensureAlternatingRoles(msgs []anthropic.Message) []anthropic.Message {
	if len(msgs) <= 1 {
		return msgs
	}
	result := []anthropic.Message{msgs[0]}
	for _, msg := range msgs[1:] {
		last := result[len(result)-1]
		if msg.Role == last.Role {
			oppositeRole := "assistant"
			if msg.Role == "assistant" {
				oppositeRole = "user"
			}
			result = append(result, anthropic.Message{
				Role:    oppositeRole,
				Content: anthropic.MessageContent{Text: syntheticEmpty},
			})
		}
		result = append(result, msg)
	}
	return result
}
