package kiroclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func apiKeyTestPayload() *kiroproto.Payload {
	return &kiroproto.Payload{
		ConversationState: kiroproto.ConversationState{
			ChatTriggerType: "MANUAL",
			AgentTaskType:   "vibe",
			CurrentMessage: kiroproto.CurrentMessage{
				UserInputMessage: kiroproto.UserInputMessage{Content: "Hello"},
			},
		},
	}
}

// The API distinguishes an API-key bearer from an OAuth bearer by this header
// alone; without it the request is rejected for a missing profileArn, which an
// API key cannot supply.
func TestHTTPClient_APIKeyAuth_SendsTokenTypeHeader(t *testing.T) {
	tests := []struct {
		name     string
		opts     []HTTPClientOption
		wantType string
	}{
		{name: "api key auth", opts: []HTTPClientOption{WithAPIKeyAuth()}, wantType: "API_KEY"},
		{name: "database credential", opts: nil, wantType: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var authorization string
			srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("TokenType")
				authorization = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("stream-body"))
			}))
			defer srv.Close()

			opts := append([]HTTPClientOption{WithBaseURL(srv.URL)}, tt.opts...)
			c := NewHTTPClient(opts...)

			resp, err := c.GenerateAssistantResponse(context.Background(), "ksk_test", apiKeyTestPayload(), "us-east-1")
			if err != nil {
				t.Fatalf("GenerateAssistantResponse: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if got != tt.wantType {
				t.Errorf("TokenType = %q, want %q", got, tt.wantType)
			}
			// The key is the bearer either way: there is no token exchange.
			if authorization != "Bearer ksk_test" {
				t.Errorf("Authorization = %q, want %q", authorization, "Bearer ksk_test")
			}
		})
	}
}

// The user-agent is load-bearing: the API authorizes on it, and an API key does
// not exempt a request from that check.
func TestHTTPClient_APIKeyAuth_KeepsUserAgent(t *testing.T) {
	var ua string
	srv := newTCP4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("stream-body"))
	}))
	defer srv.Close()

	c := NewHTTPClient(WithBaseURL(srv.URL), WithAPIKeyAuth())
	resp, err := c.GenerateAssistantResponse(context.Background(), "ksk_test", apiKeyTestPayload(), "us-east-1")
	if err != nil {
		t.Fatalf("GenerateAssistantResponse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ua != userAgentValue {
		t.Errorf("User-Agent = %q, want %q", ua, userAgentValue)
	}
}
