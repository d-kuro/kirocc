package reqconv

import (
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// ExtractTextContent extracts plain text from message content.
// String content is returned as-is.
// For block arrays: text blocks are joined with space, thinking blocks are ignored,
// unknown blocks are converted to text like [type: name].
func ExtractTextContent(content anthropic.MessageContent) string {
	if content.IsString() {
		return content.Text
	}
	var parts []string
	for _, b := range content.Blocks {
		switch {
		case b.Type == anthropic.BlockTypeText:
			parts = append(parts, b.Text)
		case handledSeparately(b.Type):
			// Skip — handled separately.
		default:
			// Unknown block type → textualize.
			parts = append(parts, textualizeUnknownBlock(b))
		}
	}
	return strings.Join(parts, " ")
}

// handledSeparately reports whether a block type stays out of the message text:
// tool use/results and images are carried in their own fields, thinking is
// dropped.
func handledSeparately(blockType string) bool {
	switch blockType {
	case anthropic.BlockTypeThinking, anthropic.BlockTypeRedactedThinking, anthropic.BlockTypeToolUse, anthropic.BlockTypeToolResult, anthropic.BlockTypeImage, anthropic.BlockTypeToolReference,
		anthropic.BlockTypeServerToolUse, anthropic.BlockTypeToolSearchToolResult:
		return true
	}
	return false
}

// textualizeUnknownBlock converts an unknown content block to a text representation.
func textualizeUnknownBlock(b anthropic.ContentBlock) string {
	identifier := b.ToolName // tool_reference uses tool_name
	if identifier == "" {
		identifier = b.Name
	}
	if identifier == "" {
		identifier = b.ID
	}
	if identifier != "" {
		return "[" + b.Type + ": " + identifier + "]"
	}
	return "[" + b.Type + "]"
}

// billingHeaderPrefix identifies Claude Code's standalone attribution block.
// Its per-request cch value changes the prompt prefix without adding instructions.
const billingHeaderPrefix = "x-anthropic-billing-header:"

// isBillingHeaderBlock reports whether text is Claude Code's attribution block and nothing else.
func isBillingHeaderBlock(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, billingHeaderPrefix) && !strings.ContainsAny(t, "\r\n")
}

// ExtractSystemPrompt extracts the system prompt text from the SystemPrompt union type.
// String form returns as-is. Array form joins text blocks with "\n", skipping Claude Code's
// per-request billing block.
func ExtractSystemPrompt(system anthropic.SystemPrompt) string {
	if system.IsEmpty() {
		return ""
	}
	if system.Text != "" {
		return system.Text
	}
	var parts []string
	for _, block := range system.Blocks {
		if block.Type == anthropic.BlockTypeText && block.Text != "" && !isBillingHeaderBlock(block.Text) {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
