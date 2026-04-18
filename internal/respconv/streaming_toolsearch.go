package respconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/toolsearch"
)

// WriteServerToolUse writes a server_tool_use content block start + input delta + stop.
func (s *SSEWriter) WriteServerToolUse(id, name, input string) {
	s.ensureStarted()
	s.fireVisibleOutput()
	s.writeBlock(
		map[string]any{
			"type":  anthropic.BlockTypeServerToolUse,
			"id":    id,
			"name":  name,
			"input": map[string]any{},
		},
		map[string]any{
			"type":         "input_json_delta",
			"partial_json": input,
		},
	)
}

// WriteToolSearchResult writes a tool_search_tool_result content block.
func (s *SSEWriter) WriteToolSearchResult(toolUseID string, toolRefs []string) {
	s.writeBlock(
		map[string]any{
			"type":        anthropic.BlockTypeToolSearchToolResult,
			"tool_use_id": toolUseID,
			"content": map[string]any{
				"type":            anthropic.BlockTypeToolSearchSearchResult,
				"tool_references": toolsearch.ToolRefMaps(toolRefs),
			},
		},
		nil,
	)
}

// WriteToolSearchError writes a tool_search_tool_result error content block.
func (s *SSEWriter) WriteToolSearchError(toolUseID string, errorCode string) {
	s.writeBlock(
		map[string]any{
			"type":        anthropic.BlockTypeToolSearchToolResult,
			"tool_use_id": toolUseID,
			"content": map[string]any{
				"type":       anthropic.BlockTypeToolSearchResultError,
				"error_code": errorCode,
			},
		},
		nil,
	)
}
