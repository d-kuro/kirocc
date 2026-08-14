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
	// DisplayName is the label for the model picker. It doubles as the opt-in
	// for advertising the Anthropic ID in /v1/models at all: a mapping without
	// one contributes only its Kiro SKU. See ListModels for what gets
	// advertised. On a `[1m]` row it names the base model ("Opus 5"); the
	// "(1M context)" part is appended by display1M.
	DisplayName string `json:"display_name,omitempty"`
}

// hasSeparate1MSKU reports whether the mapping's 1M context window lives in a
// distinct upstream SKU (e.g. claude-sonnet-4.6 → claude-sonnet-4.6-1m), so
// reaching 1M means switching SKUs. Its twin is the always-1M shape
// (Kiro1M == Kiro), which Resolve's switch tests directly against the resolved
// SKU because the mapping may not have matched at all.
func (m Mapping) hasSeparate1MSKU() bool {
	return m.Kiro1M != "" && m.Kiro1M != m.Kiro
}

const ThinkingSuffix = "[1m]"

// oneMLabel is appended to a picker label for the 1M-context variant of a
// model. Kept as one constant so every advertised `[1m]` entry reads the same.
const oneMLabel = " (1M context)"

// display1M labels the 1M-context variant of the model named by base. An empty
// base stays empty so a mapping without a DisplayName is advertised unlabelled
// rather than as a bare " (1M context)".
func display1M(base string) string {
	if base == "" {
		return ""
	}
	return base + oneMLabel
}

// hasThinkingSuffix reports whether a model ID carries the trailing 1M context
// marker, in either case Claude Code emits.
func hasThinkingSuffix(model string) bool {
	_, ok := strings.CutSuffix(normalizeThinkingSuffix(model), ThinkingSuffix)
	return ok
}

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
	{Anthropic: "claude-opus-5[1m]", Kiro: "claude-opus-5", Kiro1M: "claude-opus-5", DisplayName: "Opus 5"},
	{Anthropic: "claude-opus-4-8[1m]", Kiro: "claude-opus-4.8", Kiro1M: "claude-opus-4.8", DisplayName: "Opus 4.8"},
	{Anthropic: "claude-opus-4-7[1m]", Kiro: "claude-opus-4.7", Kiro1M: "claude-opus-4.7", DisplayName: "Opus 4.7"},
	{Anthropic: "claude-opus-4-6[1m]", Kiro: "claude-opus-4.6", Kiro1M: "claude-opus-4.6", DisplayName: "Opus 4.6"},
	{Anthropic: "claude-sonnet-5[1m]", Kiro: "claude-sonnet-5", Kiro1M: "claude-sonnet-5", DisplayName: "Sonnet 5"},
	{Anthropic: "claude-opus-5", Kiro: "claude-opus-5", Kiro1M: "claude-opus-5"},
	{Anthropic: "claude-opus-4-8", Kiro: "claude-opus-4.8", Kiro1M: "claude-opus-4.8"},
	{Anthropic: "claude-opus-4-7", Kiro: "claude-opus-4.7", Kiro1M: "claude-opus-4.7"},
	{Anthropic: "claude-sonnet-5", Kiro: "claude-sonnet-5", Kiro1M: "claude-sonnet-5"},
	{Anthropic: "claude-sonnet-4-6", Kiro: "claude-sonnet-4.6", Kiro1M: "claude-sonnet-4.6-1m", DisplayName: "Sonnet 4.6"},
	// No DisplayName, so this legacy row stays out of the picker (and gets no
	// `[1m]` entry) even though it has a 1M SKU. Deliberate: give it a display
	// name to surface it.
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
	// Canonicalize the suffix here, once, so every consumer compares and
	// advertises the same spelling: lookups normalize the incoming model ID, so
	// a row written as `custom[1M]` would otherwise be unreachable.
	for i := range mappings {
		mappings[i].Anthropic = normalizeThinkingSuffix(mappings[i].Anthropic)
	}
	envCache.parsed = mappings
	return mappings
}

