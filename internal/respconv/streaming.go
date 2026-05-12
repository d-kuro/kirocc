package respconv

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/kiroproto"
	"github.com/google/uuid"
)

// SSEWriter writes Anthropic-compatible SSE events to an http.ResponseWriter.
type SSEWriter struct {
	ctx        context.Context
	w          http.ResponseWriter
	flusher    http.Flusher
	model      string
	msgID      string
	blockIndex int
	activeType string // "thinking", "text", "tool_use", or ""
	started    bool
	writeErr   bool // true if a write/flush to the client failed
	acc        responseAccumulator

	// OnVisibleOutput is called once, just before the first visible output
	// (text delta or tool_use) is written. Used by GateWriter to promote
	// the buffered writer to direct mode.
	OnVisibleOutput func()
	visibleFired    bool
}

// NewSSEWriter creates a new SSEWriter and sets response headers.
func NewSSEWriter(ctx context.Context, w http.ResponseWriter, model string, contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int) *SSEWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	f, _ := w.(http.Flusher)
	sw := &SSEWriter{
		ctx:        ctx,
		w:          w,
		flusher:    f,
		model:      model,
		msgID:      "msg_" + uuid.New().String()[:24],
		blockIndex: -1,
		acc:        newAccumulator(contextWindowSize, stopSequences, maxTokens, preCountedInputTokens),
	}
	return sw
}

// Started reports whether the SSE stream has been started (message_start sent).
func (s *SSEWriter) Started() bool {
	return s.started
}

// LocalStop reports whether the stream was stopped by adapter-side logic (stop_sequence / max_tokens).
func (s *SSEWriter) LocalStop() bool {
	return s.acc.LocalStop
}

// WriteErr reports whether a write to the client failed (client likely disconnected).
func (s *SSEWriter) WriteErr() bool {
	return s.writeErr
}

// HandleEvent processes a single Kiro event and writes SSE events.
// Returns true if the stream should be terminated (error or adapter-side stop).
func (s *SSEWriter) HandleEvent(e kiroproto.Event) bool {
	d := s.acc.ProcessEvent(e)

	switch e.Type {
	case kiroproto.EventAssistantResponse:
		// Handle thinking delta from tag parsing.
		if d.ThinkingDelta != "" {
			s.writeThinkingDelta(d)
		}
		// Handle text delta.
		if d.TextDelta != "" {
			s.ensureStarted()
			s.fireVisibleOutput()
			s.switchBlock(anthropic.BlockTypeText)
			s.writeDelta("text_delta", "text", d.TextDelta)
		}
		if d.StopSignal {
			s.Finish()
			return true
		}

	case kiroproto.EventReasoningContent:
		if d.RedactedContent != "" {
			s.ensureStarted()
			s.closeActiveBlock()
			s.blockIndex++
			s.activeType = anthropic.BlockTypeRedactedThinking
			s.writeSSE("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": s.blockIndex,
				"content_block": map[string]any{
					"type": anthropic.BlockTypeRedactedThinking,
					"data": d.RedactedContent,
				},
			})
			s.closeActiveBlock()
			return false
		}
		if d.ThinkingDelta == "" {
			return false
		}
		s.writeThinkingDelta(d)
		if d.StopSignal {
			s.Finish()
			return true
		}

	case kiroproto.EventToolUse:
		if d.ThinkingDelta != "" {
			s.writeThinkingDelta(d)
			if d.StopSignal {
				s.Finish()
				return true
			}
			return false
		}
		if !d.ToolStop {
			return false
		}
		s.ensureStarted()
		s.fireVisibleOutput()
		s.closeActiveBlock()
		s.blockIndex++
		s.activeType = anthropic.BlockTypeToolUse
		s.writeSSE("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": s.blockIndex,
			"content_block": map[string]any{
				"type":  anthropic.BlockTypeToolUse,
				"id":    d.ToolUseID,
				"name":  d.ToolName,
				"input": map[string]any{},
			},
		})
		s.writeSSE("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": s.blockIndex,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": d.ToolInput,
			},
		})
		if d.StopSignal {
			s.Finish()
			return true
		}

	case kiroproto.EventInvalidState, kiroproto.EventException:
		if !s.started {
			return true
		}
		errType := "invalid_state"
		if e.Type == kiroproto.EventException {
			errType = "api_error"
		}
		s.WriteError(errType, d.ErrorMessage)
		return true
	}
	return false
}

