package messages

import (
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/reqconv"
)

// advisorOrchestrator builds an orchestrator whose only job is payload
// construction, for asserting what the advisor subcall actually sends.
func advisorOrchestrator(msgs []anthropic.Message) *serverToolOrchestrator {
	return &serverToolOrchestrator{
		req: &anthropic.Request{
			Model: "claude-sonnet-4-6",
			Tools: []anthropic.Tool{
				{Type: anthropic.ToolTypeAdvisor, Name: "advisor", Model: "claude-opus-5"},
				{Name: "read", InputSchema: map[string]any{"type": "object"}},
			},
			Messages: msgs,
		},
		advCtx:    &advisor.Context{KiroModel: "claude-opus-5", MaxUses: 2},
		buildOpts: reqconv.BuildOptions{ProfileARN: "arn:test", ModelID: "claude-sonnet-4.6"},
	}
}

// toolResultTail is a conversation whose last message is a tool_result-only user
// turn — the overwhelmingly common shape mid-session, since the executor calls
// advisor right after acting on a tool.
func toolResultTail() []anthropic.Message {
	return []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "add pagination"}},
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeToolUse, ID: "toolu_1", Name: "read", Input: map[string]any{"path": "a.go"}},
		}}},
		{Role: "user", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeToolResult, ToolUseID: "toolu_1", Content: anthropic.MessageContent{Text: "package a"}},
		}}},
	}
}

// The advisor instruction must arrive as its own directive, not merged into the
// trailing tool output. When the two are concatenated, the advisor model reads
// the instruction as untrusted content embedded in a file it was shown and
// declines to act on it — which surfaces to the client as an empty response,
// i.e. advisor error "unavailable".
func TestBuildAdvisorPayload_PromptNotMergedIntoToolResult(t *testing.T) {
	o := advisorOrchestrator(toolResultTail())

	payload, err := o.buildAdvisorPayload(o.req.Messages)
	if err != nil {
		t.Fatal(err)
	}

	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, advisor.SubcallPrompt) {
		t.Fatalf("subcall prompt missing from current message: %q", content)
	}
	// The prompt must not be glued to the tool output: anything preceding it in
	// the same message is conversation content masquerading as instruction.
	before := content[:strings.Index(content, advisor.SubcallPrompt)]
	if strings.TrimSpace(before) != "" {
		t.Errorf("subcall prompt is preceded by conversation content in the same message:\nbefore = %q", before)
	}
}

// The same must hold when the tail is an ordinary text turn: appending the
// prompt to a user message merges it into that turn's text.
func TestBuildAdvisorPayload_PromptNotMergedIntoUserText(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "add pagination"}},
	}
	o := advisorOrchestrator(msgs)

	payload, err := o.buildAdvisorPayload(msgs)
	if err != nil {
		t.Fatal(err)
	}
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	before, _, ok := strings.Cut(content, advisor.SubcallPrompt)
	if !ok {
		t.Fatalf("subcall prompt missing: %q", content)
	}
	if before := strings.TrimSpace(before); before != "" {
		t.Errorf("subcall prompt merged into the user turn:\nbefore = %q", before)
	}
}
