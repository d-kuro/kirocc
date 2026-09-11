package websearch

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Provider identifiers accepted by Config.Provider.
const (
	ProviderBrave  = "brave"
	ProviderTavily = "tavily"
	ProviderExa    = "exa"
	ProviderCustom = "custom"
)

// Providers lists the identifiers New accepts, for flag help and validation
// messages.
var Providers = []string{ProviderBrave, ProviderTavily, ProviderExa, ProviderCustom}

// DefaultTimeout bounds one provider call. A search the executor model is
// waiting on must fail fast rather than hold the stream open.
const DefaultTimeout = 15 * time.Second

// maxBodyBytes bounds a provider response. Generous for a result list, and a
// hard stop on a provider that streams something unexpected.
const maxBodyBytes = 4 << 20

// endpoints are the provider APIs, held in a struct rather than inline so a
// test can point a provider at a local server.
type endpoints struct {
	brave  string
	tavily string
	exa    string
}

var defaultEndpoints = endpoints{
	brave:  "https://api.search.brave.com/res/v1/web/search",
	tavily: "https://api.tavily.com/search",
	exa:    "https://api.exa.ai/search",
}

// Config selects and configures the search provider. An empty Provider means
// web search is disabled, which is the default: the feature sends queries to a
// third party, so it is opt-in.
type Config struct {
	Provider string
	APIKey   string
	// URL is the endpoint for the custom provider, ignored by the others.
	URL string
	// MaxResults caps results per search; 0 means DefaultMaxResults.
	MaxResults int
	// Timeout bounds one provider call; 0 means DefaultTimeout.
	Timeout time.Duration
}

// Enabled reports whether a provider is configured.
func (c Config) Enabled() bool { return c.Provider != "" }

// Validate checks the config is usable, so a typo is reported at startup
// rather than as a failed search halfway through a session.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	switch c.Provider {
	case ProviderBrave, ProviderTavily, ProviderExa:
		if c.APIKey == "" {
			return fmt.Errorf("web-search-api-key is required for provider %q", c.Provider)
		}
	case ProviderCustom:
		if c.URL == "" {
			return fmt.Errorf("web-search-url is required for provider %q", c.Provider)
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
			return fmt.Errorf("web-search-url must be an http(s) URL, got %q", c.URL)
		}
	default:
		return fmt.Errorf("unknown web-search-provider %q (want one of %s)",
			c.Provider, strings.Join(Providers, ", "))
	}
	if c.MaxResults < 0 {
		return fmt.Errorf("web-search-max-results must be >= 0, got %d", c.MaxResults)
	}
	return nil
}

// New builds the provider named by the config. It returns nil, nil when web
// search is disabled, so the caller can pass the result straight to NewContext.
func New(cfg Config) (Provider, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	p := &httpProvider{
		name:   cfg.Provider,
		apiKey: cfg.APIKey,
		url:    cfg.URL,
		eps:    defaultEndpoints,
		client: &http.Client{Timeout: timeout},
	}
	return p, nil
}

// httpProvider talks to one of the supported search APIs. They differ only in
// endpoint, auth header and response shape, so one type with a switch is
// clearer than four near-identical ones.
type httpProvider struct {
	name   string
	apiKey string
	url    string
	eps    endpoints
	client *http.Client
}

func (p *httpProvider) Name() string { return p.name }

func (p *httpProvider) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.MaxResults <= 0 {
		q.MaxResults = DefaultMaxResults
	}
	req, err := p.buildRequest(ctx, q)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, p.name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: reading response: %w", ErrUnavailable, p.name, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: %s", ErrTooManyRequests, p.name)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %s: status %d: %s",
			ErrUnavailable, p.name, resp.StatusCode, firstLine(body))
	}
	results, err := p.parse(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, p.name, err)
	}
	return results, nil
}

