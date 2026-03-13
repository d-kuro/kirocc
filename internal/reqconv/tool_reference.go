package reqconv

import "github.com/d-kuro/kirocc/internal/anthropic"

// ExtractToolReferences scans conversation history for tool_reference blocks
// and returns the referenced tool names. These appear in:
// - tool_search_tool_result content (nested tool_references array via ToolReferences field)
// - tool_result content with tool_reference blocks
// - top-level tool_reference blocks
func ExtractToolReferences(messages []anthropic.Message) []string {
	seen := make(map[string]struct{})
	var names []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	for _, msg := range messages {
		if msg.Content.IsString() {
			continue
		}
		for _, b := range msg.Content.Blocks {
			switch b.Type {
			case anthropic.BlockTypeToolReference:
				add(b.ToolName)
			case anthropic.BlockTypeToolSearchToolResult:
				// Content is an object decoded as single ContentBlock.
				// Check nested Content.Blocks for tool_reference blocks.
				for _, inner := range b.Content.Blocks {
					if inner.Type == anthropic.BlockTypeToolReference {
						add(inner.ToolName)
					}
					// Also check ToolReferences on tool_search_tool_search_result.
					for _, ref := range inner.ToolReferences {
						add(ref.ToolName)
					}
				}
			case anthropic.BlockTypeToolResult:
				for _, inner := range b.Content.Blocks {
					if inner.Type == anthropic.BlockTypeToolReference {
						add(inner.ToolName)
					}
				}
			}
		}
	}
	return names
}
