package toolsearch

import "github.com/d-kuro/kirocc/internal/kiroproto"

// KiroToolSearchName is the name of the Kiro-side tool search tool.
const KiroToolSearchName = "ToolSearch"

const toolSearchDescription = `Fetches full schema definitions for deferred tools so they can be called.

Deferred tools appear by name in <available-deferred-tools> messages. Until fetched, only the name is known — there is no parameter schema, so the tool cannot be invoked. This tool takes a query, matches it against the deferred tool list, and returns the matched tools' complete JSONSchema definitions inside a <functions> block. Once a tool's schema appears in that result, it is callable exactly like any tool defined at the top of the prompt.

Result format: each matched tool appears as one <function>{"description": "...", "name": "...", "parameters": {...}}</function> line inside the <functions> block — the same encoding as the tool list at the top of this prompt.

Query forms:
- "select:Read,Edit,Grep" — fetch these exact tools by name
- "notebook jupyter" — keyword search, up to max_results best matches`

// KiroToolSearchEntry returns the Kiro tool search tool entry.
func KiroToolSearchEntry() kiroproto.ToolEntry {
	return kiroproto.ToolEntry{
		ToolSpecification: &kiroproto.ToolSpecification{
			Name:        KiroToolSearchName,
			Description: toolSearchDescription,
			InputSchema: kiroproto.InputSchema{
				JSON: map[string]any{
					"type":     "object",
					"required": []any{"query"},
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Query to find deferred tools. Use \"select:<tool_name>\" for direct selection, or keywords to search.",
						},
						"max_results": map[string]any{
							"type":        "integer",
							"description": "Maximum number of results to return (default: 5)",
						},
					},
				},
			},
		},
	}
}
