package respconv

import (
	"context"
	"sync"
	"time"
)

// TruncationType indicates what kind of truncation was detected.
type TruncationType int

const (
	TruncationNone    TruncationType = iota
	TruncationContent                // content was cut off (no metadataEvent, has text, no tool use)
	TruncationTool                   // tool arguments JSON parse failed
)

// TruncationInfo holds details about a detected truncation.
type TruncationInfo struct {
	Type      TruncationType
	CreatedAt time.Time
}

// TruncationInput holds the fields needed for truncation detection.
type TruncationInput struct {
	HasMetadata     bool
	HasContextUsage bool
	HasText         bool
	HasToolUse      bool
	ToolParseError  bool
	LocalStop       bool // adapter-side stop (stop_sequence / max_tokens) — not a truncation
}

// DetectTruncation checks if the stream was truncated.
func DetectTruncation(input TruncationInput) TruncationType {
	// Adapter-side stop (stop_sequence / max_tokens) is intentional, not truncation.
	if input.LocalStop {
		return TruncationNone
	}
	if input.ToolParseError {
		return TruncationTool
	}
	// metadataEvent or contextUsageEvent means the stream completed normally.
	if !input.HasMetadata && !input.HasContextUsage && input.HasText && !input.HasToolUse {
		return TruncationContent
	}
	return TruncationNone
}

// ContentTruncationNotice is injected into the next request's message history.
const ContentTruncationNotice = "[System Notice] Your previous response was truncated by the upstream API. Please continue from where you left off."

// ToolTruncationNotice is injected into the next request's message history.
const ToolTruncationNotice = "[API Limitation] Your tool call was truncated by the upstream API. Please retry with a shorter input."

const defaultTTL = 10 * time.Minute

// TruncationStore is a thread-safe in-memory cache mapping conversationId to truncation info.
// Entries expire after a TTL and are lazily evicted.
type TruncationStore struct {
	mu    sync.Mutex
	store map[string]TruncationInfo
	ttl   time.Duration
}

// NewTruncationStore creates a new TruncationStore with the default TTL.
// A background goroutine periodically evicts expired entries until ctx is cancelled.
func NewTruncationStore(ctx context.Context) *TruncationStore {
	ts := &TruncationStore{store: make(map[string]TruncationInfo), ttl: defaultTTL}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ts.Evict()
			case <-ctx.Done():
				return
			}
		}
	}()
	return ts
}

// Set stores truncation info for a conversation.
func (ts *TruncationStore) Set(conversationID string, info TruncationInfo) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	info.CreatedAt = time.Now()
	ts.store[conversationID] = info
}

// Pop retrieves and removes truncation info for a conversation.
// Returns false if the entry does not exist or has expired.
func (ts *TruncationStore) Pop(conversationID string) (TruncationInfo, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	info, ok := ts.store[conversationID]
	if !ok {
		return TruncationInfo{}, false
	}
	delete(ts.store, conversationID)
	if time.Since(info.CreatedAt) > ts.ttl {
		return TruncationInfo{}, false
	}
	return info, true
}

// Evict removes all expired entries. Call periodically to bound memory.
func (ts *TruncationStore) Evict() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := time.Now()
	for k, v := range ts.store {
		if now.Sub(v.CreatedAt) > ts.ttl {
			delete(ts.store, k)
		}
	}
}

// Len returns the number of entries (for testing).
func (ts *TruncationStore) Len() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.store)
}
