package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	tu "github.com/d-kuro/kirocc/internal/testutil"
)

const toolSearchStreamingRequest = `{
	"model":"claude-sonnet-4-6",
	"max_tokens":64,
	"stream":true,
	"messages":[{"role":"user","content":"find a read tool"}],
	"tools":[
		{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},
		{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}},"defer_loading":true}
	]
}`

type pendingStream struct {
	call   int
	writer *io.PipeWriter
}

// keepAliveScriptClient returns a pipe for nil script entries and a complete
// in-memory event stream for non-nil entries.
type keepAliveScriptClient struct {
	mu      sync.Mutex
	scripts [][]any
	calls   int
	pending chan pendingStream
}

type retryThenErrorClient struct {
	mu      sync.Mutex
	calls   int
	pending chan pendingStream
	err     error
}

func (c *retryThenErrorClient) GenerateAssistantResponse(context.Context, string, *kiroproto.Payload, string) (*kiroclient.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call > 1 {
		return nil, c.err
	}
	reader, writer := io.Pipe()
	c.pending <- pendingStream{call: call, writer: writer}
	return &kiroclient.Response{
		StatusCode: http.StatusOK,
		Body:       reader,
		Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
	}, nil
}

func newKeepAliveScriptClient(scripts ...[]any) *keepAliveScriptClient {
	return &keepAliveScriptClient{
		scripts: scripts,
		pending: make(chan pendingStream, len(scripts)),
	}
}

func (c *keepAliveScriptClient) GenerateAssistantResponse(context.Context, string, *kiroproto.Payload, string) (*kiroclient.Response, error) {
	c.mu.Lock()
	call := c.calls
	c.calls++
	script := c.scripts[min(call, len(c.scripts)-1)]
	c.mu.Unlock()

	header := http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}
	if script != nil {
		return &kiroclient.Response{StatusCode: http.StatusOK, Body: buildEventStream(script...), Header: header}, nil
	}

	reader, writer := io.Pipe()
	c.pending <- pendingStream{call: call + 1, writer: writer}
	return &kiroclient.Response{StatusCode: http.StatusOK, Body: reader, Header: header}, nil
}

func (c *keepAliveScriptClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type streamingRecorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	body    bytes.Buffer
	flushed chan struct{}
}

func newStreamingRecorder() *streamingRecorder {
	return &streamingRecorder{
		header:  make(http.Header),
		flushed: make(chan struct{}, 256),
	}
}

func (w *streamingRecorder) Header() http.Header { return w.header }

func (w *streamingRecorder) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *streamingRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *streamingRecorder) FlushError() error {
	w.mu.Lock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.mu.Unlock()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
	return nil
}

func (w *streamingRecorder) snapshot() (status int, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.body.String()
}

func newKeepAliveServer(client kiroclient.Client, interval time.Duration) *Server {
	mgr := &mockAuthManager{creds: &auth.Credentials{
		AccessToken: "test-token",
		ProfileARN:  "arn:test",
		Region:      "us-east-1",
	}}
	return New(mgr, "", client, WithKeepAliveInterval(interval))
}

func startStreamingHandler(t *testing.T, srv *Server, body string) (*streamingRecorder, <-chan struct{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "keepalive-test-session")
	w := newStreamingRecorder()
	done := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(w, req)
		close(done)
	}()
	return w, done
}

func awaitPendingStream(t *testing.T, pending <-chan pendingStream) pendingStream {
	t.Helper()
	select {
	case stream := <-pending:
		return stream
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upstream stream")
		return pendingStream{}
	}
}

func awaitHandlerDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler completion")
	}
}

func awaitBodyContains(t *testing.T, w *streamingRecorder, want string) string {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		_, body := w.snapshot()
		if strings.Contains(body, want) {
			return body
		}
		select {
		case <-w.flushed:
		case <-deadline.C:
			t.Fatalf("timed out waiting for downstream body %q; body = %s", want, body)
		}
	}
}

func writeUpstreamEvents(t *testing.T, w *io.PipeWriter, events ...any) {
	t.Helper()
	for i := 0; i < len(events); i += 2 {
		if _, err := w.Write(tu.BuildFrame(events[i].(string), events[i+1].([]byte))); err != nil {
			t.Fatalf("write upstream event: %v", err)
		}
	}
}

