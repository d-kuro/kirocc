package models

import (
	"slices"
	"strings"
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
			name:               "claude-opus-5 uses 1m context without thinking",
			model:              "claude-opus-5",
			wantKiroModel:      "claude-opus-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-5[1m]",
		},
		{
			name:               "claude-opus-5[1m] exact-match preserves suffix without thinking",
			model:              "claude-opus-5[1m]",
			wantKiroModel:      "claude-opus-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-5[1m]",
		},
		{
			name:               "claude-opus-5 uppercase [1M] is normalized without thinking",
			model:              "claude-opus-5[1M]",
			wantKiroModel:      "claude-opus-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-5[1m]",
		},
		{
			name:               "claude-opus-5 with context1M keeps thinking off",
			model:              "claude-opus-5",
			context1M:          true,
			wantKiroModel:      "claude-opus-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-5[1m]",
		},
		{
			name:               "claude-opus-4-8 uses 1m context without thinking",
			model:              "claude-opus-4-8",
			wantKiroModel:      "claude-opus-4.8",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-8[1m]",
		},
		{
			name:               "claude-opus-4-8[1m] exact-match preserves suffix without thinking",
			model:              "claude-opus-4-8[1m]",
			wantKiroModel:      "claude-opus-4.8",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-8[1m]",
		},
		{
			name:               "claude-opus-4-8 with context1M keeps thinking off",
			model:              "claude-opus-4-8",
			context1M:          true,
			wantKiroModel:      "claude-opus-4.8",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-8[1m]",
		},
		{
			name:               "kiro model name claude-opus-4.8 always resolves to 1m",
			model:              "claude-opus-4.8[1m]",
			wantKiroModel:      "claude-opus-4.8",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-8[1m]",
		},
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
			name:               "claude-opus-4-7 with context1M keeps thinking off",
			model:              "claude-opus-4-7",
			context1M:          true,
			wantKiroModel:      "claude-opus-4.7",
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
			name:               "claude-opus-4-6 with context1M keeps thinking off",
			model:              "claude-opus-4-6",
			context1M:          true,
			wantKiroModel:      "claude-opus-4.6",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-4-6[1m]",
		},
		{
			name:               "claude-sonnet-5 always resolves to 1m without thinking",
			model:              "claude-sonnet-5",
			wantKiroModel:      "claude-sonnet-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-5[1m]",
		},
		{
			name:               "claude-sonnet-5[1m] exact-match preserves suffix without thinking",
			model:              "claude-sonnet-5[1m]",
			wantKiroModel:      "claude-sonnet-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-5[1m]",
		},
		{
			name:               "claude-sonnet-5 uppercase [1M] is normalized without thinking",
			model:              "claude-sonnet-5[1M]",
			wantKiroModel:      "claude-sonnet-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-5[1m]",
		},
		{
			name:               "claude-sonnet-5 with context1M keeps thinking off",
			model:              "claude-sonnet-5",
			context1M:          true,
			wantKiroModel:      "claude-sonnet-5",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-5[1m]",
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
			name:               "claude-sonnet-4-6 uppercase [1M] selects 1m SKU",
			model:              "claude-sonnet-4-6[1M]",
			wantKiroModel:      "claude-sonnet-4.6-1m",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6[1m]",
		},
		{
			name:               "claude-sonnet-4-6 with context1M resolves to 1m without thinking",
			model:              "claude-sonnet-4-6",
			context1M:          true,
			wantKiroModel:      "claude-sonnet-4.6-1m",
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
			name:               "claude-haiku-4.5 with context1M no 1m variant keeps thinking off",
			model:              "claude-haiku-4.5",
			context1M:          true,
			wantKiroModel:      "claude-haiku-4.5",
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
			name:               "gpt-5.6-sol resolves with 272k window",
			model:              "gpt-5.6-sol",
			wantKiroModel:      "gpt-5.6-sol",
			wantContextWindow:  272_000,
			wantAnthropicModel: "gpt-5.6-sol",
		},
		{
			name:               "gpt-5.6-terra resolves with 272k window",
			model:              "gpt-5.6-terra",
			wantKiroModel:      "gpt-5.6-terra",
			wantContextWindow:  272_000,
			wantAnthropicModel: "gpt-5.6-terra",
		},
		{
			name:               "gpt-5.6-luna resolves with 272k window",
			model:              "gpt-5.6-luna",
			wantKiroModel:      "gpt-5.6-luna",
			wantContextWindow:  272_000,
			wantAnthropicModel: "gpt-5.6-luna",
		},
		{
			name:               "gpt-5.6-sol[1m] does not match and falls back to default",
			model:              "gpt-5.6-sol[1m]",
			wantThinking:       true,
			wantKiroModel:      DefaultModel,
			wantContextWindow:  DefaultContextWindowSize,
			wantAnthropicModel: "claude-sonnet-4-6",
		},
		{
			name:               "claude-gpt-5.6-sol discovery alias resolves to gpt-5.6-sol",
			model:              "claude-gpt-5.6-sol",
			wantKiroModel:      "gpt-5.6-sol",
			wantContextWindow:  272_000,
			wantAnthropicModel: "claude-gpt-5.6-sol",
		},
		{
			name:               "claude-gpt-5.6-terra discovery alias resolves to gpt-5.6-terra",
			model:              "claude-gpt-5.6-terra",
			wantKiroModel:      "gpt-5.6-terra",
			wantContextWindow:  272_000,
			wantAnthropicModel: "claude-gpt-5.6-terra",
		},
		{
			name:               "claude-gpt-5.6-luna discovery alias resolves to gpt-5.6-luna",
			model:              "claude-gpt-5.6-luna",
			wantKiroModel:      "gpt-5.6-luna",
			wantContextWindow:  272_000,
			wantAnthropicModel: "claude-gpt-5.6-luna",
		},
		{
			name:               "claude-gpt-5.6-sol[1m] falls back to default instead of claude passthrough",
			model:              "claude-gpt-5.6-sol[1m]",
			wantThinking:       true,
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
		{
			// A row that spells `[1m]` in its own ID exists to advertise 1M, so
			// the exact-match tier must still switch to the separate 1M SKU —
			// without turning thinking on, since the ID itself carried no opt-in
			// beyond the window.
			name:               "env override suffixed row routes to its separate 1m SKU",
			envMappings:        `[{"anthropic":"custom[1m]","kiro":"claude-custom","kiro_1m":"claude-custom-1m"}]`,
			model:              "custom[1m]",
			wantKiroModel:      "claude-custom-1m",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "custom[1m]",
		},
		{
			// Same row, requested in the case Claude Code sometimes emits.
			name:               "env override suffixed row matches an upper-case request",
			envMappings:        `[{"anthropic":"custom[1M]","kiro":"claude-custom","kiro_1m":"claude-custom-1m"}]`,
			model:              "custom[1M]",
			wantKiroModel:      "claude-custom-1m",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "custom[1m]",
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
		checkModel  string // if set, verify this model ID is in the list
	}{
		{
			name:       "default models are deduplicated and contain DefaultModel",
			checkModel: DefaultModel,
		},
		{
			name:       "claude-sonnet-5 is listed exactly once (both alias rows dedupe to one Kiro value)",
			checkModel: "claude-sonnet-5",
		},
		{
			name:       "claude-opus-5 is listed exactly once (both alias rows dedupe to one Kiro value)",
			checkModel: "claude-opus-5",
		},
		{
			name:       "canonical GPT ID is listed",
			checkModel: "gpt-5.6-sol",
		},
		{
			name:       "GPT discovery alias is listed alongside the canonical ID",
			checkModel: "claude-gpt-5.6-sol",
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
			var ids []string
			for _, m := range result {
				if seen[m.ID] {
					t.Errorf("ListModels returned duplicate: %q", m.ID)
				}
				seen[m.ID] = true
				ids = append(ids, m.ID)
			}

			if tt.checkModel != "" && !slices.Contains(ids, tt.checkModel) {
				t.Errorf("ListModels missing expected model %q", tt.checkModel)
			}
		})
	}
}

