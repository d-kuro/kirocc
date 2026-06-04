package messages

import (
	"context"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/models"
)

func TestResolveEffort(t *testing.T) {
	tests := []struct {
		name      string
		kiroModel string
		effort    string // request output_config.effort
		thinking  bool   // resolved thinking flag (type/[1m]/context-1m)
		want      string
	}{
		// Explicit effort passes through (validated against the model enum).
		{"explicit max on opus-4.8", "claude-opus-4.8", "max", false, "max"},
		{"explicit xhigh on opus-4.8", "claude-opus-4.8", "xhigh", true, "xhigh"},
		{"explicit xhigh clamps on sonnet-4.6", "claude-sonnet-4.6", "xhigh", false, "max"},

		// No effort + no thinking: nothing sent.
		{"no effort no thinking", "claude-opus-4.8", "", false, ""},

		// No effort but thinking enabled: fall back to the default effort so the
		// reasoning request still reaches the backend natively.
		{"thinking only, effort-capable opus-4.8", "claude-opus-4.8", "", true, models.EffortMedium},
		{"thinking only, effort-capable sonnet-4.6", "claude-sonnet-4.6", "", true, models.EffortMedium},

		// Thinking enabled but model has no effort schema: cannot express it, omit.
		{"thinking only, unsupported model", "claude-opus-4.5", "", true, ""},

		// Explicit effort wins even when thinking is also enabled.
		{"explicit low beats thinking default", "claude-opus-4.8", "low", true, "low"},

		// Invalid effort value is dropped; thinking default does NOT rescue an
		// explicitly-bad value (the explicit value was unrecognized, so we omit).
		{"invalid effort dropped despite thinking", "claude-opus-4.8", "bogus", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &anthropic.Request{}
			if tt.effort != "" {
				req.OutputConfig = &anthropic.OutputConfig{Effort: tt.effort}
			}
			got := resolveEffort(context.Background(), tt.kiroModel, req, tt.thinking)
			if got != tt.want {
				t.Errorf("resolveEffort(%q, effort=%q, thinking=%v) = %q, want %q",
					tt.kiroModel, tt.effort, tt.thinking, got, tt.want)
			}
		})
	}
}
