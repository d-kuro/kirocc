package toolsearch

import (
	"github.com/d-kuro/kirocc/internal/anthropic"
)

const (
	SearchTypeRegex = "regex"
	SearchTypeBM25  = "bm25"
)

// Context holds the state for tool search: which tools are deferred and which are active.
type Context struct {
	SearchType     string                    // "regex" or "bm25"
	SearchToolName string                    // client-specified name (e.g. "tool_search_tool_regex")
	DeferredTools  map[string]anthropic.Tool // name → full definition
	ActiveTools    []anthropic.Tool          // non-deferred tools (grows dynamically)
}

// NewContext inspects the tool list for a tool_search_tool_* entry and partitions
// tools into deferred vs active. Returns nil if no search tool is found.
func NewContext(tools []anthropic.Tool) *Context {
	var ctx *Context

	for _, t := range tools {
		if !t.IsToolSearchTool() {
			continue
		}
		st := SearchTypeRegex
		if t.Type == anthropic.ToolTypeSearchBM25 {
			st = SearchTypeBM25
		}
		ctx = &Context{
			SearchType:     st,
			SearchToolName: t.Name,
			DeferredTools:  make(map[string]anthropic.Tool),
		}
		break
	}

	if ctx == nil {
		return nil
	}

	for _, t := range tools {
		if t.IsToolSearchTool() {
			continue
		}
		if t.DeferLoading {
			ctx.DeferredTools[t.Name] = t
		} else {
			ctx.ActiveTools = append(ctx.ActiveTools, t)
		}
	}

	return ctx
}

// PromoteTools moves named tools from DeferredTools to ActiveTools.
func (c *Context) PromoteTools(names []string) {
	for _, name := range names {
		if t, ok := c.DeferredTools[name]; ok {
			c.ActiveTools = append(c.ActiveTools, t)
			delete(c.DeferredTools, name)
		}
	}
}

// ToolRefMaps builds tool_reference map entries from a list of tool names.
func ToolRefMaps(names []string) []map[string]any {
	refs := make([]map[string]any, len(names))
	for i, name := range names {
		refs[i] = map[string]any{"type": anthropic.BlockTypeToolReference, "tool_name": name}
	}
	return refs
}
