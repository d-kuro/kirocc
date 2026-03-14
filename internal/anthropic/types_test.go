package anthropic

import (
	"encoding/json/v2"
	"testing"
)

func TestMessageContent_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantString bool
		wantText   string
		wantBlocks int
		check      func(t *testing.T, mc MessageContent)
	}{
		{
			name:       "string",
			raw:        `"Hello world"`,
			wantString: true,
			wantText:   "Hello world",
		},
		{
			name:       "array",
			raw:        `[{"type":"text","text":"Hello"},{"type":"text","text":"World"}]`,
			wantString: false,
			wantBlocks: 2,
			check: func(t *testing.T, mc MessageContent) {
				if mc.Blocks[0].Text != "Hello" {
					t.Fatalf("block[0].Text = %q", mc.Blocks[0].Text)
				}
			},
		},
		{
			name:       "tool_use",
			raw:        `[{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"city":"Tokyo"}}]`,
			wantBlocks: 1,
			check: func(t *testing.T, mc MessageContent) {
				b := mc.Blocks[0]
				if b.Type != "tool_use" || b.ID != "toolu_01" || b.Name != "get_weather" {
					t.Fatalf("unexpected tool_use block: %+v", b)
				}
				if b.Input["city"] != "Tokyo" {
					t.Fatalf("input.city = %v", b.Input["city"])
				}
			},
		},
		{
			name:       "tool_result",
			raw:        `[{"type":"tool_result","tool_use_id":"toolu_01","content":"Sunny, 25°C","is_error":false}]`,
			wantBlocks: 1,
			check: func(t *testing.T, mc MessageContent) {
				b := mc.Blocks[0]
				if b.Type != "tool_result" || b.ToolUseID != "toolu_01" {
					t.Fatalf("unexpected tool_result block: %+v", b)
				}
				if b.Content.Text != "Sunny, 25°C" {
					t.Fatalf("content = %q", b.Content.Text)
				}
				if b.IsError {
					t.Fatal("expected is_error == false")
				}
			},
		},
		{
			name:       "image",
			raw:        `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]`,
			wantBlocks: 1,
			check: func(t *testing.T, mc MessageContent) {
				b := mc.Blocks[0]
				if b.Type != "image" || b.Source == nil {
					t.Fatalf("unexpected image block: %+v", b)
				}
				if b.Source.MediaType != "image/png" || b.Source.Data != "iVBOR" {
					t.Fatalf("unexpected source: %+v", b.Source)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mc MessageContent
			if err := json.Unmarshal([]byte(tt.raw), &mc); err != nil {
				t.Fatal(err)
			}
			if tt.wantString && !mc.IsString() {
				t.Fatal("expected IsString() == true")
			}
			if !tt.wantString && mc.IsString() {
				t.Fatal("expected IsString() == false")
			}
			if tt.wantText != "" && mc.Text != tt.wantText {
				t.Fatalf("got %q, want %q", mc.Text, tt.wantText)
			}
			if tt.wantBlocks > 0 && len(mc.Blocks) != tt.wantBlocks {
				t.Fatalf("got %d blocks, want %d", len(mc.Blocks), tt.wantBlocks)
			}
			if tt.check != nil {
				tt.check(t, mc)
			}
		})
	}
}

