package models

import (
	"slices"
	"testing"
)

// resetCatalog clears discovered state so cases cannot leak into each other.
func resetCatalog(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetCatalog(nil) })
	SetCatalog(nil)
}

func TestAnthropicAlias(t *testing.T) {
	tests := []struct {
		kiro string
		want string
	}{
		{"claude-opus-4.6", "claude-opus-4-6"},
		{"claude-sonnet-4.6", "claude-sonnet-4-6"},
		{"claude-opus-5", "claude-opus-5"},
		{"claude-sonnet-5", "claude-sonnet-5"},
	}
	for _, tt := range tests {
		if got := anthropicAlias(tt.kiro); got != tt.want {
			t.Errorf("anthropicAlias(%q) = %q, want %q", tt.kiro, got, tt.want)
		}
	}
}

func TestSetCatalog_ReportsOnlyNewModels(t *testing.T) {
	resetCatalog(t)

	added := SetCatalog([]CatalogModel{
		{ID: "claude-opus-5", MaxInputTokens: 1_000_000, EffortEnum: []string{"low", "max"}},
		{ID: "claude-opus-9", MaxInputTokens: 1_000_000},
		{ID: "gpt-5.6-terra", MaxInputTokens: 272_000},
		{ID: "auto", MaxInputTokens: 1_000_000},
	})

	want := []string{"claude-opus-9"}
	if !slices.Equal(added, want) {
		t.Errorf("SetCatalog added = %v, want %v", added, want)
	}
}

func TestSetCatalog_DiscoveredModelResolves(t *testing.T) {
	resetCatalog(t)
	SetCatalog([]CatalogModel{
		{ID: "claude-opus-9", MaxInputTokens: 1_000_000, EffortEnum: []string{"low", "medium", "high", "xhigh", "max"}},
		{ID: "claude-mini-1.2", MaxInputTokens: 200_000},
	})

	tests := []struct {
		name               string
		model              string
		context1M          bool
		wantKiroModel      string
		wantThinking       bool
		wantContextWindow  int
		wantAnthropicModel string
	}{
		{
			name:               "1m model advertises 1m without enabling thinking",
			model:              "claude-opus-9",
			wantKiroModel:      "claude-opus-9",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-9[1m]",
		},
		{
			name:               "1m alias is exact-matched so the suffix does not opt into thinking",
			model:              "claude-opus-9[1m]",
			wantKiroModel:      "claude-opus-9",
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-9[1m]",
		},
		{
			name:               "context1M header still enables thinking",
			model:              "claude-opus-9",
			context1M:          true,
			wantKiroModel:      "claude-opus-9",
			wantThinking:       true,
			wantContextWindow:  ThinkingContextWindowSize,
			wantAnthropicModel: "claude-opus-9[1m]",
		},
		{
			name:               "dotted SKU resolves to the dashed anthropic alias",
			model:              "claude-mini-1.2",
			wantKiroModel:      "claude-mini-1.2",
			wantContextWindow:  200_000,
			wantAnthropicModel: "claude-mini-1-2",
		},
		{
			name:               "dashed alias resolves to the dotted SKU",
			model:              "claude-mini-1-2",
			wantKiroModel:      "claude-mini-1.2",
			wantContextWindow:  200_000,
			wantAnthropicModel: "claude-mini-1-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotThinking, gotWindow, gotAnthropic := Resolve(tt.model, tt.context1M)
			if gotModel != tt.wantKiroModel {
				t.Errorf("kiroModel = %q, want %q", gotModel, tt.wantKiroModel)
			}
			if gotThinking != tt.wantThinking {
				t.Errorf("thinking = %v, want %v", gotThinking, tt.wantThinking)
			}
			if gotWindow != tt.wantContextWindow {
				t.Errorf("contextWindowSize = %d, want %d", gotWindow, tt.wantContextWindow)
			}
			if gotAnthropic != tt.wantAnthropicModel {
				t.Errorf("anthropicModel = %q, want %q", gotAnthropic, tt.wantAnthropicModel)
			}
		})
	}
}

