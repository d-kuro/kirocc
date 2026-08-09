package models

import "testing"

func TestResolveKnown(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantKiro      string
		wantAnthropic string
		wantOK        bool
	}{
		{
			name:          "anthropic form",
			model:         "claude-opus-5",
			wantKiro:      "claude-opus-5",
			wantAnthropic: "claude-opus-5",
			wantOK:        true,
		},
		{
			name:          "dotted kiro sku",
			model:         "claude-opus-4.8",
			wantKiro:      "claude-opus-4.8",
			wantAnthropic: "claude-opus-4-8",
			wantOK:        true,
		},
		{
			// The 1M marker is a context-window advertisement, not part of the
			// model identity reported in usage.
			name:          "1m suffix resolves to the base sku without the marker",
			model:         "claude-opus-5[1m]",
			wantKiro:      "claude-opus-5",
			wantAnthropic: "claude-opus-5",
			wantOK:        true,
		},
		{
			// A model with a dedicated 1M SKU must route to it — silently
			// downgrading an explicit [1m] request to the 200k SKU would make
			// long advisor prompts fail.
			name:          "1m suffix routes to the dedicated 1m sku",
			model:         "claude-sonnet-4-6[1m]",
			wantKiro:      "claude-sonnet-4.6-1m",
			wantAnthropic: "claude-sonnet-4-6",
			wantOK:        true,
		},
		{
			name:          "uppercase 1m suffix normalizes",
			model:         "claude-opus-5[1M]",
			wantKiro:      "claude-opus-5",
			wantAnthropic: "claude-opus-5",
			wantOK:        true,
		},
		{
			// Reasoning models have no 1M variant and no thinking opt-in, so a
			// [1m]-suffixed GPT ID must not resolve — matching Resolve, which
			// refuses to route it.
			name:   "reasoning model with 1m suffix is rejected",
			model:  "gpt-5.6-sol[1m]",
			wantOK: false,
		},
		{
			name:          "reasoning model without suffix resolves",
			model:         "gpt-5.6-sol",
			wantKiro:      "gpt-5.6-sol",
			wantAnthropic: "gpt-5.6-sol",
			wantOK:        true,
		},
		{
			// The key difference from Resolve: no claude- passthrough.
			name:   "unknown claude model is rejected",
			model:  "claude-does-not-exist",
			wantOK: false,
		},
		{
			// And no silent fallback to DefaultModel.
			name:   "non-claude model is rejected",
			model:  "gpt-4o",
			wantOK: false,
		},
		{
			name:   "empty model is rejected",
			model:  "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kiro, anthropic, ok := ResolveKnown(tt.model)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (kiro=%q anthropic=%q)", ok, tt.wantOK, kiro, anthropic)
			}
			if !tt.wantOK {
				if kiro != "" || anthropic != "" {
					t.Errorf("rejected model returned kiro=%q anthropic=%q, want empty", kiro, anthropic)
				}
				return
			}
			if kiro != tt.wantKiro {
				t.Errorf("kiro = %q, want %q", kiro, tt.wantKiro)
			}
			if anthropic != tt.wantAnthropic {
				t.Errorf("anthropic = %q, want %q", anthropic, tt.wantAnthropic)
			}
		})
	}
}

func TestResolveKnownNeverFallsBackToDefault(t *testing.T) {
	if kiro, _, ok := ResolveKnown("totally-unknown"); ok || kiro == DefaultModel {
		t.Fatalf("ResolveKnown fell back to default: kiro=%q ok=%v", kiro, ok)
	}
}
