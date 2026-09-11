package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// stubProvider records the query it was asked for and returns scripted results.
type stubProvider struct {
	got     Query
	results []Result
	err     error
	calls   int
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Search(_ context.Context, q Query) ([]Result, error) {
	s.calls++
	s.got = q
	return s.results, s.err
}

func webSearchTool() anthropic.Tool {
	return anthropic.Tool{Type: anthropic.ToolTypeWebSearch, Name: "web_search", MaxUses: 8}
}

func TestNewContext(t *testing.T) {
	p := &stubProvider{}

	if got := NewContext([]anthropic.Tool{{Name: "Read"}}, p, 0); got != nil {
		t.Error("NewContext with no web_search definition = non-nil, want nil")
	}
	// Without a provider there is nothing to emulate: the caller rejects the
	// request instead of answering it without the search.
	if got := NewContext([]anthropic.Tool{webSearchTool()}, nil, 0); got != nil {
		t.Error("NewContext with no provider = non-nil, want nil")
	}

	c := NewContext([]anthropic.Tool{{Name: "Read"}, webSearchTool()}, p, 0)
	if c == nil {
		t.Fatal("NewContext = nil, want a context")
	}
	if c.ToolName != "web_search" {
		t.Errorf("ToolName = %q, want web_search", c.ToolName)
	}
	if c.MaxUses != 8 {
		t.Errorf("MaxUses = %d, want 8 (from the definition)", c.MaxUses)
	}
	if c.maxResults != DefaultMaxResults {
		t.Errorf("maxResults = %d, want %d", c.maxResults, DefaultMaxResults)
	}
	if c.PreflightError != "" {
		t.Errorf("PreflightError = %q, want empty", c.PreflightError)
	}
}

func TestNewContextDefaultsAndPreflight(t *testing.T) {
	p := &stubProvider{}

	c := NewContext([]anthropic.Tool{{Type: anthropic.ToolTypeWebSearch}}, p, 0)
	if c.MaxUses != DefaultMaxUses {
		t.Errorf("MaxUses = %d, want default %d", c.MaxUses, DefaultMaxUses)
	}
	if c.ToolName != anthropic.WebSearchToolName {
		t.Errorf("ToolName = %q, want %q", c.ToolName, anthropic.WebSearchToolName)
	}

	// The API rejects a definition that sets both lists; this reports it as a
	// tool result error rather than failing the whole request.
	both := anthropic.Tool{
		Type:           anthropic.ToolTypeWebSearch,
		AllowedDomains: []string{"example.com"},
		BlockedDomains: []string{"spam.example"},
	}
	c = NewContext([]anthropic.Tool{both}, p, 0)
	if c.PreflightError != anthropic.WebSearchErrorInvalidToolInput {
		t.Errorf("PreflightError = %q, want %q", c.PreflightError, anthropic.WebSearchErrorInvalidToolInput)
	}
}

func TestConsumeStopsAtMaxUses(t *testing.T) {
	c := NewContext([]anthropic.Tool{{Type: anthropic.ToolTypeWebSearch, MaxUses: 2}}, &stubProvider{}, 0)
	if !c.Consume() || !c.Consume() {
		t.Fatal("first two Consume calls must succeed")
	}
	if c.Consume() {
		t.Error("third Consume = true, want false once max_uses is spent")
	}
	if c.Uses() != 2 {
		t.Errorf("Uses = %d, want 2", c.Uses())
	}
}

func TestSearchValidatesQuery(t *testing.T) {
	p := &stubProvider{}
	c := NewContext([]anthropic.Tool{webSearchTool()}, p, 0)

	if _, err := c.Search(context.Background(), "   "); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty query err = %v, want ErrInvalidInput", err)
	}
	if _, err := c.Search(context.Background(), strings.Repeat("a", maxQueryLen+1)); !errors.Is(err, ErrQueryTooLong) {
		t.Errorf("long query err = %v, want ErrQueryTooLong", err)
	}
	if p.calls != 0 {
		t.Errorf("provider called %d times for invalid input, want 0", p.calls)
	}
}

