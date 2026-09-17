package models

import "testing"

func TestResolve_AutoMappings(t *testing.T) {
	tests := []struct {
		name         string
		mappings     string
		model        string
		context1M    bool
		wantUpstream string
		wantResponse string
		wantWindow   int
	}{
		{name: "bare auto unchanged", model: "auto", wantUpstream: "auto", wantResponse: "auto"},
		{name: "auto alias unchanged", model: "claude-auto", wantUpstream: "auto", wantResponse: "claude-auto"},
		{name: "auto ignores context header", model: "auto", context1M: true, wantUpstream: "auto", wantResponse: "auto"},
		{name: "auto suffix does not enable thinking", model: "auto[1m]", wantUpstream: "auto", wantResponse: "auto"},
		{name: "auto alias uppercase suffix", model: "claude-auto[1M]", wantUpstream: "auto", wantResponse: "claude-auto"},
		{
			name: "override bare auto", model: "auto",
			mappings:     `[{"anthropic":"auto","kiro":"claude-sonnet-4.6","context_window_size":200000}]`,
			wantUpstream: "claude-sonnet-4.6", wantResponse: "auto", wantWindow: 200000,
		},
		{
			name: "override auto alias", model: "claude-auto",
			mappings:     `[{"anthropic":"claude-auto","kiro":"gpt-5.6-luna","context_window_size":272000}]`,
			wantUpstream: "gpt-5.6-luna", wantResponse: "claude-auto", wantWindow: 272000,
		},
		{
			name: "override keeps real context routing", model: "auto", context1M: true,
			mappings:     `[{"anthropic":"auto","kiro":"claude-sonnet-4.6","kiro_1m":"claude-sonnet-4.6-1m"}]`,
			wantUpstream: "claude-sonnet-4.6-1m", wantResponse: "auto[1m]", wantWindow: 1000000,
		},
		{
			name: "exact suffix override wins", model: "claude-auto[1M]",
			mappings:     `[{"anthropic":"claude-auto[1m]","kiro":"claude-fable-5.1","kiro_1m":"claude-fable-5.1"}]`,
			wantUpstream: "claude-fable-5.1", wantResponse: "claude-auto[1m]", wantWindow: 1000000,
		},
		{
			name: "custom alias inherits auto behavior", model: "my-auto[1M]",
			mappings:     `[{"anthropic":"my-auto","kiro":"auto"}]`,
			wantUpstream: "auto", wantResponse: "my-auto",
		},
		{
			name: "explicit suffix alias to auto", model: "my-auto[1M]",
			mappings:     `[{"anthropic":"my-auto[1m]","kiro":"auto"}]`,
			wantUpstream: "auto", wantResponse: "my-auto",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KIROCC_MODEL_MAPPINGS", tt.mappings)
			upstream, thinking, window, response := Resolve(tt.model, tt.context1M)
			if upstream != tt.wantUpstream || response != tt.wantResponse || window != tt.wantWindow || thinking {
				t.Errorf("Resolve(%q, %t) = (%q, %t, %d, %q), want (%q, false, %d, %q)",
					tt.model, tt.context1M, upstream, thinking, window, response, tt.wantUpstream, tt.wantWindow, tt.wantResponse)
			}
		})
	}
}