// The catalog advertises 1M input for claude-sonnet-4.6, but kirocc deliberately
// routes bare sonnet-4.6 to the 200k window and only switches to the -1m SKU on
// an explicit 1M opt-in. Discovery must not quietly change that.
func TestSetCatalog_BuiltinsWin(t *testing.T) {
	resetCatalog(t)
	SetCatalog([]CatalogModel{
		{ID: "claude-sonnet-4.6", MaxInputTokens: 1_000_000, EffortEnum: []string{"low", "max"}},
	})

	kiroModel, thinking, window, anthropicModel := Resolve("claude-sonnet-4-6", false)
	if kiroModel != "claude-sonnet-4.6" || thinking || window != DefaultContextWindowSize || anthropicModel != "claude-sonnet-4-6" {
		t.Errorf("Resolve(claude-sonnet-4-6) = (%q, %v, %d, %q), want (claude-sonnet-4.6, false, %d, claude-sonnet-4-6)",
			kiroModel, thinking, window, anthropicModel, DefaultContextWindowSize)
	}

	// The built-in 4-value enum must still clamp xhigh, not adopt the 2-value
	// enum from the catalog.
	if got := ResolveEffort("claude-sonnet-4.6", "xhigh"); got != EffortMax {
		t.Errorf("ResolveEffort(claude-sonnet-4.6, xhigh) = %q, want %q", got, EffortMax)
	}
}

func TestSetCatalog_EnvOverrideBeatsDiscovery(t *testing.T) {
	resetCatalog(t)
	t.Setenv("KIROCC_MODEL_MAPPINGS", `[{"anthropic":"claude-opus-9","kiro":"claude-pinned","context_window_size":200000}]`)
	SetCatalog([]CatalogModel{{ID: "claude-opus-9", MaxInputTokens: 1_000_000}})

	kiroModel, _, window, _ := Resolve("claude-opus-9", false)
	if kiroModel != "claude-pinned" || window != 200_000 {
		t.Errorf("Resolve(claude-opus-9) = (%q, %d), want (claude-pinned, 200000)", kiroModel, window)
	}
}

func TestSetCatalog_EffortFallback(t *testing.T) {
	resetCatalog(t)
	SetCatalog([]CatalogModel{
		{ID: "claude-opus-9", MaxInputTokens: 1_000_000, EffortEnum: []string{EffortLow, EffortMedium, EffortHigh, EffortMax}},
		{ID: "claude-plain-1", MaxInputTokens: 200_000},
	})

	tests := []struct {
		name      string
		kiroModel string
		requested string
		want      string
	}{
		{"discovered enum honoured", "claude-opus-9", "high", "high"},
		{"discovered enum clamps missing xhigh", "claude-opus-9", "xhigh", EffortMax},
		{"unrecognized value still dropped", "claude-opus-9", "turbo", ""},
		{"model without effort schema drops effort", "claude-plain-1", "high", ""},
		{"model absent from catalog drops effort", "claude-absent", "high", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveEffort(tt.kiroModel, tt.requested); got != tt.want {
				t.Errorf("ResolveEffort(%q, %q) = %q, want %q", tt.kiroModel, tt.requested, got, tt.want)
			}
		})
	}
}

func TestSetCatalog_ListModelsIncludesDiscovered(t *testing.T) {
	resetCatalog(t)
	t.Setenv("KIROCC_MODEL_MAPPINGS", "")
	SetCatalog([]CatalogModel{
		{ID: "claude-opus-9", MaxInputTokens: 1_000_000},
		{ID: "gpt-5.6-terra", MaxInputTokens: 272_000},
	})

	got := ListModels()
	if !slices.Contains(got, "claude-opus-9") {
		t.Errorf("ListModels() = %v, missing claude-opus-9", got)
	}
	if slices.Contains(got, "gpt-5.6-terra") {
		t.Errorf("ListModels() = %v, must not include non-claude models", got)
	}
	seen := make(map[string]bool, len(got))
	for _, m := range got {
		if seen[m] {
			t.Errorf("ListModels() returned duplicate %q", m)
		}
		seen[m] = true
	}
}

func TestSetCatalog_NilResets(t *testing.T) {
	resetCatalog(t)
	SetCatalog([]CatalogModel{{ID: "claude-opus-9", MaxInputTokens: 1_000_000}})
	if _, _, window, _ := Resolve("claude-opus-9", false); window != ThinkingContextWindowSize {
		t.Fatalf("precondition failed: window = %d", window)
	}

	SetCatalog(nil)
	kiroModel, _, window, anthropicModel := Resolve("claude-opus-9", false)
	if kiroModel != "claude-opus-9" || window != DefaultContextWindowSize || anthropicModel != "claude-opus-9" {
		t.Errorf("after reset Resolve = (%q, %d, %q), want passthrough (claude-opus-9, %d, claude-opus-9)",
			kiroModel, window, anthropicModel, DefaultContextWindowSize)
	}
}
