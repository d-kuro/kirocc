package reqconv

import (
	"slices"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestExtractToolReferences(t *testing.T) {
	tests := []struct {
		name     string
		messages []anthropic.Message
		want     []string
	}{
		{
			name:     "empty_messages",
			messages: nil,
			want:     nil,
		},
		{
			name: "string_content_skipped",
			messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{Text: "hello"}},
			},
			want: nil,
		},
		{
			name: "top_level_tool_reference",
			messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: anthropic.BlockTypeToolReference, ToolName: "Read"},
					},
				}},
			},
			want: []string{"Read"},
		},
		{
			name: "tool_search_tool_result_nested",
			messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: anthropic.BlockTypeToolSearchToolResult, Content: anthropic.MessageContent{
							Blocks: []anthropic.ContentBlock{
								{Type: anthropic.BlockTypeToolSearchSearchResult, ToolReferences: []anthropic.ContentBlock{
									{Type: anthropic.BlockTypeToolReference, ToolName: "Edit"},
								}},
							},
						}},
					},
				}},
			},
			want: []string{"Edit"},
		},
		{
			name: "tool_result_nested_tool_reference",
			messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: anthropic.BlockTypeToolResult, Content: anthropic.MessageContent{
							Blocks: []anthropic.ContentBlock{
								{Type: anthropic.BlockTypeToolReference, ToolName: "Grep"},
							},
						}},
					},
				}},
			},
			want: []string{"Grep"},
		},
		{
			name: "deduplication",
			messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: anthropic.BlockTypeToolReference, ToolName: "Read"},
						{Type: anthropic.BlockTypeToolReference, ToolName: "Read"},
					},
				}},
			},
			want: []string{"Read"},
		},
		{
			name: "empty_tool_name_skipped",
			messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Blocks: []anthropic.ContentBlock{
						{Type: anthropic.BlockTypeToolReference, ToolName: ""},
						{Type: anthropic.BlockTypeToolReference, ToolName: "Bash"},
					},
				}},
			},
			want: []string{"Bash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolReferences(tt.messages)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
