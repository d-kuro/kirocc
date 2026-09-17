package server

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestE2E_AutoMappingPriority(t *testing.T) {
	for _, tt := range []struct {
		name         string
		mappings     string
		model        string
		wantUpstream string
		wantResponse string
		wantEffort   string
	}{
		{
			name: "bare auto override", model: "auto",
			mappings:     `[{"anthropic":"auto","kiro":"claude-sonnet-4.6"}]`,
			wantUpstream: "claude-sonnet-4.6", wantResponse: "auto", wantEffort: "high",
		},
		{
			name: "auto alias override", model: "claude-auto",
			mappings:     `[{"anthropic":"claude-auto","kiro":"claude-sonnet-4.6"}]`,
			wantUpstream: "claude-sonnet-4.6", wantResponse: "claude-auto", wantEffort: "high",
		},
		{name: "native auto drops effort", model: "auto", wantUpstream: "auto", wantResponse: "auto"},
		{
			name: "custom alias to auto", model: "my-auto[1M]",
			mappings:     `[{"anthropic":"my-auto","kiro":"auto"}]`,
			wantUpstream: "auto", wantResponse: "my-auto",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KIROCC_MODEL_MAPPINGS", tt.mappings)
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
					client := &capturingClient{events: []any{"assistantResponseEvent", mustJSON(map[string]string{"content": "ok"})}}
					srv := newE2EServer(t, client)
					defer srv.Close()
					body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%t,"output_config":{"effort":"high"}}`, tt.model, stream)
					resp := postMessages(t, srv.URL, body)
					defer func() {
						if err := resp.Body.Close(); err != nil {
							t.Error(err)
						}
					}()
					requireStatus(t, resp, 200)
					data, err := io.ReadAll(resp.Body)
					if err != nil {
						t.Fatal(err)
					}
					if stream {
						if !strings.Contains(string(data), `"model":"`+tt.wantResponse+`"`) {
							t.Errorf("response missing model %q: %s", tt.wantResponse, data)
						}
					} else {
						var result struct {
							Model string `json:"model"`
						}
						if err := json.Unmarshal(data, &result); err != nil {
							t.Fatal(err)
						}
						if result.Model != tt.wantResponse {
							t.Errorf("model = %q, want %q", result.Model, tt.wantResponse)
						}
					}
					requireCaptured(t, client)
					if got := client.captured.ConversationState.CurrentMessage.UserInputMessage.ModelID; got != tt.wantUpstream {
						t.Errorf("upstream = %q, want %q", got, tt.wantUpstream)
					}
					fields := client.captured.AdditionalModelRequestFields
					if tt.wantEffort == "" {
						if fields != nil {
							t.Errorf("auto must omit effort fields, got %+v", fields)
						}
					} else if fields == nil || fields.OutputConfig == nil || fields.OutputConfig.Effort != tt.wantEffort {
						t.Errorf("expected output_config.effort=%q, got %+v", tt.wantEffort, fields)
					}
				})
			}
		})
	}
}
