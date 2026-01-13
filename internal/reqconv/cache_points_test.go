package reqconv

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestApplySystemCachePoints_NoOp(t *testing.T) {
	// v2 captures show system cache_control is NOT converted to cachePoint.
	system := anthropic.SystemPrompt{
		Blocks: []anthropic.SystemBlock{
			{Type: "text", Text: "System", CacheControl: &anthropic.CacheControl{Type: "ephemeral"}},
		},
	}
	msg := &kiroproto.UserInputMessage{Content: "Hello"}
	ApplySystemCachePoints(system, nil, msg)
	if msg.CachePoint != nil {
		t.Fatal("system cache_control should not produce cachePoint")
	}
}

func TestApplySystemCachePoints_EmptyHistory(t *testing.T) {
	system := anthropic.SystemPrompt{
		Blocks: []anthropic.SystemBlock{
			{Type: "text", Text: "System", CacheControl: &anthropic.CacheControl{Type: "ephemeral"}},
		},
	}
	// Should not panic with empty history and nil currentMessage.
	ApplySystemCachePoints(system, nil, nil)
}

func TestApplyToolCachePoints_WithCacheControl(t *testing.T) {
	tools := []anthropic.Tool{
		{Name: "a", CacheControl: &anthropic.CacheControl{Type: "ephemeral"}},
		{Name: "b"},
	}
	entries := []kiroproto.ToolEntry{
		{ToolSpecification: &kiroproto.ToolSpecification{Name: "a"}},
		{ToolSpecification: &kiroproto.ToolSpecification{Name: "b"}},
	}
	got := ApplyToolCachePoints(tools, entries)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if got[0].ToolSpecification == nil || got[0].ToolSpecification.Name != "a" {
		t.Fatal("first should be tool a")
	}
	if got[1].CachePoint == nil || got[1].CachePoint.Type != "default" {
		t.Fatal("second should be cachePoint")
	}
	if got[2].ToolSpecification == nil || got[2].ToolSpecification.Name != "b" {
		t.Fatal("third should be tool b")
	}
}

func TestApplyToolCachePoints_NoCacheControl(t *testing.T) {
	tools := []anthropic.Tool{{Name: "a"}, {Name: "b"}}
	entries := []kiroproto.ToolEntry{
		{ToolSpecification: &kiroproto.ToolSpecification{Name: "a"}},
		{ToolSpecification: &kiroproto.ToolSpecification{Name: "b"}},
	}
	got := ApplyToolCachePoints(tools, entries)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
}
