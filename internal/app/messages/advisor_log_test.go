package messages

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/advisor"
	"github.com/d-kuro/kirocc/internal/logging"
)

func testAdvisorContext(t *testing.T) *advisor.Context {
	t.Helper()
	c := &advisor.Context{
		ToolName:       advisor.KiroToolName,
		RequestedModel: "claude-opus-5",
		KiroModel:      "claude-opus-5",
		ResponseModel:  "claude-opus-5",
		MaxUses:        2,
	}
	return c
}

// The advisor subcall must be visible in the INFO log at the same granularity
// as the executor's own request/response pair, correlated by trace and session.
func TestLogAdvisorRequest(t *testing.T) {
	buf := captureSlog(t)
	ctx := logging.WithSessionID(t.Context(), "session-abcdef123456")

	// round is 0-indexed internally and reported 1-indexed. attempt 0 is the
	// first try, which must not clutter the line with an attempt field.
	logAdvisorRequest(ctx, "abc123", testAdvisorContext(t), 0, 0, 42)

	rec := findRecord(t, buf, "--> advisor")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	if _, present := attrs["attempt"]; present {
		t.Errorf("first attempt must not log an attempt field; attrs=%v", attrs)
	}
	for key, want := range map[string]any{
		"trace_id":   "abc123",
		"session_id": "session-",
		"model":      "claude-opus-5",
		"round":      float64(1),
		"messages":   float64(42),
		"max_uses":   float64(2),
	} {
		if attrs[key] != want {
			t.Errorf("%s = %v, want %v", key, attrs[key], want)
		}
	}
}

func TestLogAdvisorResponse(t *testing.T) {
	buf := captureSlog(t)
	ctx := logging.WithSessionID(t.Context(), "session-abcdef123456")
	advCtx := testAdvisorContext(t)
	advCtx.Consume()

	logAdvisorResponse(ctx, "abc123", advCtx, 0, advisorOutcome{
		text:         "Start with the contract layer.",
		stopReason:   "end_turn",
		inputTokens:  1200,
		outputTokens: 340,
		credits:      0.4567,
		hasCredits:   true,
	})

	rec := findRecord(t, buf, "<-- advisor")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	for key, want := range map[string]any{
		"trace_id":      "abc123",
		"session_id":    "session-",
		"status":        float64(200),
		"model":         "claude-opus-5",
		"uses":          float64(1),
		"max_uses":      float64(2),
		"input_tokens":  float64(1200),
		"output_tokens": float64(340),
		"stop_reason":   "end_turn",
		"credits":       0.457,
	} {
		if attrs[key] != want {
			t.Errorf("%s = %v, want %v", key, attrs[key], want)
		}
	}
	// The advice text itself stays out of the INFO line; it is DEBUG-only
	// because it carries conversation content.
	if _, present := attrs["advice"]; present {
		t.Errorf("advice must not appear in the INFO line; attrs=%v", attrs)
	}
	if _, present := attrs["error_code"]; present {
		t.Errorf("successful consultation must not log error_code; attrs=%v", attrs)
	}
}

// A retry is visible in the request line so a doubled latency is explainable.
func TestLogAdvisorRequestRetry(t *testing.T) {
	buf := captureSlog(t)

	logAdvisorRequest(t.Context(), "abc123", testAdvisorContext(t), 0, 1, 42)

	rec := findRecord(t, buf, "--> advisor")
	attrs, _ := rec["attributes"].(map[string]any)
	if attrs["attempt"] != float64(2) {
		t.Errorf("attempt = %v, want 2", attrs["attempt"])
	}
}

// An unresolvable advisor model has no ResponseModel, so the log must fall
// back to the model the client asked for — otherwise the one line that explains
// the failure names no model at all.
func TestLogAdvisorResponseUnresolvedModel(t *testing.T) {
	buf := captureSlog(t)
	advCtx := &advisor.Context{
		ToolName:       advisor.KiroToolName,
		RequestedModel: "claude-nonexistent-99",
		MaxUses:        2,
		PreflightError: "model_not_found",
	}

	logAdvisorResponse(t.Context(), "abc123", advCtx, 0, advisorOutcome{errorCode: "model_not_found"})

	rec := findRecord(t, buf, "<-- advisor")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	if attrs["model"] != "claude-nonexistent-99" {
		t.Errorf("model = %v, want the requested model", attrs["model"])
	}
}

// A failed consultation logs the error code instead of advice, and reports the
// advisor error as the status so it is greppable alongside HTTP statuses.
func TestLogAdvisorResponseError(t *testing.T) {
	buf := captureSlog(t)
	advCtx := testAdvisorContext(t)

	logAdvisorResponse(t.Context(), "abc123", advCtx, 1, advisorOutcome{
		errorCode: "model_not_found",
	})

	rec := findRecord(t, buf, "<-- advisor")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	if attrs["error_code"] != "model_not_found" {
		t.Errorf("error_code = %v, want model_not_found", attrs["error_code"])
	}
	if _, present := attrs["advice"]; present {
		t.Errorf("failed consultation must not log advice; attrs=%v", attrs)
	}
	if _, present := attrs["credits"]; present {
		t.Errorf("failed consultation without metering must omit credits; attrs=%v", attrs)
	}
}
