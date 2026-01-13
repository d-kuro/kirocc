package respconv

import "testing"

func TestDeduplicateToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		calls     []ToolCall
		wantLen   int
		wantInput string // optional: check first result's Input
	}{
		{
			name: "no_duplicates",
			calls: []ToolCall{
				{ID: "1", Name: "a", Input: `{"x":1}`},
				{ID: "2", Name: "b", Input: `{"y":2}`},
			},
			wantLen: 2,
		},
		{
			name: "id_dedup_keep_longer",
			calls: []ToolCall{
				{ID: "1", Name: "a", Input: `{"x":1}`},
				{ID: "1", Name: "a", Input: `{"x":1,"y":2}`},
			},
			wantLen:   1,
			wantInput: `{"x":1,"y":2}`,
		},
		{
			name: "name_args_dedup",
			calls: []ToolCall{
				{ID: "1", Name: "a", Input: `{"x": 1}`},
				{ID: "2", Name: "a", Input: `{"x":1}`}, // same after normalization
			},
			wantLen: 1,
		},
		{
			name:    "empty",
			calls:   nil,
			wantLen: 0,
		},
		{
			name:    "single",
			calls:   []ToolCall{{ID: "1", Name: "a", Input: `{}`}},
			wantLen: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeduplicateToolCalls(tt.calls)
			if len(got) != tt.wantLen {
				t.Fatalf("got %d, want %d", len(got), tt.wantLen)
			}
			if tt.wantInput != "" && got[0].Input != tt.wantInput {
				t.Fatalf("input = %s, want %s", got[0].Input, tt.wantInput)
			}
		})
	}
}
