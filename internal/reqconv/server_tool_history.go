package reqconv

import (
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// ExpandServerToolResults rewrites assistant messages that carry server-side
// tool results into the user/assistant alternation Kiro's history requires.
//
// The Anthropic wire format packs a server tool round into a single assistant
// message: text, then server_tool_use, then the matching result block
// (advisor_tool_result / tool_search_tool_result). Kiro has no equivalent — a
// tool call in history must be answered by a user turn carrying a toolResult.
// Without this rewrite the result block is dropped and the advice is lost on
// every subsequent turn.
//
// Each affected assistant message becomes a run of messages that preserves
// block order:
//
//	assistant: <blocks before the first server tool call> + server_tool_use…
//	user:      tool_result(s) answering those calls
//	assistant: <blocks after the last server tool result>
//
// Messages without server tool results pass through untouched.
func ExpandServerToolResults(msgs []anthropic.Message) []anthropic.Message {
	// Single pass with lazy allocation: the common case is a history with no
	// server tool results at all, which returns the input untouched. Blocks are
	// indexed rather than ranged by value — ContentBlock is wide enough that
	// copying every block of every message dominates the scan.
	var out []anthropic.Message
	for i := range msgs {
		if !messageHasServerToolResult(msgs[i]) {
			if out != nil {
				out = append(out, msgs[i])
			}
			continue
		}
		if out == nil {
			out = make([]anthropic.Message, 0, len(msgs)+2)
			out = append(out, msgs[:i]...)
		}
		out = append(out, expandAssistantMessage(msgs[i])...)
	}
	if out == nil {
		return msgs
	}
	return out
}

func messageHasServerToolResult(msg anthropic.Message) bool {
	if msg.Role != "assistant" || msg.Content.IsString() {
		return false
	}
	blocks := msg.Content.Blocks
	for i := range blocks {
		if blocks[i].IsServerToolResult() {
			return true
		}
	}
	return false
}

// expandAssistantMessage splits one assistant message into the alternating run
// described on ExpandServerToolResults. Blocks are emitted in their original
// order; a server tool result closes the current assistant segment and opens a
// user segment.
func expandAssistantMessage(msg anthropic.Message) []anthropic.Message {
	var (
		out       []anthropic.Message
		assistant []anthropic.ContentBlock
		results   []anthropic.ContentBlock
	)

	flush := func() {
		if len(assistant) > 0 {
			out = append(out, anthropic.Message{
				Role:    "assistant",
				Content: anthropic.MessageContent{Blocks: assistant},
			})
			assistant = nil
		}
		if len(results) > 0 {
			out = append(out, anthropic.Message{
				Role:    "user",
				Content: anthropic.MessageContent{Blocks: results},
			})
			results = nil
		}
	}

	for _, b := range msg.Content.Blocks {
		if !b.IsServerToolResult() {
			// A block following a pending result starts the next assistant
			// segment, so the pending pair must be emitted first.
			if len(results) > 0 {
				flush()
			}
			assistant = append(assistant, b)
			continue
		}
		results = append(results, anthropic.ContentBlock{
			Type:      anthropic.BlockTypeToolResult,
			ToolUseID: b.ToolUseID,
			Content:   anthropic.MessageContent{Text: ServerToolResultText(b)},
			IsError:   serverToolResultIsError(b),
		})
	}
	flush()

	if len(out) == 0 {
		// All blocks were consumed into results that produced no text; keep the
		// original message so the turn is not silently dropped.
		return []anthropic.Message{msg}
	}
	return out
}

// serverToolResultIsError reports whether a server tool result block carries
// an error variant, so the synthetic tool_result replays with the same failure
// status the in-flight round reported (Kiro sees status=error, not success).
func serverToolResultIsError(b anthropic.ContentBlock) bool {
	for _, inner := range b.Content.Blocks {
		switch inner.Type {
		case anthropic.BlockTypeAdvisorResultError, anthropic.BlockTypeToolSearchResultError:
			return true
		}
	}
	return false
}

// ServerToolResultText renders a server tool result block as the plain text a
// Kiro toolResult carries. Advisor advice is restored verbatim so the executor
// can act on it; a redacted result cannot be decrypted by the proxy and is
// reduced to a marker. An empty string is returned when nothing rendered —
// ExtractToolResults and scanCurrentMessage own the "(empty result)" fallback.
func ServerToolResultText(b anthropic.ContentBlock) string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	for _, inner := range b.Content.Blocks {
		switch inner.Type {
		case anthropic.BlockTypeAdvisorResult:
			add(inner.Text)
		case anthropic.BlockTypeAdvisorRedactedResult:
			add("[advisor guidance was redacted and is unavailable]")
		case anthropic.BlockTypeAdvisorResultError:
			add("advisor error: " + inner.ErrorCode)
		case anthropic.BlockTypeToolSearchResultError:
			add("tool search error: " + inner.ErrorCode)
		case anthropic.BlockTypeText:
			add(inner.Text)
		case anthropic.BlockTypeToolSearchSearchResult:
			var names []string
			for _, ref := range inner.ToolReferences {
				if ref.ToolName != "" {
					names = append(names, ref.ToolName)
				}
			}
			if len(names) > 0 {
				add("Found tools: " + strings.Join(names, ", "))
			}
		}
	}
	if len(parts) == 0 && b.Content.IsString() {
		add(b.Content.Text)
	}
	return strings.Join(parts, "\n")
}
