package messages

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/logging"
)

// captureSlog redirects slog.Default to a buffer-backed OTel handler for the
// duration of the test, restoring the previous default in cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(logging.NewOTelHandler(&buf, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// findRecord scans the buffer for the first JSON-Lines record whose body matches.
func findRecord(t *testing.T, buf *bytes.Buffer, body string) map[string]any {
	t.Helper()
	for line := range strings.SplitSeq(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["body"] == body {
			return rec
		}
	}
	t.Fatalf("no log record with body=%q in output:\n%s", body, buf.String())
	return nil
}

func TestLogResponseStats_IncludesCredits(t *testing.T) {
	buf := captureSlog(t)
	logResponseStats(t.Context(), "abc123", 100, 50, true, 5.0, 200000, 0.19354654693200665, true)

	rec := findRecord(t, buf, "<-- POST /v1/messages")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	got, ok := attrs["credits"]
	if !ok {
		t.Fatalf("credits attribute missing; attrs=%v", attrs)
	}
	if got != 0.194 {
		t.Fatalf("credits = %v, want 0.194 (rounded to 3 decimals)", got)
	}
}

func TestLogResponseStats_OmitsCreditsWhenAbsent(t *testing.T) {
	buf := captureSlog(t)
	logResponseStats(t.Context(), "abc123", 100, 50, true, 5.0, 200000, 0, false)

	rec := findRecord(t, buf, "<-- POST /v1/messages")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	if _, present := attrs["credits"]; present {
		t.Fatalf("credits attribute should be omitted when hasCredits=false; attrs=%v", attrs)
	}
}

func TestRoundCredits(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.19354654693200665, 0.194},
		{0.5417483374129354, 0.542},
		{0.0004, 0},
		{0.0005, 0.001}, // half rounds away from zero (math.Round)
		{0, 0},
		{1, 1},
	}
	for _, tc := range cases {
		if got := roundCredits(tc.in); got != tc.want {
			t.Errorf("roundCredits(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLogAbortedAttemptCredits(t *testing.T) {
	buf := captureSlog(t)
	logAbortedAttemptCredits(t.Context(), "abc123", 0.12345, retryReasonEmptyVisibleEndTurn)

	rec := findRecord(t, buf, "upstream attempt credits (aborted)")
	attrs, ok := rec["attributes"].(map[string]any)
	if !ok {
		t.Fatal("attributes missing")
	}
	if attrs["credits"] != 0.123 {
		t.Fatalf("credits = %v, want 0.123 (rounded)", attrs["credits"])
	}
	if attrs["reason"] != retryReasonEmptyVisibleEndTurn {
		t.Fatalf("reason = %v, want %q", attrs["reason"], retryReasonEmptyVisibleEndTurn)
	}
}
