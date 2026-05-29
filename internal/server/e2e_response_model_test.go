package server

import (
	"bufio"
	"encoding/json/v2"
	"io"
	"strings"
	"testing"
)

// TestE2E_ResponseModel_NonStreaming verifies the non-streaming response body
// carries the Anthropic-form model ID so Claude Code's context-window table
// (which keys off hyphenated IDs) picks up 1M-context models correctly.
func TestE2E_ResponseModel_NonStreaming(t *testing.T) {
	tests := []struct {
		name         string
		requestModel string
		wantResponse string
		wantUpstream string // Kiro SKU sent upstream
	}{
		{
			name:         "opus-4-8 hyphen preserved in response",
			requestModel: "claude-opus-4-8",
			wantResponse: "claude-opus-4-8[1m]",
			wantUpstream: "claude-opus-4.8",
		},
		{
			name:         "opus-4-8[1m] exact-match preserved verbatim",
			requestModel: "claude-opus-4-8[1m]",
			wantResponse: "claude-opus-4-8[1m]",
			wantUpstream: "claude-opus-4.8",
		},
		{
			name:         "opus-4-7 hyphen preserved in response",
			requestModel: "claude-opus-4-7",
			wantResponse: "claude-opus-4-7[1m]",
			wantUpstream: "claude-opus-4.7",
		},
		{
			name:         "opus-4-6 hyphen preserved in response",
			requestModel: "claude-opus-4-6",
			wantResponse: "claude-opus-4-6[1m]",
			wantUpstream: "claude-opus-4.6",
		},
		{
			name:         "sonnet-4-6 hyphen preserved in response",
			requestModel: "claude-sonnet-4-6",
			wantResponse: "claude-sonnet-4-6",
			wantUpstream: "claude-sonnet-4.6",
		},
		{
			name:         "kiro dotted input is rewritten to anthropic hyphen in response",
			requestModel: "claude-opus-4.7",
			wantResponse: "claude-opus-4-7[1m]",
			wantUpstream: "claude-opus-4.7",
		},
		{
			name:         "opus-4-7[1m] exact-match preserved verbatim",
			requestModel: "claude-opus-4-7[1m]",
			wantResponse: "claude-opus-4-7[1m]",
			wantUpstream: "claude-opus-4.7",
		},
		{
			name:         "opus-4-6[1m] exact-match preserved verbatim",
			requestModel: "claude-opus-4-6[1m]",
			wantResponse: "claude-opus-4-6[1m]",
			wantUpstream: "claude-opus-4.6",
		},
		{
			name:         "unknown claude model passthrough",
			requestModel: "claude-future-99",
			wantResponse: "claude-future-99",
			wantUpstream: "claude-future-99",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p1 := mustJSON(map[string]string{"content": "ok"})
			client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

			srv := newE2EServer(t, client)
			defer srv.Close()

			body := `{"model":"` + tt.requestModel + `","messages":[{"role":"user","content":"hi"}],"stream":false}`
			resp := postMessages(t, srv.URL, body)
			defer func() { _ = resp.Body.Close() }()

			requireStatus(t, resp, 200)

			var result map[string]any
			if err := json.UnmarshalRead(resp.Body, &result); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			gotModel, _ := result["model"].(string)
			if gotModel != tt.wantResponse {
				t.Errorf("response model = %q, want %q", gotModel, tt.wantResponse)
			}

			requireCaptured(t, client)
			if client.captured.ConversationState.CurrentMessage.UserInputMessage.ModelID != tt.wantUpstream {
				t.Errorf("upstream ModelID = %q, want %q",
					client.captured.ConversationState.CurrentMessage.UserInputMessage.ModelID,
					tt.wantUpstream)
			}
		})
	}
}

