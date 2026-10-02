package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// Claude Code sends a tool_result turn followed directly by a user text turn
// (hook feedback, reminders, a queued prompt). A synthetic "(empty)" assistant
// between them shows the model its own turns as empty and primes empty replies.
func TestNormalize_ToolResultThenUserText_NoSyntheticEmpty(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "run it"}},
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "t1", Name: "Bash", Input: map[string]any{"command": "ls"}},
		}}},
		{Role: "user", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: "tool_result", ToolUseID: "t1", Content: anthropic.MessageContent{Text: "a.txt"}},
		}}},
		{Role: "user", Content: anthropic.MessageContent{Text: "reminder"}},
	}
	got := Normalize(msgs, true)
	for _, m := range got {
		if m.Content.Text == syntheticEmpty {
			t.Fatalf("synthetic (empty) inserted: %+v", got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(got), got)
	}
	last := got[2]
	toolResults, _ := scanMessageContent(last.Content)
	if len(toolResults) != 1 || toolResults[0].ToolUseID != "t1" {
		t.Fatalf("tool_result lost: %+v", last)
	}
	if ExtractTextContent(last.Content) != "reminder" {
		t.Fatalf("text lost: %q", ExtractTextContent(last.Content))
	}
}
