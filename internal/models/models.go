package models

import (
	"encoding/json/v2"
	"log/slog"
	"os"
	"strings"
	"sync"
)

type Mapping struct {
	Anthropic         string `json:"anthropic"`
	Kiro              string `json:"kiro"`
	Kiro1M            string `json:"kiro_1m,omitempty"`
	ContextWindowSize int    `json:"context_window_size,omitzero"` // 0 means use default
	// DisplayName, when set, additionally advertises the Anthropic ID in
	// /v1/models with this display_name. Used for discovery aliases: Claude
	// Code's gateway model discovery drops IDs not starting with
	// claude/anthropic, so non-claude upstream models need a claude- prefixed
	// alias to appear in the model picker.
	DisplayName string `json:"display_name,omitempty"`
}

const ThinkingSuffix = "[1m]"

// normalizeThinkingSuffix canonicalizes the trailing 1M context marker while
// leaving the model ID itself untouched. Claude Code emits both `[1m]` and
// `[1M]` depending on the call path; Kiro model IDs are case-sensitive and
// must never receive either suffix.
func normalizeThinkingSuffix(model string) string {
	if len(model) < len(ThinkingSuffix) {
		return model
	}
	suffixStart := len(model) - len(ThinkingSuffix)
	if strings.EqualFold(model[suffixStart:], ThinkingSuffix) {
		return model[:suffixStart] + ThinkingSuffix
	}
	return model
}

// Context window sizes.
const (
	DefaultContextWindowSize  = 200_000
	ThinkingContextWindowSize = 1_000_000
)

// modelMapOrdered is ordered slice of model mappings.
// Uses exact key matching against both Anthropic and Kiro fields (first match wins).
// Order matters: specific entries must precede legacy aliases that share the same Kiro value.
var modelMapOrdered = []Mapping{
	{Anthropic: "claude-opus-5[1m]", Kiro: "claude-opus-5", Kiro1M: "claude-opus-5"},
	{Anthropic: "claude-opus-4-8[1m]", Kiro: "claude-opus-4.8", Kiro1M: "claude-opus-4.8"},
	{Anthropic: "claude-opus-4-7[1m]", Kiro: "claude-opus-4.7", Kiro1M: "claude-opus-4.7"},
	{Anthropic: "claude-opus-4-6[1m]", Kiro: "claude-opus-4.6", Kiro1M: "claude-opus-4.6"},
	{Anthropic: "claude-sonnet-5[1m]", Kiro: "claude-sonnet-5", Kiro1M: "claude-sonnet-5"},
	{Anthropic: "claude-opus-5", Kiro: "claude-opus-5", Kiro1M: "claude-opus-5"},
	{Anthropic: "claude-opus-4-8", Kiro: "claude-opus-4.8", Kiro1M: "claude-opus-4.8"},
	{Anthropic: "claude-opus-4-7", Kiro: "claude-opus-4.7", Kiro1M: "claude-opus-4.7"},
	{Anthropic: "claude-sonnet-5", Kiro: "claude-sonnet-5", Kiro1M: "claude-sonnet-5"},
	{Anthropic: "claude-sonnet-4-6", Kiro: "claude-sonnet-4.6", Kiro1M: "claude-sonnet-4.6-1m"},
	{Anthropic: "claude-sonnet-4.5", Kiro: "claude-sonnet-4.5", Kiro1M: "claude-sonnet-4.5-1m"},
	{Anthropic: "claude-opus-4-6", Kiro: "claude-opus-4.6", Kiro1M: "claude-opus-4.6"},
	{Anthropic: "claude-opus-4.5", Kiro: "claude-opus-4.5"},
	{Anthropic: "claude-haiku-4.5", Kiro: "claude-haiku-4.5"},
	// GPT 5.6 family (Kiro backend, reasoning.effort schema, 272k input window).
	// No [1m] aliases: these models have a fixed 272k window and no 1M variant.
	{Anthropic: "gpt-5.6-sol", Kiro: "gpt-5.6-sol", ContextWindowSize: 272_000},
	{Anthropic: "gpt-5.6-terra", Kiro: "gpt-5.6-terra", ContextWindowSize: 272_000},
	{Anthropic: "gpt-5.6-luna", Kiro: "gpt-5.6-luna", ContextWindowSize: 272_000},
	// Discovery aliases: claude- prefixed IDs that pass Claude Code's gateway
	// model discovery filter (which drops IDs not starting with
	// claude/anthropic), so the GPT models appear in the /model picker.
	{Anthropic: "claude-gpt-5.6-sol", Kiro: "gpt-5.6-sol", ContextWindowSize: 272_000, DisplayName: "GPT 5.6 Sol"},
	{Anthropic: "claude-gpt-5.6-terra", Kiro: "gpt-5.6-terra", ContextWindowSize: 272_000, DisplayName: "GPT 5.6 Terra"},
	{Anthropic: "claude-gpt-5.6-luna", Kiro: "gpt-5.6-luna", ContextWindowSize: 272_000, DisplayName: "GPT 5.6 Luna"},
}