func TestSearchPassesFiltersAndCaps(t *testing.T) {
	p := &stubProvider{results: []Result{
		{Title: "a", URL: "https://docs.example.com/a"},
		{Title: "b", URL: "https://other.test/b"},
		{Title: "c", URL: "https://example.com/c"},
	}}
	def := anthropic.Tool{
		Type:           anthropic.ToolTypeWebSearch,
		Name:           "web_search",
		AllowedDomains: []string{"example.com"},
	}
	c := NewContext([]anthropic.Tool{def}, p, 2)

	results, err := c.Search(context.Background(), " weather in Selters ")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if p.got.Text != "weather in Selters" {
		t.Errorf("provider query = %q, want the trimmed query", p.got.Text)
	}
	if len(p.got.AllowedDomains) != 1 || p.got.AllowedDomains[0] != "example.com" {
		t.Errorf("provider AllowedDomains = %v, want [example.com]", p.got.AllowedDomains)
	}
	if p.got.MaxResults != 2 {
		t.Errorf("provider MaxResults = %d, want 2", p.got.MaxResults)
	}
	// other.test is dropped even though the provider returned it: a provider
	// that ignores the domain hint must not be able to smuggle a host past the
	// definition's filter.
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2 (filtered and capped)", results)
	}
	for _, r := range results {
		if strings.Contains(r.URL, "other.test") {
			t.Errorf("result %q survived the allowed_domains filter", r.URL)
		}
	}
}

func TestFilterDomains(t *testing.T) {
	results := []Result{
		{URL: "https://www.example.com/a"},
		{URL: "https://notexample.com/b"},
		{URL: "https://sub.spam.example/c"},
		{URL: "not a url"},
	}

	got := FilterDomains(results, []string{"example.com"}, nil)
	if len(got) != 1 || got[0].URL != "https://www.example.com/a" {
		t.Errorf("allowed=[example.com] gave %+v, want only the www.example.com hit", got)
	}

	got = FilterDomains(results, nil, []string{"spam.example"})
	for _, r := range got {
		if strings.Contains(r.URL, "spam.example") {
			t.Errorf("blocked host survived: %q", r.URL)
		}
	}
	if len(got) != 2 {
		t.Errorf("blocked=[spam.example] gave %d results, want 2 (the unparsable URL is dropped)", len(got))
	}

	if got := FilterDomains(results, nil, nil); len(got) != len(results) {
		t.Errorf("no filters changed the result count: %d, want %d", len(got), len(results))
	}
}

func TestErrorCode(t *testing.T) {
	cases := map[error]string{
		ErrTooManyRequests: anthropic.WebSearchErrorTooManyRequests,
		ErrQueryTooLong:    anthropic.WebSearchErrorQueryTooLong,
		ErrInvalidInput:    anthropic.WebSearchErrorInvalidToolInput,
		ErrUnavailable:     anthropic.WebSearchErrorUnavailable,
		errors.New("boom"): anthropic.WebSearchErrorUnavailable,
	}
	for err, want := range cases {
		if got := ErrorCode(err); got != want {
			t.Errorf("ErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
	// Wrapped sentinels must keep their code — providers wrap with context.
	wrapped := errors.Join(ErrTooManyRequests, errors.New("brave: 429"))
	if got := ErrorCode(wrapped); got != anthropic.WebSearchErrorTooManyRequests {
		t.Errorf("ErrorCode(wrapped) = %q, want too_many_requests", got)
	}
}

// The synthetic Kiro tool must carry a real query parameter: a server-side
// definition has no input_schema, and forwarding that shape leaves the model
// calling web_search with an empty input, losing the query.
func TestKiroToolEntryHasQuerySchema(t *testing.T) {
	c := NewContext([]anthropic.Tool{webSearchTool()}, &stubProvider{}, 0)
	entry := c.KiroToolEntry()
	if entry.ToolSpecification == nil {
		t.Fatal("KiroToolEntry has no tool specification")
	}
	if entry.ToolSpecification.Name != KiroToolName {
		t.Errorf("name = %q, want %q", entry.ToolSpecification.Name, KiroToolName)
	}
	schema := entry.ToolSpecification.InputSchema.JSON
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %+v", schema)
	}
	if _, ok := props["query"]; !ok {
		t.Errorf("schema properties = %+v, want a query property", props)
	}
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "query" {
		t.Errorf("schema required = %v, want [query]", req)
	}
}
