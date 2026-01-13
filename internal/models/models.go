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
	ContextWindowSize int    `json:"context_window_size,omitzero"` // 0 means use default
}

const ThinkingSuffix = "[1m]"

// Context window sizes.
const (
	DefaultContextWindowSize  = 200_000
	ThinkingContextWindowSize = 1_000_000
)

// modelMapOrdered is ordered slice of model mappings.
// Uses exact key matching.
var modelMapOrdered = []Mapping{
	{Anthropic: "claude-sonnet-4-6", Kiro: "claude-sonnet-4.6"},
	{Anthropic: "claude-sonnet-4-20250514", Kiro: "claude-sonnet-4"},
	{Anthropic: "claude-sonnet-4.5", Kiro: "claude-sonnet-4.5"},
	{Anthropic: "claude-opus-4-6", Kiro: "claude-opus-4.6"},
	{Anthropic: "claude-opus-4-20250514", Kiro: "claude-opus-4"},
	{Anthropic: "claude-opus-4.5", Kiro: "claude-opus-4.5"},
	{Anthropic: "claude-haiku-4.5", Kiro: "claude-haiku-4.5"},
	{Anthropic: "claude-3-5-sonnet", Kiro: "claude-sonnet-4.5"},
	{Anthropic: "claude-3.5-sonnet", Kiro: "claude-sonnet-4.5"},
	{Anthropic: "claude-3-7-sonnet", Kiro: "claude-sonnet-4.5"},
	{Anthropic: "claude-3.7-sonnet", Kiro: "claude-sonnet-4.5"},
}

const DefaultModel = "claude-sonnet-4"

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

const thinkingModelSuffix = "-1m"

// EnsureThinkingModel appends the -1m suffix if not already present.
func EnsureThinkingModel(kiroModel string) string {
	if !strings.HasSuffix(kiroModel, thinkingModelSuffix) {
		return kiroModel + thinkingModelSuffix
	}
	return kiroModel
}

// Resolve maps an Anthropic model name to a Kiro model name and context window size.
// It strips ThinkingSuffix if present (sets thinking=true), then matches
// against modelMapOrdered using exact equality. If the model starts with
// "claude-" but has no match, it is passed through as-is. Non-claude models
// return DefaultModel. KIROCC_MODEL_MAPPINGS env var can override mappings.
func Resolve(model string) (kiroModel string, thinking bool, contextWindowSize int) {
	// Strip thinking suffix
	if before, ok := strings.CutSuffix(model, ThinkingSuffix); ok {
		model = before
		thinking = true
	}

	var matchedWindowSize int
	var matched bool
	for _, m := range effectiveMappings() {
		if model == m.Anthropic {
			kiroModel = m.Kiro
			matchedWindowSize = m.ContextWindowSize
			matched = true
			break
		}
	}

	if !matched {
		if strings.HasPrefix(model, "claude-") {
			kiroModel = model
		} else {
			slog.Warn("models.Resolve: non-claude model, falling back to default",
				"requested_model", model,
				"kiro_model", DefaultModel,
			)
			kiroModel = DefaultModel
		}
	}

	// Append -1m suffix for thinking/1M context window models.
	if thinking {
		kiroModel = EnsureThinkingModel(kiroModel)
	}

	// Determine context window size.
	if thinking {
		contextWindowSize = ThinkingContextWindowSize
	} else if matchedWindowSize > 0 {
		contextWindowSize = matchedWindowSize
	} else {
		contextWindowSize = DefaultContextWindowSize
	}

	return kiroModel, thinking, contextWindowSize
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