// WriteError writes an error SSE event to the stream.
func (s *SSEWriter) WriteError(errType, message string) {
	s.closeActiveBlock()
	s.writeSSE("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
}

// Finish writes the closing SSE events (message_delta + message_stop).
func (s *SSEWriter) Finish() {
	s.ensureStarted()

	textDelta, thinkingDelta, res := finalizeResult(&s.acc)
	if thinkingDelta != "" {
		s.writeThinkingDelta(EventDelta{ThinkingDelta: thinkingDelta})
	}
	if textDelta != "" {
		s.fireVisibleOutput()
		s.switchBlock(anthropic.BlockTypeText)
		s.writeDelta("text_delta", "text", textDelta)
	}

	s.closeActiveBlock()

	// Do NOT inject an empty text block here. If this is a thinking-only
	// response, the caller (GateWriter) will detect it via IsEmptyVisibleEndTurn
	// and retry the request instead.

	s.writeSSE("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   res.StopReason,
			"stop_sequence": res.StopSequence,
		},
		"usage": res.Usage,
	})
	s.writeSSE("message_stop", map[string]any{
		"type": "message_stop",
	})
}

func (s *SSEWriter) ensureStarted() {
	if s.started {
		return
	}
	s.started = true
	s.writeSSE("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         s.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         s.acc.UsageMap(0, 0),
		},
	})
}

func (s *SSEWriter) switchBlock(blockType string) {
	if s.activeType == blockType {
		return
	}
	s.closeActiveBlock()
	s.blockIndex++
	s.activeType = blockType

	var contentBlock map[string]any
	switch blockType {
	case anthropic.BlockTypeThinking:
		contentBlock = map[string]any{
			"type":     anthropic.BlockTypeThinking,
			"thinking": "",
		}
		if s.acc.Signature != "" {
			contentBlock["signature"] = s.acc.Signature
		}
	case anthropic.BlockTypeText:
		contentBlock = map[string]any{
			"type": anthropic.BlockTypeText,
			"text": "",
		}
	}

	s.writeSSE("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.blockIndex,
		"content_block": contentBlock,
	})
}

func (s *SSEWriter) closeActiveBlock() {
	if s.activeType == "" {
		return
	}
	s.writeRawSSE("content_block_stop", `{"type":"content_block_stop","index":%d}`, s.blockIndex)
	s.activeType = ""
}

// writeBlock emits the content_block_start → [content_block_delta] → content_block_stop
// sequence for a single self-contained block (tool_use, server_tool_use, tool_search results).
// closes any previously active block first. delta may be nil when no delta event is needed.
func (s *SSEWriter) writeBlock(contentBlock, delta map[string]any) {
	s.closeActiveBlock()
	s.blockIndex++
	s.writeSSE("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.blockIndex,
		"content_block": contentBlock,
	})
	if delta != nil {
		s.writeSSE("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": s.blockIndex,
			"delta": delta,
		})
	}
	s.writeRawSSE("content_block_stop", `{"type":"content_block_stop","index":%d}`, s.blockIndex)
}

// Usage returns the best available input and output token counts.
func (s *SSEWriter) Usage() (inputTokens, outputTokens int) {
	return s.acc.resolvedUsage()
}

// CacheReadInputTokens returns the cache read input token count.
func (s *SSEWriter) CacheReadInputTokens() int { return s.acc.CacheReadInputTokens }

// CacheWriteInputTokens returns the cache write input token count.
func (s *SSEWriter) CacheWriteInputTokens() int { return s.acc.CacheWriteInputTokens }

// ContextUsagePercentage returns the context usage percentage from Kiro, or 0 if not received.
func (s *SSEWriter) ContextUsagePercentage() float64 { return s.acc.ContextUsagePercentage }

