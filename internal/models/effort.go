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

// effortRank is the global low→high ordering of valid effort levels. It is the
// single source of truth for which strings are valid effort values; anything not
// present here is treated as an unrecognized value and dropped.
var effortRank = map[string]int{
	EffortLow:    0,
	EffortMedium: 1,
	EffortHigh:   2,
	EffortXHigh:  3,
	EffortMax:    4,
}

// effortEnums maps each effort-capable Kiro model to its allowed effort levels,
// matching the per-model additionalModelRequestFieldsSchema advertised by
// ListAvailableModels (kiro-cli 2.5.1). Models absent from this table do not
// support effort and must omit additionalModelRequestFields entirely.
var effortEnums = map[string][]string{
	// 5-value enum (includes xhigh); 128000 max-output models.
	"claude-opus-4.8": {EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
	"claude-opus-4.7": {EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
	// 4-value enum (no xhigh); 64000 max-output models.
	"claude-opus-4.6":      {EffortLow, EffortMedium, EffortHigh, EffortMax},
	"claude-sonnet-4.6":    {EffortLow, EffortMedium, EffortHigh, EffortMax},
	"claude-opus-4.6-1m":   {EffortLow, EffortMedium, EffortHigh, EffortMax},
	"claude-sonnet-4.6-1m": {EffortLow, EffortMedium, EffortHigh, EffortMax},
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
	if _, valid := effortRank[requested]; !valid {
		return "" // unrecognized value: drop rather than guess.
	}
	enum, ok := effortEnums[kiroModel]
	if !ok {
		return ""
	}
	if slices.Contains(enum, requested) {
		return requested
	}
	return enum[len(enum)-1]
}
