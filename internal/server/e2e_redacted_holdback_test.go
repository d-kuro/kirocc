package server

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestE2E_TrailingRedactedHoldback(t *testing.T) {
	for _, tt := range []struct {
		name          string
		text          string
		stopSequences []string
	}{
		{name: "ordinary text", text: "OK"},
		{name: "partial thinking tag", text: "OK <"},
		{name: "entire text held", text: "<"},
		{name: "stop sequence prefix", text: "OK ST", stopSequences: []string{"STOP"}},
		{name: "unicode with holdback", text: "確認済み <"},
	} {
		for _, stream := range []bool{true, false} {
			mode := "nonstreaming"
			if stream {
				mode = "streaming"
			}
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				client := &capturingClient{events: []any{
					"assistantResponseEvent", mustJSON(map[string]string{"content": tt.text}),
					"reasoningContentEvent", mustJSON(map[string]string{"redactedContent": "test-blob"}),
				}}
				srv := newE2EServer(t, client)
				defer srv.Close()
				request := mustJSON(map[string]any{
					"model":          "claude-sonnet-4-6",
					"messages":       []map[string]string{{"role": "user", "content": "hi"}},
					"stream":         stream,
					"stop_sequences": tt.stopSequences,
				})
				resp := postMessages(t, srv.URL, string(request))
				defer func() { _ = resp.Body.Close() }()
				requireStatus(t, resp, http.StatusOK)

				if stream {
					body, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Fatal(err)
					}
					assertRedactedHoldbackStream(t, string(body), []string{"text"}, tt.text, nil)
					return
				}
				result := decodeResponse(t, resp)
				content := result["content"].([]any)
				if len(content) != 2 || content[0].(map[string]any)["type"] != "redacted_thinking" || content[1].(map[string]any)["text"] != tt.text {
					t.Fatalf("content = %v, want reasoning followed by text %q", content, tt.text)
				}
			})
		}
	}
}

func TestE2E_RedactedBeforeHoldback(t *testing.T) {
	textEvent := func(text string) []any {
		return []any{"assistantResponseEvent", mustJSON(map[string]string{"content": text})}
	}
	blob := []any{"reasoningContentEvent", mustJSON(map[string]string{"redactedContent": "test-blob"})}
	trailingBlob := []any{"reasoningContentEvent", mustJSON(map[string]string{"redactedContent": "trailing-blob"})}
	for _, tt := range []struct {
		name          string
		events        []any
		wantTypes     []string
		wantText      string
		wantBlobs     []string
		stopSequences []string
	}{
		{
			name:      "leading blob before held text",
			events:    slices.Concat(blob, textEvent("<")),
			wantTypes: []string{"redacted_thinking", "text"},
			wantText:  "<",
			wantBlobs: []string{"test-blob"},
		},
		{
			name:      "leading blob kept trailing blob dropped",
			events:    slices.Concat(blob, textEvent("<"), trailingBlob),
			wantTypes: []string{"redacted_thinking", "text"},
			wantText:  "<",
			wantBlobs: []string{"test-blob"},
		},
		{
			name:      "interior blob before held text",
			events:    slices.Concat(textEvent("HEAD"), blob, textEvent("<"), trailingBlob),
			wantTypes: []string{"text", "redacted_thinking", "text"},
			wantText:  "HEAD<",
			wantBlobs: []string{"test-blob"},
		},
		{
			name:      "empty text event does not make a trailing blob interior",
			events:    slices.Concat(textEvent("OK <"), blob, textEvent("")),
			wantTypes: []string{"text"},
			wantText:  "OK <",
		},
		{
			name:          "empty thinking tags do not make a trailing blob interior",
			events:        slices.Concat(textEvent("OK ST"), blob, textEvent("<thinking></thinking>")),
			wantTypes:     []string{"text"},
			wantText:      "OK ST",
			stopSequences: []string{"STOP"},
		},
		{
			name: "tool round retains blob with held text",
			events: slices.Concat([]any{"toolUseEvent", mustJSON(map[string]any{
				"name": "read", "toolUseId": "tool_1", "input": map[string]string{"path": "/tmp/test.txt"}, "stop": true,
			})}, textEvent("OK <"), blob),
			wantTypes: []string{"tool_use", "text", "redacted_thinking", "text"},
			wantText:  "OK <",
			wantBlobs: []string{"test-blob"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &capturingClient{events: tt.events}
			srv := newE2EServer(t, client)
			defer srv.Close()
			request := mustJSON(map[string]any{
				"model":          "claude-sonnet-4-6",
				"messages":       []map[string]string{{"role": "user", "content": "hi"}},
				"stream":         true,
				"stop_sequences": tt.stopSequences,
			})
			resp := postMessages(t, srv.URL, string(request))
			defer func() { _ = resp.Body.Close() }()
			requireStatus(t, resp, http.StatusOK)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			assertRedactedHoldbackStream(t, string(body), tt.wantTypes, tt.wantText, tt.wantBlobs)
		})
	}
}

// Validate the client-visible block lifecycle as well as the complete text.
func assertRedactedHoldbackStream(t *testing.T, body string, wantTypes []string, wantText string, wantBlobs []string) {
	t.Helper()
	var types []string
	var blobs []string
	var text strings.Builder
	active := -1
	stopped := false
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "content_block_start":
			if active != -1 || event.Index != len(types) {
				t.Fatalf("invalid block start: active=%d index=%d", active, event.Index)
			}
			active = event.Index
			types = append(types, event.ContentBlock.Type)
			if event.ContentBlock.Type == "redacted_thinking" {
				blobs = append(blobs, event.ContentBlock.Data)
			}
		case "content_block_delta":
			if active == -1 || active != event.Index {
				t.Fatalf("delta without matching block: active=%d index=%d", active, event.Index)
			}
			if event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "content_block_stop":
			if active == -1 || active != event.Index {
				t.Fatalf("stop without matching block: active=%d index=%d", active, event.Index)
			}
			active = -1
		case "message_stop":
			stopped = true
		}
	}
	if active != -1 || !stopped {
		t.Fatalf("unfinished stream: active=%d stopped=%v", active, stopped)
	}
	if !slices.Equal(types, wantTypes) {
		t.Errorf("blocks = %v, want %v", types, wantTypes)
	}
	if text.String() != wantText {
		t.Errorf("text = %q, want %q", text.String(), wantText)
	}
	if !slices.Equal(blobs, wantBlobs) {
		t.Errorf("reasoning blobs = %v, want %v", blobs, wantBlobs)
	}
}
