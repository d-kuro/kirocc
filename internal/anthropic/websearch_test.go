package anthropic

import (
	"encoding/json/v2"
	"testing"
)

func TestWebSearchToolIsServerTool(t *testing.T) {
	def := Tool{Type: ToolTypeWebSearch, Name: "web_search", MaxUses: 8}
	if !def.IsWebSearchTool() {
		t.Error("IsWebSearchTool = false for a web_search_20250305 definition")
	}
	// Forwarding it to Kiro as a callable function tool is the bug this guards:
	// the executor calls it and the client gets a tool_use block it cannot
	// answer, so the search silently returns nothing.
	if !def.IsServerTool() {
		t.Error("IsServerTool = false, so the definition would reach Kiro as a callable tool")
	}
}

func TestCallableToolsDropsWebSearch(t *testing.T) {
	tools := []Tool{
		{Name: "Read", Description: "Read a file"},
		{Type: ToolTypeWebSearch, Name: "web_search"},
	}
	callable := CallableTools(tools)
	if len(callable) != 1 || callable[0].Name != "Read" {
		t.Fatalf("CallableTools = %+v, want only Read", callable)
	}
	if FindWebSearchTool(tools) == nil {
		t.Error("FindWebSearchTool = nil, want the definition")
	}
	if FindWebSearchTool(callable) != nil {
		t.Error("FindWebSearchTool on the callable set = non-nil, want nil")
	}
}

func TestWebSearchToolUnmarshalsDomains(t *testing.T) {
	var tool Tool
	raw := `{"type":"web_search_20250305","name":"web_search","max_uses":8,
	         "allowed_domains":["example.com"],"blocked_domains":["spam.example"]}`
	if err := json.Unmarshal([]byte(raw), &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tool.AllowedDomains) != 1 || tool.AllowedDomains[0] != "example.com" {
		t.Errorf("AllowedDomains = %v, want [example.com]", tool.AllowedDomains)
	}
	if len(tool.BlockedDomains) != 1 || tool.BlockedDomains[0] != "spam.example" {
		t.Errorf("BlockedDomains = %v, want [spam.example]", tool.BlockedDomains)
	}
	if tool.MaxUses != 8 {
		t.Errorf("MaxUses = %d, want 8", tool.MaxUses)
	}
}

// A client replaying a search round sends the result block back; the hits have
// to survive the round trip or the executor re-searches what it already found.
func TestWebSearchToolResultUnmarshal(t *testing.T) {
	var block ContentBlock
	raw := `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[
	          {"type":"web_search_result","title":"Selters weather","url":"https://example.com/w","page_age":"2 hours ago"}]}`
	if err := json.Unmarshal([]byte(raw), &block); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !block.IsServerToolResult() {
		t.Error("IsServerToolResult = false, so the round would be dropped from history")
	}
	if len(block.Content.Blocks) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(block.Content.Blocks))
	}
	inner := block.Content.Blocks[0]
	if inner.Type != BlockTypeWebSearchResult {
		t.Errorf("inner type = %q, want %q", inner.Type, BlockTypeWebSearchResult)
	}
	if inner.Title != "Selters weather" || inner.URL != "https://example.com/w" {
		t.Errorf("inner = %+v, want the title and URL preserved", inner)
	}
	if inner.PageAge != "2 hours ago" {
		t.Errorf("page age = %q, want it preserved", inner.PageAge)
	}
}

func TestWebSearchToolResultErrorUnmarshal(t *testing.T) {
	var block ContentBlock
	raw := `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1",
	         "content":{"type":"web_search_tool_result_error","error_code":"too_many_requests"}}`
	if err := json.Unmarshal([]byte(raw), &block); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(block.Content.Blocks) != 1 {
		t.Fatalf("content blocks = %d, want 1 (object content is stored as one block)", len(block.Content.Blocks))
	}
	if got := block.Content.Blocks[0].ErrorCode; got != "too_many_requests" {
		t.Errorf("error_code = %q, want too_many_requests", got)
	}
}
