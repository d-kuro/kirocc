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

// Normalize runs the 5-step normalization pipeline on messages.
func Normalize(msgs []anthropic.Message, hasTools bool) []anthropic.Message {
	msgs = step1ToolContent(msgs, hasTools)
	msgs = step4NormalizeRoles(msgs)
	msgs = step2MergeAdjacentSameRole(msgs)
	msgs = step3EnsureStartsWithUser(msgs)
	msgs = step5EnsureAlternating(msgs)
	return msgs
}

// step1ToolContent handles tool content based on whether tools are defined.
func step1ToolContent(msgs []anthropic.Message, hasTools bool) []anthropic.Message {
	if !hasTools {
		return step1aTextualizeAllToolContent(msgs)
	}
	return step1bTextualizeOrphanToolResults(msgs)
}

// step1aTextualizeAllToolContent converts all tool_use and tool_result blocks to text
// when no tools are defined in the request.
func step1aTextualizeAllToolContent(msgs []anthropic.Message) []anthropic.Message {
	result := make([]anthropic.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Content.IsString() {
			result = append(result, msg)
			continue
		}
		var newBlocks []anthropic.ContentBlock
		for _, b := range msg.Content.Blocks {
			switch b.Type {
			case "tool_use":
				inputJSON, _ := json.Marshal(b.Input)
				text := fmt.Sprintf("[Tool: %s (%s)]\n%s", b.Name, b.ID, string(inputJSON))
				newBlocks = append(newBlocks, anthropic.ContentBlock{Type: "text", Text: text})
			case "tool_result":
				content := extractToolResultContentText(b)
				text := fmt.Sprintf("[Tool Result (%s)]\n%s", b.ToolUseID, content)
				newBlocks = append(newBlocks, anthropic.ContentBlock{Type: "text", Text: text})
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

// step1bTextualizeOrphanToolResults converts tool_result blocks to text when
// the preceding assistant message doesn't have a matching tool_use.
func step1bTextualizeOrphanToolResults(msgs []anthropic.Message) []anthropic.Message {
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
				if b.Type == "tool_use" {
					assistantToolIDs[b.ID] = struct{}{}
				}
			}
		}

		var newBlocks []anthropic.ContentBlock
		for _, b := range msg.Content.Blocks {
			if b.Type == "tool_result" {
				if _, ok := assistantToolIDs[b.ToolUseID]; !ok {
					content := extractToolResultContentText(b)
					text := fmt.Sprintf("[Tool Result (%s)]\n%s", b.ToolUseID, content)
					newBlocks = append(newBlocks, anthropic.ContentBlock{Type: "text", Text: text})
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
		if cb.Type == "text" {
			parts = append(parts, cb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// step2MergeAdjacentSameRole merges consecutive messages with the same role.
// Text content is joined with "\n".
func step2MergeAdjacentSameRole(msgs []anthropic.Message) []anthropic.Message {
	if len(msgs) == 0 {
		return msgs
	}
	result := []anthropic.Message{msgs[0]}
	for _, msg := range msgs[1:] {
		last := &result[len(result)-1]
		if msg.Role == last.Role && isPlainTextContent(last.Content) && isPlainTextContent(msg.Content) {
			merged := ExtractTextContent(last.Content) + "\n" + ExtractTextContent(msg.Content)
			last.Content = anthropic.MessageContent{Text: merged}
		} else {
			result = append(result, msg)
		}
	}
	return result
}

// isPlainTextContent reports whether content is a plain string or only text blocks.
func isPlainTextContent(content anthropic.MessageContent) bool {
	if content.IsString() {
		return true
	}
	for _, b := range content.Blocks {
		if b.Type != "text" {
			return false
		}
	}
	return true
}

// step3EnsureStartsWithUser prepends a synthetic "(empty)" user message
// if the first message is not from the user.
func step3EnsureStartsWithUser(msgs []anthropic.Message) []anthropic.Message {
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

// step4NormalizeRoles converts non-standard roles (e.g., "developer") to "user".
// Returns a new slice if any mutation is needed; otherwise returns the original.
func step4NormalizeRoles(msgs []anthropic.Message) []anthropic.Message {
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

// step5EnsureAlternating inserts synthetic "(empty)" messages between
// consecutive messages with the same role.
func step5EnsureAlternating(msgs []anthropic.Message) []anthropic.Message {
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
