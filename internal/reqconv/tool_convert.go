package reqconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// ConvertTools converts Anthropic tool definitions to Kiro tool entries.
// Names exceeding 64 characters are shortened via nameMap.
func ConvertTools(tools []anthropic.Tool, nameMap *ToolNameMap) []kiroproto.ToolEntry {
	var entries []kiroproto.ToolEntry

	for _, t := range tools {
		name := nameMap.Shorten(t.Name)

		desc := t.Description
		if desc == "" {
			desc = "Tool: " + t.Name
		}

		entries = append(entries, kiroproto.ToolEntry{
			ToolSpecification: &kiroproto.ToolSpecification{
				Name:        name,
				Description: desc,
				InputSchema: kiroproto.InputSchema{JSON: SanitizeJSONSchema(t.InputSchema)},
			},
		})
	}

	return entries
}

const thinkingToolDescription = "Thinking is an internal reasoning mechanism improving the quality of complex tasks by breaking their atomic actions down; use it specifically for multi-step problems requiring step-by-step dependencies, reasoning through multiple constraints, synthesizing results from previous tool calls, planning intricate sequences of actions, troubleshooting complex errors, or making decisions involving multiple trade-offs. Avoid using it for straightforward tasks, basic information retrieval, summaries, always clearly define the reasoning challenge, structure thoughts explicitly, consider multiple perspectives, and summarize key insights before important decisions or complex tool interactions."

const thinkingToolThoughtDescription = `A reflective note or intermediate reasoning step such as "The user needs to prepare their application for production. I need to complete three major asks including 1: building their code from source, 2: bundling their release artifacts together, and 3: signing the application bundle."`

// ThinkingToolName is an alias for kiroproto.ThinkingToolName for use in tests within this package.
const ThinkingToolName = kiroproto.ThinkingToolName

// ThinkingToolEntry returns the Kiro thinking tool entry matching the real Kiro client.
func ThinkingToolEntry() kiroproto.ToolEntry {
	return kiroproto.ToolEntry{
		ToolSpecification: &kiroproto.ToolSpecification{
			Name:        kiroproto.ThinkingToolName,
			Description: thinkingToolDescription,
			InputSchema: kiroproto.InputSchema{
				JSON: map[string]any{
					"type":     "object",
					"required": []any{"thought"},
					"properties": map[string]any{
						"thought": map[string]any{
							"type":        "string",
							"description": thinkingToolThoughtDescription,
						},
					},
				},
			},
		},
	}
}
