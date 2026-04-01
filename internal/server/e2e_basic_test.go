package server

import (
	"encoding/json/v2"
	"io"
	"strings"
	"testing"
)

func TestE2E_SimpleText_Streaming(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "Hello"})
	p2 := mustJSON(map[string]string{"content": "Hello world"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1, "assistantResponseEvent", p2}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	sseBody := string(body)
	for _, want := range []string{"event: message_start", "event: content_block_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(sseBody, want) {
			t.Errorf("missing %q in SSE body", want)
		}
	}
	if !strings.Contains(sseBody, `"text_delta"`) {
		t.Error("missing text_delta in SSE body")
	}
}

func TestE2E_SimpleText_NonStreaming(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "Hello!"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var result map[string]any
	_ = json.UnmarshalRead(resp.Body, &result)
	if result["type"] != "message" {
		t.Fatalf("type = %v", result["type"])
	}
	content := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("empty content")
	}
	block := content[0].(map[string]any)
	if block["text"] != "Hello!" {
		t.Fatalf("text = %v", block["text"])
	}
}

func TestE2E_AgentTaskType(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	if client.captured == nil {
		t.Fatal("payload not captured")
	}
	if client.captured.ConversationState.AgentTaskType != "vibe" {
		t.Fatalf("agentTaskType = %q", client.captured.ConversationState.AgentTaskType)
	}
	if client.captured.ConversationState.ChatTriggerType != "MANUAL" {
		t.Fatalf("chatTriggerType = %q", client.captured.ConversationState.ChatTriggerType)
	}
}

func TestE2E_ProfileArn_Conditional(t *testing.T) {
	p1 := mustJSON(map[string]string{"content": "ok"})
	client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	defer func() { _ = resp.Body.Close() }()

	if client.captured == nil {
		t.Fatal("payload not captured")
	}
	if client.captured.ProfileARN != "arn:test" {
		t.Fatalf("profileArn = %q", client.captured.ProfileARN)
	}
}
