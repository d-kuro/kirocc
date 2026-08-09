package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/anthropic"
)

func advisorTestResolver(model string) (string, string, bool) {
	if model == "claude-opus-5" {
		return "claude-opus-5", "claude-opus-5", true
	}
	return "", "", false
}

func toolNames(t *testing.T, req *anthropic.Request, opts BuildOptions) []string {
	t.Helper()
	payload, _, err := BuildPayload(req, opts)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		return nil
	}
	var names []string
	for _, e := range ctx.Tools {
		switch {
		case e.ToolSpecification != nil:
			names = append(names, e.ToolSpecification.Name)
		case e.CachePoint != nil:
			names = append(names, "<cachePoint>")
		}
	}
	return names
}

// The advisor definition must never reach Kiro as a callable function tool:
// the executor would call it and the client has no local `advisor` tool.
func TestBuildPayloadExcludesAdvisorDefinitionFromCallableTools(t *testing.T) {
	req := &anthropic.Request{
		Model: "claude-sonnet-4-6",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Text: "hi"}},
		},
		Tools: []anthropic.Tool{
			{Name: "Read", Description: "read", InputSchema: map[string]any{"type": "object"}},
			{Type: anthropic.ToolTypeAdvisor, Name: anthropic.AdvisorToolName, Model: "claude-opus-5"},
		},
	}

	got := toolNames(t, req, BuildOptions{ModelID: "claude-sonnet-4.6"})
	for _, name := range got {
		if name == anthropic.AdvisorToolName {
			t.Fatalf("advisor leaked into Kiro tools: %v", got)
		}
	}
	if len(got) != 1 || got[0] != "Read" {
		t.Fatalf("tools = %v, want [Read]", got)
	}
}

// With an advisor context installed, the synthetic zero-parameter Kiro tool is
// injected instead — the executor calls that and kirocc intercepts it.
func TestBuildPayloadInjectsSyntheticAdvisorTool(t *testing.T) {
	tools := []anthropic.Tool{
		{Name: "Read", Description: "read", InputSchema: map[string]any{"type": "object"}},
		{Type: anthropic.ToolTypeAdvisor, Name: anthropic.AdvisorToolName, Model: "claude-opus-5"},
	}
	req := &anthropic.Request{
		Model:    "claude-sonnet-4-6",
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Text: "hi"}}},
		Tools:    tools,
	}
	advCtx := advisor.NewContext(tools, advisorTestResolver)
	if advCtx == nil {
		t.Fatal("advisor.NewContext returned nil")
	}

	got := toolNames(t, req, BuildOptions{ModelID: "claude-sonnet-4.6", AdvisorCtx: advCtx})
	want := []string{"Read", advisor.KiroToolName}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools = %v, want %v", got, want)
		}
	}
}

// Cache points are placed by walking tools and converted entries in lockstep,
// so filtering only one side would shift every later cache point.
func TestBuildPayloadKeepsCachePointsAlignedWhenAdvisorIsFiltered(t *testing.T) {
	req := &anthropic.Request{
		Model:    "claude-sonnet-4-6",
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Text: "hi"}}},
		Tools: []anthropic.Tool{
			{Type: anthropic.ToolTypeAdvisor, Name: anthropic.AdvisorToolName, Model: "claude-opus-5"},
			{Name: "Read", Description: "read", InputSchema: map[string]any{"type": "object"}},
			{
				Name: "Write", Description: "write", InputSchema: map[string]any{"type": "object"},
				CacheControl: &anthropic.CacheControl{Type: "ephemeral"},
			},
		},
	}

	got := toolNames(t, req, BuildOptions{ModelID: "claude-sonnet-4.6"})
	want := []string{"Read", "Write", "<cachePoint>"}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools = %v, want %v", got, want)
		}
	}
}

// An advisor-only request has no callable tools upstream, so normalization must
// treat it as a tool-less conversation and textualize historical tool blocks.
func TestBuildPayloadAdvisorOnlyRequestHasNoTools(t *testing.T) {
	req := &anthropic.Request{
		Model:    "claude-sonnet-4-6",
		Messages: []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Text: "hi"}}},
		Tools: []anthropic.Tool{
			{Type: anthropic.ToolTypeAdvisor, Name: anthropic.AdvisorToolName, Model: "claude-opus-5"},
		},
	}
	if got := toolNames(t, req, BuildOptions{ModelID: "claude-sonnet-4.6"}); got != nil {
		t.Fatalf("tools = %v, want none", got)
	}
}
