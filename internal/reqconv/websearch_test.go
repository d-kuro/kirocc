package reqconv

import (
	"context"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/websearch"
)

type nopProvider struct{}

func (nopProvider) Name() string { return "nop" }
func (nopProvider) Search(_ context.Context, _ websearch.Query) ([]websearch.Result, error) {
	return nil, nil
}

// The client's web_search definition must never reach Kiro as a callable tool;
// the synthetic one with a real query schema takes its place.
func TestBuildPayloadReplacesWebSearchDefinition(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "Perform a web search for the query: weather"}},
		},
		Tools: []anthropic.Tool{
			{Type: anthropic.ToolTypeWebSearch, Name: "web_search", MaxUses: 8},
		},
	}
	wsCtx := websearch.NewContext(req.Tools, nopProvider{}, 0)
	if wsCtx == nil {
		t.Fatal("websearch.NewContext = nil")
	}

	payload, _, err := BuildPayload(req, BuildOptions{ModelID: "claude-sonnet-4.6", WebSearchCtx: wsCtx})
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	mctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if mctx == nil {
		t.Fatal("payload carries no message context")
	}
	var specs []*kiroproto.ToolSpecification
	for _, e := range mctx.Tools {
		if e.ToolSpecification != nil {
			specs = append(specs, e.ToolSpecification)
		}
	}
	if len(specs) != 1 {
		t.Fatalf("tool specifications = %d, want exactly the synthetic web_search", len(specs))
	}
	spec := specs[0]
	if spec.Name != websearch.KiroToolName {
		t.Errorf("tool name = %q, want %q", spec.Name, websearch.KiroToolName)
	}
	// The client definition has no input_schema at all; ours must, or the model
	// calls the tool with an empty input and the query is lost.
	props, _ := spec.InputSchema.JSON["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Errorf("input schema = %+v, want a query property", spec.InputSchema.JSON)
	}
	if strings.HasPrefix(spec.Description, "Tool: ") {
		t.Errorf("description = %q, want the real tool description", spec.Description)
	}
}

// Without a context (no provider configured) the definition is filtered out and
// nothing takes its place: the request is refused upstream of here.
func TestBuildPayloadDropsWebSearchWithoutContext(t *testing.T) {
	req := &anthropic.Request{
		Model:    "claude-sonnet-4-6",
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Text: "hi"}}},
		Tools: []anthropic.Tool{
			{Type: anthropic.ToolTypeWebSearch, Name: "web_search"},
			{Name: "Read", Description: "Read a file", InputSchema: map[string]any{"type": "object"}},
		},
	}
	payload, _, err := BuildPayload(req, BuildOptions{ModelID: "claude-sonnet-4.6"})
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	mctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	for _, e := range mctx.Tools {
		if e.ToolSpecification != nil && e.ToolSpecification.Name == "web_search" {
			t.Fatal("web_search reached Kiro as a callable tool")
		}
	}
}

// A replayed search round has to render as text the executor can read, and the
// URL is the part it must cite.
func TestServerToolResultTextRendersWebSearchResults(t *testing.T) {
	block := anthropic.ContentBlock{
		Type: anthropic.BlockTypeWebSearchToolResult,
		Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeWebSearchResult, Title: "Selters weather", URL: "https://example.com/w"},
			{Type: anthropic.BlockTypeWebSearchResult, URL: "https://example.com/bare"},
		}},
	}
	got := ServerToolResultText(block)
	if !strings.Contains(got, "Selters weather — https://example.com/w") {
		t.Errorf("text = %q, want the titled hit", got)
	}
	if !strings.Contains(got, "https://example.com/bare") {
		t.Errorf("text = %q, want the untitled hit's URL", got)
	}
}

func TestServerToolResultTextRendersWebSearchError(t *testing.T) {
	block := anthropic.ContentBlock{
		Type: anthropic.BlockTypeWebSearchToolResult,
		Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeWebSearchResultError, ErrorCode: "too_many_requests"},
		}},
	}
	if got := ServerToolResultText(block); !strings.Contains(got, "too_many_requests") {
		t.Errorf("text = %q, want the error code", got)
	}
}

// A search round in history must expand into the assistant/user alternation
// Kiro requires, or the result is dropped and the executor searches again.
func TestExpandServerToolResultsHandlesWebSearch(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "weather?"}},
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeServerToolUse, ID: "srvtoolu_1", Name: "web_search"},
			{Type: anthropic.BlockTypeWebSearchToolResult, ToolUseID: "srvtoolu_1",
				Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeWebSearchResult, Title: "w", URL: "https://example.com/w"},
				}}},
			{Type: anthropic.BlockTypeText, Text: "12 °C"},
		}}},
	}
	out := ExpandServerToolResults(msgs)
	if len(out) != 4 {
		t.Fatalf("messages = %d, want 4 (user, assistant call, user result, assistant text)", len(out))
	}
	if out[2].Role != "user" {
		t.Fatalf("message 2 role = %q, want user", out[2].Role)
	}
	result := out[2].Content.Blocks[0]
	if result.Type != anthropic.BlockTypeToolResult || result.ToolUseID != "srvtoolu_1" {
		t.Errorf("result block = %+v, want a tool_result for srvtoolu_1", result)
	}
	if !strings.Contains(result.Content.Text, "https://example.com/w") {
		t.Errorf("result text = %q, want the URL", result.Content.Text)
	}
	if result.IsError {
		t.Error("result marked as error, want success")
	}
}
