package anthropic

import (
	"encoding/json/v2"
	"testing"
)

func TestToolUnmarshalAdvisorFields(t *testing.T) {
	const raw = `{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5","max_uses":2,"max_tokens":4096,"caching":{"type":"ephemeral","ttl":"1h"}}`

	var tool Tool
	if err := json.Unmarshal([]byte(raw), &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !tool.IsAdvisorTool() {
		t.Errorf("IsAdvisorTool() = false, want true")
	}
	if !tool.IsServerTool() {
		t.Errorf("IsServerTool() = false, want true")
	}
	if tool.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", tool.Model)
	}
	if tool.MaxUses != 2 {
		t.Errorf("MaxUses = %d, want 2", tool.MaxUses)
	}
	if tool.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", tool.MaxTokens)
	}
	if tool.Caching == nil || tool.Caching.TTL != AdvisorCacheTTL1h {
		t.Fatalf("Caching = %+v, want ephemeral/1h", tool.Caching)
	}
	if !tool.Caching.IsValid() {
		t.Errorf("Caching.IsValid() = false, want true")
	}
}

func TestAdvisorCachingIsValid(t *testing.T) {
	tests := []struct {
		name    string
		caching *AdvisorCaching
		want    bool
	}{
		{"nil is valid", nil, true},
		{"ephemeral without ttl", &AdvisorCaching{Type: "ephemeral"}, true},
		{"ephemeral 5m", &AdvisorCaching{Type: "ephemeral", TTL: "5m"}, true},
		{"ephemeral 1h", &AdvisorCaching{Type: "ephemeral", TTL: "1h"}, true},
		{"unknown ttl", &AdvisorCaching{Type: "ephemeral", TTL: "2h"}, false},
		{"unknown type", &AdvisorCaching{Type: "persistent"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.caching.IsValid(); got != tt.want {
				t.Errorf("IsValid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCallableToolsFiltersServerTools(t *testing.T) {
	tools := []Tool{
		{Name: "Read"},
		{Type: ToolTypeAdvisor, Name: AdvisorToolName, Model: "claude-opus-5"},
		{Name: "Write"},
		{Type: ToolTypeSearchRegex, Name: "tool_search_tool_regex"},
	}
	got := CallableTools(tools)
	if len(got) != 2 || got[0].Name != "Read" || got[1].Name != "Write" {
		t.Fatalf("CallableTools() = %+v, want [Read Write]", got)
	}
	// The input slice must not be mutated.
	if len(tools) != 4 || tools[1].Type != ToolTypeAdvisor {
		t.Fatalf("input mutated: %+v", tools)
	}
}

func TestCallableToolsPassthroughWhenNoServerTools(t *testing.T) {
	tools := []Tool{{Name: "Read"}, {Name: "Write"}}
	got := CallableTools(tools)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestFindAdvisorTool(t *testing.T) {
	if got := FindAdvisorTool([]Tool{{Name: "Read"}}); got != nil {
		t.Errorf("FindAdvisorTool() = %+v, want nil", got)
	}
	tools := []Tool{{Name: "Read"}, {Type: ToolTypeAdvisor, Name: AdvisorToolName, Model: "claude-opus-5"}}
	got := FindAdvisorTool(tools)
	if got == nil || got.Model != "claude-opus-5" {
		t.Fatalf("FindAdvisorTool() = %+v, want the advisor entry", got)
	}
}

func TestIsServerToolResult(t *testing.T) {
	tests := []struct {
		blockType string
		want      bool
	}{
		{BlockTypeAdvisorToolResult, true},
		{BlockTypeToolSearchToolResult, true},
		{BlockTypeToolResult, false},
		{BlockTypeText, false},
	}
	for _, tt := range tests {
		t.Run(tt.blockType, func(t *testing.T) {
			b := ContentBlock{Type: tt.blockType}
			if got := b.IsServerToolResult(); got != tt.want {
				t.Errorf("IsServerToolResult() = %v, want %v", got, tt.want)
			}
			// advisor_tool_result must NOT be treated as a user tool_result.
			if tt.blockType == BlockTypeAdvisorToolResult && b.IsToolResult() {
				t.Error("advisor_tool_result must not satisfy IsToolResult()")
			}
		})
	}
}

func TestAdvisorToolResultUnmarshalsNestedContent(t *testing.T) {
	const raw = `{"type":"advisor_tool_result","tool_use_id":"srvtoolu_x","content":{"type":"advisor_result","text":"do this","stop_reason":"end_turn"}}`
	var b ContentBlock
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(b.Content.Blocks) != 1 {
		t.Fatalf("Content.Blocks len = %d, want 1", len(b.Content.Blocks))
	}
	inner := b.Content.Blocks[0]
	if inner.Type != BlockTypeAdvisorResult {
		t.Errorf("inner type = %q, want %q", inner.Type, BlockTypeAdvisorResult)
	}
	if inner.Text != "do this" {
		t.Errorf("inner text = %q, want %q", inner.Text, "do this")
	}
	if inner.StopReason != "end_turn" {
		t.Errorf("inner stop_reason = %q, want end_turn", inner.StopReason)
	}
}

func TestAdvisorRedactedResultUnmarshal(t *testing.T) {
	const raw = `{"type":"advisor_tool_result","tool_use_id":"srvtoolu_x","content":{"type":"advisor_redacted_result","encrypted_content":"AAAA","stop_reason":"end_turn"}}`
	var b ContentBlock
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inner := b.Content.Blocks[0]
	if inner.Type != BlockTypeAdvisorRedactedResult {
		t.Errorf("inner type = %q", inner.Type)
	}
	if inner.EncryptedContent != "AAAA" {
		t.Errorf("encrypted_content = %q, want AAAA", inner.EncryptedContent)
	}
}

func TestAdvisorErrorResultUnmarshal(t *testing.T) {
	const raw = `{"type":"advisor_tool_result","tool_use_id":"srvtoolu_x","content":{"type":"advisor_tool_result_error","error_code":"model_not_found"}}`
	var b ContentBlock
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inner := b.Content.Blocks[0]
	if inner.ErrorCode != AdvisorErrorModelNotFound {
		t.Errorf("error_code = %q, want %q", inner.ErrorCode, AdvisorErrorModelNotFound)
	}
}
