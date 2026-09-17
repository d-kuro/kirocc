package websearch

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingServer answers every request with body and records what it received.
type recordingServer struct {
	srv    *httptest.Server
	path   string
	query  string
	header http.Header
	body   map[string]any
}

func newRecordingServer(t *testing.T, status int, response string) *recordingServer {
	t.Helper()
	rec := &recordingServer{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.query = r.URL.RawQuery
		rec.header = r.Header.Clone()
		if b, err := io.ReadAll(r.Body); err == nil && len(b) > 0 {
			_ = json.Unmarshal(b, &rec.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// provider builds an httpProvider pointed at the recording server.
func (rec *recordingServer) provider(name, apiKey string) *httpProvider {
	p := &httpProvider{
		name:   name,
		apiKey: apiKey,
		url:    rec.srv.URL,
		client: rec.srv.Client(),
		eps:    endpoints{brave: rec.srv.URL, tavily: rec.srv.URL, exa: rec.srv.URL},
	}
	return p
}

func TestBraveSearch(t *testing.T) {
	rec := newRecordingServer(t, 200, `{"web":{"results":[
		{"title":"Selters weather","url":"https://example.com/w","description":"  12  °C  ","age":"2 hours ago"},
		{"title":"no url","url":""}
	]}}`)
	p := rec.provider(ProviderBrave, "brave-key")

	results, err := p.Search(context.Background(), Query{Text: "weather selters", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := rec.header.Get("X-Subscription-Token"); got != "brave-key" {
		t.Errorf("X-Subscription-Token = %q, want brave-key", got)
	}
	if !strings.Contains(rec.query, "q=weather+selters") || !strings.Contains(rec.query, "count=3") {
		t.Errorf("query = %q, want the query and count", rec.query)
	}
	// The entry without a URL is dropped: a result the client cannot cite is
	// worse than one fewer result.
	if len(results) != 1 {
		t.Fatalf("results = %+v, want 1", results)
	}
	if results[0].Title != "Selters weather" || results[0].URL != "https://example.com/w" {
		t.Errorf("result = %+v, want the titled hit", results[0])
	}
	if results[0].Snippet != "12 °C" {
		t.Errorf("snippet = %q, want whitespace collapsed", results[0].Snippet)
	}
	if results[0].PageAge != "2 hours ago" {
		t.Errorf("page age = %q, want the age field", results[0].PageAge)
	}
}

func TestTavilySearchSendsDomains(t *testing.T) {
	rec := newRecordingServer(t, 200, `{"results":[
		{"title":"t","url":"https://example.com/t","content":"body","published_date":"2026-09-01"}
	]}`)
	p := rec.provider(ProviderTavily, "tvly-key")

	results, err := p.Search(context.Background(), Query{
		Text:           "q",
		MaxResults:     4,
		AllowedDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := rec.header.Get("Authorization"); got != "Bearer tvly-key" {
		t.Errorf("Authorization = %q, want a bearer key", got)
	}
	if rec.body["query"] != "q" {
		t.Errorf("payload query = %v, want q", rec.body["query"])
	}
	if _, ok := rec.body["include_domains"]; !ok {
		t.Errorf("payload = %+v, want include_domains", rec.body)
	}
	if _, ok := rec.body["exclude_domains"]; ok {
		t.Errorf("payload = %+v, want no exclude_domains when none were set", rec.body)
	}
	if len(results) != 1 || results[0].Snippet != "body" {
		t.Errorf("results = %+v, want one hit with its content as the snippet", results)
	}
}

func TestExaSearchTruncatesPageText(t *testing.T) {
	long := strings.Repeat("x", maxSnippetLen+50)
	rec := newRecordingServer(t, 200, `{"results":[
		{"title":"e","url":"https://example.com/e","text":"`+long+`","publishedDate":"2026-01-01"}
	]}`)
	p := rec.provider(ProviderExa, "exa-key")

	results, err := p.Search(context.Background(), Query{Text: "q", MaxResults: 2, BlockedDomains: []string{"spam.example"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := rec.header.Get("x-api-key"); got != "exa-key" {
		t.Errorf("x-api-key = %q, want exa-key", got)
	}
	if rec.body["numResults"] != float64(2) {
		t.Errorf("payload numResults = %v, want 2", rec.body["numResults"])
	}
	if _, ok := rec.body["excludeDomains"]; !ok {
		t.Errorf("payload = %+v, want excludeDomains", rec.body)
	}
	// Exa returns page text, which would otherwise dominate the conversation
	// the result is replayed into.
	if len(results) != 1 || len(results[0].Snippet) > maxSnippetLen+len("…") {
		t.Errorf("snippet length = %d, want it truncated to %d", len(results[0].Snippet), maxSnippetLen)
	}
}

func TestCustomProviderAcceptsAnyResultSpelling(t *testing.T) {
	rec := newRecordingServer(t, 200, `{"results":[
		{"title":"a","url":"https://example.com/a","snippet":"s"},
		{"title":"b","url":"https://example.com/b","content":"c"},
		{"title":"c","url":"https://example.com/c","description":"d","age":"1 day"}
	]}`)
	p := rec.provider(ProviderCustom, "")

	results, err := p.Search(context.Background(), Query{Text: "q", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rec.body["q"] != "q" {
		t.Errorf("payload = %+v, want the query under q (the desktop web search contract)", rec.body)
	}
	if rec.header.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want none when no key is configured", rec.header.Get("Authorization"))
	}
	want := []string{"s", "c", "d"}
	if len(results) != 3 {
		t.Fatalf("results = %+v, want 3", results)
	}
	for i, r := range results {
		if r.Snippet != want[i] {
			t.Errorf("result %d snippet = %q, want %q", i, r.Snippet, want[i])
		}
	}
	if results[2].PageAge != "1 day" {
		t.Errorf("page age = %q, want the age field", results[2].PageAge)
	}
}

func TestSearchErrorMapping(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		rec := newRecordingServer(t, http.StatusTooManyRequests, `{"error":"slow down"}`)
		_, err := rec.provider(ProviderTavily, "k").Search(context.Background(), Query{Text: "q"})
		if !errors.Is(err, ErrTooManyRequests) {
			t.Errorf("err = %v, want ErrTooManyRequests", err)
		}
		if got := ErrorCode(err); got != "too_many_requests" {
			t.Errorf("ErrorCode = %q, want too_many_requests", got)
		}
	})
	t.Run("server error", func(t *testing.T) {
		rec := newRecordingServer(t, http.StatusInternalServerError, "boom")
		_, err := rec.provider(ProviderBrave, "k").Search(context.Background(), Query{Text: "q"})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		rec := newRecordingServer(t, http.StatusUnauthorized, `{"error":"bad key"}`)
		_, err := rec.provider(ProviderExa, "k").Search(context.Background(), Query{Text: "q"})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		// The provider's own message survives, so a bad key is diagnosable
		// from the log rather than only visible as "unavailable".
		if !strings.Contains(err.Error(), "401") {
			t.Errorf("err = %v, want the status in the message", err)
		}
	})
	t.Run("malformed body", func(t *testing.T) {
		rec := newRecordingServer(t, 200, "not json")
		_, err := rec.provider(ProviderTavily, "k").Search(context.Background(), Query{Text: "q"})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"disabled", Config{}, ""},
		{"brave with key", Config{Provider: "brave", APIKey: "k"}, ""},
		{"brave without key", Config{Provider: "brave"}, "web-search-api-key"},
		{"custom with url", Config{Provider: "custom", URL: "https://search.internal/api"}, ""},
		{"custom without url", Config{Provider: "custom"}, "web-search-url"},
		{"custom with bad url", Config{Provider: "custom", URL: "search.internal"}, "http(s) URL"},
		{"unknown provider", Config{Provider: "googol", APIKey: "k"}, "unknown web-search-provider"},
		{"negative max results", Config{Provider: "brave", APIKey: "k", MaxResults: -1}, "web-search-max-results"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// A disabled config yields a nil provider, which NewContext reads as "no
// emulation" — so the nil must be a true nil interface, not a typed one.
func TestNewDisabledReturnsNilProvider(t *testing.T) {
	p, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p != nil {
		t.Errorf("New(disabled) = %v, want nil", p)
	}
	if _, err := New(Config{Provider: "brave"}); err == nil {
		t.Error("New with a missing key = nil error, want a startup error")
	}
}
