package reqconv

import (
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestNormalize_FullPipeline(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		{Role: "user", Content: anthropic.MessageContent{Text: "Are you there?"}},
		{Role: "assistant", Content: anthropic.MessageContent{Text: "Yes"}},
	}
	got := Normalize(msgs, false)
	// Step 2 merges the two user messages
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Content.Text != "Hello\nAre you there?" {
		t.Fatalf("merged = %q", got[0].Content.Text)
	}
}

func TestStep3_EnsureStartsWithUser(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi"}},
	}
	got := step3EnsureStartsWithUser(msgs)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Role != "user" || got[0].Content.Text != "(empty)" {
		t.Fatalf("got[0] = %+v", got[0])
	}
}

func TestStep4_NormalizeRoles(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "developer", Content: anthropic.MessageContent{Text: "System"}},
		{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
	}
	got := step4NormalizeRoles(msgs)
	if got[0].Role != "user" {
		t.Fatalf("developer should become user, got %q", got[0].Role)
	}
}

func TestStep5_EnsureAlternating(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "A"}},
		{Role: "user", Content: anthropic.MessageContent{Text: "B"}},
	}
	got := step5EnsureAlternating(msgs)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[1].Role != "assistant" || got[1].Content.Text != "(empty)" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestStep1a_TextualizeAllToolContent(t *testing.T) {
	msgs := []anthropic.Message{
		{
			Role: "assistant",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "text", Text: "Let me check."},
					{Type: "tool_use", ID: "toolu_01", Name: "get_weather", Input: map[string]any{"city": "Tokyo"}},
				},
			},
		},
	}
	got := step1aTextualizeAllToolContent(msgs)
	if len(got[0].Content.Blocks) != 2 {
		t.Fatalf("got %d blocks", len(got[0].Content.Blocks))
	}
	if got[0].Content.Blocks[1].Type != "text" {
		t.Fatalf("tool_use should be textualized, got %q", got[0].Content.Blocks[1].Type)
	}
}

func TestStep1b_TextualizeOrphanToolResults(t *testing.T) {
	msgs := []anthropic.Message{
		{
			Role: "assistant",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_use", ID: "toolu_01", Name: "get_weather"},
				},
			},
		},
		{
			Role: "user",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_01", Content: anthropic.MessageContent{Text: "Sunny"}},
					{Type: "tool_result", ToolUseID: "toolu_orphan", Content: anthropic.MessageContent{Text: "Orphan"}},
				},
			},
		},
	}
	got := step1bTextualizeOrphanToolResults(msgs)
	userBlocks := got[1].Content.Blocks
	if len(userBlocks) != 2 {
		t.Fatalf("got %d blocks", len(userBlocks))
	}
	// toolu_01 should remain as tool_result
	if userBlocks[0].Type != "tool_result" {
		t.Fatalf("matched tool_result should stay, got %q", userBlocks[0].Type)
	}
	// toolu_orphan should be textualized
	if userBlocks[1].Type != "text" {
		t.Fatalf("orphan should be textualized, got %q", userBlocks[1].Type)
	}
}

func TestStep2_MergeAdjacentSameRole(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		{Role: "user", Content: anthropic.MessageContent{Text: "World"}},
		{Role: "assistant", Content: anthropic.MessageContent{Text: "Hi"}},
	}
	got := step2MergeAdjacentSameRole(msgs)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Content.Text != "Hello\nWorld" {
		t.Fatalf("merged = %q", got[0].Content.Text)
	}
}

func TestNormalize_DeveloperRoleFirst(t *testing.T) {
	// When the first message has role "developer", normalization should NOT
	// insert a synthetic user+assistant pair before it. It should simply
	// convert "developer" to "user" and keep the conversation clean.
	msgs := []anthropic.Message{
		{Role: "developer", Content: anthropic.MessageContent{Text: "System instructions"}},
		{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
	}
	got := Normalize(msgs, false)
	// developer → user, then merged with next user → single user message.
	// Then assistant follows normally.
	// Key assertion: no synthetic "(empty)" messages should appear.
	for _, m := range got {
		if m.Content.Text == syntheticEmpty {
			t.Fatalf("unexpected synthetic (empty) message in normalized output: %+v", got)
		}
	}
	// First message should be user role.
	if got[0].Role != "user" {
		t.Fatalf("got[0].Role = %q, want user", got[0].Role)
	}
}

func TestNormalize_DeveloperRoleFirst_NoExtraAssistant(t *testing.T) {
	// developer at start should not cause an extra assistant placeholder.
	msgs := []anthropic.Message{
		{Role: "developer", Content: anthropic.MessageContent{Text: "Be helpful"}},
		{Role: "assistant", Content: anthropic.MessageContent{Text: "OK"}},
		{Role: "user", Content: anthropic.MessageContent{Text: "Hi"}},
	}
	got := Normalize(msgs, false)
	// Should be: user("Be helpful"), assistant("OK"), user("Hi")
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3: %+v", len(got), got)
	}
	if got[0].Role != "user" {
		t.Fatalf("got[0].Role = %q", got[0].Role)
	}
	if got[1].Role != "assistant" {
		t.Fatalf("got[1].Role = %q", got[1].Role)
	}
	if got[2].Role != "user" {
		t.Fatalf("got[2].Role = %q", got[2].Role)
	}
}

func TestNormalize_DoesNotMutateInput(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "developer", Content: anthropic.MessageContent{Text: "hi"}},
	}
	original := msgs[0].Role
	Normalize(msgs, false)
	if msgs[0].Role != original {
		t.Fatalf("input mutated: role changed from %q to %q", original, msgs[0].Role)
	}
}

func TestStep4NormalizeRoles_DoesNotMutateInput(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "developer", Content: anthropic.MessageContent{Text: "hi"}},
	}
	original := msgs[0].Role
	step4NormalizeRoles(msgs)
	if msgs[0].Role != original {
		t.Fatalf("input mutated: role changed from %q to %q", original, msgs[0].Role)
	}
}

func TestExtractToolResultContentText_ToolSearchResult(t *testing.T) {
	// Use step1aTextualizeAllToolContent to exercise extractToolResultContentText indirectly.
	msgs := []anthropic.Message{
		{
			Role: "user",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{
						Type:      anthropic.BlockTypeToolSearchToolResult,
						ToolUseID: "toolu_ts",
						Content: anthropic.MessageContent{
							Blocks: []anthropic.ContentBlock{
								{
									Type: anthropic.BlockTypeToolSearchSearchResult,
									ToolReferences: []anthropic.ContentBlock{
										{Type: anthropic.BlockTypeToolReference, ToolName: "Read"},
										{Type: anthropic.BlockTypeToolReference, ToolName: "Edit"},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	got := step1aTextualizeAllToolContent(msgs)
	text := got[0].Content.Blocks[0].Text
	if !strings.Contains(text, "tool_reference: Read") {
		t.Fatalf("expected 'tool_reference: Read' in %q", text)
	}
	if !strings.Contains(text, "tool_reference: Edit") {
		t.Fatalf("expected 'tool_reference: Edit' in %q", text)
	}
}

func TestStep2_DoesNotMergeStructuredContent(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "Hello"}},
		{
			Role: "user",
			Content: anthropic.MessageContent{
				Blocks: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", Content: anthropic.MessageContent{Text: "result"}},
				},
			},
		},
	}
	got := step2MergeAdjacentSameRole(msgs)
	if len(got) != 2 {
		t.Fatalf("should not merge structured content, got %d", len(got))
	}
}
