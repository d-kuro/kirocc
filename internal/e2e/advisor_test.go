//go:build e2e

package e2e

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// advisorModel is the model advisor consultations escalate to in these tests.
const advisorModel = "claude-opus-5"

// executorModel runs the outer turn that calls the advisor tool.
const executorModel = "claude-sonnet-4-6"

// advisorSystemPrompt mirrors the "# Advisor Tool" section Claude Code injects,
// which is what actually drives the executor to call advisor() unprompted.
const advisorSystemPrompt = `You have access to an ` + "`advisor`" + ` tool backed by a stronger reviewer model. It takes NO parameters -- when you call advisor(), your entire conversation history is automatically forwarded.

Call advisor BEFORE substantive work -- before writing, before committing to an interpretation, before building on an assumption. Do not produce your own answer until you have consulted the advisor at least once.`

// advisorRequest builds a request carrying the advisor tool definition plus one
// ordinary client tool, so the executor has a real choice between them.
func advisorRequest(t *testing.T, advisorModelID string, stream bool, extra map[string]any) string {
	t.Helper()
	advisorTool := map[string]any{
		"type":  "advisor_20260301",
		"name":  "advisor",
		"model": advisorModelID,
	}
	for k, v := range extra {
		advisorTool[k] = v
	}
	body := map[string]any{
		"model":      executorModel,
		"max_tokens": 2048,
		"stream":     stream,
		"system":     advisorSystemPrompt,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "I need to add pagination to a Go HTTP handler. Consult the advisor first, then summarize the guidance you received in one sentence.",
		}},
		"tools": []any{
			advisorTool,
			map[string]any{
				"name":         "read",
				"description":  "Read a file from disk",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
			},
		},
	}
	return mustJSONString(t, body)
}

// advisorBlocks splits a non-streaming response's content into the advisor
// server_tool_use block and the advisor_tool_result's inner union member.
func advisorBlocks(t *testing.T, content []any) (serverToolUse, inner map[string]any) {
	t.Helper()
	for _, b := range content {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch bm["type"] {
		case "server_tool_use":
			if bm["name"] == "advisor" {
				serverToolUse = bm
			}
		case "advisor_tool_result":
			inner, _ = bm["content"].(map[string]any)
		}
	}
	return serverToolUse, inner
}

// TestE2E_Advisor_ModelReachable isolates the advisor model itself: if this
// fails, an advisor consultation failure is the model's reachability, not the
// advisor emulation. Run first so the diagnosis is unambiguous.
func TestE2E_Advisor_ModelReachable(t *testing.T) {
	url := newRealServer(t)
	body := `{
		"model": "` + advisorModel + `",
		"max_tokens": 512,
		"messages": [{"role": "user", "content": "Say hello in one word"}]
	}`
	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	content, _ := result["content"].([]any)
	var hasText bool
	for _, b := range content {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "text" {
			hasText = true
		}
	}
	if !hasText {
		t.Fatalf("advisor model %q returned no text block: %+v", advisorModel, result)
	}
}

// The full advisor loop against the real Kiro backend: the executor calls the
// synthetic advisor tool, kirocc issues a real subcall to the advisor model,
// and the client sees server_tool_use + advisor_tool_result + usage.iterations.
func TestE2E_Advisor_NonStreaming(t *testing.T) {
	url := newRealServer(t)
	resp := postMessages(t, url, advisorRequest(t, advisorModel, false, nil))
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("empty content: %+v", result)
	}

	serverToolUse, inner := advisorBlocks(t, content)
	if serverToolUse == nil {
		t.Fatalf("executor never called the advisor tool; content = %+v", content)
	}
	if inner == nil {
		t.Fatalf("advisor_tool_result missing; content = %+v", content)
	}
	if inner["type"] == "advisor_tool_result_error" {
		t.Fatalf("advisor consultation failed: error_code = %v", inner["error_code"])
	}
	if inner["type"] != "advisor_result" {
		t.Fatalf("advisor result content = %+v, want advisor_result", inner)
	}
	advice, _ := inner["text"].(string)
	if strings.TrimSpace(advice) == "" {
		t.Fatalf("advisor_result carries no advice text: %+v", inner)
	}
	if sr, _ := inner["stop_reason"].(string); sr == "" {
		t.Errorf("advisor_result missing stop_reason: %+v", inner)
	}

	// The consultation must be metered separately in usage.iterations[], with
	// the advisor model reported (never the executor's).
	usage, _ := result["usage"].(map[string]any)
	iters, _ := usage["iterations"].([]any)
	if len(iters) == 0 {
		t.Fatalf("usage.iterations missing the advisor round: %+v", usage)
	}
	iter, _ := iters[0].(map[string]any)
	if iter["type"] != "advisor_message" {
		t.Errorf("iteration type = %v, want advisor_message", iter["type"])
	}
	if iter["model"] != advisorModel {
		t.Errorf("iteration model = %v, want %q", iter["model"], advisorModel)
	}
	in, _ := iter["input_tokens"].(float64)
	out, _ := iter["output_tokens"].(float64)
	if in <= 0 || out <= 0 {
		t.Errorf("iteration tokens = in %v / out %v, want both > 0", iter["input_tokens"], iter["output_tokens"])
	}

	// The proxy must never forge Anthropic-encrypted advisor payloads.
	raw := mustJSONString(t, result)
	if strings.Contains(raw, "encrypted_content") {
		t.Errorf("response contains forged encrypted_content: %s", raw)
	}
}

