package reqconv

import (
	"log/slog"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// ExtractImages extracts image blocks from message content and converts to Kiro format.
// URL-based images are skipped with a warning log.
func ExtractImages(content anthropic.MessageContent) []kiroproto.Image {
	if content.IsString() {
		return nil
	}
	var images []kiroproto.Image
	for _, b := range content.Blocks {
		if b.Type != anthropic.BlockTypeImage || b.Source == nil {
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
