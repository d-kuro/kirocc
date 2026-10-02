package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// Claude Code sends a tool_result turn followed directly by a user text turn
// (hook feedback, reminders, a queued prompt). A synthetic "(empty)" assistant
// between them shows the model its own turns as empty and primes empty replies.
func TestNormalize_ToolResultThenUserText_NoSyntheticEmpty(t *testing.T) {
	toolResult := anthropic.ContentBlock{Type: "tool_result", ToolUseID: "t1", Content: anthropic.MessageContent{Text: "a.txt"}}
	tests := []struct {
		name     string
		turns    []anthropic.Message
		wantText string
	}{
		{
			name: "tool_result then text",
			turns: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{toolResult}}},
				{Role: "user", Content: anthropic.MessageContent{Text: "reminder"}},
			},
			wantText: "reminder",
		},
		{
			name: "turn boundary keeps a newline",
			turns: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					toolResult,
					{Type: "text", Text: "Stop hook: tests failing"},
				}}},
				{Role: "user", Content: anthropic.MessageContent{Text: "also update docs"}},
			},
			wantText: "Stop hook: tests failing\nalso update docs",
		},
		{
			name: "three turns",
			turns: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{toolResult}}},
				{Role: "user", Content: anthropic.MessageContent{Text: "first"}},
				{Role: "user", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					{Type: "text", Text: "second"},
					{Type: "text", Text: "third"},
				}}},
			},
			wantText: "first\nsecond third",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := append([]anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{Text: "run it"}},
				{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					{Type: "tool_use", ID: "t1", Name: "Bash", Input: map[string]any{"command": "ls"}},
				}}},
			}, tt.turns...)
			got := Normalize(msgs, true)
			if len(got) != 3 {
				t.Fatalf("want 3 messages, got %d: %+v", len(got), got)
			}
			last := got[2]
			toolResults, _ := scanMessageContent(last.Content)
			if len(toolResults) != 1 || toolResults[0].ToolUseID != "t1" {
				t.Fatalf("tool_result lost: %+v", last)
			}
			if text := ExtractTextContent(last.Content); text != tt.wantText {
				t.Fatalf("text = %q, want %q", text, tt.wantText)
			}
		})
	}
}
