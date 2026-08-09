package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func advisorResultBlock(id, text string) anthropic.ContentBlock {
	return anthropic.ContentBlock{
		Type:      anthropic.BlockTypeAdvisorToolResult,
		ToolUseID: id,
		Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeAdvisorResult, Text: text, StopReason: "end_turn"},
		}},
	}
}

func TestExpandServerToolResultsPassesThroughWithoutServerResults(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "hi"}},
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeText, Text: "hello"},
		}}},
	}
	got := ExpandServerToolResults(msgs)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

// The advice must survive into a user tool_result so the executor sees it on
// the next turn instead of the "[advisor_tool_result]" placeholder.
func TestExpandServerToolResultsRestoresAdvisorAdvice(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "refactor this"}},
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeText, Text: "Let me consult."},
			{Type: anthropic.BlockTypeServerToolUse, ID: "srvtoolu_1", Name: "advisor", Input: map[string]any{}},
			advisorResultBlock("srvtoolu_1", "Start with the contract layer."),
			{Type: anthropic.BlockTypeText, Text: "Understood."},
		}}},
		{Role: "user", Content: anthropic.MessageContent{Text: "go on"}},
	}

	got := ExpandServerToolResults(msgs)

	wantRoles := []string{"user", "assistant", "user", "assistant", "user"}
	if len(got) != len(wantRoles) {
		t.Fatalf("len = %d, want %d (%+v)", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("msg[%d].Role = %q, want %q", i, got[i].Role, role)
		}
	}

	// The assistant segment keeps the text and the server_tool_use, in order.
	first := got[1].Content.Blocks
	if len(first) != 2 || first[0].Type != anthropic.BlockTypeText || first[1].Type != anthropic.BlockTypeServerToolUse {
		t.Fatalf("first assistant segment = %+v", first)
	}

	// The synthetic user turn carries the advice as a tool_result.
	result := got[2].Content.Blocks
	if len(result) != 1 {
		t.Fatalf("result blocks = %+v", result)
	}
	if result[0].Type != anthropic.BlockTypeToolResult {
		t.Errorf("result type = %q, want tool_result", result[0].Type)
	}
	if result[0].ToolUseID != "srvtoolu_1" {
		t.Errorf("tool_use_id = %q, want srvtoolu_1", result[0].ToolUseID)
	}
	if got, want := result[0].Content.Text, "Start with the contract layer."; got != want {
		t.Errorf("advice = %q, want %q", got, want)
	}

	// Trailing text becomes its own assistant segment.
	if trailing := got[3].Content.Blocks; len(trailing) != 1 || trailing[0].Text != "Understood." {
		t.Fatalf("trailing assistant segment = %+v", trailing)
	}
}

// The same hole exists for tool search today; the fix must cover it too.
func TestExpandServerToolResultsRestoresToolSearchResults(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeServerToolUse, ID: "srvtoolu_2", Name: "tool_search_tool_regex"},
			{
				Type:      anthropic.BlockTypeToolSearchToolResult,
				ToolUseID: "srvtoolu_2",
				Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{{
					Type: anthropic.BlockTypeToolSearchSearchResult,
					ToolReferences: []anthropic.ContentBlock{
						{Type: anthropic.BlockTypeToolReference, ToolName: "Read"},
						{Type: anthropic.BlockTypeToolReference, ToolName: "Edit"},
					},
				}}},
			},
		}}},
	}

	got := ExpandServerToolResults(msgs)
	if len(got) != 2 || got[0].Role != "assistant" || got[1].Role != "user" {
		t.Fatalf("roles = %+v", got)
	}
	if want := "Found tools: Read, Edit"; got[1].Content.Blocks[0].Content.Text != want {
		t.Errorf("result text = %q, want %q", got[1].Content.Blocks[0].Content.Text, want)
	}
}

func TestServerToolResultText(t *testing.T) {
	tests := []struct {
		name  string
		block anthropic.ContentBlock
		want  string
	}{
		{
			name:  "advisor result",
			block: advisorResultBlock("x", "advice text"),
			want:  "advice text",
		},
		{
			name: "advisor redacted result cannot be decrypted",
			block: anthropic.ContentBlock{
				Type: anthropic.BlockTypeAdvisorToolResult, ToolUseID: "x",
				Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeAdvisorRedactedResult, EncryptedContent: "AAAA"},
				}},
			},
			want: "[advisor guidance was redacted and is unavailable]",
		},
		{
			name: "advisor error",
			block: anthropic.ContentBlock{
				Type: anthropic.BlockTypeAdvisorToolResult, ToolUseID: "x",
				Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeAdvisorResultError, ErrorCode: anthropic.AdvisorErrorOverloaded},
				}},
			},
			want: "advisor error: overloaded",
		},
		{
			// The "(empty result)" placeholder belongs to ExtractToolResults /
			// scanCurrentMessage, which apply it to every empty tool_result.
			name: "empty content renders empty",
			block: anthropic.ContentBlock{
				Type: anthropic.BlockTypeAdvisorToolResult, ToolUseID: "x",
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServerToolResultText(tt.block); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// An error result must replay as a failed tool_result: the in-flight round
// reported it as an error and Kiro must see the same status.
func TestExpandServerToolResultsPreservesErrorStatus(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeServerToolUse, ID: "e1", Name: "advisor"},
			{
				Type: anthropic.BlockTypeAdvisorToolResult, ToolUseID: "e1",
				Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
					{Type: anthropic.BlockTypeAdvisorResultError, ErrorCode: anthropic.AdvisorErrorOverloaded},
				}},
			},
		}}},
	}
	got := ExpandServerToolResults(msgs)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	result := got[1].Content.Blocks[0]
	if !result.IsError {
		t.Error("error result replayed with IsError = false")
	}
	if got[1].Content.Blocks[0].Content.Text != "advisor error: overloaded" {
		t.Errorf("text = %q", result.Content.Text)
	}
}

// Multiple advisor rounds in one assistant message must each get their own
// answering user turn, in order.
func TestExpandServerToolResultsHandlesMultipleRounds(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "assistant", Content: anthropic.MessageContent{Blocks: []anthropic.ContentBlock{
			{Type: anthropic.BlockTypeServerToolUse, ID: "a", Name: "advisor"},
			advisorResultBlock("a", "first"),
			{Type: anthropic.BlockTypeServerToolUse, ID: "b", Name: "advisor"},
			advisorResultBlock("b", "second"),
		}}},
	}
	got := ExpandServerToolResults(msgs)
	wantRoles := []string{"assistant", "user", "assistant", "user"}
	if len(got) != len(wantRoles) {
		t.Fatalf("len = %d, want %d (%+v)", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("msg[%d].Role = %q, want %q", i, got[i].Role, role)
		}
	}
	if got[1].Content.Blocks[0].Content.Text != "first" {
		t.Errorf("round 1 advice = %q", got[1].Content.Blocks[0].Content.Text)
	}
	if got[3].Content.Blocks[0].Content.Text != "second" {
		t.Errorf("round 2 advice = %q", got[3].Content.Blocks[0].Content.Text)
	}
}
