package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestExtractSystemPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt anthropic.SystemPrompt
		want   string
	}{
		{
			name:   "string",
			prompt: anthropic.SystemPrompt{Text: "You are helpful."},
			want:   "You are helpful.",
		},
		{
			name: "array",
			prompt: anthropic.SystemPrompt{
				Blocks: []anthropic.SystemBlock{
					{Type: "text", Text: "Part 1"},
					{Type: "text", Text: "Part 2"},
				},
			},
			want: "Part 1\nPart 2",
		},
		{
			name:   "empty",
			prompt: anthropic.SystemPrompt{},
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSystemPrompt(tt.prompt)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// Per-request attribution must not change the system prompt sent upstream.
func TestExtractSystemPrompt_DropsBillingHeaderBlock(t *testing.T) {
	mk := func(cch string) anthropic.SystemPrompt {
		return anthropic.SystemPrompt{Blocks: []anthropic.SystemBlock{
			{Type: "text", Text: "x-anthropic-billing-header: cc_version=2.1.280.096; cc_entrypoint=cli; cch=" + cch + ";"},
			{Type: "text", Text: "You are Claude Code."},
			{Type: "text", Text: "Big stable instructions."},
		}}
	}
	a, b := ExtractSystemPrompt(mk("d402a")), ExtractSystemPrompt(mk("9245a"))
	if a != b {
		t.Fatalf("prompts differ only by cch and must be identical:\n%q\n%q", a, b)
	}
	if want := "You are Claude Code.\nBig stable instructions."; a != want {
		t.Fatalf("got %q, want %q", a, want)
	}
}

func TestExtractSystemPrompt_KeepsTextThatOnlyMentionsTheHeader(t *testing.T) {
	// Only a block that IS the header line is dropped; real instructions that quote it stay.
	p := anthropic.SystemPrompt{Blocks: []anthropic.SystemBlock{
		{Type: "text", Text: "x-anthropic-billing-header: cc_version=1; cch=abc;\nThen follow these rules."},
		{Type: "text", Text: "Docs: the x-anthropic-billing-header: line is added by the CLI."},
	}}
	got := ExtractSystemPrompt(p)
	want := "x-anthropic-billing-header: cc_version=1; cch=abc;\nThen follow these rules.\nDocs: the x-anthropic-billing-header: line is added by the CLI."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIsBillingHeaderBlock(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"standalone", "x-anthropic-billing-header: cch=abc;", true},
		{"outer whitespace", " \tx-anthropic-billing-header: cch=abc;\r\n", true},
		{"newline instructions", "x-anthropic-billing-header: cch=abc;\nKeep this.", false},
		{"carriage return instructions", "x-anthropic-billing-header: cch=abc;\rKeep this.", false},
		{"mention", "Explain x-anthropic-billing-header: cch=abc;", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBillingHeaderBlock(tt.text); got != tt.want {
				t.Fatalf("isBillingHeaderBlock(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestExtractSystemPrompt_StringBillingHeaderPreserved(t *testing.T) {
	text := "x-anthropic-billing-header: cch=abc;\nKeep these instructions."
	if got := ExtractSystemPrompt(anthropic.SystemPrompt{Text: text}); got != text {
		t.Fatalf("string system prompt changed: %q", got)
	}
}
