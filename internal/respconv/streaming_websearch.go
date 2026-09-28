package respconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/websearch"
)

// WebSearchResultMaps renders search results as web_search_result content
// entries. encrypted_content is deliberately absent: the real API sends an
// opaque blob there for replaying a search in a later turn, and there is
// nothing to encrypt with here. Clients read title and url.
func WebSearchResultMaps(results []websearch.Result) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		entry := map[string]any{
			"type":  anthropic.BlockTypeWebSearchResult,
			"title": r.Title,
			"url":   r.URL,
		}
		if r.PageAge != "" {
			entry["page_age"] = r.PageAge
		}
		out = append(out, entry)
	}
	return out
}

// WebSearchResultBlock builds a web_search_tool_result content block. Its
// content is an array, which is what distinguishes a result from an error.
func WebSearchResultBlock(toolUseID string, results []websearch.Result) map[string]any {
	return map[string]any{
		"type":        anthropic.BlockTypeWebSearchToolResult,
		"tool_use_id": toolUseID,
		"content":     WebSearchResultMaps(results),
	}
}

// WebSearchErrorBlock builds a web_search_tool_result content block carrying a
// search failure code.
func WebSearchErrorBlock(toolUseID, errorCode string) map[string]any {
	return map[string]any{
		"type":        anthropic.BlockTypeWebSearchToolResult,
		"tool_use_id": toolUseID,
		"content": map[string]any{
			"type":       anthropic.BlockTypeWebSearchResultError,
			"error_code": errorCode,
		},
	}
}

// WriteWebSearchResult writes a web_search_tool_result content block.
func (s *SSEWriter) WriteWebSearchResult(toolUseID string, results []websearch.Result) {
	s.writeBlock(WebSearchResultBlock(toolUseID, results), nil)
}

// WriteWebSearchError writes a web_search_tool_result error content block.
func (s *SSEWriter) WriteWebSearchError(toolUseID, errorCode string) {
	s.writeBlock(WebSearchErrorBlock(toolUseID, errorCode), nil)
}
