package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestExtractSystemPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt anthropic.SystemPrompt
		want   string
	}{
		{
			name:   "string",
			prompt: anthropic.SystemPrompt{Text: "You are helpful."},
			want:   "You are helpful.",
		},
		{
			name: "array",
			prompt: anthropic.SystemPrompt{
				Blocks: []anthropic.SystemBlock{
					{Type: "text", Text: "Part 1"},
					{Type: "text", Text: "Part 2"},
				},
			},
			want: "Part 1\nPart 2",
		},
		{
			name:   "empty",
			prompt: anthropic.SystemPrompt{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSystemPrompt(tt.prompt)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
