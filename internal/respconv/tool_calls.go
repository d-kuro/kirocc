package respconv

import "encoding/json/v2"

// ToolCall represents a finalized tool call from the response stream.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // raw JSON string
}

// DeduplicateToolCalls removes duplicate tool calls:
// 1. ID-based: if multiple calls share the same ID, keep the one with the longest input.
// 2. Name+Args: remove exact duplicates (same name and input).
func DeduplicateToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) <= 1 {
		return calls
	}

	// Phase 1: ID-based dedup — keep longest input per ID.
	byID := make(map[string]ToolCall)
	var order []string
	for _, c := range calls {
		if existing, ok := byID[c.ID]; ok {
			if len(c.Input) > len(existing.Input) {
				byID[c.ID] = c
			}
		} else {
			byID[c.ID] = c
			order = append(order, c.ID)
		}
	}

	// Phase 2: Name+Args exact dedup.
	type key struct {
		name  string
		input string
	}
	seen := make(map[key]bool)
	var result []ToolCall
	for _, id := range order {
		c := byID[id]
		// Normalize input for comparison.
		normalized := normalizeJSON(c.Input)
		k := key{name: c.Name, input: normalized}
		if !seen[k] {
			seen[k] = true
			result = append(result, c)
		}
	}
	return result
}

// normalizeJSON re-marshals JSON to normalize whitespace for comparison.
func normalizeJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(b)
}
