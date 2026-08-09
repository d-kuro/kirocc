package respconv

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestWriteAdvisorResult(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.WriteAdvisorResult("srvtoolu_1", "Take the contract-first approach.", "end_turn")

	body := w.Body.String()
	for _, want := range []string{
		`"type":"advisor_tool_result"`,
		`"tool_use_id":"srvtoolu_1"`,
		`"type":"advisor_result"`,
		`"text":"Take the contract-first approach."`,
		`"stop_reason":"end_turn"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE body missing %s:\n%s", want, body)
		}
	}
	// Plaintext result only — the proxy must never forge encrypted content.
	if strings.Contains(body, "encrypted_content") {
		t.Errorf("SSE body contains forged encrypted_content:\n%s", body)
	}
}

func TestWriteAdvisorError(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.WriteAdvisorError("srvtoolu_2", anthropic.AdvisorErrorMaxUsesExceeded)

	body := w.Body.String()
	for _, want := range []string{
		`"type":"advisor_tool_result"`,
		`"tool_use_id":"srvtoolu_2"`,
		`"type":"advisor_tool_result_error"`,
		`"error_code":"max_uses_exceeded"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SSE body missing %s:\n%s", want, body)
		}
	}
}

// Advisor rounds report their tokens in usage.iterations[], separate from the
// executor's top-level counts.
func TestFinishIncludesAdvisorIterations(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)

	sw.AddAdvisorIteration("claude-opus-5", 1200, 340)
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	body := w.Body.String()
	for _, want := range []string{
		`"iterations":[`,
		`"type":"advisor_message"`,
		`"model":"claude-opus-5"`,
		`"input_tokens":1200`,
		`"output_tokens":340`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("message_delta usage missing %s:\n%s", want, body)
		}
	}
}

func TestFinishWithoutIterationsOmitsKey(t *testing.T) {
	w := httptest.NewRecorder()
	sw := NewSSEWriter(context.Background(), w, "claude-sonnet-4.6", 200000, nil, 0, 0)
	if err := sw.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if strings.Contains(w.Body.String(), `"iterations"`) {
		t.Errorf("usage contains iterations key without advisor rounds:\n%s", w.Body.String())
	}
}
