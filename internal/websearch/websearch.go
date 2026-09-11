// Package websearch emulates Anthropic's web_search_20250305 server-side tool
// inside the proxy. Kiro has no web search of its own, so a client-supplied
// web_search definition cannot be forwarded upstream: if it is sent as an
// ordinary function tool, the executor model calls it and the client receives a
// tool_use block it has no way to answer — which is how Claude Code's WebSearch
// ends up reporting zero results without an error.
//
// Instead the definition is intercepted, the executor model is offered a
// synthetic Kiro-side tool with a real query parameter, and the search itself
// runs here against a configured provider. The result reaches the client as the
// server_tool_use + web_search_tool_result pair the Anthropic API would send.
package websearch

import (
	"context"
	"errors"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

// KiroToolName is the name of the synthetic Kiro-side web search tool. It
// matches the Anthropic tool name, so a system prompt that refers to web_search
// (as Claude Code's side query does) stays coherent.
const KiroToolName = anthropic.WebSearchToolName

// DefaultMaxUses caps searches per request when the client does not specify
// max_uses. Each search is an outbound HTTP call to the provider, billed by
// them, so the default is deliberately small.
const DefaultMaxUses = 5

// DefaultMaxResults is how many results a search returns when the provider
// config does not say otherwise. Enough to answer a question, small enough that
// the result block stays cheap to carry in history.
const DefaultMaxResults = 5

// maxQueryLen bounds the query the executor model may send. The real API
// reports query_too_long rather than truncating, and so does this.
const maxQueryLen = 400

// Errors a provider may return. They map onto the error codes the Anthropic API
// puts in a web_search_tool_result_error block, so the client sees the same
// vocabulary it would from the real thing.
var (
	ErrUnavailable     = errors.New("web search unavailable")
	ErrTooManyRequests = errors.New("web search rate limited")
	ErrQueryTooLong    = errors.New("web search query too long")
	ErrInvalidInput    = errors.New("web search input invalid")
)

// ErrorCode maps a search failure to its web_search_tool_result_error code.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrTooManyRequests):
		return anthropic.WebSearchErrorTooManyRequests
	case errors.Is(err, ErrQueryTooLong):
		return anthropic.WebSearchErrorQueryTooLong
	case errors.Is(err, ErrInvalidInput):
		return anthropic.WebSearchErrorInvalidToolInput
	default:
		return anthropic.WebSearchErrorUnavailable
	}
}

// Result is one search hit. Title and URL are what the Anthropic API returns
// and all a client is guaranteed to read; Snippet and PageAge are carried too
// because they cost nothing and make the replayed history readable.
type Result struct {
	Title   string
	URL     string
	Snippet string
	PageAge string
}

// Query is one search request as it reaches a provider. The domain lists come
// from the tool definition, never from the model.
type Query struct {
	Text           string
	AllowedDomains []string
	BlockedDomains []string
	MaxResults     int
}

// Provider runs a search outside this process.
type Provider interface {
	// Name identifies the provider in logs.
	Name() string
	// Search returns results ordered by relevance. It must return one of the
	// package's sentinel errors (wrapped is fine) so the failure maps to an
	// error code the client understands.
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Context holds the per-request state for web search emulation. It is nil when
// the request carries no web_search definition.
type Context struct {
	// ToolName is the client-facing tool name from the definition, which is
	// what the server_tool_use block must be named.
	ToolName string
	// MaxUses caps searches for this request.
	MaxUses int
	// AllowedDomains and BlockedDomains are the definition's filters. The API
	// rejects a definition setting both; so does NewContext, via
	// PreflightError.
	AllowedDomains []string
	BlockedDomains []string
	// PreflightError, when non-empty, is the error code to report for every
	// search without calling the provider. A malformed definition must not fail
	// the request — the executor sees a web_search_tool_result_error, which is
	// how the real API surfaces it.
	PreflightError string

	provider   Provider
	maxResults int
	uses       int
}

// NewContext builds the web search context from the request's tool list.
// Returns nil when there is no web_search definition, or when no provider is
// configured — with no provider there is nothing to emulate, and the caller
// rejects the request instead.
func NewContext(tools []anthropic.Tool, p Provider, maxResults int) *Context {
	if p == nil {
		return nil
	}
	def := anthropic.FindWebSearchTool(tools)
	if def == nil {
		return nil
	}

	name := def.Name
	if name == "" {
		name = anthropic.WebSearchToolName
	}
	maxUses := def.MaxUses
	if maxUses <= 0 {
		maxUses = DefaultMaxUses
	}
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}

	c := &Context{
		ToolName:       name,
		MaxUses:        maxUses,
		AllowedDomains: def.AllowedDomains,
		BlockedDomains: def.BlockedDomains,
		provider:       p,
		maxResults:     maxResults,
	}
	if len(def.AllowedDomains) > 0 && len(def.BlockedDomains) > 0 {
		c.PreflightError = anthropic.WebSearchErrorInvalidToolInput
	}
	return c
}

