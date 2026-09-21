package safeguard

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveJev exercises the real TypeSafe Jev endpoint end-to-end: the key-file
// path, the generalizable question, the verdict cache, and the debug logging.
// It is skipped unless KIROCC_LIVE_JEV=1 so ordinary `go test` never makes a
// network call or needs a key. Run with:
//
//	KIROCC_LIVE_JEV=1 go test ./internal/safeguard/ -run TestLiveJev -v -count=1
func TestLiveJev(t *testing.T) {
	if os.Getenv("KIROCC_LIVE_JEV") != "1" {
		t.Skip("set KIROCC_LIVE_JEV=1 to run the live Jev e2e check")
	}
	keyPath := os.Getenv("HOME") + "/.config/kiro/typesafe-key"
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key file %s: %v", keyPath, err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		t.Fatalf("key file %s is empty", keyPath)
	}

	// Route the DEBUG "jev verdict" lines to the test log so the raw nouls,
	// severity and generalizable signal are visible.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	c := New(key, WithHTTPClient(&http.Client{Timeout: 15 * time.Second}))
	ctx := context.Background()

	// A read: expect NotFlagged and generalizable. Judge two argument variants;
	// the second must be a cache hit (no extra latency, served from shape key).
	g1 := ToolUse{ID: "g1", Name: "Bash", Input: `{"command":"grep foo a.go"}`}
	st1, err := c.judge(ctx, nil, g1)
	if err != nil {
		t.Fatalf("judge grep 1: %v", err)
	}
	t.Logf("grep a.go -> outcome=%q skip=%v", st1.Outcome, st1.Skip)

	start := time.Now()
	g2 := ToolUse{ID: "g2", Name: "Bash", Input: `{"command":"grep foo b.go"}`}
	st2, err := c.judge(ctx, nil, g2)
	if err != nil {
		t.Fatalf("judge grep 2: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("grep b.go -> outcome=%q skip=%v (%.0fms)", st2.Outcome, st2.Skip, float64(elapsed.Milliseconds()))
	if elapsed > 50*time.Millisecond {
		t.Errorf("second grep took %v; expected a fast shape-cache hit (Jev not re-called)", elapsed)
	}

	// A dangerous command: expect Flagged or Skip, and NOT generalizable, so it
	// is not shape-cached. This documents the actual live verdict.
	rm := ToolUse{ID: "rm1", Name: "Bash", Input: `{"command":"rm -rf /"}`}
	st3, err := c.judge(ctx, nil, rm)
	if err != nil {
		t.Fatalf("judge rm: %v", err)
	}
	t.Logf("rm -rf / -> outcome=%q skip=%v explanation=%q", st3.Outcome, st3.Skip, st3.Explanation)
}
