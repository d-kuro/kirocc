package reqconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// CacheMapping tracks cache_control positions from the original Anthropic request
// and applies cachePoint to the corresponding Kiro payload elements after normalization.

// ApplySystemCachePoints is a no-op for now.
// v2 captures show cachePoint is NOT placed on currentMessage or history entries
// for system-level cache_control. Only tool-level cachePoints are used.
func ApplySystemCachePoints(system anthropic.SystemPrompt, history []kiroproto.HistoryEntry, currentMessage *kiroproto.UserInputMessage) {
	// Intentionally empty: v2 kiro-cli does not convert system cache_control to cachePoint.
}

// ApplyToolCachePoints inserts cachePoint entries into the tools array
// after tools that have cache_control set.
func ApplyToolCachePoints(tools []anthropic.Tool, entries []kiroproto.ToolEntry) []kiroproto.ToolEntry {
	if len(tools) == 0 {
		return entries
	}
	var result []kiroproto.ToolEntry
	entryIdx := 0
	for _, t := range tools {
		if entryIdx < len(entries) {
			result = append(result, entries[entryIdx])
			entryIdx++
		}
		if t.CacheControl != nil {
			result = append(result, kiroproto.ToolEntry{
				CachePoint: &kiroproto.CachePoint{Type: "default"},
			})
		}
	}
	// Append any remaining entries (shouldn't happen normally).
	for ; entryIdx < len(entries); entryIdx++ {
		result = append(result, entries[entryIdx])
	}
	return result
}
