package server

import (
	"bufio"
	"encoding/json/v2"
	"io"
	"strings"
	"testing"
)

// TestE2E_ResponseModel_NonStreaming verifies that the /v1/messages non-streaming
// response body returns the Anthropic-form model ID (e.g. claude-opus-4-7), not
// the Kiro SKU (claude-opus-4.7). Claude Code matches this ID against its
// hard-coded table to detect the 1M context window; the dotted Kiro SKU would
// fall back to the 200k default and trigger premature auto-compact.
func TestE2E_ResponseModel_NonStreaming(t *testing.T) {
	tests := []struct {
		name         string
		requestModel string
		wantResponse string
		wantUpstream string // Kiro SKU sent upstream
	}{
		{
			name:         "opus-4-7 hyphen preserved in response",
			requestModel: "claude-opus-4-7",
			wantResponse: "claude-opus-4-7",
			wantUpstream: "claude-opus-4.7",
		},
		{
			name:         "opus-4-6 hyphen preserved in response",
			requestModel: "claude-opus-4-6",
			wantResponse: "claude-opus-4-6",
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
			wantResponse: "claude-opus-4-7",
			wantUpstream: "claude-opus-4.7",
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

// TestE2E_ResponseModel_Streaming verifies that the SSE message_start event
// contains the Anthropic-form model ID.
func TestE2E_ResponseModel_Streaming(t *testing.T) {
	tests := []struct {
		name         string
		requestModel string
		wantResponse string
	}{
		{
			name:         "opus-4-7 hyphen preserved in message_start",
			requestModel: "claude-opus-4-7",
			wantResponse: "claude-opus-4-7",
		},
		{
			name:         "kiro dotted input is rewritten to hyphen in message_start",
			requestModel: "claude-opus-4.7",
			wantResponse: "claude-opus-4-7",
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

// extractMessageStartModel scans an SSE body for the message_start event and
// returns its message.model field.
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
