package models

import (
	"slices"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name               string
		envMappings        string // KIROCC_MODEL_MAPPINGS value; empty = unset
		model              string
		context1M          bool
		wantKiroModel      string
		wantThinking       bool
		wantContextWindow  int
		wantAnthropicModel string
	}{
		{
			name:               "claude-opus-4-7 uses 1m context without thinking",
			model:              "claude-opus-4-7",
			wantKiroModel:      "claude-opus-4.7",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-7[1m]",
		},
		{
			name:               "claude-opus-4-7[1m] exact-match preserves suffix without thinking",
			model:              "claude-opus-4-7[1m]",
			wantKiroModel:      "claude-opus-4.7",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-7[1m]",
		},
		{
			name:               "claude-opus-4-7 with context1M",
			model:              "claude-opus-4-7",
			context1M:          true,
			wantKiroModel:      "claude-opus-4.7",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-7[1m]",
		},
		{
			name:               "kiro model name claude-opus-4.7 always resolves to 1m",
			model:              "claude-opus-4.7[1m]",
			wantKiroModel:      "claude-opus-4.7",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-7[1m]",
		},
		{
			name:               "claude-opus-4-6 uses 1m context without thinking",
			model:              "claude-opus-4-6",
			wantKiroModel:      "claude-opus-4.6",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-6[1m]",
		},
		{
			name:               "claude-opus-4-6[1m] exact-match preserves suffix without thinking",
			model:              "claude-opus-4-6[1m]",
			wantKiroModel:      "claude-opus-4.6",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-6[1m]",
		},
		{
			name:               "claude-opus-4-6 with context1M",
			model:              "claude-opus-4-6",
			context1M:          true,
			wantKiroModel:      "claude-opus-4.6",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-6[1m]",
		},
		{
			name:               "claude-sonnet-4-6",
			model:              "claude-sonnet-4-6",
			wantKiroModel:      "claude-sonnet-4.6",
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6",
		},
		{
			name:               "kiro model name claude-sonnet-4.6 without thinking suffix",
			model:              "claude-sonnet-4.6",
			wantKiroModel:      "claude-sonnet-4.6",
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6",
		},
		{
			name:               "claude-sonnet-4-6 with thinking suffix",
			model:              "claude-sonnet-4-6[1m]",
			wantKiroModel:      "claude-sonnet-4.6-1m",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6[1m]",
		},
		{
			name:               "claude-sonnet-4-6 with context1M resolves to 1m",
			model:              "claude-sonnet-4-6",
			context1M:          true,
			wantKiroModel:      "claude-sonnet-4.6-1m",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6[1m]",
		},
		{
			name:               "claude-sonnet-4 with thinking suffix passthrough no 1m variant",
			model:              "claude-sonnet-4[1m]",
			wantKiroModel:      "claude-sonnet-4",
			wantThinking:       true,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4",
		},
		{
			name:               "claude-haiku-4.5",
			model:              "claude-haiku-4.5",
			wantKiroModel:      "claude-haiku-4.5",
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-haiku-4.5",
		},
		{
			name:               "claude-haiku-4.5 with thinking suffix no 1m variant",
			model:              "claude-haiku-4.5[1m]",
			wantKiroModel:      "claude-haiku-4.5",
			wantThinking:       true,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-haiku-4.5",
		},
		{
			name:               "claude-haiku-4.5 with context1M no 1m variant",
			model:              "claude-haiku-4.5",
			context1M:          true,
			wantKiroModel:      "claude-haiku-4.5",
			wantThinking:       true,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-haiku-4.5",
		},
		{
			name:               "kiro model name claude-sonnet-4.6 with thinking suffix resolves to 1m",
			model:              "claude-sonnet-4.6[1m]",
			wantKiroModel:      "claude-sonnet-4.6-1m",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6[1m]",
		},
		{
			name:               "kiro model name claude-opus-4.6 with thinking suffix",
			model:              "claude-opus-4.6[1m]",
			wantKiroModel:      "claude-opus-4.6",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-6[1m]",
		},
		{
			name:               "unknown claude model passthrough",
			model:              "claude-future-99",
			wantKiroModel:      "claude-future-99",
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-future-99",
		},
		{
			name:               "unknown claude model with thinking suffix passthrough",
			model:              "claude-future-99[1m]",
			wantKiroModel:      "claude-future-99",
			wantThinking:       true,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-future-99",
		},
		{
			name:               "non-claude model returns default",
			model:              "gpt-4o",
			wantKiroModel:      DefaultModel,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6",
		},
		{
			name:               "env override custom model",
			envMappings:        `[{"anthropic":"my-custom-model","kiro":"claude-custom-1"}]`,
			model:              "my-custom-model",
			wantKiroModel:      "claude-custom-1",
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "my-custom-model",
		},
		{
			name:               "env override invalid JSON falls back",
			envMappings:        `not-valid-json`,
			model:              "claude-sonnet-4-6",
			wantKiroModel:      "claude-sonnet-4.6",
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6",
		},
		{
			name:               "env override with empty anthropic does not poison non-claude fallback",
			envMappings:        `[{"anthropic":"","kiro":"claude-sonnet-4.6"}]`,
			model:              "gpt-4o",
			wantKiroModel:      DefaultModel,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6",
		},
		{
			name:               "env override with already-suffixed anthropic does not double-suffix at 1m",
			envMappings:        `[{"anthropic":"custom-1m[1m]","kiro":"claude-custom-1m","kiro_1m":"claude-custom-1m"}]`,
			model:              "claude-custom-1m",
			wantKiroModel:      "claude-custom-1m",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "custom-1m[1m]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envMappings != "" {
				t.Setenv("KIROCC_MODEL_MAPPINGS", tt.envMappings)
			}
			gotModel, gotThinking, gotWindow, gotAnthropic := Resolve(tt.model, tt.context1M)
			if gotModel != tt.wantKiroModel {
				t.Errorf("Resolve(%q) kiroModel = %q, want %q", tt.model, gotModel, tt.wantKiroModel)
			}
			if gotThinking != tt.wantThinking {
				t.Errorf("Resolve(%q) thinking = %v, want %v", tt.model, gotThinking, tt.wantThinking)
			}
			if gotWindow != tt.wantContextWindow {
				t.Errorf("Resolve(%q) contextWindowSize = %d, want %d", tt.model, gotWindow, tt.wantContextWindow)
			}
			if gotAnthropic != tt.wantAnthropicModel {
				t.Errorf("Resolve(%q) anthropicModel = %q, want %q", tt.model, gotAnthropic, tt.wantAnthropicModel)
			}
		})
	}
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name        string
		envMappings string
		checkModel  string // if set, verify this model is in the list
	}{
		{
			name:       "default models are deduplicated and contain DefaultModel",
			checkModel: DefaultModel,
		},
		{
			name:        "env override model included",
			envMappings: `[{"anthropic":"extra-model","kiro":"claude-extra-1"}]`,
			checkModel:  "claude-extra-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envMappings != "" {
				t.Setenv("KIROCC_MODEL_MAPPINGS", tt.envMappings)
			} else {
				t.Setenv("KIROCC_MODEL_MAPPINGS", "")
			}

			result := ListModels()
			if len(result) == 0 {
				t.Fatal("ListModels returned empty slice")
			}

			// Check deduplication
			seen := make(map[string]bool)
			for _, m := range result {
				if seen[m] {
					t.Errorf("ListModels returned duplicate: %q", m)
				}
				seen[m] = true
			}

			if tt.checkModel != "" && !slices.Contains(result, tt.checkModel) {
				t.Errorf("ListModels missing expected model %q", tt.checkModel)
			}
		})
	}
}

func TestMapping_FieldNames(t *testing.T) {
	m := Mapping{Anthropic: "claude-test", Kiro: "claude-test-kiro", Kiro1M: "claude-test-kiro-1m", ContextWindowSize: 100_000}
	if m.Anthropic != "claude-test" {
		t.Errorf("Anthropic = %q, want %q", m.Anthropic, "claude-test")
	}
	if m.Kiro != "claude-test-kiro" {
		t.Errorf("Kiro = %q, want %q", m.Kiro, "claude-test-kiro")
	}
	if m.Kiro1M != "claude-test-kiro-1m" {
		t.Errorf("Kiro1M = %q, want %q", m.Kiro1M, "claude-test-kiro-1m")
	}
}
