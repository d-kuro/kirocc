package server

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/websearch"
)

// The side query Claude Code issues for one WebSearch call: a single user
// message, the server-tool definition, and nothing else.
const webSearchRequest = `{
	"model":"claude-sonnet-4-6",
	"max_tokens":1024,
	"messages":[{"role":"user","content":"Perform a web search for the query: weather in Selters"}],
	"tool_choice":{"type":"tool","name":"web_search"},
	"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}]
}`

// stubSearch is a provider that records its queries and returns scripted hits.
type stubSearch struct {
	queries []string
	results []websearch.Result
	err     error
}

func (s *stubSearch) Name() string { return "stub" }

func (s *stubSearch) Search(_ context.Context, q websearch.Query) ([]websearch.Result, error) {
	s.queries = append(s.queries, q.Text)
	return s.results, s.err
}

func newE2EServerWithWebSearch(t *testing.T, client kiroclient.Client, p websearch.Provider) *httptest.Server {
	t.Helper()
	mgr := &mockAuthManager{
		creds: &auth.Credentials{
			AccessToken: "test-token",
			ProfileARN:  "arn:test",
			Region:      "us-east-1",
		},
	}
	s := New(mgr, "", client, WithCapture(true), WithWebSearch(p, 3))
	return newTCP4TestServer(t, s.Handler())
}

// webSearchCallEvents is an executor round that calls the synthetic web search
// tool with a real query.
func webSearchCallEvents(query string) []any {
	return []any{
		"toolUseEvent", mustJSON(map[string]any{
			"name": "web_search", "toolUseId": "ws-call-1",
			"input": `{"query":"` + query + `"}`, "stop": true,
		}),
	}
}

// The full loop: executor calls web_search → the proxy runs the search → the
// executor answers with the results in context. The client sees
// server_tool_use + web_search_tool_result, which is what Claude Code reads.
func TestE2E_WebSearch_NonStreaming(t *testing.T) {
	provider := &stubSearch{results: []websearch.Result{
		{Title: "Selters weather", URL: "https://example.com/w", Snippet: "12 °C", PageAge: "2 hours ago"},
	}}
	client := &multiResponseClient{responses: [][]any{
		webSearchCallEvents("weather in Selters"),
		textEvents("12 °C and sunny. Source: https://example.com/w"),
	}}
	srv := newE2EServerWithWebSearch(t, client, provider)
	defer srv.Close()

	resp := postMessages(t, srv.URL, webSearchRequest)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	result := decodeResponse(t, resp)

	if client.callCount != 2 {
		t.Fatalf("upstream calls = %d, want 2 (executor, executor with results)", client.callCount)
	}
	if len(provider.queries) != 1 || provider.queries[0] != "weather in Selters" {
		t.Fatalf("provider queries = %v, want the model's query", provider.queries)
	}

	// The client's definition must not reach Kiro as a callable tool; the
	// synthetic one with a query schema takes its place.
	mctx := client.payloads[0].ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if mctx == nil || len(mctx.Tools) != 1 {
		t.Fatalf("first payload tools = %+v, want exactly the synthetic web_search", mctx)
	}
	spec := mctx.Tools[0].ToolSpecification
	if spec == nil || spec.Name != "web_search" {
		t.Fatalf("tool = %+v, want web_search", spec)
	}
	if props, _ := spec.InputSchema.JSON["properties"].(map[string]any); props["query"] == nil {
		t.Errorf("tool schema = %+v, want a query property", spec.InputSchema.JSON)
	}

	// Block sequence: server_tool_use(web_search) → web_search_tool_result → text.
	content, _ := result["content"].([]any)
	var types []string
	var searchResult map[string]any
	for _, b := range content {
		block := b.(map[string]any)
		types = append(types, block["type"].(string))
		if block["type"] == "web_search_tool_result" {
			searchResult = block
		}
	}
	if !strings.Contains(strings.Join(types, ","), "server_tool_use,web_search_tool_result") {
		t.Fatalf("content types = %v, want server_tool_use followed by web_search_tool_result", types)
	}
	// The content is an array — that is what tells a client the search
	// succeeded, an object means an error.
	hits, ok := searchResult["content"].([]any)
	if !ok || len(hits) != 1 {
		t.Fatalf("result content = %+v, want an array of one hit", searchResult["content"])
	}
	hit := hits[0].(map[string]any)
	if hit["type"] != "web_search_result" || hit["url"] != "https://example.com/w" {
		t.Errorf("hit = %+v, want a web_search_result carrying the URL", hit)
	}
	if hit["title"] != "Selters weather" {
		t.Errorf("hit title = %v, want the page title", hit["title"])
	}

	// The second round has to carry the results, or the executor answers from
	// nothing.
	if got := historyText(client.payloads[1]); !strings.Contains(got, "https://example.com/w") {
		t.Errorf("second payload history does not carry the result URL:\n%s", got)
	}
}