func TestMessageContent_MarshalJSON_String(t *testing.T) {
	mc := MessageContent{Text: "hello"}
	data, err := json.Marshal(mc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"hello"` {
		t.Fatalf("got %s, want %q", data, `"hello"`)
	}
}

func TestMessageContent_MarshalJSON_Blocks(t *testing.T) {
	mc := MessageContent{Blocks: []ContentBlock{{Type: "text", Text: "hi"}}}
	data, err := json.Marshal(mc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == `"hi"` {
		t.Fatal("should not marshal as string when blocks are set")
	}
}

func TestSystemPrompt_UnmarshalJSON_String(t *testing.T) {
	var sp SystemPrompt
	if err := json.Unmarshal([]byte(`"You are helpful."`), &sp); err != nil {
		t.Fatal(err)
	}
	if sp.Text != "You are helpful." {
		t.Fatalf("got %q", sp.Text)
	}
	if sp.IsEmpty() {
		t.Fatal("should not be empty")
	}
}

func TestSystemPrompt_UnmarshalJSON_Array(t *testing.T) {
	raw := `[{"type":"text","text":"Part 1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Part 2"}]`
	var sp SystemPrompt
	if err := json.Unmarshal([]byte(raw), &sp); err != nil {
		t.Fatal(err)
	}
	if len(sp.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(sp.Blocks))
	}
	if sp.Blocks[0].Text != "Part 1" {
		t.Fatalf("block[0].Text = %q", sp.Blocks[0].Text)
	}
	if sp.Blocks[0].CacheControl == nil || sp.Blocks[0].CacheControl.Type != "ephemeral" {
		t.Fatal("expected cache_control on block[0]")
	}
	if sp.Blocks[1].CacheControl != nil {
		t.Fatal("expected no cache_control on block[1]")
	}
}

func TestSystemPrompt_UnmarshalJSON_Null(t *testing.T) {
	var sp SystemPrompt
	if err := json.Unmarshal([]byte(`null`), &sp); err != nil {
		t.Fatal(err)
	}
	if !sp.IsEmpty() {
		t.Fatal("should be empty")
	}
}

func TestRequest_UnmarshalJSON_Full(t *testing.T) {
	raw := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 8096,
		"stream": true,
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": [{"type": "text", "text": "Hi there"}]}
		],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}
			}
		]
	}`
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", req.Model)
	}
	if !req.Stream {
		t.Fatal("expected stream == true")
	}
	if req.System.Text != "You are helpful." {
		t.Fatalf("system = %q", req.System.Text)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("got %d messages", len(req.Messages))
	}
	if !req.Messages[0].Content.IsString() {
		t.Fatal("message[0] should be string content")
	}
	if req.Messages[1].Content.IsString() {
		t.Fatal("message[1] should be block content")
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("tools: %+v", req.Tools)
	}
}

func TestContentBlock_CacheControl(t *testing.T) {
	raw := `{"type":"text","text":"cached","cache_control":{"type":"ephemeral"}}`
	var b ContentBlock
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if b.CacheControl == nil || b.CacheControl.Type != "ephemeral" {
		t.Fatal("expected cache_control")
	}
}

func TestTool_CacheControl(t *testing.T) {
	raw := `{"name":"t","description":"d","input_schema":{},"cache_control":{"type":"ephemeral"}}`
	var tool Tool
	if err := json.Unmarshal([]byte(raw), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.CacheControl == nil || tool.CacheControl.Type != "ephemeral" {
		t.Fatal("expected cache_control on tool")
	}
}

func TestContentBlock_IsToolUse(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{BlockTypeToolUse, true},
		{BlockTypeServerToolUse, true},
		{"text", false},
		{"tool_result", false},
	}
	for _, tt := range tests {
		b := ContentBlock{Type: tt.typ}
		if got := b.IsToolUse(); got != tt.want {
			t.Fatalf("IsToolUse(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestContentBlock_IsToolResult(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{BlockTypeToolResult, true},
		{BlockTypeToolSearchToolResult, true},
		{"text", false},
		{"tool_use", false},
	}
	for _, tt := range tests {
		b := ContentBlock{Type: tt.typ}
		if got := b.IsToolResult(); got != tt.want {
			t.Fatalf("IsToolResult(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestTool_IsToolSearchTool(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{ToolTypeSearchRegex, true},
		{ToolTypeSearchBM25, true},
		{"", false},
		{"custom", false},
	}
	for _, tt := range tests {
		tool := Tool{Type: tt.typ}
		if got := tool.IsToolSearchTool(); got != tt.want {
			t.Fatalf("IsToolSearchTool(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestMessageContent_UnmarshalJSON_Object(t *testing.T) {
	raw := `{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"Read"}]}`
	var mc MessageContent
	if err := json.Unmarshal([]byte(raw), &mc); err != nil {
		t.Fatal(err)
	}
	if len(mc.Blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(mc.Blocks))
	}
	b := mc.Blocks[0]
	if b.Type != BlockTypeToolSearchSearchResult {
		t.Fatalf("type = %q, want %q", b.Type, BlockTypeToolSearchSearchResult)
	}
	if len(b.ToolReferences) != 1 || b.ToolReferences[0].ToolName != "Read" {
		t.Fatalf("tool_references = %+v", b.ToolReferences)
	}
}