func TestSSEKeepAlive_StalledFirstFrameThenSuccess(t *testing.T) {
	client := newKeepAliveScriptClient(nil)
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })

	body := awaitBodyContains(t, w, ": keep-alive\n\n")
	if body != ": keep-alive\n\n" {
		t.Fatalf("first downstream bytes = %q, want keep-alive comment", body)
	}
	writeUpstreamEvents(t, upstream.writer, "assistantResponseEvent", mustJSON(map[string]string{"content": "eventual answer"}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	status, body := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "eventual answer") || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("successful SSE did not follow keep-alive: %s", body)
	}
}

func TestSSEKeepAlive_FinalErrorAfterCommitIsSSE(t *testing.T) {
	client := newKeepAliveScriptClient(nil)
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })
	_ = awaitBodyContains(t, w, ": keep-alive\n\n")

	writeUpstreamEvents(t, upstream.writer, "invalidStateEvent", mustJSON(map[string]string{
		"reason": "FATAL_STATE", "message": "request rejected",
	}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	status, body := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want committed 200", status)
	}
	if !strings.Contains(body, "event: error") {
		t.Fatalf("missing SSE error after keep-alive commit: %s", body)
	}
	if strings.Contains(body, "\n{\"type\":\"error\"") {
		t.Fatalf("standalone JSON error injected into SSE body: %s", body)
	}
}

func TestSSEKeepAlive_FinalErrorAfterPromotionClosesActiveBlock(t *testing.T) {
	events := []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "partial"}),
		"invalidStateEvent", mustJSON(map[string]string{"reason": "FATAL_STATE", "message": "request rejected"}),
	}
	client := newKeepAliveScriptClient(events)
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	awaitHandlerDone(t, done)
	status, body := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want promoted 200", status)
	}
	stop := strings.Index(body, "event: content_block_stop")
	errEvent := strings.Index(body, "event: error")
	if stop < 0 || errEvent < 0 || stop > errEvent {
		t.Fatalf("active block was not closed before SSE error: %s", body)
	}
	if !strings.Contains(body, `"type":"invalid_state"`) {
		t.Fatalf("mid-stream invalid state type changed: %s", body)
	}
}

func TestSSEKeepAlive_FinalErrorBeforeCommitIsNon200JSON(t *testing.T) {
	srv := newKeepAliveServer(&errorClient{err: errors.New("upstream unavailable")}, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	awaitHandlerDone(t, done)
	status, body := w.snapshot()
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", status, body)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if !strings.HasPrefix(body, "{") || !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("body is not a JSON error: %s", body)
	}
}

func TestSSEKeepAlive_DisabledDoesNotWriteWhileStalled(t *testing.T) {
	client := newKeepAliveScriptClient(nil)
	srv := newKeepAliveServer(client, 0)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })

	select {
	case <-w.flushed:
		_, body := w.snapshot()
		t.Fatalf("disabled keep-alive flushed early: %s", body)
	case <-time.After(40 * time.Millisecond):
	}
	if _, body := w.snapshot(); body != "" {
		t.Fatalf("disabled keep-alive wrote while stalled: %s", body)
	}

	writeUpstreamEvents(t, upstream.writer, "assistantResponseEvent", mustJSON(map[string]string{"content": "normal response"}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	_, body := w.snapshot()
	if strings.Contains(body, ": keep-alive") {
		t.Fatalf("disabled keep-alive wrote a comment: %s", body)
	}
}

func TestSSEKeepAlive_TransparentRetryAfterCommit(t *testing.T) {
	normal := []any{"assistantResponseEvent", mustJSON(map[string]string{"content": "retry answer"})}
	client := newKeepAliveScriptClient(nil, normal)
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })
	_ = awaitBodyContains(t, w, ": keep-alive\n\n")

	writeUpstreamEvents(t, upstream.writer, "assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>discard me</thinking>"}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	_, body := w.snapshot()
	if client.callCount() != 2 {
		t.Fatalf("upstream calls = %d, want 2", client.callCount())
	}
	if !strings.Contains(body, "retry answer") {
		t.Fatalf("retry response missing: %s", body)
	}
	if strings.Contains(body, "thinking_delta") || strings.Contains(body, "discard me") {
		t.Fatalf("first attempt semantic events leaked: %s", body)
	}
}

