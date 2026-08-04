package messages

import (
	"slices"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/reqconv"
)

func TestParseToolSearchInput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantQuery   string
		wantMax     int
		wantErr     bool
		errContains string
	}{
		{
			name:      "valid with max_results",
			input:     `{"query":"foo","max_results":5}`,
			wantQuery: "foo",
			wantMax:   5,
		},
		{
			name:      "valid without max_results",
			input:     `{"query":"bar"}`,
			wantQuery: "bar",
			wantMax:   0,
		},
		{
			name:        "invalid JSON",
			input:       `{broken`,
			wantErr:     true,
			errContains: "parse",
		},
		{
			name:        "empty string",
			input:       ``,
			wantErr:     true,
			errContains: "parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, maxResults, err := parseToolSearchInput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (query=%q, max=%d)", query, maxResults)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if query != tt.wantQuery {
				t.Errorf("query: got %q, want %q", query, tt.wantQuery)
			}
			if maxResults != tt.wantMax {
				t.Errorf("maxResults: got %d, want %d", maxResults, tt.wantMax)
			}
		})
	}
}

func TestAppendSearchMessages_RedactedThinkingReplay(t *testing.T) {
	o := &toolSearchOrchestrator{}
	nameMap := reqconv.NewToolNameMap()

	tests := []struct {
		name      string
		redacted  []string
		wantBlobs []string
	}{
		{
			name:      "no blobs",
			redacted:  nil,
			wantBlobs: nil,
		},
		{
			name:      "single blob replayed",
			redacted:  []string{"blob-1"},
			wantBlobs: []string{"blob-1"},
		},
		{
			name:      "multiple blobs kept as separate blocks",
			redacted:  []string{"blob-1", "blob-2"},
			wantBlobs: []string{"blob-1", "blob-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := o.appendSearchMessages(nil, "srvtoolu_x", map[string]any{"query": "q"}, []string{"tool_a"}, nil, nameMap, tt.redacted)
			if len(msgs) != 2 {
				t.Fatalf("len(msgs) = %d, want 2", len(msgs))
			}
			assistant := msgs[0]
			if assistant.Role != "assistant" {
				t.Fatalf("first message role = %q, want assistant", assistant.Role)
			}
			var gotBlobs []string
			var hasServerToolUse bool
			for _, b := range assistant.Content.Blocks {
				switch b.Type {
				case anthropic.BlockTypeRedactedThinking:
					gotBlobs = append(gotBlobs, b.Data)
				case anthropic.BlockTypeServerToolUse:
					hasServerToolUse = true
				}
			}
			if !hasServerToolUse {
				t.Fatal("missing server_tool_use block")
			}
			if !slices.Equal(gotBlobs, tt.wantBlobs) {
				t.Fatalf("redacted blobs = %v, want %v", gotBlobs, tt.wantBlobs)
			}
		})
	}
}
