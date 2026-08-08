package reqconv

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// scanCurrentMessage walks message content once and extracts tool_results and
// images. Replaces the former pattern of calling ExtractToolResults and
// ExtractImages separately, which scanned the block list twice.
//
// Images nested inside tool_result blocks are promoted to the returned images
// slice so they reach the model. Kiro's ToolResultContent only carries Text or
// JSON (see kiroproto.ToolResultContent), so an image cannot be attached to the
// tool result itself; UserInputMessage.Images is the only image channel for the
// current turn. Without promotion, a tool that returns an image (for example
// Read on a PNG) yields no text, so stdout becomes "(empty result)" and the
// image is silently lost.
func scanCurrentMessage(content anthropic.MessageContent) (toolResults []kiroproto.ToolResult, images []kiroproto.Image) {
	if content.IsString() {
		return nil, nil
	}
	for _, b := range content.Blocks {
		switch {
		case b.IsToolResult():
			status := kiroproto.ToolResultStatusSuccess
			exitStatus := "0"
			if b.IsError {
				status = kiroproto.ToolResultStatusError
				exitStatus = "1"
			}
			text := extractToolResultContentText(b)
			// Promote any images carried by this tool_result to the turn-level
			// image list, and describe them in stdout so the text and the
			// attached images stay correlated for the model.
			promoted := extractToolResultImages(b)
			if len(promoted) > 0 {
				images = append(images, promoted...)
				text = appendImageNotice(text, len(promoted))
			}
			if text == "" {
				text = "(empty result)"
			}
			toolResults = append(toolResults, kiroproto.ToolResult{
				ToolUseID: b.ToolUseID,
				Status:    status,
				Content: []kiroproto.ToolResultContent{{JSON: map[string]any{
					"exit_status": exitStatus,
					"stdout":      text,
					"stderr":      "",
				}}},
			})
		case b.Type == anthropic.BlockTypeImage && b.Source != nil:
			if img, ok := convertImageBlock(b); ok {
				images = append(images, img)
			}
		}
	}
	return toolResults, images
}

// convertImageBlock converts a base64 Anthropic image block to a Kiro image.
// Non-base64 sources (for example URL references) are skipped with a warning
// because the Kiro wire format only accepts inline bytes.
func convertImageBlock(b anthropic.ContentBlock) (kiroproto.Image, bool) {
	if b.Source == nil {
		return kiroproto.Image{}, false
	}
	if b.Source.Type != "base64" {
		slog.Warn("skipping non-base64 image source type", "type", b.Source.Type)
		return kiroproto.Image{}, false
	}
	format := b.Source.MediaType
	if idx := strings.LastIndex(format, "/"); idx >= 0 {
		format = format[idx+1:]
	}
	return kiroproto.Image{
		Format: format,
		Source: kiroproto.ImageSource{Bytes: b.Source.Data},
	}, true
}

// extractToolResultImages collects image blocks nested in a tool_result's
// content. A tool_result whose content is a plain string carries no images.
func extractToolResultImages(b anthropic.ContentBlock) []kiroproto.Image {
	if b.Content.IsString() {
		return nil
	}
	var images []kiroproto.Image
	for _, cb := range b.Content.Blocks {
		if cb.Type != anthropic.BlockTypeImage || cb.Source == nil {
			continue
		}
		if img, ok := convertImageBlock(cb); ok {
			images = append(images, img)
		}
	}
	return images
}

// appendImageNotice records in the tool result's stdout that images were moved
// to the message-level image list, so the model can tell which tool produced
// them instead of seeing an unexplained attachment.
func appendImageNotice(text string, n int) string {
	notice := fmt.Sprintf("[%d image(s) from this tool result attached to the message]", n)
	if text == "" {
		return notice
	}
	return text + "\n" + notice
}
