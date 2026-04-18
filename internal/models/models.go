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
}

const ThinkingSuffix = "[1m]"

// Context window sizes.
const (
	DefaultContextWindowSize  = 200_000
	ThinkingContextWindowSize = 1_000_000
)

// modelMapOrdered is ordered slice of model mappings.
// Uses exact key matching against both Anthropic and Kiro fields (first match wins).
// Order matters: specific entries must precede legacy aliases that share the same Kiro value.
var modelMapOrdered = []Mapping{
	{Anthropic: "claude-opus-4-7[1m]", Kiro: "claude-opus-4.7", Kiro1M: "claude-opus-4.7"},
	{Anthropic: "claude-opus-4-6[1m]", Kiro: "claude-opus-4.6", Kiro1M: "claude-opus-4.6"},
	{Anthropic: "claude-opus-4-7", Kiro: "claude-opus-4.7", Kiro1M: "claude-opus-4.7"},
	{Anthropic: "claude-sonnet-4-6", Kiro: "claude-sonnet-4.6", Kiro1M: "claude-sonnet-4.6-1m"},
	{Anthropic: "claude-sonnet-4.5", Kiro: "claude-sonnet-4.5", Kiro1M: "claude-sonnet-4.5-1m"},
	{Anthropic: "claude-opus-4-6", Kiro: "claude-opus-4.6", Kiro1M: "claude-opus-4.6"},
	{Anthropic: "claude-opus-4.5", Kiro: "claude-opus-4.5"},
	{Anthropic: "claude-haiku-4.5", Kiro: "claude-haiku-4.5"},
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
	if !matched {
		if before, ok := strings.CutSuffix(model, ThinkingSuffix); ok {
			model = before
			thinking = true
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
		}
	}

	if context1M {
		thinking = true
	}

	if !matched {
		if strings.HasPrefix(model, "claude-") {
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

// ListModels returns a deduplicated list of all Kiro model values from
// modelMapOrdered plus any env overrides.
func ListModels() []string {
	seen := make(map[string]struct{})
	var result []string
	for _, m := range effectiveMappings() {
		if _, ok := seen[m.Kiro]; !ok {
			seen[m.Kiro] = struct{}{}
			result = append(result, m.Kiro)
		}
	}
	return result
}
