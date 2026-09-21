package server

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/safeguard"
)

// jevStub serves a Choice verdict response: the given choice
// (not_flagged/flagged/uncertain) at the given confidence, plus a generalizable
// noul so the verdict is shape-cacheable.
func jevStub(t *testing.T, choice string, confidence float64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-typesafe-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"model": "jev-test",
			"answers": map[string]any{
				"verdict": map[string]any{
					"type": "choice", "choice": choice, "confidence": confidence,
					"probabilities": map[string]any{choice: confidence},
				},
				"generalizable": map[string]any{"type": "noul", "noul": 1.0},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jevErrorStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newSafeguardServer is newE2EServer plus a classifier. A nil jevURL leaves the
// feature unconfigured.
func newSafeguardServer(t *testing.T, client *capturingClient, jevURL string) *httptest.Server {
	t.Helper()
	mgr := &mockAuthManager{
		creds: &auth.Credentials{
			AccessToken: "test-token",
			ProfileARN:  "arn:test",
			Region:      "us-east-1",
		},
	}
	opts := []ServerOption{WithCapture(true)}
	if jevURL != "" {
		// A single keyed test provider pointed at the stub, so the request
		// carries the auth header the stub checks. (The default chain now leads
		// with the keyless opencode provider, which WithEndpoint would leave
		// unauthenticated.)
		opts = append(opts, WithSafeguard(safeguard.New("",
			safeguard.WithProviders(safeguard.NewProvider("typesafe", jevURL, "jev-test", "test-typesafe-key")))))
	}
	s := New(mgr, "", client, opts...)
	return newTCP4TestServer(t, s.Handler())
}

// A streaming request that asks for classification and produces one tool call.
func safeguardRequestBody(withSafeguards bool) string {
	body := map[string]any{
		"model":      "claude-sonnet-4.6",
		"max_tokens": 1024,
		"stream":     true,
		"messages":   []any{map[string]any{"role": "user", "content": "run the tests"}},
	}
	if withSafeguards {
		body["safeguards"] = []any{map[string]any{
			"type":               "dangerous_tool_use",
			"classifier_context": map[string]any{"v": 1, "permission_mode": "auto"},
		}}
	}
	return string(mustJSON(body))
}

func toolCallEvents() []any {
	return []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "running them"}),
		"toolUseEvent", mustJSON(map[string]any{
			"name": "Bash", "toolUseId": "toolu_test_1", "input": `{"command":"go test ./..."}`, "stop": true,
		}),
	}
}

