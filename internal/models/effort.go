package models

import "slices"

// Effort levels accepted by output_config.effort, ordered low → high.
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortXHigh  = "xhigh"
	EffortMax    = "max"
)

// EffortNone disables reasoning on models whose enum lists it (GPT 5.6 family).
// Deliberately NOT in effortRank: it is not a rankable level, and adding it
// there would make ResolveEffort clamp "none" to "max" on Claude models.
const EffortNone = "none"

// effortRank is the global low→high ordering of rankable effort levels.
// Model-specific unranked values such as "none" are accepted only through the
// capability's explicit enum.
var effortRank = map[string]int{
	EffortLow:    0,
	EffortMedium: 1,
	EffortHigh:   2,
	EffortXHigh:  3,
	EffortMax:    4,
}

type effortCapability struct {
	// reasoning marks the GPT 5.6 reasoning-style family; see IsReasoningModel.
	reasoning bool
	levels    []string
}

var (
	fullEffortLevels      = []string{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
	standardEffortLevels  = []string{EffortLow, EffortMedium, EffortHigh, EffortMax}
	reasoningEffortLevels = []string{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
)

// effortCapabilities is the single source of truth for both the wire schema
// and accepted effort values of each upstream model. Models absent from this
// table do not support effort.
var effortCapabilities = map[string]effortCapability{
	// 5-value enum (includes xhigh); 128000 max-output models.
	"claude-opus-5":   {levels: fullEffortLevels},
	"claude-opus-4.8": {levels: fullEffortLevels},
	"claude-opus-4.7": {levels: fullEffortLevels},
	// 5-value enum (includes xhigh); 64000 max-output model.
	"claude-sonnet-5": {levels: fullEffortLevels},
	// 4-value enum (no xhigh); 64000 max-output models.
	"claude-opus-4.6":      {levels: standardEffortLevels},
	"claude-sonnet-4.6":    {levels: standardEffortLevels},
	"claude-opus-4.6-1m":   {levels: standardEffortLevels},
	"claude-sonnet-4.6-1m": {levels: standardEffortLevels},
	// GPT 5.6 family: 6-value enum including none (reasoning.effort schema);
	// 128000 max-output models.
	"gpt-5.6-sol":   {reasoning: true, levels: reasoningEffortLevels},
	"gpt-5.6-terra": {reasoning: true, levels: reasoningEffortLevels},
	"gpt-5.6-luna":  {reasoning: true, levels: reasoningEffortLevels},
}

// IsReasoningModel reports whether the resolved Kiro model belongs to a
// reasoning-style family (GPT 5.6). These models share a bundle of behaviors:
// the reasoning.effort wire schema (instead of output_config.effort), opaque
// redacted_thinking blobs that trail tool_use and must be drained and
// replayed, and no [1m]/thinking suffix support. Unknown models are
// Claude-compatible (false).
func IsReasoningModel(kiroModel string) bool {
	return effortCapabilities[kiroModel].reasoning
}

// ResolveEffort returns the effort level to send for the given Kiro model.
//
// It returns "" (effort omitted) when: no effort was requested, the model does
// not support effort, or the requested string is not a recognized effort level —
// typos and arbitrary values like "enabled" are dropped, never promoted. A valid
// level the model doesn't list maps to the model's highest supported tier; in
// practice the only such gap is "xhigh" on 4-value models, which kiro.dev treats
// as the top tier ("max").
func ResolveEffort(kiroModel, requested string) string {
	if requested == "" {
		return ""
	}
	capability, ok := effortCapabilities[kiroModel]
	if !ok {
		// Fall back to the discovered catalog so a model launched after this
		// build still gets its effort forwarded instead of silently dropped.
		enum, ok = catalogEffortEnum(kiroModel)
	}
	if !ok {
		return ""
	}
	// Enum membership wins: accepts model-specific values like "none" that
	// are not rankable levels.
	if slices.Contains(capability.levels, requested) {
		return requested
	}
	if _, valid := effortRank[requested]; !valid {
		return "" // unrecognized value: drop rather than guess.
	}
	return capability.levels[len(capability.levels)-1]
}