const DefaultModel = "claude-sonnet-4.6"

// DefaultAnthropicModel is the Anthropic-form ID corresponding to DefaultModel.
// Returned as the response model for non-claude fallback so callers like
// Claude Code can map it to a context window size. Kept as a separate constant
// (not derived from modelMapOrdered) so env overrides cannot poison it.
const DefaultAnthropicModel = "claude-sonnet-4-6"

// envCache caches parsed env mappings, re-parsing only when the raw string changes.
var envCache struct {
	mu     sync.Mutex
	raw    string
	parsed []Mapping
}

// envMappings parses KIROCC_MODEL_MAPPINGS env var and returns the overrides.
// Results are cached and only re-parsed when the env var value changes.
func envMappings() []Mapping {
	raw := os.Getenv("KIROCC_MODEL_MAPPINGS")

	envCache.mu.Lock()
	defer envCache.mu.Unlock()

	if envCache.raw == raw {
		return envCache.parsed
	}

	envCache.raw = raw

	if raw == "" {
		envCache.parsed = nil
		return nil
	}
	var mappings []Mapping
	if err := json.Unmarshal([]byte(raw), &mappings); err != nil {
		slog.Warn("KIROCC_MODEL_MAPPINGS: invalid JSON, ignoring", "err", err)
		envCache.parsed = nil
		return nil
	}
	envCache.parsed = mappings
	return mappings
}

// effectiveMappings returns env overrides followed by built-in mappings.
func effectiveMappings() []Mapping {
	overrides := envMappings()
	if len(overrides) == 0 {
		return modelMapOrdered
	}
	result := make([]Mapping, 0, len(overrides)+len(modelMapOrdered))
	result = append(result, overrides...)
	result = append(result, modelMapOrdered...)
	return result
}