func TestSSEKeepAlive_RetryHTTPFailureAfterCommitIsSSE(t *testing.T) {
	client := &retryThenErrorClient{
		pending: make(chan pendingStream, 1),
		err:     errors.New("second upstream call failed"),
	}
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })
	_ = awaitBodyContains(t, w, ": keep-alive\n\n")

	writeUpstreamEvents(t, upstream.writer, "assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>retry me</thinking>"}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	status, body := w.snapshot()
	if status != http.StatusOK || !strings.Contains(body, "event: error") {
		t.Fatalf("retry HTTP failure was not SSE on committed response: status=%d body=%s", status, body)
	}
	if strings.Contains(body, "\n{\"type\":\"error\"") {
		t.Fatalf("retry HTTP failure injected JSON: %s", body)
	}
}

func TestSSEKeepAlive_ToolSearchAcrossRounds(t *testing.T) {
	toolRound := []any{"toolUseEvent", mustJSON(map[string]any{
		"name": "ToolSearch", "toolUseId": "tool-search-1", "input": `{"query":"Read"}`, "stop": true,
	})}
	client := newKeepAliveScriptClient(toolRound, nil)
	srv := newKeepAliveServer(client, 20*time.Millisecond)
	w, done := startStreamingHandler(t, srv, toolSearchStreamingRequest)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })
	if upstream.call != 2 {
		t.Fatalf("stalled stream is call %d, want round 2", upstream.call)
	}

	body := awaitBodyContains(t, w, ": keep-alive\n\n")
	if !strings.Contains(body, "tool_search_tool_result") {
		t.Fatalf("heartbeat did not remain after round-1 semantic output: %s", body)
	}
	writeUpstreamEvents(t, upstream.writer, "assistantResponseEvent", mustJSON(map[string]string{"content": "round two answer"}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	_, body = w.snapshot()
	if !strings.Contains(body, "round two answer") {
		t.Fatalf("round 2 response missing: %s", body)
	}
}

func TestSSEKeepAlive_ToolSearchOuterRetryAfterCommit(t *testing.T) {
	normal := []any{"assistantResponseEvent", mustJSON(map[string]string{"content": "tool-search retry answer"})}
	client := newKeepAliveScriptClient(nil, normal)
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, toolSearchStreamingRequest)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })
	_ = awaitBodyContains(t, w, ": keep-alive\n\n")

	writeUpstreamEvents(t, upstream.writer, "assistantResponseEvent", mustJSON(map[string]string{"content": "<thinking>discard tool-search attempt</thinking>"}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	_, body := w.snapshot()
	if client.callCount() != 2 || !strings.Contains(body, "tool-search retry answer") {
		t.Fatalf("tool-search outer retry failed: calls=%d body=%s", client.callCount(), body)
	}
	if strings.Contains(body, "discard tool-search attempt") || strings.Contains(body, "thinking_delta") {
		t.Fatalf("tool-search first attempt leaked semantic output: %s", body)
	}
}

func TestSSEKeepAlive_ToolSearchTailErrorIsEmitted(t *testing.T) {
	client := newKeepAliveScriptClient(nil)
	srv := newKeepAliveServer(client, 10*time.Millisecond)
	w, done := startStreamingHandler(t, srv, toolSearchStreamingRequest)
	upstream := awaitPendingStream(t, client.pending)
	t.Cleanup(func() { _ = upstream.writer.Close() })
	writeUpstreamEvents(t, upstream.writer, "toolUseEvent", mustJSON(map[string]any{
		"name": "ToolSearch", "toolUseId": "tool-search-tail", "input": `{"query":"Read"}`, "stop": true,
	}))
	_ = awaitBodyContains(t, w, ": keep-alive\n\n")
	writeUpstreamEvents(t, upstream.writer, "invalidStateEvent", mustJSON(map[string]string{
		"reason": "TAIL_FAILURE", "message": "tail failed",
	}))
	_ = upstream.writer.Close()
	awaitHandlerDone(t, done)
	_, body := w.snapshot()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("tail exception produced neither error nor message_stop: %s", body)
	}
}
