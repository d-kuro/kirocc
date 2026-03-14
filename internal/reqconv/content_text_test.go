package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestExtractTextContent(t *testing.T) {
	tests := []struct {
		name    string
		content anthropic.MessageContent
		want    string
	}{
		{
			name:    "string",
			content: anthropic.MessageContent{Text: "Hello world"},
			want:    "Hello world",
		},
		{
			name: "text_blocks",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "text", Text: "Hello"},
					{Type: "text", Text: "World"},
				},
			},
			want: "Hello World",
		},
		{
			name: "ignores_thinking",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "thinking", Thinking: "Let me think..."},
					{Type: "text", Text: "Answer"},
				},
			},
			want: "Answer",
		},
		{
			name: "ignores_tool_use",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "text", Text: "I'll check."},
					{Type: "tool_use", ID: "toolu_01", Name: "get_weather"},
				},
			},
			want: "I'll check.",
		},
		{
			name: "ignores_tool_reference",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_reference", ToolName: "Bash"},
				},
			},
			want: "",
		},
		{
			name: "ignores_server_tool_use",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeServerToolUse, ID: "stu_01", Name: "web_search"},
					{Type: "text", Text: "Result"},
				},
			},
			want: "Result",
		},
		{
			name: "ignores_tool_search_tool_result",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeToolSearchToolResult, ToolUseID: "ts_01"},
					{Type: "text", Text: "Done"},
				},
			},
			want: "Done",
		},
		{
			name: "unknown_block_no_identifier",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "unknown_type"},
				},
			},
			want: "[unknown_type]",
		},
		{
			name:    "empty",
			content: anthropic.MessageContent{},
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTextContent(tt.content)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
