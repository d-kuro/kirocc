package messages

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/testutil"
)

// scriptedClient replays one scripted event stream per upstream call.
type scriptedClient struct {
	streams   [][]byte
	callCount int
}

func (c *scriptedClient) GenerateAssistantResponse(_ context.Context, _ string, _ *kiroproto.Payload, _ string) (*kiroclient.Response, error) {
	idx := min(c.callCount, len(c.streams)-1)
	c.callCount++
	return &kiroclient.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(string(c.streams[idx]))),
		Header:     http.Header{},
	}, nil
}

// emptyStream is a clean stream carrying no assistant text: the shape Kiro
// intermittently returns for an advisor subcall.
func emptyStream() []byte {
	return testutil.BuildFrame("metadataEvent", []byte(`{"tokenUsage":{"uncachedInputTokens":300,"outputTokens":0}}`))
}

// textStream is a normal advisor answer.
func textStream(text string) []byte {
	frames := testutil.BuildFrame("assistantResponseEvent", []byte(`{"content":`+quote(text)+`}`))
	return append(frames, testutil.BuildFrame("metadataEvent", []byte(`{"tokenUsage":{"uncachedInputTokens":300,"outputTokens":40}}`))...)
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func advisorSubcallOrchestrator(client kiroclient.Client) *serverToolOrchestrator {
	msgs := []anthropic.Message{
		{Role: "user", Content: anthropic.MessageContent{Text: "review this design"}},
	}
	return &serverToolOrchestrator{
		service: &Service{client: client},
		creds:   &auth.Credentials{AccessToken: "t", Region: "us-east-1"},
		req: &anthropic.Request{
			Model:    "claude-sonnet-4-6",
			Messages: msgs,
			Tools: []anthropic.Tool{
				{Type: anthropic.ToolTypeAdvisor, Name: "advisor", Model: "claude-opus-5"},
			},
		},
		advCtx:            &advisor.Context{KiroModel: "claude-opus-5", ResponseModel: "claude-opus-5", MaxUses: 2},
		buildOpts:         reqconv.BuildOptions{ProfileARN: "arn:test", ModelID: "claude-sonnet-4.6"},
		contextWindowSize: 200_000,
	}
}

// A clean-but-empty upstream response is a transient backend failure, not a
// verdict: replaying the identical payload succeeds. One retry must happen
// before the consultation is reported unavailable, and the retry must not
// consume a second max_uses slot — the client asked for one consultation.
func TestConsultAdvisor_RetriesEmptyResponse(t *testing.T) {
	client := &scriptedClient{streams: [][]byte{emptyStream(), textStream("Start with the contract layer.")}}
	o := advisorSubcallOrchestrator(client)

	outcome := o.consultAdvisor(t.Context(), "abc123", 0, o.req.Messages)

	if client.callCount != 2 {
		t.Fatalf("upstream calls = %d, want 2 (initial + one retry)", client.callCount)
	}
	if outcome.errorCode != "" {
		t.Fatalf("outcome error = %q, want success after retry", outcome.errorCode)
	}
	if outcome.text != "Start with the contract layer." {
		t.Errorf("advice = %q", outcome.text)
	}
	if got := o.advCtx.Uses(); got != 1 {
		t.Errorf("uses = %d, want 1: a retry of the same consultation must not consume another use", got)
	}
}

// Two consecutive empty responses are reported as unavailable rather than
// retried indefinitely.
func TestConsultAdvisor_EmptyTwiceIsUnavailable(t *testing.T) {
	client := &scriptedClient{streams: [][]byte{emptyStream(), emptyStream()}}
	o := advisorSubcallOrchestrator(client)

	outcome := o.consultAdvisor(t.Context(), "abc123", 0, o.req.Messages)

	if client.callCount != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2", client.callCount)
	}
	if outcome.errorCode != anthropic.AdvisorErrorUnavailable {
		t.Fatalf("outcome error = %q, want unavailable", outcome.errorCode)
	}
}

// A successful first response must not trigger a retry.
func TestConsultAdvisor_NoRetryOnSuccess(t *testing.T) {
	client := &scriptedClient{streams: [][]byte{textStream("advice"), textStream("should not be reached")}}
	o := advisorSubcallOrchestrator(client)

	outcome := o.consultAdvisor(t.Context(), "abc123", 0, o.req.Messages)

	if client.callCount != 1 {
		t.Fatalf("upstream calls = %d, want 1", client.callCount)
	}
	if outcome.text != "advice" {
		t.Errorf("advice = %q", outcome.text)
	}
}

// max_uses is enforced before the retry logic: an exhausted budget must not
// issue any upstream call at all.
func TestConsultAdvisor_MaxUsesBlocksSubcall(t *testing.T) {
	client := &scriptedClient{streams: [][]byte{textStream("advice")}}
	o := advisorSubcallOrchestrator(client)
	o.advCtx.MaxUses = 1
	if !o.advCtx.Consume() {
		t.Fatal("first Consume must succeed")
	}

	outcome := o.consultAdvisor(t.Context(), "abc123", 0, o.req.Messages)

	if client.callCount != 0 {
		t.Fatalf("upstream calls = %d, want 0", client.callCount)
	}
	if outcome.errorCode != anthropic.AdvisorErrorMaxUsesExceeded {
		t.Errorf("outcome error = %q, want max_uses_exceeded", outcome.errorCode)
	}
}
