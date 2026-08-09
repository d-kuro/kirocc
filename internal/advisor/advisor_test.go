package advisor

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func resolver(model string) (string, string, bool) {
	if model == "claude-opus-5" {
		return "claude-opus-5", "claude-opus-5", true
	}
	return "", "", false
}

func advisorTool(mutate func(*anthropic.Tool)) []anthropic.Tool {
	tool := anthropic.Tool{
		Type:  anthropic.ToolTypeAdvisor,
		Name:  anthropic.AdvisorToolName,
		Model: "claude-opus-5",
	}
	if mutate != nil {
		mutate(&tool)
	}
	return []anthropic.Tool{tool}
}

func TestNewContextNilWithoutAdvisorTool(t *testing.T) {
	tools := []anthropic.Tool{{Name: "Read", InputSchema: map[string]any{"type": "object"}}}
	if c := NewContext(tools, resolver); c != nil {
		t.Fatalf("NewContext = %+v, want nil", c)
	}
}

func TestNewContextResolvesModel(t *testing.T) {
	c := NewContext(advisorTool(nil), resolver)
	if c == nil {
		t.Fatal("NewContext returned nil")
	}
	if c.PreflightError != "" {
		t.Fatalf("PreflightError = %q, want empty", c.PreflightError)
	}
	if c.KiroModel != "claude-opus-5" || c.ResponseModel != "claude-opus-5" {
		t.Errorf("resolved = (%q, %q)", c.KiroModel, c.ResponseModel)
	}
	if c.MaxUses != DefaultMaxUses {
		t.Errorf("MaxUses = %d, want %d", c.MaxUses, DefaultMaxUses)
	}
}

// Unresolvable or missing models must become a preflight model_not_found, not
// a request failure and never a silent fallback to a weaker model.
func TestNewContextPreflightErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*anthropic.Tool)
		want   string
	}{
		{
			name:   "unknown model",
			mutate: func(tool *anthropic.Tool) { tool.Model = "claude-unknown-1" },
			want:   anthropic.AdvisorErrorModelNotFound,
		},
		{
			name:   "empty model",
			mutate: func(tool *anthropic.Tool) { tool.Model = "" },
			want:   anthropic.AdvisorErrorModelNotFound,
		},
		{
			name: "malformed caching",
			mutate: func(tool *anthropic.Tool) {
				tool.Caching = &anthropic.AdvisorCaching{Type: "bogus"}
			},
			want: anthropic.AdvisorErrorPromptTooLong,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewContext(advisorTool(tt.mutate), resolver)
			if c == nil {
				t.Fatal("NewContext returned nil")
			}
			if c.PreflightError != tt.want {
				t.Errorf("PreflightError = %q, want %q", c.PreflightError, tt.want)
			}
		})
	}
}

func TestConsumeEnforcesMaxUses(t *testing.T) {
	c := NewContext(advisorTool(func(tool *anthropic.Tool) { tool.MaxUses = 2 }), resolver)
	for i := range 2 {
		if !c.Consume() {
			t.Fatalf("consultation %d must succeed", i+1)
		}
	}
	if c.Consume() {
		t.Fatal("third consultation must fail with max_uses = 2")
	}
	if c.Uses() != 2 {
		t.Errorf("Uses = %d, want 2", c.Uses())
	}
}

func TestKiroToolEntryIsZeroParameter(t *testing.T) {
	c := NewContext(advisorTool(nil), resolver)
	entry := c.KiroToolEntry()
	if entry.ToolSpecification == nil || entry.ToolSpecification.Name != KiroToolName {
		t.Fatalf("entry = %+v", entry)
	}
	props, _ := entry.ToolSpecification.InputSchema.JSON["properties"].(map[string]any)
	if len(props) != 0 {
		t.Errorf("advisor tool must take no parameters, got %v", props)
	}
}
