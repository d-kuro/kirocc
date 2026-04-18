package messages

import (
	"strings"
	"testing"
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