// Streaming: the advisor blocks arrive as SSE content blocks and the terminal
// message_delta carries the advisor iteration.
func TestE2E_Advisor_Streaming(t *testing.T) {
	url := newRealServer(t)
	resp := postMessages(t, url, advisorRequest(t, advisorModel, true, nil))
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	events := readSSEEvents(t, resp.Body)
	requireSSEContains(t, events, "server_tool_use")
	requireSSEContains(t, events, "advisor_tool_result")
	requireSSEContains(t, events, "advisor_result")

	var joined strings.Builder
	for _, e := range events {
		joined.WriteString(e.data)
	}
	sse := joined.String()
	if strings.Contains(sse, "advisor_tool_result_error") {
		t.Fatalf("advisor consultation failed mid-stream: %s", sse)
	}
	if !strings.Contains(sse, `"type":"advisor_message"`) {
		t.Errorf("SSE stream missing usage.iterations advisor_message: %s", sse)
	}
	if strings.Contains(sse, "encrypted_content") {
		t.Errorf("SSE stream contains forged encrypted_content")
	}
}

// An unresolvable advisor model must degrade to advisor_tool_result_error with
// model_not_found — never an HTTP error, and never a silent downgrade to a
// weaker model that the executor would mistake for real advice.
func TestE2E_Advisor_ModelNotFound(t *testing.T) {
	url := newRealServer(t)
	resp := postMessages(t, url, advisorRequest(t, "claude-nonexistent-99", false, nil))
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	content, _ := result["content"].([]any)
	serverToolUse, inner := advisorBlocks(t, content)
	if serverToolUse == nil {
		t.Fatalf("executor never called the advisor tool; content = %+v", content)
	}
	if inner["type"] != "advisor_tool_result_error" || inner["error_code"] != "model_not_found" {
		t.Fatalf("advisor result = %+v, want advisor_tool_result_error/model_not_found", inner)
	}
	usage, _ := result["usage"].(map[string]any)
	if _, has := usage["iterations"]; has {
		t.Errorf("failed consultation must not be metered in iterations: %+v", usage)
	}
}

// A mid-session conversation carrying a client tool round is the shape that
// actually reaches the advisor in production, and it is where two failures were
// found: the subcall instruction being merged into the trailing tool output, and
// the advisor deferring instead of answering when the conversation is itself
// about advisor tooling. Both surfaced as an empty upstream response, i.e.
// advisor error "unavailable".
func TestE2E_Advisor_AfterToolRound(t *testing.T) {
	url := newRealServer(t)

	toolID := "toolu_e2e_advisor_1"
	body := mustJSONString(t, map[string]any{
		"model":      executorModel,
		"max_tokens": 2048,
		"system":     advisorSystemPrompt,
		"messages": []map[string]any{
			// Self-referential subject matter on purpose: an advisor model that
			// reads the conversation as instructions concludes the request is
			// someone else's and returns nothing.
			{"role": "user", "content": "Implement the advisor server-side tool in this Go proxy. Read the contract layer first, then consult the advisor about the design."},
			{"role": "assistant", "content": []map[string]any{
				{"type": "text", "text": "Reading the contract layer."},
				{"type": "tool_use", "id": toolID, "name": "read", "input": map[string]any{"path": "internal/anthropic/types.go"}},
			}},
			{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": toolID, "content": "package anthropic\n\nconst ToolTypeAdvisor = \"advisor_20260301\"\n\ntype Tool struct {\n\tType string\n\tName string\n\tModel string\n}"},
			}},
			{"role": "user", "content": "You have now read the contract layer. Do not read any more files. Call advisor() now, then summarize its guidance in one sentence."},
		},
		"tools": []any{
			map[string]any{"type": "advisor_20260301", "name": "advisor", "model": advisorModel},
			map[string]any{
				"name":         "read",
				"description":  "Read a file from disk",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
			},
		},
	})

	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	content, _ := result["content"].([]any)
	serverToolUse, inner := advisorBlocks(t, content)
	if serverToolUse == nil {
		t.Fatalf("executor never called the advisor tool; content = %+v", content)
	}
	if inner["type"] != "advisor_result" {
		t.Fatalf("advisor consultation failed after a tool round: %+v", inner)
	}
	if advice, _ := inner["text"].(string); strings.TrimSpace(advice) == "" {
		t.Fatalf("advisor returned empty advice: %+v", inner)
	}
}

// max_uses is enforced against the real backend: the first consultation
// succeeds and any further one reports max_uses_exceeded rather than issuing
// another paid subcall.
func TestE2E_Advisor_MaxUses(t *testing.T) {
	url := newRealServer(t)
	body := advisorRequest(t, advisorModel, false, map[string]any{"max_uses": 1})
	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	content, _ := result["content"].([]any)

	var results, exceeded int
	for _, b := range content {
		bm, ok := b.(map[string]any)
		if !ok || bm["type"] != "advisor_tool_result" {
			continue
		}
		inner, _ := bm["content"].(map[string]any)
		switch {
		case inner["type"] == "advisor_result":
			results++
		case inner["error_code"] == "max_uses_exceeded":
			exceeded++
		default:
			t.Fatalf("unexpected advisor result: %+v", inner)
		}
	}
	if results != 1 {
		t.Fatalf("advisor_result count = %d, want exactly 1 under max_uses=1; content = %+v", results, content)
	}
	// The executor is free to stop after one consultation, so an absent
	// max_uses_exceeded is valid; a second successful result is not.
	t.Logf("advisor results = %d, max_uses_exceeded = %d", results, exceeded)
}