// messageDeltaSafeguards pulls safeguard_results out of the terminal
// message_delta's `delta` object -- the one place the client looks on a streamed
// response. Returns nil when the key is absent anywhere in the stream.
func messageDeltaSafeguards(t *testing.T, sse string) []map[string]any {
	t.Helper()
	for chunk := range strings.SplitSeq(sse, "\n\n") {
		if !strings.Contains(chunk, "event: message_delta") {
			continue
		}
		for line := range strings.SplitSeq(chunk, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev struct {
				Delta struct {
					SafeguardResults []map[string]any `json:"safeguard_results"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("decode message_delta %s: %v", data, err)
			}
			if ev.Delta.SafeguardResults != nil {
				return ev.Delta.SafeguardResults
			}
		}
	}
	return nil
}

// toolUseVerdict digs out the per-tool entry, asserting the envelope on the way.
func toolUseVerdict(t *testing.T, results []map[string]any, toolUseID string) map[string]any {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("want exactly one result entry, got %d", len(results))
	}
	if results[0]["type"] != "dangerous_tool_use" {
		t.Fatalf("type = %v", results[0]["type"])
	}
	status, ok := results[0]["status"].(map[string]any)
	if !ok {
		t.Fatalf("status is %T", results[0]["status"])
	}
	if status["type"] != "available" {
		t.Fatalf("status.type = %v, want available", status["type"])
	}
	toolUses, ok := status["tool_uses"].(map[string]any)
	if !ok {
		t.Fatalf("tool_uses is %T", status["tool_uses"])
	}
	entry, ok := toolUses[toolUseID].(map[string]any)
	if !ok {
		t.Fatalf("no verdict for %s; have %v", toolUseID, toolUses)
	}
	return entry
}

func readSSE(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	requireStatus(t, resp, http.StatusOK)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestE2E_Safeguard_ClearedActionRidesInMessageDelta(t *testing.T) {
	jev := jevStub(t, "not_flagged", 0.95)
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	entry := toolUseVerdict(t, messageDeltaSafeguards(t, sse), "toolu_test_1")
	if entry["type"] != "evaluated" || entry["outcome"] != "not_flagged" {
		t.Fatalf("verdict = %v, want evaluated/not_flagged", entry)
	}
}

func TestE2E_Safeguard_DangerousActionIsFlaggedWithExplanation(t *testing.T) {
	jev := jevStub(t, "flagged", 0.95)
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	entry := toolUseVerdict(t, messageDeltaSafeguards(t, sse), "toolu_test_1")
	if entry["type"] != "evaluated" || entry["outcome"] != "flagged" {
		t.Fatalf("verdict = %v, want evaluated/flagged", entry)
	}
	if entry["explanation"] == "" || entry["explanation"] == nil {
		t.Error("a flagged verdict must explain itself, or the client reports \"it gave no explanation\"")
	}
}

// An uncertain judgment defers that one action rather than refusing it.
func TestE2E_Safeguard_UncertainActionIsSkipped(t *testing.T) {
	jev := jevStub(t, "uncertain", 0.9)
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	entry := toolUseVerdict(t, messageDeltaSafeguards(t, sse), "toolu_test_1")
	if entry["type"] != "skipped" {
		t.Fatalf("verdict = %v, want skipped", entry)
	}
}

// A classifier outage must still produce a well-formed result. Omitting the key
// would make the client stop asking for the rest of the session; a per-tool
// skip costs only this turn.
func TestE2E_Safeguard_OutageDefersRatherThanOmitting(t *testing.T) {
	jev := jevErrorStub(t)
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	results := messageDeltaSafeguards(t, sse)
	if results == nil {
		t.Fatal("safeguard_results must still be present on a classifier outage")
	}
	entry := toolUseVerdict(t, results, "toolu_test_1")
	if entry["type"] != "skipped" {
		t.Fatalf("verdict = %v, want skipped", entry)
	}
}

// A response with no tool calls still answers, for the same reason.
func TestE2E_Safeguard_NoToolCallsStillAnswers(t *testing.T) {
	jev := jevStub(t, "not_flagged", 0.95)
	client := &capturingClient{events: []any{
		"assistantResponseEvent", mustJSON(map[string]string{"content": "nothing to run"}),
	}}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	results := messageDeltaSafeguards(t, sse)
	if results == nil {
		t.Fatal("want an empty available result, got no safeguard_results at all")
	}
	status := results[0]["status"].(map[string]any)
	toolUses, ok := status["tool_uses"].(map[string]any)
	if !ok || len(toolUses) != 0 {
		t.Fatalf("tool_uses = %v, want an empty object", status["tool_uses"])
	}
}

// Gating is on the request field, so a session that never asks is untouched.
func TestE2E_Safeguard_AbsentWhenClientDidNotAsk(t *testing.T) {
	jev := jevStub(t, "not_flagged", 0.95)
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(false)))
	if results := messageDeltaSafeguards(t, sse); results != nil {
		t.Fatalf("want no safeguard_results, got %v", results)
	}
}

// With safeguards disabled (no classifier attached), the field is left
// unanswered entirely -- no `safeguard_results` in the stream -- so Claude Code
// falls back to its own classifier, exactly as before kirocc answered the
// field. This is distinct from an empty answer, which would latch the client
// off for the session.
func TestE2E_Safeguard_DisabledLeavesFieldUnanswered(t *testing.T) {
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, "")
	defer srv.Close()

	sse := readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	if results := messageDeltaSafeguards(t, sse); results != nil {
		t.Fatalf("want no safeguard_results when disabled, got %v", results)
	}
}

// The `safeguards` field is ours to consume: it must not reach Kiro, which
// would reject an unknown member.
func TestE2E_Safeguard_FieldNotForwardedUpstream(t *testing.T) {
	jev := jevStub(t, "not_flagged", 0.95)
	client := &capturingClient{events: toolCallEvents()}
	srv := newSafeguardServer(t, client, jev.URL)
	defer srv.Close()

	_ = readSSE(t, postMessages(t, srv.URL, safeguardRequestBody(true)))
	if client.captured == nil {
		t.Fatal("no upstream payload captured")
	}
	b, err := json.Marshal(client.captured)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"safeguards", "classifier_context"} {
		if strings.Contains(string(b), needle) {
			t.Errorf("upstream payload leaked %q: %s", needle, b)
		}
	}
}
