package reqconv

import (
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// Images nested in a tool_result must be promoted to the message-level image
// list. Kiro's ToolResultContent carries only Text or JSON, so without
// promotion an image-only tool result (e.g. the Read tool on a PNG) collapses
// to "(empty result)" and the image never reaches the model.
func TestScanCurrentMessagePromotesToolResultImages(t *testing.T) {
	content := anthropic.MessageContent{
		Blocks: []anthropic.ContentBlock{{
			Type:      anthropic.BlockTypeToolResult,
			ToolUseID: "toolu_read_png",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{{
					Type: anthropic.BlockTypeImage,
					Source: &anthropic.ImageSource{
						Type:      "base64",
						MediaType: "image/png",
						Data:      "iVBORw0KGgo=",
					},
				}},
			},
		}},
	}

	toolResults, images := scanCurrentMessage(content)

	if len(toolResults) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(toolResults))
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 promoted image, got %d", len(images))
	}
	if images[0].Format != "png" {
		t.Errorf("expected format png, got %q", images[0].Format)
	}
	if images[0].Source.Bytes != "iVBORw0KGgo=" {
		t.Errorf("image bytes not carried through, got %q", images[0].Source.Bytes)
	}

	stdout, _ := toolResults[0].Content[0].JSON["stdout"].(string)
	if stdout == "(empty result)" {
		t.Error("stdout is still \"(empty result)\"; the image was not accounted for")
	}
	if !strings.Contains(stdout, "1 image(s)") {
		t.Errorf("stdout should note the attached image, got %q", stdout)
	}
}

// A tool_result mixing text and an image keeps its text and appends the notice.
func TestScanCurrentMessageToolResultTextPlusImage(t *testing.T) {
	content := anthropic.MessageContent{
		Blocks: []anthropic.ContentBlock{{
			Type:      anthropic.BlockTypeToolResult,
			ToolUseID: "toolu_mixed",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeText, Text: "rendered ortho_front.png"},
					{Type: anthropic.BlockTypeImage, Source: &anthropic.ImageSource{
						Type: "base64", MediaType: "image/jpeg", Data: "/9j/4AAQ",
					}},
				},
			},
		}},
	}

	toolResults, images := scanCurrentMessage(content)

	if len(images) != 1 || images[0].Format != "jpeg" {
		t.Fatalf("expected one jpeg image, got %+v", images)
	}
	stdout, _ := toolResults[0].Content[0].JSON["stdout"].(string)
	if !strings.Contains(stdout, "rendered ortho_front.png") {
		t.Errorf("original text lost, got %q", stdout)
	}
	if !strings.Contains(stdout, "1 image(s)") {
		t.Errorf("missing image notice, got %q", stdout)
	}
}

// Non-base64 image sources cannot be sent inline and must be skipped rather
// than emitted with empty bytes.
func TestScanCurrentMessageSkipsNonBase64ToolResultImage(t *testing.T) {
	content := anthropic.MessageContent{
		Blocks: []anthropic.ContentBlock{{
			Type:      anthropic.BlockTypeToolResult,
			ToolUseID: "toolu_url",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{{
					Type: anthropic.BlockTypeImage,
					Source: &anthropic.ImageSource{
						Type: "url", MediaType: "image/png", Data: "https://example.com/a.png",
					},
				}},
			},
		}},
	}

	toolResults, images := scanCurrentMessage(content)

	if len(images) != 0 {
		t.Errorf("expected no images for url source, got %d", len(images))
	}
	stdout, _ := toolResults[0].Content[0].JSON["stdout"].(string)
	if stdout != "(empty result)" {
		t.Errorf("expected \"(empty result)\" when nothing usable was found, got %q", stdout)
	}
}

// Regression guard: a top-level image block (the paste-into-chat path) must
// keep working exactly as before.
func TestScanCurrentMessageTopLevelImageUnchanged(t *testing.T) {
	content := anthropic.MessageContent{
		Blocks: []anthropic.ContentBlock{{
			Type: anthropic.BlockTypeImage,
			Source: &anthropic.ImageSource{
				Type: "base64", MediaType: "image/webp", Data: "UklGRg==",
			},
		}},
	}

	toolResults, images := scanCurrentMessage(content)

	if len(toolResults) != 0 {
		t.Errorf("expected no tool results, got %d", len(toolResults))
	}
	if len(images) != 1 || images[0].Format != "webp" {
		t.Fatalf("expected one webp image, got %+v", images)
	}
}
