package respconv

import (
	"context"
	"testing"
	"time"
)

func TestDetectTruncation(t *testing.T) {
	tests := []struct {
		name  string
		input TruncationInput
		want  TruncationType
	}{
		{"none", TruncationInput{HasMetadata: true, HasText: true}, TruncationNone},
		{"content", TruncationInput{HasText: true}, TruncationContent},
		{"tool", TruncationInput{HasMetadata: true, HasText: true, HasToolUse: true, ToolParseError: true}, TruncationTool},
		{"no_text", TruncationInput{}, TruncationNone},
		{"has_tool_use_no_metadata", TruncationInput{HasText: true, HasToolUse: true}, TruncationNone},
		{"context_usage_prevents_truncation", TruncationInput{HasContextUsage: true, HasText: true}, TruncationNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectTruncation(tt.input)
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTruncationStore_SetAndPop(t *testing.T) {
	store := NewTruncationStore(context.Background())
	store.Set("conv-1", TruncationInfo{Type: TruncationContent})

	info, ok := store.Pop("conv-1")
	if !ok {
		t.Fatal("expected ok")
	}
	if info.Type != TruncationContent {
		t.Fatalf("got %d, want TruncationContent", info.Type)
	}

	// Second pop should return false
	_, ok = store.Pop("conv-1")
	if ok {
		t.Fatal("expected not ok after pop")
	}
}

func TestTruncationStore_PopMissing(t *testing.T) {
	store := NewTruncationStore(context.Background())
	_, ok := store.Pop("nonexistent")
	if ok {
		t.Fatal("expected not ok")
	}
}

func TestTruncationStore_TTLExpiry(t *testing.T) {
	store := NewTruncationStore(context.Background())
	store.ttl = 1 * time.Millisecond
	store.Set("conv-1", TruncationInfo{Type: TruncationContent})

	time.Sleep(5 * time.Millisecond)

	_, ok := store.Pop("conv-1")
	if ok {
		t.Fatal("expected expired entry to return not ok")
	}
}

func TestTruncationStore_Evict(t *testing.T) {
	store := NewTruncationStore(context.Background())
	store.ttl = 1 * time.Millisecond
	store.Set("conv-1", TruncationInfo{Type: TruncationContent})
	store.Set("conv-2", TruncationInfo{Type: TruncationTool})

	time.Sleep(5 * time.Millisecond)

	// Add a fresh entry.
	store.Set("conv-3", TruncationInfo{Type: TruncationContent})

	store.Evict()
	if store.Len() != 1 {
		t.Fatalf("expected 1 entry after evict, got %d", store.Len())
	}
}

func TestTruncationStore_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := NewTruncationStore(ctx)
	store.Set("conv-1", TruncationInfo{Type: TruncationContent})
	cancel()
	// After cancel, store should still work for Set/Pop (goroutine just stops).
	info, ok := store.Pop("conv-1")
	if !ok {
		t.Fatal("expected ok")
	}
	if info.Type != TruncationContent {
		t.Fatalf("got %d", info.Type)
	}
}