// The display names drive Claude Code's model picker, and the `[1m]` IDs are
// what make it track the 1M window (it matches /\[1m\]/i on the session model
// string client-side).
func TestListModels_DisplayNames(t *testing.T) {
	t.Setenv("KIROCC_MODEL_MAPPINGS", "")

	tests := []struct {
		id         string
		want       string // expected display_name ("" = advertised unlabelled)
		wantAbsent bool
	}{
		// Discovery aliases for the non-claude models.
		{id: "claude-gpt-5.6-sol", want: "GPT 5.6 Sol"},
		{id: "claude-gpt-5.6-terra", want: "GPT 5.6 Terra"},
		{id: "claude-gpt-5.6-luna", want: "GPT 5.6 Luna"},
		// Canonical SKUs are advertised unlabelled.
		{id: "gpt-5.6-sol"},
		{id: "claude-opus-5"},
		// Always-1M models: one labelled `[1m]` entry each.
		{id: "claude-opus-5[1m]", want: "Opus 5 (1M context)"},
		{id: "claude-opus-4-8[1m]", want: "Opus 4.8 (1M context)"},
		{id: "claude-opus-4-7[1m]", want: "Opus 4.7 (1M context)"},
		{id: "claude-opus-4-6[1m]", want: "Opus 4.6 (1M context)"},
		{id: "claude-sonnet-5[1m]", want: "Sonnet 5 (1M context)"},
		// Separate 1M SKU: both windows are pickable.
		{id: "claude-sonnet-4-6", want: "Sonnet 4.6"},
		{id: "claude-sonnet-4-6[1m]", want: "Sonnet 4.6 (1M context)"},
		// An unnamed row stays out of the picker, `[1m]` alias included.
		{id: "claude-sonnet-4.5[1m]", wantAbsent: true},
	}

	got := make(map[string]string)
	for _, m := range ListModels() {
		got[m.ID] = m.DisplayName
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			name, ok := got[tt.id]
			switch {
			case tt.wantAbsent:
				if ok {
					t.Errorf("ListModels advertises %q, want it absent", tt.id)
				}
			case !ok:
				t.Errorf("ListModels missing %q", tt.id)
			case name != tt.want:
				t.Errorf("ListModels %q display name = %q, want %q", tt.id, name, tt.want)
			}
		})
	}
}