// ProviderName identifies the configured provider, for logs.
func (c *Context) ProviderName() string {
	if c == nil || c.provider == nil {
		return ""
	}
	return c.provider.Name()
}

// Uses returns the number of searches performed so far.
func (c *Context) Uses() int { return c.uses }

// Consume records one search. It returns false when max_uses is already
// exhausted, in which case the caller must emit max_uses_exceeded.
func (c *Context) Consume() bool {
	if c.uses >= c.MaxUses {
		return false
	}
	c.uses++
	return true
}

// Search runs one search through the provider and applies the definition's
// domain filters to what comes back. The filters are applied here even when the
// provider was also asked to apply them: a provider that ignores the hint must
// not be able to return a host the client excluded.
func (c *Context) Search(ctx context.Context, query string) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidInput
	}
	if len(query) > maxQueryLen {
		return nil, ErrQueryTooLong
	}
	results, err := c.provider.Search(ctx, Query{
		Text:           query,
		AllowedDomains: c.AllowedDomains,
		BlockedDomains: c.BlockedDomains,
		MaxResults:     c.maxResults,
	})
	if err != nil {
		return nil, err
	}
	results = FilterDomains(results, c.AllowedDomains, c.BlockedDomains)
	if len(results) > c.maxResults {
		results = results[:c.maxResults]
	}
	return results, nil
}

const toolDescription = `Searches the web and returns the matching pages.

Pass the user's question as the query, in natural language. The search runs outside this conversation and returns page titles with their URLs, so cite the URLs you use. Any domain restrictions the client set are applied for you.`

// KiroToolEntry returns the synthetic Kiro tool the executor model calls to run
// a search. Unlike the client's definition it carries a real schema: a
// server-side tool definition has no input_schema at all, and forwarding that
// as-is leaves the model calling web_search with an empty input — the query
// never reaches the proxy.
func (c *Context) KiroToolEntry() kiroproto.ToolEntry {
	return kiroproto.ToolEntry{
		ToolSpecification: &kiroproto.ToolSpecification{
			Name:        KiroToolName,
			Description: toolDescription,
			InputSchema: kiroproto.InputSchema{
				JSON: map[string]any{
					"type":     "object",
					"required": []any{"query"},
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "The search query, in natural language.",
						},
					},
				},
			},
		},
	}
}

// FilterDomains drops results outside allowed (when set) and inside blocked.
// Matching is on the host: an entry matches a result whose host equals it or is
// a subdomain of it, so "example.com" covers "www.example.com" but not
// "notexample.com".
func FilterDomains(results []Result, allowed, blocked []string) []Result {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}
	out := make([]Result, 0, len(results))
	for _, r := range results {
		host := resultHost(r.URL)
		if host == "" {
			continue
		}
		if len(allowed) > 0 && !hostMatchesAny(host, allowed) {
			continue
		}
		if len(blocked) > 0 && hostMatchesAny(host, blocked) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// resultHost extracts the lowercase host from a result URL without parsing it
// fully: providers occasionally return URLs that net/url rejects, and one bad
// entry must not drop the whole answer. It returns "" for anything that cannot
// be a host, which the domain filters treat as unmatchable.
func resultHost(rawURL string) string {
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if strings.ContainsAny(s, " \t") || !strings.Contains(s, ".") {
		return ""
	}
	return s
}

func hostMatchesAny(host string, domains []string) bool {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "*.")))
		d = strings.TrimSuffix(d, ".")
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
