package server

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/auth"
	"github.com/d-kuro/kirocc/internal/kiroclient"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/d-kuro/kirocc/internal/logging"
	tu "github.com/d-kuro/kirocc/internal/testutil"
)

// newTCP4TestServer creates an httptest.Server bound to tcp4 to avoid IPv6 bind failures in sandboxed environments.
func newTCP4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return tu.NewTCP4TestServer(t, handler)
}

// capturingClient captures the payload sent to the Kiro API and returns a scripted response.
type capturingClient struct {
	captured     *kiroproto.Payload
	events       []any // pairs of (eventType string, payload []byte)
	promptTokens int   // pre-counted prompt tokens to return
}

func (c *capturingClient) GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
	c.captured = payload
	body := buildEventStream(c.events...)
	return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}, PromptTokens: c.promptTokens}, nil
}

func newE2EServer(t *testing.T, client *capturingClient) *httptest.Server {
	t.Helper()
	mgr := &mockAuthManager{
		creds: &auth.Credentials{
			AccessToken: "test-token",
			ProfileARN:  "arn:test",
			Region:      "us-east-1",
		},
	}
	s := New(mgr, "", client, WithCapture(true))
	return newTCP4TestServer(t, s.Handler())
}

func postMessages(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/messages", strings.NewReader(body))
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

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// requireStatus checks the response status code and fails with the body on mismatch.
func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d, body = %s", resp.StatusCode, want, body)
	}
}

// requireCaptured fails if the client did not capture a payload.
func requireCaptured(t *testing.T, client *capturingClient) {
	t.Helper()
	if client.captured == nil {
		t.Fatal("payload not captured")
	}
}

// multiResponseClient returns different scripted responses on successive calls.
type multiResponseClient struct {
	responses    [][]any // each element is an events slice for one call
	promptTokens int
	callCount    int
	payloads     []*kiroproto.Payload
}

func (c *multiResponseClient) GenerateAssistantResponse(ctx context.Context, token string, payload *kiroproto.Payload, region string) (*kiroclient.Response, error) {
	c.payloads = append(c.payloads, payload)
	idx := c.callCount
	if idx >= len(c.responses) {
		idx = len(c.responses) - 1
	}
	c.callCount++
	body := buildEventStream(c.responses[idx]...)
	return &kiroclient.Response{StatusCode: 200, Body: body, Header: http.Header{}, PromptTokens: c.promptTokens}, nil
}

func newE2EServerWithClient(t *testing.T, client kiroclient.Client) *httptest.Server {
	t.Helper()
	mgr := &mockAuthManager{
		creds: &auth.Credentials{
			AccessToken: "test-token",
			ProfileARN:  "arn:test",
			Region:      "us-east-1",
		},
	}
	s := New(mgr, "", client, WithCapture(true))
	return newTCP4TestServer(t, s.Handler())
}

// setupCaptureTest redirects slog to a buffer at debug level, so tests can
// assert capture log output. Capture itself is enabled on the server via
// WithCapture in newE2EServer / newE2EServerWithClient.
func setupCaptureTest(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(logging.NewOTelHandler(&buf, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}