// An env override may spell the suffix in either case, so the suffix test must
// match case-insensitively — otherwise the row reads as unsuffixed and gets a
// second suffix appended.
func TestListModels_UppercaseSuffixOverrideIsNotDoubleSuffixed(t *testing.T) {
	t.Setenv("KIROCC_MODEL_MAPPINGS",
		`[{"anthropic":"custom[1M]","kiro":"claude-custom","kiro_1m":"claude-custom-1m","display_name":"Custom"}]`)

	for _, m := range ListModels() {
		if strings.Contains(strings.ToLower(m.ID), "[1m][1m]") {
			t.Errorf("ListModels advertises double-suffixed ID %q", m.ID)
		}
	}
}

// The published ID set must agree with what Resolve can actually route.
// /v1/models is a promise: a `[1m]` entry that resolves to the 200k window, or
// any entry that matches no mapping at all (so Resolve falls back), offers the
// picker a choice kirocc cannot honour.
func TestListModels_AdvertisedIDsResolveAsAdvertised(t *testing.T) {
	tests := []struct {
		name        string
		envMappings string
	}{
		{
			name: "built-ins only",
		},
		{
			// The override shadows the built-in claude-sonnet-4-6 row and has no
			// 1M SKU, so the built-in's `[1m]` alias can no longer be delivered.
			name:        "override shadows a mapping that has a 1M SKU",
			envMappings: `[{"anthropic":"claude-sonnet-4-6","kiro":"claude-pinned"}]`,
		},
		{
			// Resolve normalizes the request before comparing, so an ID
			// advertised with an upper-case suffix would never match its own row.
			name:        "override spells the suffix in upper case",
			envMappings: `[{"anthropic":"custom[1M]","kiro":"claude-custom","kiro_1m":"claude-custom-1m","display_name":"Custom"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KIROCC_MODEL_MAPPINGS", tt.envMappings)

			for _, m := range ListModels() {
				if _, _, ok := lookupMapping(m.ID); !ok {
					t.Errorf("advertised %q matches no mapping, so Resolve falls back", m.ID)
				}
				if !hasThinkingSuffix(m.ID) {
					continue
				}
				if _, _, window, _ := Resolve(m.ID, false); window != ThinkingContextWindowSize {
					t.Errorf("advertised %q resolves to a %d window, want %d", m.ID, window, ThinkingContextWindowSize)
				}
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

func TestIsReasoningModel_EnvAliasToGPT(t *testing.T) {
	// An env alias must inherit the intrinsic capability of its resolved Kiro
	// model instead of carrying a second, potentially inconsistent style value.
	t.Setenv("KIROCC_MODEL_MAPPINGS", `[{"anthropic":"my-gpt","kiro":"gpt-5.6-sol","context_window_size":272000}]`)

	kiroModel, _, _, _ := Resolve("my-gpt", false)
	if kiroModel != "gpt-5.6-sol" {
		t.Fatalf("Resolve(my-gpt) kiroModel = %q, want gpt-5.6-sol", kiroModel)
	}
	if !IsReasoningModel(kiroModel) {
		t.Fatalf("IsReasoningModel(%q) = false, want true", kiroModel)
	}
}

func TestResolve_EnvAliasToGPT_SuffixStillRejected(t *testing.T) {
	// The tier-2 [1m]-strip exclusion is judged by the resolved Kiro model's
	// intrinsic capability, so an env alias to a GPT model inherits it. Both
	// the canonical ID and the alias must reject the suffix and fall through
	// to the default fallback.
	t.Setenv("KIROCC_MODEL_MAPPINGS", `[{"anthropic":"my-gpt","kiro":"gpt-5.6-sol","context_window_size":272000}]`)

	for _, model := range []string{"gpt-5.6-sol[1m]", "my-gpt[1m]"} {
		kiroModel, _, _, _ := Resolve(model, false)
		if kiroModel != DefaultModel {
			t.Errorf("Resolve(%q) kiroModel = %q, want default fallback %q", model, kiroModel, DefaultModel)
		}
	}
}
