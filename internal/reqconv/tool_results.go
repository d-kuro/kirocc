package reqconv

import (
	"log/slog"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/google/uuid"
)

// ExtractToolResults extracts tool_result blocks from message content and converts to Kiro format.
func ExtractToolResults(content anthropic.MessageContent) []kiroproto.ToolResult {
	if content.IsString() {
		return nil
	}
	var results []kiroproto.ToolResult
	for _, b := range content.Blocks {
		if b.Type != "tool_result" {
			continue
		}
		status := kiroproto.ToolResultStatusSuccess
		if b.IsError {
			status = kiroproto.ToolResultStatusError
		}
		text := extractToolResultContentText(b)
		if text == "" {
			text = "(empty result)"
		}
		// v3 captures show kiro-cli uses exit_status/stdout/stderr format.
		exitStatus := "0"
		if b.IsError {
			exitStatus = "1"
		}
		results = append(results, kiroproto.ToolResult{
			ToolUseID: b.ToolUseID,
			Status:    status,
			Content: []kiroproto.ToolResultContent{{JSON: map[string]any{
				"exit_status": exitStatus,
				"stdout":      text,
				"stderr":      "",
			}}},
		})
	}
	return results
}

// ExtractToolUses extracts tool_use blocks from assistant message content and converts to Kiro format.
func ExtractToolUses(content anthropic.MessageContent) []kiroproto.HistoryToolUse {
	if content.IsString() {
		return nil
	}
	var toolUses []kiroproto.HistoryToolUse
	for _, b := range content.Blocks {
		if b.Type != "tool_use" {
			continue
		}
		toolUses = append(toolUses, kiroproto.HistoryToolUse{
			ToolUseID: b.ID,
			Name:      b.Name,
			Input:     b.Input,
		})
	}
	return toolUses
}

// ExtractThinkingToolUses extracts thinking content blocks from assistant messages
// and converts them to Kiro thinking tool_use entries for history.
// Unlike regular tool_use, thinking tool results are NOT sent back to the upstream.
func ExtractThinkingToolUses(content anthropic.MessageContent) []kiroproto.HistoryToolUse {
	if content.IsString() {
		return nil
	}
	var toolUses []kiroproto.HistoryToolUse
	for _, b := range content.Blocks {
		if b.Type != "thinking" || b.Thinking == "" {
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

// ReorderToolResults reorders tool results to match the order of tool_use IDs
// from the preceding assistant message. Results not found in toolUseIDs are appended at the end.
func ReorderToolResults(results []kiroproto.ToolResult, toolUseIDs []string) []kiroproto.ToolResult {
	if len(results) <= 1 || len(toolUseIDs) == 0 {
		return results
	}
	index := make(map[string]kiroproto.ToolResult, len(results))
	for _, r := range results {
		index[r.ToolUseID] = r
	}
	ordered := make([]kiroproto.ToolResult, 0, len(results))
	used := make(map[string]struct{}, len(results))
	for _, id := range toolUseIDs {
		if r, ok := index[id]; ok {
			ordered = append(ordered, r)
			used[id] = struct{}{}
		}
	}
	for _, r := range results {
		if _, ok := used[r.ToolUseID]; !ok {
			ordered = append(ordered, r)
		}
	}
	return ordered
}

// ExtractImages extracts image blocks from message content and converts to Kiro format.
// URL-based images are skipped with a warning log.
func ExtractImages(content anthropic.MessageContent) []kiroproto.Image {
	if content.IsString() {
		return nil
	}
	var images []kiroproto.Image
	for _, b := range content.Blocks {
		if b.Type != "image" || b.Source == nil {
			continue
		}
		if b.Source.Type != "base64" {
			slog.Warn("skipping non-base64 image source type", "type", b.Source.Type)
			continue
		}
		format := b.Source.MediaType
		if idx := strings.LastIndex(format, "/"); idx >= 0 {
			format = format[idx+1:]
		}
		images = append(images, kiroproto.Image{
			Format: format,
			Source: kiroproto.ImageSource{Bytes: b.Source.Data},
		})
	}
	return images
}
