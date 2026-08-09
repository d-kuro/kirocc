package respconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/toolsearch"
)

// ServerToolUseBlock builds a server_tool_use content block. Input is carried
// as a decoded map for the non-streaming path; the streaming writer sends the
// same shape with an empty input plus an input_json_delta.
func ServerToolUseBlock(id, name string, input map[string]any) map[string]any {
	if input == nil {
		input = map[string]any{}
	}
	return map[string]any{
		"type":  anthropic.BlockTypeServerToolUse,
		"id":    id,
		"name":  name,
		"input": input,
	}
}

// ToolSearchResultBlock builds a tool_search_tool_result content block naming
// the tools the search promoted.
func ToolSearchResultBlock(toolUseID string, toolRefs []string) map[string]any {
	return map[string]any{
		"type":        anthropic.BlockTypeToolSearchToolResult,
		"tool_use_id": toolUseID,
		"content": map[string]any{
			"type":            anthropic.BlockTypeToolSearchSearchResult,
			"tool_references": toolsearch.ToolRefMaps(toolRefs),
		},
	}
}

// ToolSearchErrorBlock builds a tool_search_tool_result content block carrying
// a search failure code.
func ToolSearchErrorBlock(toolUseID, errorCode string) map[string]any {
	return map[string]any{
		"type":        anthropic.BlockTypeToolSearchToolResult,
		"tool_use_id": toolUseID,
		"content": map[string]any{
			"type":       anthropic.BlockTypeToolSearchResultError,
			"error_code": errorCode,
		},
	}
}

// WriteServerToolUse writes a server_tool_use content block start + input delta + stop.
func (s *SSEWriter) WriteServerToolUse(id, name, input string) {
	s.ensureStarted()
	s.fireVisibleOutput()
	s.writeBlock(
		ServerToolUseBlock(id, name, nil),
		map[string]any{
			"type":         "input_json_delta",
			"partial_json": input,
		},
	)
}

// WriteToolSearchResult writes a tool_search_tool_result content block.
func (s *SSEWriter) WriteToolSearchResult(toolUseID string, toolRefs []string) {
	s.writeBlock(ToolSearchResultBlock(toolUseID, toolRefs), nil)
}

// WriteToolSearchError writes a tool_search_tool_result error content block.
func (s *SSEWriter) WriteToolSearchError(toolUseID string, errorCode string) {
	s.writeBlock(ToolSearchErrorBlock(toolUseID, errorCode), nil)
}
