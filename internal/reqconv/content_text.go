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
		switch b.Type {
		case anthropic.BlockTypeText:
			parts = append(parts, b.Text)
		case anthropic.BlockTypeThinking, anthropic.BlockTypeToolUse, anthropic.BlockTypeToolResult, anthropic.BlockTypeImage, anthropic.BlockTypeToolReference,
			anthropic.BlockTypeServerToolUse, anthropic.BlockTypeToolSearchToolResult:
			// Skip — handled separately.
		default:
			// Unknown block type → textualize.
			parts = append(parts, textualizeUnknownBlock(b))
		}
	}
	return strings.Join(parts, " ")
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