// TestE2E_ResponseModel_Streaming verifies message_start carries the
// Anthropic-form model ID.
func TestE2E_ResponseModel_Streaming(t *testing.T) {
	tests := []struct {
		name         string
		requestModel string
		wantResponse string
	}{
		{
			name:         "opus-4-8 hyphen preserved in message_start",
			requestModel: "claude-opus-4-8",
			wantResponse: "claude-opus-4-8[1m]",
		},
		{
			name:         "opus-4-8[1m] exact-match preserved verbatim in message_start",
			requestModel: "claude-opus-4-8[1m]",
			wantResponse: "claude-opus-4-8[1m]",
		},
		{
			name:         "opus-4-7 hyphen preserved in message_start",
			requestModel: "claude-opus-4-7",
			wantResponse: "claude-opus-4-7[1m]",
		},
		{
			name:         "opus-4-7[1m] exact-match preserved verbatim in message_start",
			requestModel: "claude-opus-4-7[1m]",
			wantResponse: "claude-opus-4-7[1m]",
		},
		{
			name:         "kiro dotted input is rewritten to hyphen in message_start",
			requestModel: "claude-opus-4.7",
			wantResponse: "claude-opus-4-7[1m]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p1 := mustJSON(map[string]string{"content": "Hello"})
			client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

			srv := newE2EServer(t, client)
			defer srv.Close()

			body := `{"model":"` + tt.requestModel + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
			resp := postMessages(t, srv.URL, body)
			defer func() { _ = resp.Body.Close() }()

			requireStatus(t, resp, 200)

			msg := extractMessageStartModel(t, resp.Body)
			if msg != tt.wantResponse {
				t.Errorf("message_start model = %q, want %q", msg, tt.wantResponse)
			}
		})
	}
}

// extractMessageStartModel returns message.model from the first message_start
// SSE event in body.
func extractMessageStartModel(t *testing.T, body io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			eventType = after
			continue
		}
		if eventType == "message_start" && strings.HasPrefix(line, "data: ") {
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatalf("parse message_start data: %v", err)
			}
			msg, _ := payload["message"].(map[string]any)
			model, _ := msg["model"].(string)
			return model
		}
	}
	t.Fatal("message_start event not found in SSE body")
	return ""
}

// TestE2E_ResponseModel_ToolSearch_1M verifies that the tool-search
// orchestrator threads the `[1m]` suffix through both its streaming
// message_start event and its final non-streaming JSON body when the
// resolved context window is 1M.
func TestE2E_ResponseModel_ToolSearch_1M(t *testing.T) {
	toolSearchRequest := func(model string, stream bool) string {
		streamStr := "false"
		if stream {
			streamStr = "true"
		}
		return `{"model":"` + model + `","max_tokens":16,"stream":` + streamStr + `,` +
			`"messages":[{"role":"user","content":"hi"}],` +
			`"tools":[` +
			`{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},` +
			`{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"defer_loading":true}` +
			`]}`
	}

	t.Run("streaming message_start carries [1m] suffix", func(t *testing.T) {
		p1 := mustJSON(map[string]string{"content": "ok"})
		client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

		srv := newE2EServer(t, client)
		defer srv.Close()

		resp := postMessages(t, srv.URL, toolSearchRequest("claude-opus-4-7", true))
		defer func() { _ = resp.Body.Close() }()
		requireStatus(t, resp, 200)

		msg := extractMessageStartModel(t, resp.Body)
		if msg != "claude-opus-4-7[1m]" {
			t.Errorf("tool-search streaming message_start model = %q, want %q", msg, "claude-opus-4-7[1m]")
		}
	})

	t.Run("non-streaming final JSON carries [1m] suffix", func(t *testing.T) {
		p1 := mustJSON(map[string]string{"content": "ok"})
		client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

		srv := newE2EServer(t, client)
		defer srv.Close()

		resp := postMessages(t, srv.URL, toolSearchRequest("claude-opus-4-7", false))
		defer func() { _ = resp.Body.Close() }()
		requireStatus(t, resp, 200)

		var result map[string]any
		if err := json.UnmarshalRead(resp.Body, &result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		gotModel, _ := result["model"].(string)
		if gotModel != "claude-opus-4-7[1m]" {
			t.Errorf("tool-search non-streaming response model = %q, want %q", gotModel, "claude-opus-4-7[1m]")
		}
	})

	t.Run("exact-match [1m] input preserved in streaming message_start", func(t *testing.T) {
		p1 := mustJSON(map[string]string{"content": "ok"})
		client := &capturingClient{events: []any{"assistantResponseEvent", p1}}

		srv := newE2EServer(t, client)
		defer srv.Close()

		resp := postMessages(t, srv.URL, toolSearchRequest("claude-opus-4-7[1m]", true))
		defer func() { _ = resp.Body.Close() }()
		requireStatus(t, resp, 200)

		msg := extractMessageStartModel(t, resp.Body)
		if msg != "claude-opus-4-7[1m]" {
			t.Errorf("tool-search streaming message_start model = %q, want %q", msg, "claude-opus-4-7[1m]")
		}
	})
}
