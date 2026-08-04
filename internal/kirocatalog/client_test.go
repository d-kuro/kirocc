package kirocatalog

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// catalogFixture mirrors the shape of a real ListAvailableModels response,
// including the nested JSON Schema that carries the effort enum.
const catalogFixture = `{
  "defaultModel": {"modelId": "auto"},
  "models": [
    {
      "modelId": "auto",
      "tokenLimits": {"maxInputTokens": 1000000, "maxOutputTokens": 64000}
    },
    {
      "modelId": "claude-opus-5",
      "tokenLimits": {"maxInputTokens": 1000000, "maxOutputTokens": 128000},
      "additionalModelRequestFieldsSchema": {
        "type": "object",
        "properties": {
          "thinking": {"type": "object", "properties": {"type": {"type": "string", "enum": ["adaptive", "disabled"]}}},
          "output_config": {
            "type": "object",
            "properties": {
              "effort": {"type": "string", "enum": ["low", "medium", "high", "xhigh", "max"], "default": "high"}
            }
          },
          "max_tokens": {"type": "integer", "minimum": 1024, "maximum": 128000}
        },
        "additionalProperties": false
      }
    },
    {
      "modelId": "claude-haiku-4.5",
      "tokenLimits": {"maxInputTokens": 200000, "maxOutputTokens": 64000}
    }
  ]
}`

func TestClient_List(t *testing.T) {
	var (
		gotPath   string
		gotTarget string
		gotAuth   string
		gotBody   map[string]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTarget = r.Header.Get("X-Amz-Target")
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer srv.Close()

	got, err := New(WithBaseURL(srv.URL)).List(context.Background(), Request{
		Token:      "tok",
		Region:     "us-east-1",
		ProfileARN: "arn:aws:codewhisperer:us-east-1:123:profile/ABC",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if gotPath != "/" {
		t.Errorf("path = %q, want /", gotPath)
	}
	if gotTarget != amzTarget {
		t.Errorf("X-Amz-Target = %q, want %q", gotTarget, amzTarget)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	if gotBody["origin"] != origin {
		t.Errorf("body origin = %q, want %q", gotBody["origin"], origin)
	}
	if gotBody["profileArn"] != "arn:aws:codewhisperer:us-east-1:123:profile/ABC" {
		t.Errorf("body profileArn = %q, want the request ARN", gotBody["profileArn"])
	}

	want := []Model{
		{ID: "auto", MaxInputTokens: 1_000_000, MaxOutputTokens: 64_000},
		{
			ID:              "claude-opus-5",
			MaxInputTokens:  1_000_000,
			MaxOutputTokens: 128_000,
			EffortEnum:      []string{"low", "medium", "high", "xhigh", "max"},
		},
		{ID: "claude-haiku-4.5", MaxInputTokens: 200_000, MaxOutputTokens: 64_000},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i].ID ||
			got[i].MaxInputTokens != want[i].MaxInputTokens ||
			got[i].MaxOutputTokens != want[i].MaxOutputTokens ||
			!slices.Equal(got[i].EffortEnum, want[i].EffortEnum) {
			t.Errorf("model %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestClient_List_MissingInputs(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{"no token", Request{Region: "us-east-1", ProfileARN: "arn:x"}},
		{"no region", Request{Token: "t", ProfileARN: "arn:x"}},
		{"no profile arn", Request{Token: "t", Region: "us-east-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("request must not be sent when inputs are incomplete")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer srv.Close()
			if _, err := New(WithBaseURL(srv.URL)).List(context.Background(), tt.req); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestClient_List_ErrorResponses(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "validation error surfaces the message",
			status:  http.StatusBadRequest,
			body:    `{"__type":"com.amazon.kiro.controlplane#ValidationException","message":"Invalid profileArn."}`,
			wantErr: "Invalid profileArn",
		},
		{
			name:    "malformed json",
			status:  http.StatusOK,
			body:    `{"models":`,
			wantErr: "decode response",
		},
		{
			name:    "empty catalog",
			status:  http.StatusOK,
			body:    `{"models":[]}`,
			wantErr: "no models",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := New(WithBaseURL(srv.URL)).List(context.Background(), Request{
				Token: "t", Region: "us-east-1", ProfileARN: "arn:x",
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestClient_EndpointURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		region  string
		want    string
	}{
		{"region-based", "", "us-east-1", "https://management.us-east-1.kiro.dev/"},
		{"region-based eu", "", "eu-central-1", "https://management.eu-central-1.kiro.dev/"},
		{"override", "http://localhost:8080", "us-east-1", "http://localhost:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.baseURL != "" {
				opts = append(opts, WithBaseURL(tt.baseURL))
			}
			if got := New(opts...).endpointURL(tt.region); got != tt.want {
				t.Errorf("endpointURL(%q) = %q, want %q", tt.region, got, tt.want)
			}
		})
	}
}