func (p *httpProvider) buildRequest(ctx context.Context, q Query) (*http.Request, error) {
	switch p.name {
	case ProviderBrave:
		// Brave takes the query in the URL and has no domain parameters, so the
		// definition's filters are applied to the response instead.
		u := p.eps.brave + "?q=" + url.QueryEscape(q.Text) +
			"&count=" + strconv.Itoa(q.MaxResults)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, p.name, err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Subscription-Token", p.apiKey)
		return req, nil

	case ProviderTavily:
		payload := map[string]any{"query": q.Text, "max_results": q.MaxResults}
		if len(q.AllowedDomains) > 0 {
			payload["include_domains"] = q.AllowedDomains
		}
		if len(q.BlockedDomains) > 0 {
			payload["exclude_domains"] = q.BlockedDomains
		}
		return p.jsonRequest(ctx, p.eps.tavily, payload, func(h http.Header) {
			h.Set("Authorization", "Bearer "+p.apiKey)
		})

	case ProviderExa:
		payload := map[string]any{
			"query":      q.Text,
			"numResults": q.MaxResults,
			"contents":   map[string]any{"text": map[string]any{"maxCharacters": 600}},
		}
		if len(q.AllowedDomains) > 0 {
			payload["includeDomains"] = q.AllowedDomains
		}
		if len(q.BlockedDomains) > 0 {
			payload["excludeDomains"] = q.BlockedDomains
		}
		return p.jsonRequest(ctx, p.eps.exa, payload, func(h http.Header) {
			h.Set("x-api-key", p.apiKey)
		})

	case ProviderCustom:
		// Same contract as the built-in web search server Claude Desktop talks
		// to: POST {q}, answer with a results[] array. That makes an existing
		// endpoint reusable here without a shim.
		payload := map[string]any{"q": q.Text, "max_results": q.MaxResults}
		if len(q.AllowedDomains) > 0 {
			payload["allowed_domains"] = q.AllowedDomains
		}
		if len(q.BlockedDomains) > 0 {
			payload["blocked_domains"] = q.BlockedDomains
		}
		return p.jsonRequest(ctx, p.url, payload, func(h http.Header) {
			if p.apiKey != "" {
				h.Set("Authorization", "Bearer "+p.apiKey)
			}
		})
	}
	return nil, fmt.Errorf("%w: unknown provider %q", ErrUnavailable, p.name)
}

func (p *httpProvider) jsonRequest(ctx context.Context, endpoint string, payload map[string]any, auth func(http.Header)) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, p.name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	auth(req.Header)
	return req, nil
}

// parse maps a provider response onto Results. Every provider is read
// leniently: unknown fields are ignored and an entry without a URL is dropped,
// since a result the client cannot cite is worse than one fewer result.
func (p *httpProvider) parse(body []byte) ([]Result, error) {
	switch p.name {
	case ProviderBrave:
		var parsed struct {
			Web struct {
				Results []struct {
					Title       string `json:"title"`
					URL         string `json:"url"`
					Description string `json:"description"`
					Age         string `json:"age"`
				} `json:"results"`
			} `json:"web"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		out := make([]Result, 0, len(parsed.Web.Results))
		for _, r := range parsed.Web.Results {
			out = appendResult(out, r.Title, r.URL, r.Description, r.Age)
		}
		return out, nil

	case ProviderTavily:
		var parsed struct {
			Results []struct {
				Title         string `json:"title"`
				URL           string `json:"url"`
				Content       string `json:"content"`
				PublishedDate string `json:"published_date"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		out := make([]Result, 0, len(parsed.Results))
		for _, r := range parsed.Results {
			out = appendResult(out, r.Title, r.URL, r.Content, r.PublishedDate)
		}
		return out, nil

	case ProviderExa:
		var parsed struct {
			Results []struct {
				Title         string `json:"title"`
				URL           string `json:"url"`
				Text          string `json:"text"`
				PublishedDate string `json:"publishedDate"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		out := make([]Result, 0, len(parsed.Results))
		for _, r := range parsed.Results {
			out = appendResult(out, r.Title, r.URL, r.Text, r.PublishedDate)
		}
		return out, nil

	case ProviderCustom:
		// Accept the field spellings the other providers use, so a hand-rolled
		// endpoint does not have to guess which one this expects.
		var parsed struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Snippet     string `json:"snippet"`
				Content     string `json:"content"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
				Age         string `json:"age"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		out := make([]Result, 0, len(parsed.Results))
		for _, r := range parsed.Results {
			snippet := firstNonEmpty(r.Snippet, r.Content, r.Description)
			out = appendResult(out, r.Title, r.URL, snippet, firstNonEmpty(r.PageAge, r.Age))
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown provider %q", p.name)
}

func appendResult(out []Result, title, rawURL, snippet, age string) []Result {
	rawURL = strings.TrimSpace(rawURL)
	// Only an absolute http(s) URL is citable, and citing the result is the
	// point — anything else is dropped rather than passed on.
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return out
	}
	if title == "" {
		title = rawURL
	}
	return append(out, Result{
		Title:   collapseSpace(title),
		URL:     rawURL,
		Snippet: collapseSpace(snippet),
		PageAge: strings.TrimSpace(age),
	})
}

// maxSnippetLen bounds what a provider's excerpt contributes to the result
// block. Exa in particular returns page text, which would otherwise dominate
// the conversation it is replayed into.
const maxSnippetLen = 500

func collapseSpace(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxSnippetLen {
		s = strings.TrimSpace(s[:maxSnippetLen]) + "…"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstLine(body []byte) string {
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
