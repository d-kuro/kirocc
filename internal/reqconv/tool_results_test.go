package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestExtractToolResults(t *testing.T) {
	tests := []struct {
		name           string
		content        anthropic.MessageContent
		wantLen        int
		wantStatus     string // check first result's Status if non-empty
		wantStdout     string // check first result's Content[0].JSON["stdout"] if non-empty
		wantExitStatus string // check first result's Content[0].JSON["exit_status"] if non-empty
		wantID         string // check first result's ToolUseID if non-empty
	}{
		{
			name: "basic",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_01", Content: anthropic.MessageContent{Text: "Sunny, 25°C"}},
				},
			},
			wantLen:        1,
			wantStatus:     "success",
			wantStdout:     "Sunny, 25°C",
			wantExitStatus: "0",
			wantID:         "toolu_01",
		},
		{
			name: "empty_content",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_01", Content: anthropic.MessageContent{Text: ""}},
				},
			},
			wantLen:        1,
			wantStdout:     "(empty result)",
			wantExitStatus: "0",
		},
		{
			name: "is_error",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_01", Content: anthropic.MessageContent{Text: "Error"}, IsError: true},
				},
			},
			wantLen:        1,
			wantStatus:     "error",
			wantExitStatus: "1",
		},
		{
			name:    "string_content",
			content: anthropic.MessageContent{Text: "just text"},
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToolResults(tt.content)
			if len(got) != tt.wantLen {
				t.Fatalf("got %d, want %d", len(got), tt.wantLen)
			}
			if tt.wantID != "" && got[0].ToolUseID != tt.wantID {
				t.Fatalf("tool_use_id = %q, want %q", got[0].ToolUseID, tt.wantID)
			}
			if tt.wantStatus != "" && got[0].Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got[0].Status, tt.wantStatus)
			}
			if tt.wantStdout != "" {
				stdout, ok := got[0].Content[0].JSON["stdout"].(string)
				if !ok {
					t.Fatalf("JSON[\"stdout\"] is not a string: %v", got[0].Content[0].JSON)
				}
				if stdout != tt.wantStdout {
					t.Fatalf("JSON[\"stdout\"] = %q, want %q", stdout, tt.wantStdout)
				}
			}
			if tt.wantExitStatus != "" {
				es, ok := got[0].Content[0].JSON["exit_status"].(string)
				if !ok {
					t.Fatalf("JSON[\"exit_status\"] is not a string: %v", got[0].Content[0].JSON)
				}
				if es != tt.wantExitStatus {
					t.Fatalf("JSON[\"exit_status\"] = %q, want %q", es, tt.wantExitStatus)
				}
			}
		})
	}
}

func TestExtractToolUses_Basic(t *testing.T) {
	content := anthropic.MessageContent{
		Blocks: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "toolu_01", Name: "get_weather", Input: map[string]any{"city": "Tokyo"}},
		},
	}
	got := ExtractToolUses(content)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].ToolUseID != "toolu_01" || got[0].Name != "get_weather" {
		t.Fatalf("unexpected: %+v", got[0])
	}
}

func TestExtractThinkingToolUses(t *testing.T) {
	tests := []struct {
		name           string
		content        anthropic.MessageContent
		wantToolUseLen int
		wantToolName   string
		wantThought    string
	}{
		{
			name: "single thinking block",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "thinking", Thinking: "Step 1: analyze"},
					{Type: "text", Text: "The answer"},
				},
			},
			wantToolUseLen: 1,
			wantToolName:   "thinking",
			wantThought:    "Step 1: analyze",
		},
		{
			name: "no thinking blocks",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "text", Text: "Just text"},
				},
			},
			wantToolUseLen: 0,
		},
		{
			name:           "string content",
			content:        anthropic.MessageContent{Text: "plain string"},
			wantToolUseLen: 0,
		},
		{
			name: "multiple thinking blocks",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "thinking", Thinking: "First thought"},
					{Type: "text", Text: "Some text"},
					{Type: "thinking", Thinking: "Second thought"},
				},
			},
			wantToolUseLen: 2,
			wantToolName:   "thinking",
		},
		{
			name: "empty thinking",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "thinking", Thinking: ""},
				},
			},
			wantToolUseLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolUses := ExtractThinkingToolUses(tt.content)
			if len(toolUses) != tt.wantToolUseLen {
				t.Fatalf("toolUses len = %d, want %d", len(toolUses), tt.wantToolUseLen)
			}
			if tt.wantToolUseLen > 0 {
				if toolUses[0].Name != tt.wantToolName {
					t.Fatalf("name = %q, want %q", toolUses[0].Name, tt.wantToolName)
				}
				input, ok := toolUses[0].Input.(map[string]any)
				if !ok {
					t.Fatal("input should be map[string]any")
				}
				if tt.wantThought != "" && input["thought"] != tt.wantThought {
					t.Fatalf("thought = %q, want %q", input["thought"], tt.wantThought)
				}
			}
		})
	}
}

