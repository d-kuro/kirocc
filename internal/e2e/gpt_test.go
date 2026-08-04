//go:build e2e

package e2e

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// gptModel is the cheapest GPT 5.6 family model (rate 0.1) for E2E round trips.
const gptModel = "gpt-5.6-luna"

const gptToolsJSON = `[
	{"name": "read", "description": "Read a file from disk", "input_schema": {"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
]`

func TestE2E_GPT_TextOnly_Streaming(t *testing.T) {
	url := newRealServer(t)
	body := `{
		"model": "` + gptModel + `",
		"max_tokens": 1024,
		"stream": true,
		"messages": [{"role": "user", "content": "Say hello in one word"}]
	}`
	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	events := readSSEEvents(t, resp.Body)
	requireSSEContains(t, events, "text")
	requireSSEEventField(t, events, "message_delta", "stop_reason", "end_turn")
}

// TestE2E_GPT_ToolUseContinuation exercises the full GPT 5.6 tool round trip:
// round 1 returns tool_use (call_* ID) plus a trailing redacted_thinking blob;
// round 2 replays both with a tool_result and must complete the turn.
func TestE2E_GPT_ToolUseContinuation(t *testing.T) {
	url := newRealServer(t)

	round1 := `{
		"model": "` + gptModel + `",
		"max_tokens": 2048,
		"messages": [{"role": "user", "content": "Read the file at /tmp/test.txt using the read tool."}],
		"tools": ` + gptToolsJSON + `
	}`
	resp := postMessages(t, url, round1)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode round-1 response: %v", err)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("round-1 content is empty")
	}

	var toolUseID, redactedData string
	for _, block := range content {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		switch bm["type"] {
		case "tool_use":
			toolUseID, _ = bm["id"].(string)
		case "redacted_thinking":
			redactedData, _ = bm["data"].(string)
		}
	}
	if toolUseID == "" {
		t.Fatalf("round-1 has no tool_use block: %v", content)
	}
	if !strings.HasPrefix(toolUseID, "call_") {
		t.Errorf("tool_use ID = %q, want call_ prefix (pass-through)", toolUseID)
	}
	// The backend does not always emit a reasoning blob for trivial prompts;
	// when present, the continuation below verifies the replay path.
	if redactedData == "" {
		t.Log("round-1 returned no redacted_thinking blob; continuing without replay")
	}
	if sr, _ := result["stop_reason"].(string); sr != "tool_use" {
		t.Errorf("round-1 stop_reason = %q, want tool_use", sr)
	}

	// Round 2: replay assistant turn (redacted_thinking + tool_use) with a tool_result.
	assistantBlocks := []map[string]any{}
	if redactedData != "" {
		assistantBlocks = append(assistantBlocks, map[string]any{"type": "redacted_thinking", "data": redactedData})
	}
	assistantBlocks = append(assistantBlocks, map[string]any{"type": "tool_use", "id": toolUseID, "name": "read", "input": map[string]any{"path": "/tmp/test.txt"}})
	assistantContent := mustJSONString(t, assistantBlocks)
	userContent := mustJSONString(t, []map[string]any{
		{"type": "tool_result", "tool_use_id": toolUseID, "content": "hello from test file"},
	})
	round2 := `{
		"model": "` + gptModel + `",
		"max_tokens": 2048,
		"messages": [
			{"role": "user", "content": "Read the file at /tmp/test.txt using the read tool."},
			{"role": "assistant", "content": ` + assistantContent + `},
			{"role": "user", "content": ` + userContent + `}
		],
		"tools": ` + gptToolsJSON + `
	}`
	resp2 := postMessages(t, url, round2)
	defer resp2.Body.Close()
	requireStatus(t, resp2, 200)

	var result2 map[string]any
	if err := json.UnmarshalRead(resp2.Body, &result2); err != nil {
		t.Fatalf("decode round-2 response: %v", err)
	}
	content2, _ := result2["content"].([]any)
	var hasText bool
	for _, block := range content2 {
		bm, ok := block.(map[string]any)
		if ok && bm["type"] == "text" {
			hasText = true
		}
	}
	if !hasText {
		t.Errorf("round-2 has no text block: %v", content2)
	}
}

func TestE2E_GPT_EffortNone_ThinkingDisabled(t *testing.T) {
	url := newRealServer(t)
	body := `{
		"model": "` + gptModel + `",
		"max_tokens": 512,
		"thinking": {"type": "disabled"},
		"messages": [{"role": "user", "content": "Say hello in one word"}]
	}`
	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if content, _ := result["content"].([]any); len(content) == 0 {
		t.Fatal("empty content")
	}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
