package reqconv

import (
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// ExtractSystemPrompt extracts the system prompt text from the SystemPrompt union type.
// String form returns as-is. Array form joins text blocks with "\n".
func ExtractSystemPrompt(system anthropic.SystemPrompt) string {
	if system.IsEmpty() {
		return ""
	}
	if system.Text != "" {
		return system.Text
	}
	var parts []string
	for _, block := range system.Blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