// Resolve maps an Anthropic or Kiro model name to the Kiro SKU sent upstream,
// the thinking flag, the context window size, and the Anthropic-form ID to
// echo back in /v1/messages responses.
//
// Lookup is two-tier:
//  1. Exact match against `m.Anthropic` / `m.Kiro` first (no `[1m]` strip).
//     This catches always-1M aliases like `claude-opus-4-7[1m]` that are a
//     context-window advertisement, not a thinking opt-in — the suffix is
//     preserved verbatim in `anthropicModel` and `thinking` stays false.
//  2. If no exact match, strip a trailing `[1m]` from the input, set
//     `thinking = true`, and retry the lookup. This is the legacy path
//     used by aliases that don't have an explicit `[1m]` entry (e.g.
//     `claude-sonnet-4-6[1m]` routes to the `-1m` Kiro SKU with thinking).
//
// The output `anthropicModel` gets a trailing `[1m]` when the routed
// context window is 1M (regardless of thinking), so Claude Code's
// `mR()` / `A2()` picks the 1M window even if the input was bare.
//
// Upstream `kiroModel` is never `[1m]`-suffixed — it always comes from
// mapping tables. KIROCC_MODEL_MAPPINGS env var can override mappings.
func Resolve(model string, context1M bool) (kiroModel string, thinking bool, contextWindowSize int, anthropicModel string) {
	model = normalizeThinkingSuffix(model)

	var matchedWindowSize int
	var matchedKiro1M string
	var matchedAnthropic string
	var matched bool

	// Tier 1: exact match (no strip). Handles `claude-opus-4-7[1m]` etc.
	mappings := effectiveMappings()
	for _, m := range mappings {
		if model == m.Anthropic || model == m.Kiro {
			kiroModel = m.Kiro
			matchedKiro1M = m.Kiro1M
			matchedWindowSize = m.ContextWindowSize
			matchedAnthropic = m.Anthropic
			matched = true
			break
		}
	}

	// Tier 2: strip `[1m]` (treated as thinking opt-in) and retry.
	// Reasoning-style models (GPT 5.6) are excluded — they have no 1M variant
	// and no thinking opt-in, so e.g. `gpt-5.6-sol[1m]` falls through to the
	// default fallback below. Judging by the resolved Kiro model's intrinsic
	// capability (not a per-row flag) means env aliases inherit the exclusion
	// automatically.
	var reasoningExcluded bool
	if !matched {
		if before, ok := strings.CutSuffix(model, ThinkingSuffix); ok {
			model = before
			thinking = true
			for _, m := range mappings {
				if model == m.Anthropic || model == m.Kiro {
					if IsReasoningModel(m.Kiro) {
						reasoningExcluded = true
						continue
					}
					kiroModel = m.Kiro
					matchedKiro1M = m.Kiro1M
					matchedWindowSize = m.ContextWindowSize
					matchedAnthropic = m.Anthropic
					matched = true
					break
				}
			}
		}
	}

	if context1M {
		thinking = true
	}

	if !matched {
		// reasoningExcluded blocks the claude- passthrough: a claude- prefixed
		// discovery alias to a GPT model (`claude-gpt-5.6-sol[1m]`) must not
		// be forwarded verbatim as an unknown upstream SKU.
		if strings.HasPrefix(model, "claude-") && !reasoningExcluded {
			kiroModel = model
			anthropicModel = model
		} else {
			slog.Warn("models.Resolve: non-claude model, falling back to default",
				"requested_model", model,
				"kiro_model", DefaultModel,
			)
			kiroModel = DefaultModel
			anthropicModel = DefaultAnthropicModel
		}
	} else {
		anthropicModel = matchedAnthropic
	}

	// A mapping with Kiro1M == Kiro means the model always uses 1M context
	// (no separate -1m SKU exists upstream, e.g. claude-opus-4.7). Thinking
	// stays off unless explicitly requested via suffix, header, or request field.
	switch {
	case matchedKiro1M == kiroModel:
		contextWindowSize = ThinkingContextWindowSize
	case thinking && matchedKiro1M != "":
		kiroModel = matchedKiro1M
		contextWindowSize = ThinkingContextWindowSize
	case matchedWindowSize > 0:
		contextWindowSize = matchedWindowSize
	default:
		contextWindowSize = DefaultContextWindowSize
	}

	// Advertise 1M context to Claude Code by appending ThinkingSuffix to the
	// response model ID. Guarded against double-suffix when a user-supplied
	// env override specifies an already-suffixed anthropic value.
	if contextWindowSize == ThinkingContextWindowSize && !strings.HasSuffix(anthropicModel, ThinkingSuffix) {
		anthropicModel += ThinkingSuffix
	}

	return kiroModel, thinking, contextWindowSize, anthropicModel
}

// ModelInfo is one entry served by /v1/models.
type ModelInfo struct {
	ID          string
	DisplayName string // optional; picked up by Claude Code's model picker
}

// ListModels returns a deduplicated list of all model IDs to advertise in
// /v1/models: each mapping's Kiro value, plus the Anthropic ID of any mapping
// with a DisplayName (discovery aliases). Env overrides are included.
func ListModels() []ModelInfo {
	seen := make(map[string]struct{})
	var result []ModelInfo
	add := func(id, displayName string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		result = append(result, ModelInfo{ID: id, DisplayName: displayName})
	}
	for _, m := range effectiveMappings() {
		if m.DisplayName != "" {
			add(m.Anthropic, m.DisplayName)
		}
		add(m.Kiro, "")
	}
	return result
}