func TestReorderToolResults(t *testing.T) {
	r1 := kiroproto.ToolResult{ToolUseID: "a", Status: "success"}
	r2 := kiroproto.ToolResult{ToolUseID: "b", Status: "success"}
	r3 := kiroproto.ToolResult{ToolUseID: "c", Status: "success"}

	tests := []struct {
		name       string
		results    []kiroproto.ToolResult
		toolUseIDs []string
		wantOrder  []string
	}{
		{
			name:       "reorder to match assistant order",
			results:    []kiroproto.ToolResult{r3, r1, r2},
			toolUseIDs: []string{"a", "b", "c"},
			wantOrder:  []string{"a", "b", "c"},
		},
		{
			name:       "already in order",
			results:    []kiroproto.ToolResult{r1, r2, r3},
			toolUseIDs: []string{"a", "b", "c"},
			wantOrder:  []string{"a", "b", "c"},
		},
		{
			name:       "empty toolUseIDs",
			results:    []kiroproto.ToolResult{r3, r1},
			toolUseIDs: nil,
			wantOrder:  []string{"c", "a"},
		},
		{
			name:       "single result",
			results:    []kiroproto.ToolResult{r1},
			toolUseIDs: []string{"a"},
			wantOrder:  []string{"a"},
		},
		{
			name:       "extra results not in toolUseIDs appended",
			results:    []kiroproto.ToolResult{r3, r1, r2},
			toolUseIDs: []string{"b"},
			wantOrder:  []string{"b", "c", "a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReorderToolResults(tt.results, tt.toolUseIDs)
			if len(got) != len(tt.wantOrder) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.wantOrder))
			}
			for i, want := range tt.wantOrder {
				if got[i].ToolUseID != want {
					t.Fatalf("got[%d].ToolUseID = %q, want %q", i, got[i].ToolUseID, want)
				}
			}
		})
	}
}

func TestExtractImages(t *testing.T) {
	tests := []struct {
		name       string
		content    anthropic.MessageContent
		wantLen    int
		wantFormat string // check first result's Format if non-empty
		wantBytes  string // check first result's Source.Bytes if non-empty
	}{
		{
			name: "base64_png",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "image", Source: &anthropic.ImageSource{Type: "base64", MediaType: "image/png", Data: "iVBOR"}},
				},
			},
			wantLen:    1,
			wantFormat: "png",
			wantBytes:  "iVBOR",
		},
		{
			name: "jpeg",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "image", Source: &anthropic.ImageSource{Type: "base64", MediaType: "image/jpeg", Data: "data"}},
				},
			},
			wantLen:    1,
			wantFormat: "jpeg",
			wantBytes:  "data",
		},
		{
			name: "url_skipped",
			content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "image", Source: &anthropic.ImageSource{Type: "url", MediaType: "image/png", Data: "http://example.com/img.png"}},
				},
			},
			wantLen: 0,
		},
		{
			name:    "string_content",
			content: anthropic.MessageContent{Text: "just text"},
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractImages(tt.content)
			if len(got) != tt.wantLen {
				t.Fatalf("got %d, want %d", len(got), tt.wantLen)
			}
			if tt.wantFormat != "" && got[0].Format != tt.wantFormat {
				t.Fatalf("format = %q, want %q", got[0].Format, tt.wantFormat)
			}
			if tt.wantBytes != "" && got[0].Source.Bytes != tt.wantBytes {
				t.Fatalf("bytes = %q, want %q", got[0].Source.Bytes, tt.wantBytes)
			}
		})
	}
}