// HasContextUsage reports whether a contextUsageEvent was received.
func (s *SSEWriter) HasContextUsage() bool { return s.acc.HasContextUsage }

// Credits returns the per-response credit consumption from meteringEvent.
// The bool is false if no meteringEvent was received.
func (s *SSEWriter) Credits() (float64, bool) { return s.acc.Credits, s.acc.HasCredits }

// RecordTail forwards trailing metadata events (meteringEvent, contextUsageEvent)
// to the inner accumulator without writing anything to the SSE stream. Used by
// the tool-search orchestrator to keep collecting credit/context usage after the
// upstream tool-use frame is detected, so cumulative stats remain accurate.
func (s *SSEWriter) RecordTail(e kiroproto.Event) {
	switch e.Type {
	case kiroproto.EventMetering, kiroproto.EventContextUsage:
		s.acc.ProcessEvent(e)
	}
}

// writeThinkingDelta writes a thinking_delta SSE event using direct formatting.
func (s *SSEWriter) writeThinkingDelta(d EventDelta) {
	s.ensureStarted()
	s.switchBlock(anthropic.BlockTypeThinking)
	s.writeDelta("thinking_delta", "thinking", d.ThinkingDelta)
}

// fireVisibleOutput calls OnVisibleOutput once when the first visible content
// (text or tool_use) is about to be written.
func (s *SSEWriter) fireVisibleOutput() {
	if s.visibleFired {
		return
	}
	s.visibleFired = true
	if s.OnVisibleOutput != nil {
		s.OnVisibleOutput()
	}
}

// IsEmptyVisibleEndTurn reports whether the completed stream had thinking
// content but no visible text and no tool use.
func (s *SSEWriter) IsEmptyVisibleEndTurn() bool {
	return s.acc.IsEmptyVisibleEndTurn()
}

// ThinkingLen returns the length of accumulated thinking content.
func (s *SSEWriter) ThinkingLen() int {
	return s.acc.ThinkingBuf.Len()
}

// SetDropToolName sets the tool name to filter from accumulator recording.
func (s *SSEWriter) SetDropToolName(name string) {
	s.acc.DropToolName = name
}

// SetToolNameMap sets the short→original tool name map for response remapping.
func (s *SSEWriter) SetToolNameMap(m map[string]string) {
	s.acc.toolNameMap = m
}

// ResetAccumulator replaces the internal accumulator with a fresh one,
// preserving the SSEWriter's block index and started state for continuation.
func (s *SSEWriter) ResetAccumulator(contextWindowSize int, stopSequences []string, maxTokens int, preCountedInputTokens int) {
	filterName := s.acc.DropToolName
	nameMap := s.acc.toolNameMap
	s.acc = newAccumulator(contextWindowSize, stopSequences, maxTokens, preCountedInputTokens)
	s.acc.DropToolName = filterName
	s.acc.toolNameMap = nameMap
	s.activeType = ""
}

func (s *SSEWriter) writeSSE(eventType string, data map[string]any) {
	if s.writeErr {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		slog.ErrorContext(s.ctx, "SSE JSON marshal failed", "event", eventType, "err", err)
		return
	}
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, b)
	if err != nil {
		s.writeErr = true
		return
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// writeRawSSE writes a pre-formatted SSE event using fmt.Fprintf, avoiding map allocation and json.Marshal.
func (s *SSEWriter) writeRawSSE(eventType, format string, args ...any) {
	if s.writeErr {
		return
	}
	_, err := fmt.Fprintf(s.w, "event: "+eventType+"\ndata: "+format+"\n\n", args...)
	if err != nil {
		s.writeErr = true
		return
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// writeDelta writes a content_block_delta SSE event with a single string field.
func (s *SSEWriter) writeDelta(deltaType, fieldName, value string) {
	escaped, _ := json.Marshal(value)
	s.writeRawSSE("content_block_delta",
		`{"type":"content_block_delta","index":%d,"delta":{"type":"%s","%s":%s}}`,
		s.blockIndex, deltaType, fieldName, escaped)
}
