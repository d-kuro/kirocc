package reqconv

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/google/uuid"
)

// ExtractThinkingToolUses extracts thinking content blocks from assistant messages
// and converts them to Kiro thinking tool_use entries for history.
// Unlike regular tool_use, thinking tool results are NOT sent back to the upstream.
func ExtractThinkingToolUses(content anthropic.MessageContent) []kiroproto.HistoryToolUse {
	if content.IsString() {
		return nil
	}
	var toolUses []kiroproto.HistoryToolUse
	for _, b := range content.Blocks {
		if b.Type != anthropic.BlockTypeThinking || b.Thinking == "" {
			continue
		}
		id := "thinking_" + uuid.New().String()[:8]
		toolUses = append(toolUses, kiroproto.HistoryToolUse{
			ToolUseID: id,
			Name:      ThinkingToolName,
			Input:     map[string]any{"thought": b.Thinking},
		})
	}
	return toolUses
}
