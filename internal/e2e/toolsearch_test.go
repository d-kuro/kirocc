//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/config"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/server"
	"github.com/d-kuro/kirocc/internal/testutil"
	"github.com/d-kuro/kirocc/internal/tokencount"
)

func newRealServer(t *testing.T) string {
	t.Helper()
	authMgr := auth.NewAuthManager(config.DefaultDBPath())
	client := kiroclient.NewHTTPClient(kiroclient.WithTokenCounter(tokencount.CountBytes))
	srv := server.New(authMgr, "", client)
	ts := testutil.NewTCP4TestServer(t, srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func postMessages(t *testing.T, baseURL, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "test-session")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func toolSearchBody(toolType, toolName string, stream bool) string {
	streamStr := "false"
	if stream {
		streamStr = "true"
	}
	return `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"stream": ` + streamStr + `,
		"messages": [{"role": "user", "content": "Read the file at /tmp/test.txt"}],
		"tools": [
			{"type": "` + toolType + `", "name": "` + toolName + `"},
			{"name": "Read", "description": "Read a file from disk", "input_schema": {"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}, "defer_loading": true},
			{"name": "Write", "description": "Write content to a file", "input_schema": {"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}, "defer_loading": true},
			{"name": "Bash", "description": "Execute a bash command", "input_schema": {"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}, "defer_loading": true}
		]
	}`
}

func TestE2E_ToolSearch_Regex_Streaming(t *testing.T) {
	url := newRealServer(t)
	resp := postMessages(t, url, toolSearchBody("tool_search_tool_regex_20251119", "tool_search_tool_regex", true))
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	events := readSSEEvents(t, resp.Body)
	requireSSEContains(t, events, "server_tool_use")
	requireSSEContains(t, events, "tool_search_tool_result")
	requireSSEContains(t, events, "tool_use")
	requireSSEEventField(t, events, "message_delta", "stop_reason", "tool_use")
}

func TestE2E_ToolSearch_BM25_Streaming(t *testing.T) {
	url := newRealServer(t)
	body := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"stream": true,
		"messages": [{"role": "user", "content": "Execute ls -la in the current directory"}],
		"tools": [
			{"type": "tool_search_tool_bm25_20251119", "name": "tool_search_tool_bm25"},
			{"name": "Read", "description": "Read a file from disk", "input_schema": {"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}, "defer_loading": true},
			{"name": "Bash", "description": "Execute a bash command and return output", "input_schema": {"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}, "defer_loading": true}
		]
	}`
	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	events := readSSEEvents(t, resp.Body)
	requireSSEContains(t, events, "server_tool_use")
	requireSSEContains(t, events, "tool_search_tool_result")
	requireSSEContains(t, events, "tool_use")
}

func TestE2E_ToolSearch_NonStreaming(t *testing.T) {
	url := newRealServer(t)
	resp := postMessages(t, url, toolSearchBody("tool_search_tool_regex_20251119", "tool_search_tool_regex", false))
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	var result map[string]any
	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("empty content array")
	}

	var hasServerToolUse, hasToolSearchResult, hasToolUse bool
	for _, block := range content {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		switch bm["type"] {
		case "server_tool_use":
			hasServerToolUse = true
		case "tool_search_tool_result":
			hasToolSearchResult = true
		case "tool_use":
			hasToolUse = true
		}
	}

	if !hasServerToolUse {
		t.Error("missing server_tool_use block")
	}
	if !hasToolSearchResult {
		t.Error("missing tool_search_tool_result block")
	}
	if !hasToolUse {
		t.Error("missing tool_use block")
	}
	if sr, _ := result["stop_reason"].(string); sr != "tool_use" {
		t.Errorf("stop_reason = %q, want %q", sr, "tool_use")
	}
}

func TestE2E_ToolSearch_NoSearchTool_Passthrough(t *testing.T) {
	url := newRealServer(t)
	body := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 256,
		"stream": true,
		"messages": [{"role": "user", "content": "Say hello in one word"}]
	}`
	resp := postMessages(t, url, body)
	defer resp.Body.Close()
	requireStatus(t, resp, 200)

	events := readSSEEvents(t, resp.Body)
	requireSSEContains(t, events, "message_start")
	requireSSEContains(t, events, "message_stop")

	for _, e := range events {
		if strings.Contains(e.data, "server_tool_use") {
			t.Error("unexpected server_tool_use in non-tool-search response")
		}
		if strings.Contains(e.data, "tool_search_tool_result") {
			t.Error("unexpected tool_search_tool_result in non-tool-search response")
		}
	}
}

// --- SSE helpers ---

type sseEvent struct {
	event string
	data  string
}

func readSSEEvents(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(r)
	var curEvent, curData string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			curEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			curData = strings.TrimPrefix(line, "data: ")
		case line == "":
			if curEvent != "" || curData != "" {
				events = append(events, sseEvent{event: curEvent, data: curData})
				curEvent, curData = "", ""
			}
		}
	}
	if len(events) == 0 {
		t.Fatal("no SSE events received")
	}
	return events
}

func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d, body = %s", resp.StatusCode, want, body)
	}
}

func requireSSEContains(t *testing.T, events []sseEvent, blockType string) {
	t.Helper()
	for _, e := range events {
		if strings.Contains(e.data, `"type":"`+blockType+`"`) || strings.Contains(e.data, `"type": "`+blockType+`"`) {
			return
		}
	}
	t.Errorf("SSE stream missing block type %q", blockType)
}

func requireSSEEventField(t *testing.T, events []sseEvent, eventType, field, want string) {
	t.Helper()
	for _, e := range events {
		if e.event != eventType {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(e.data), &m); err != nil {
			continue
		}
		if v, _ := m[field].(string); v == want {
			return
		}
		if delta, ok := m["delta"].(map[string]any); ok {
			if v, _ := delta[field].(string); v == want {
				return
			}
		}
	}
	t.Errorf("SSE event %q missing %s=%q", eventType, field, want)
}
