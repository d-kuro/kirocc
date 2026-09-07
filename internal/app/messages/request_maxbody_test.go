package messages

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// body builds a minimal valid Anthropic request whose JSON is at least
// sizeBytes long, padded inside the user message.
func body(sizeBytes int) string {
	const tmpl = `{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":%q}]}`
	pad := sizeBytes - len(fmt.Sprintf(tmpl, ""))
	if pad < 1 {
		pad = 1
	}
	return fmt.Sprintf(tmpl, strings.Repeat("x", pad))
}

func TestParseAndValidateRequest_MaxBody(t *testing.T) {
	tests := []struct {
		name     string
		maxBody  int64
		bodySize int
		wantErr  bool
	}{
		{name: "under the cap", maxBody: 4 << 20, bodySize: 1 << 10},
		{name: "over the cap", maxBody: 1 << 10, bodySize: 8 << 10, wantErr: true},
		// A request that the old hardcoded 4 MiB cap rejected must pass once
		// the cap is raised — the regression this option exists to fix.
		{name: "over the old 4MiB cap, allowed by a higher one", maxBody: 32 << 20, bodySize: 6 << 20},
		{name: "zero disables the cap", maxBody: 0, bodySize: 6 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body(tt.bodySize)))
			w := httptest.NewRecorder()

			req, err := parseAndValidateRequest(r.Context(), w, r, tt.maxBody)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				// The caller distinguishes "too large" from "malformed" by this
				// type to return 413 instead of 400.
				if !errors.As(err, new(*http.MaxBytesError)) {
					t.Errorf("error is not *http.MaxBytesError: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(req.Messages) != 1 {
				t.Errorf("Messages = %d, want 1", len(req.Messages))
			}
		})
	}
}

// A Service built without WithMaxRequestBody must still cap the body, so an
// omitted option cannot silently disable the limit.
func TestNew_MaxRequestBodyDefault(t *testing.T) {
	s := New(nil, nil)
	if s.maxRequestBody != 32<<20 {
		t.Errorf("maxRequestBody = %d, want %d", s.maxRequestBody, 32<<20)
	}
}

func TestWithMaxRequestBody(t *testing.T) {
	s := New(nil, nil, WithMaxRequestBody(0))
	if s.maxRequestBody != 0 {
		t.Errorf("maxRequestBody = %d, want 0 (unlimited)", s.maxRequestBody)
	}
}