func TestE2E_WebSearch_Streaming(t *testing.T) {
	provider := &stubSearch{results: []websearch.Result{
		{Title: "Selters weather", URL: "https://example.com/w"},
	}}
	client := &multiResponseClient{responses: [][]any{
		webSearchCallEvents("weather in Selters"),
		textEvents("12 °C and sunny."),
	}}
	srv := newE2EServerWithWebSearch(t, client, provider)
	defer srv.Close()

	resp := postMessages(t, srv.URL, strings.Replace(webSearchRequest, `"max_tokens":1024,`, `"max_tokens":1024,"stream":true,`, 1))
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	sse := string(body)

	for _, want := range []string{
		`"type":"server_tool_use"`,
		`"name":"web_search"`,
		`"type":"web_search_tool_result"`,
		`"type":"web_search_result"`,
		"https://example.com/w",
	} {
		if !strings.Contains(sse, want) {
			t.Errorf("SSE body is missing %s:\n%s", want, sse)
		}
	}
	// A plain tool_use block for web_search is the bug: Claude Code ignores it
	// and reports an empty result list.
	if strings.Contains(sse, `"type":"tool_use"`) {
		t.Errorf("SSE body carries a client tool_use block for the search:\n%s", sse)
	}
	if got := concatTextDeltas(t, sse); got != "12 °C and sunny." {
		t.Errorf("text = %q, want the final answer", got)
	}
}

// A provider failure is reported as an error block, not an HTTP error: the
// executor can then say the search failed instead of inventing an answer.
func TestE2E_WebSearch_ProviderFailure(t *testing.T) {
	provider := &stubSearch{err: websearch.ErrTooManyRequests}
	client := &multiResponseClient{responses: [][]any{
		webSearchCallEvents("weather in Selters"),
		textEvents("The search was rate limited."),
	}}
	srv := newE2EServerWithWebSearch(t, client, provider)
	defer srv.Close()

	resp := postMessages(t, srv.URL, webSearchRequest)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	result := decodeResponse(t, resp)

	content, _ := result["content"].([]any)
	var inner map[string]any
	for _, b := range content {
		block := b.(map[string]any)
		if block["type"] == "web_search_tool_result" {
			inner, _ = block["content"].(map[string]any)
		}
	}
	if inner == nil {
		t.Fatalf("content = %+v, want a web_search_tool_result with an error object", content)
	}
	if inner["type"] != "web_search_tool_result_error" || inner["error_code"] != "too_many_requests" {
		t.Errorf("error block = %+v, want too_many_requests", inner)
	}
	if got := historyText(client.payloads[1]); !strings.Contains(got, "too_many_requests") {
		t.Errorf("second payload does not carry the failure:\n%s", got)
	}
}

// max_uses is the client's budget and has to hold: the second search in one
// request is refused without calling the provider again.
func TestE2E_WebSearch_MaxUses(t *testing.T) {
	provider := &stubSearch{results: []websearch.Result{{Title: "w", URL: "https://example.com/w"}}}
	client := &multiResponseClient{responses: [][]any{
		webSearchCallEvents("first query"),
		webSearchCallEvents("second query"),
		textEvents("done"),
	}}
	srv := newE2EServerWithWebSearch(t, client, provider)
	defer srv.Close()

	req := strings.Replace(webSearchRequest, `"max_uses":8`, `"max_uses":1`, 1)
	resp := postMessages(t, srv.URL, req)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 200)
	result := decodeResponse(t, resp)

	if len(provider.queries) != 1 {
		t.Errorf("provider called %d times, want 1 (max_uses)", len(provider.queries))
	}
	var codes []string
	content, _ := result["content"].([]any)
	for _, b := range content {
		block := b.(map[string]any)
		if block["type"] != "web_search_tool_result" {
			continue
		}
		if inner, ok := block["content"].(map[string]any); ok {
			codes = append(codes, inner["error_code"].(string))
		}
	}
	if len(codes) != 1 || codes[0] != "max_uses_exceeded" {
		t.Errorf("error codes = %v, want one max_uses_exceeded", codes)
	}
}

// With no provider configured the request is refused. Answering it without the
// search is the trap: the client reports zero results and no error, and the
// upstream call is billed for nothing.
func TestE2E_WebSearch_NoProviderIsRefused(t *testing.T) {
	client := &capturingClient{events: textEvents("should never run")}
	srv := newE2EServer(t, client)
	defer srv.Close()

	resp := postMessages(t, srv.URL, webSearchRequest)
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, 400)

	body, _ := io.ReadAll(resp.Body)
	msg := string(body)
	if !strings.Contains(msg, "web_search_20250305") || !strings.Contains(msg, "web-search-provider") {
		t.Errorf("error = %s, want it to name the tool and the flag that enables it", msg)
	}
	if client.captured != nil {
		t.Error("the request reached Kiro, want it refused before any upstream call")
	}
}
