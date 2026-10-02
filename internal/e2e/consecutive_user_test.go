//go:build e2e

package e2e

import (
	"encoding/json/v2"
	"testing"
)

// TestE2E_ToolResultThenUserText sends the Claude Code shape that the
// normalizer folds into one user turn: a tool_result turn followed directly by
// a user text turn. Kiro must accept the merged turn both as the current
// message and later as a history entry (tool results plus text content).
func TestE2E_ToolResultThenUserText(t *testing.T) {
	url := newRealServer(t)
	toolTurns := `
		{"role": "user", "content": "Read the file at /tmp/test.txt using the read tool."},
		{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_e2e_merge_1", "name": "read", "input": {"path": "/tmp/test.txt"}}]},
		{"role": "user", "content": [
			{"type": "tool_result", "tool_use_id": "toolu_e2e_merge_1", "content": "hello from test file"},
			{"type": "text", "text": "The read tool succeeded."}
		]},
		{"role": "user", "content": "Reply with the file content verbatim."}`

	tests := []struct {
		name     string
		messages string
	}{
		{name: "current message", messages: toolTurns},
		{name: "history entry", messages: toolTurns + `,
			{"role": "assistant", "content": "hello from test file"},
			{"role": "user", "content": "Now say done in one word."}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{
				"model": "claude-sonnet-4-6",
				"max_tokens": 512,
				"messages": [` + tt.messages + `],
				"tools": [{"name": "read", "description": "Read a file from disk", "input_schema": {"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]
			}`
			resp := postMessages(t, url, body)
			defer resp.Body.Close()
			requireStatus(t, resp, 200)

			var result map[string]any
			if err := json.UnmarshalRead(resp.Body, &result); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if sr, _ := result["stop_reason"].(string); sr != "end_turn" {
				t.Errorf("stop_reason = %q, want end_turn", sr)
			}
			content, _ := result["content"].([]any)
			var text string
			for _, block := range content {
				if bm, ok := block.(map[string]any); ok && bm["type"] == "text" {
					s, _ := bm["text"].(string)
					text += s
				}
			}
			if text == "" {
				t.Errorf("empty text reply: %v", content)
			}
		})
	}
}
