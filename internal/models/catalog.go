package models

import (
	"strings"
	"sync"
)

// CatalogModel is one entry of Kiro's ListAvailableModels response, reduced to
// the fields that drive model resolution. Declared here rather than imported so
// the models package stays free of transport dependencies.
type CatalogModel struct {
	// ID is the Kiro SKU, e.g. "claude-opus-5" or "claude-sonnet-4.6".
	ID string
	// MaxInputTokens is the advertised context window.
	MaxInputTokens int
	// EffortEnum lists the accepted output_config.effort values, low → high.
	// Empty means the model does not support effort.
	EffortEnum []string
}

// catalog holds mappings derived from Kiro's model catalog. It is consulted
// *after* the built-in tables, so built-ins always win and discovery only fills
// gaps — models Kiro has launched that this build has no entry for. That
// ordering matters: the built-in rows encode Claude Code-specific behaviour
// (which `[1m]` aliases must not enable thinking, which SKU a 1M request routes
// to) that a mechanically derived row cannot reproduce.
var catalog struct {
	mu       sync.RWMutex
	mappings []Mapping
	efforts  map[string][]string
}

// SetCatalog installs a discovered model catalog and returns the Kiro SKUs that
// the built-in table did not already cover, for logging. Non-Claude models are
// ignored: kirocc speaks the Anthropic Messages API, and its fallback for a
// non-claude request is DefaultModel.
func SetCatalog(discovered []CatalogModel) []string {
	var (
		mappings []Mapping
		added    []string
	)
	efforts := make(map[string][]string, len(discovered))

	builtin := make(map[string]struct{}, len(modelMapOrdered))
	for _, m := range modelMapOrdered {
		builtin[m.Kiro] = struct{}{}
	}

	for _, d := range discovered {
		if !strings.HasPrefix(d.ID, "claude-") {
			continue
		}
		if len(d.EffortEnum) > 0 {
			efforts[d.ID] = d.EffortEnum
		}
		if _, ok := builtin[d.ID]; !ok {
			added = append(added, d.ID)
		}

		alias := anthropicAlias(d.ID)
		entry := Mapping{Anthropic: alias, Kiro: d.ID, ContextWindowSize: d.MaxInputTokens}
		if d.MaxInputTokens >= ThinkingContextWindowSize {
			// An always-1M SKU: no separate -1m variant exists, so Kiro1M is the
			// SKU itself and the `[1m]` alias is a context-window advertisement
			// that must not turn thinking on (matching the built-in opus rows).
			// It carries a display name so the picker labels it like a built-in
			// 1M entry instead of showing a bare suffixed ID; there is no
			// friendly name for a model this build predates, so the ID stands in.
			entry.Kiro1M = d.ID
			mappings = append(mappings, Mapping{
				Anthropic:         alias + ThinkingSuffix,
				Kiro:              d.ID,
				Kiro1M:            d.ID,
				ContextWindowSize: d.MaxInputTokens,
				DisplayName:       alias,
			})
		}
		mappings = append(mappings, entry)
	}

	catalog.mu.Lock()
	catalog.mappings = mappings
	catalog.efforts = efforts
	catalog.mu.Unlock()
	return added
}

// anthropicAlias converts a Kiro SKU to the Anthropic-form ID clients send,
// which spells version separators with hyphens: "claude-opus-4.6" →
// "claude-opus-4-6". IDs without a dot ("claude-opus-5") are already in
// Anthropic form and pass through unchanged.
func anthropicAlias(kiroModel string) string {
	return strings.ReplaceAll(kiroModel, ".", "-")
}

// catalogMappings returns the discovered mappings, or nil when discovery has not
// run or found nothing usable.
func catalogMappings() []Mapping {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	return catalog.mappings
}

// catalogEffortEnum returns the discovered effort enum for a Kiro SKU.
func catalogEffortEnum(kiroModel string) ([]string, bool) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	enum, ok := catalog.efforts[kiroModel]
	return enum, ok
}