// effectiveMappings returns env overrides, then built-in mappings, then any
// mappings discovered from Kiro's catalog. First match wins, so an explicit
// override beats a built-in and a built-in beats discovery.
func effectiveMappings() []Mapping {
	overrides := envMappings()
	discovered := catalogMappings()
	if len(overrides) == 0 && len(discovered) == 0 {
		return modelMapOrdered
	}
	result := make([]Mapping, 0, len(overrides)+len(modelMapOrdered)+len(discovered))
	result = append(result, overrides...)
	result = append(result, modelMapOrdered...)
	result = append(result, discovered...)
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
//     preserved verbatim in `anthropicModel` and `thinking` stays false. Such a
//     row still routes to its `Kiro1M` SKU when that is a separate one.
//  2. If no exact match, strip a trailing `[1m]` from the input, set
//     `thinking = true`, and retry the lookup. This is the legacy path
//     used by aliases that don't have an explicit `[1m]` entry (e.g.
//     `claude-sonnet-4-6[1m]` routes to the `-1m` Kiro SKU with thinking).
//
// `context1M` (the `context-1m` Anthropic-Beta header) routes to the 1M SKU
// without enabling thinking: Claude Code sends the header automatically
// whenever the session model carries `[1m]`, so coupling it to thinking would
// force thinking on for every 1M session.
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

	// Tier 2: strip `[1m]` (a thinking + 1M opt-in) and retry.
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

	// Route to the mapping's 1M SKU when any signal asked for it: the
	// `context-1m` header (window only), the tier-2 suffix (window + thinking,
	// which is why `thinking` implies it), or a matched row that spells the
	// suffix in its own Anthropic ID — such a row exists to advertise 1M, so it
	// must not answer with the 200k SKU.
	want1M := context1M || thinking || hasThinkingSuffix(matchedAnthropic)

	// A mapping with Kiro1M == Kiro means the model always uses 1M context
	// (no separate -1m SKU exists upstream, e.g. claude-opus-4.7), so it needs
	// no opt-in.
	switch {
	case matchedKiro1M == kiroModel:
		contextWindowSize = ThinkingContextWindowSize
	case want1M && matchedKiro1M != "":
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

// ResolveKnown maps a model ID to its upstream Kiro SKU and Anthropic-form ID,
// requiring an explicit mapping entry. Unlike Resolve it never passes an unknown
// `claude-` ID through verbatim and never falls back to DefaultModel — callers
// that escalate work to a specific model (advisor) must fail loudly rather than
// silently route to a weaker one.
//
// The returned anthropicModel never carries the `[1m]` context-window marker:
// it identifies the model for usage reporting, not the response's routed
// context window.
func ResolveKnown(model string) (kiroModel, anthropicModel string, ok bool) {
	m, want1M, found := lookupMapping(model)
	if !found {
		return "", "", false
	}
	// An explicit `[1m]` request routes to the 1M-capable SKU when the mapping
	// has one; never a silent downgrade to the 200k SKU.
	kiroModel = m.Kiro
	if want1M && m.Kiro1M != "" {
		kiroModel = m.Kiro1M
	}
	alias := m.Anthropic
	if alias == "" {
		alias = m.Kiro
	}
	return kiroModel, strings.TrimSuffix(alias, ThinkingSuffix), true
}

// lookupMapping resolves a model ID to its mapping entry using the project's
// two-tier precedence, shared with Resolve's matching half:
//
//  1. Exact match on the ID as written (so an always-1M alias like
//     `claude-opus-4-7[1m]` wins over the bare row).
//  2. Strip a trailing `[1m]` and retry, reporting want1M. Reasoning models
//     (GPT 5.6) are excluded from this tier: they have no 1M variant and no
//     thinking opt-in, so `gpt-5.6-sol[1m]` must not resolve.
//
// Within each tier an exact Anthropic-alias hit is preferred over a Kiro-SKU
// hit, so a bare ID is not captured by a `[1m]` row sharing the same SKU.
func lookupMapping(model string) (m Mapping, want1M, ok bool) {
	model = normalizeThinkingSuffix(model)
	base, stripped := strings.CutSuffix(model, ThinkingSuffix)

	mappings := effectiveMappings()
	find := func(candidate string, excludeReasoning bool) (Mapping, bool) {
		eligible := func(m Mapping) bool {
			return !excludeReasoning || !IsReasoningModel(m.Kiro)
		}
		for _, m := range mappings {
			if candidate == m.Anthropic && eligible(m) {
				return m, true
			}
		}
		for _, m := range mappings {
			if candidate == m.Kiro && eligible(m) {
				return m, true
			}
		}
		return Mapping{}, false
	}

	if m, found := find(model, false); found {
		return m, false, true
	}
	if stripped {
		if m, found := find(base, true); found {
			return m, true, true
		}
	}
	return Mapping{}, false, false
}

// routes1M reports whether the model ID actually resolves to the 1M context
// window. Used by ListModels so an advertised `[1m]` ID is never one Resolve
// would answer with the 200k window.
func routes1M(model string) bool {
	_, _, contextWindowSize, _ := Resolve(model, false)
	return contextWindowSize == ThinkingContextWindowSize
}

// ModelInfo is one entry served by /v1/models.
type ModelInfo struct {
	ID          string
	DisplayName string // optional; picked up by Claude Code's model picker
}

// ListModels returns a deduplicated list of all model IDs to advertise in
// /v1/models: every mapping's Kiro SKU, plus each mapping's Anthropic ID when
// it is a `[1m]` row or carries a DisplayName. A named mapping whose 1M window
// lives in a separate SKU also gets a `[1m]` entry, so both windows are
// pickable — but only when that ID really resolves to 1M, so the list never
// offers a window kirocc cannot deliver. Env overrides and discovered models
// are included.
//
// The `[1m]` entries exist for Claude Code: its context-window logic runs
// client-side on the session model string (/\[1m\]/i), so only a picker entry
// whose ID carries the suffix makes it track the 1M window — the response
// `model` field cannot influence it.
func ListModels() []ModelInfo {
	mappings := effectiveMappings()
	// Each mapping contributes its SKU and up to two Anthropic IDs; most
	// contribute two entries in total.
	seen := make(map[string]struct{}, 2*len(mappings))
	result := make([]ModelInfo, 0, 2*len(mappings))
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
	for _, m := range mappings {
		// Branching on the suffix keeps a double-suffixed ID unrepresentable.
		switch {
		case hasThinkingSuffix(m.Anthropic):
			add(m.Anthropic, display1M(m.DisplayName))
		case m.DisplayName != "":
			add(m.Anthropic, m.DisplayName)
			// Advertise the synthesized 1M ID only when it really routes to 1M.
			// Asking Resolve instead of trusting this row keeps the promise
			// honest when an earlier mapping shadows it — an env override on the
			// same Anthropic ID with no 1M SKU wins the lookup, so the alias
			// would resolve to a 200k window. hasSeparate1MSKU short-circuits
			// first: rows that never get an alias must not pay for a lookup, and
			// suffixing a reasoning model would log a fallback warning.
			if alias := m.Anthropic + ThinkingSuffix; m.hasSeparate1MSKU() && routes1M(alias) {
				add(alias, display1M(m.DisplayName))
			}
		}
		add(m.Kiro, "")
	}
	return result
}
