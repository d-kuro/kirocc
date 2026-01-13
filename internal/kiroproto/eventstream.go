package kiroproto

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"slices"
)

// Event represents a parsed event from the Kiro event stream.
type Event struct {
	Type         string // event type from header
	Content      string // assistantResponseEvent → content field
	ThinkingText string // reasoningContentEvent → text field
	ToolName     string // toolUseEvent
	ToolUseID    string
	ToolInput    string
	ToolStop     bool
	Credits      float64 // meteringEvent → usage field
	InputTokens  int     // meteringEvent or metadataEvent
	OutputTokens int     // meteringEvent or metadataEvent
	// metadataEvent token breakdown
	UncachedInputTokens   int
	CacheReadInputTokens  int
	CacheWriteInputTokens int
	TotalTokens           int
	// reasoningContentEvent
	Signature       string
	RedactedContent string // base64-encoded
	// contextUsageEvent
	ContextUsagePercentage float64
	// messageMetadataEvent
	ConversationID string
	UtteranceID    string
	// invalidStateEvent
	InvalidStateReason string
	ErrorMessage       string
}

// ErrorText returns the error message, falling back to InvalidStateReason.
func (e Event) ErrorText() string {
	if e.ErrorMessage != "" {
		return e.ErrorMessage
	}
	return e.InvalidStateReason
}

// Kiro API event type constants.
const (
	EventAssistantResponse  = "assistantResponseEvent"
	EventReasoningContent   = "reasoningContentEvent"
	EventToolUse            = "toolUseEvent"
	EventMetadata           = "metadataEvent"
	EventMetering           = "meteringEvent"
	EventInvalidState       = "invalidStateEvent"
	EventException          = "exception"
	EventMessageMetadata    = "messageMetadataEvent"
	EventFollowupPrompt     = "followupPromptEvent"
	EventCitation           = "citationEvent"
	EventCode               = "codeEvent"
	EventCodeReference      = "codeReferenceEvent"
	EventSupplementaryLinks = "supplementaryWebLinksEvent"
	EventIntents            = "intentsEvent"
	EventInteractionComps   = "interactionComponentsEvent"
	EventDryRunSucceed      = "dryRunSucceedEvent"
	EventContextUsage       = "contextUsageEvent"
)

// ParseStream reads AWS Event Stream binary frames from r and calls callback for each parsed Event.
// If callback returns true, parsing stops and ParseStream returns nil (early stop).
func ParseStream(ctx context.Context, r io.Reader, callback func(Event) bool) error {
	br := bufio.NewReader(r)
	var acc toolUseAccumulator

	for {
		// Check for context cancellation before reading the next frame.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		headers, payload, err := readFrame(br)
		if err == io.EOF {
			// Flush any in-progress tool call that never received a stop frame.
			if event, ok := acc.flush(); ok {
				callback(event)
			}
			return nil
		}
		if err != nil {
			return err
		}

		msgType, eventType := extractFrameHeaders(headers)

		// Handle exception frames (stream-level errors).
		if msgType == "exception" {
			var m struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(payload, &m); err != nil {
				slog.Warn("kiro: failed to decode exception payload", "err", err)
			}
			callback(Event{
				Type:               EventException,
				ErrorMessage:       m.Message,
				InvalidStateReason: eventType,
			})
			return nil
		}

		var stop bool

		switch eventType {
		case EventAssistantResponse:
			var m struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(payload, &m); err != nil {
				return fmt.Errorf("decode %s: %w", eventType, err)
			}
			stop = callback(Event{Type: eventType, Content: m.Content})

		case EventReasoningContent:
			var m struct {
				Text            string `json:"text"`
				Signature       string `json:"signature"`
				RedactedContent string `json:"redactedContent"`
			}
			if err := json.Unmarshal(payload, &m); err != nil {
				return fmt.Errorf("decode %s: %w", eventType, err)
			}
			stop = callback(Event{
				Type:            eventType,
				ThinkingText:    m.Text,
				Signature:       m.Signature,
				RedactedContent: m.RedactedContent,
			})

		case EventToolUse:
			var raw map[string]jsontext.Value
			if err := json.Unmarshal(payload, &raw); err != nil {
				slog.Warn("kiro: failed to decode toolUseEvent payload", "err", err)
				continue
			}
			if slices.ContainsFunc(acc.update(raw), callback) {
				stop = true
			}

		case EventMetadata:
			var m struct {
				TokenUsage struct {
					UncachedInputTokens   int `json:"uncachedInputTokens"`
					OutputTokens          int `json:"outputTokens"`
					TotalTokens           int `json:"totalTokens"`
					CacheReadInputTokens  int `json:"cacheReadInputTokens"`
					CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
				} `json:"tokenUsage"`
			}
			if err := json.Unmarshal(payload, &m); err == nil {
				tu := m.TokenUsage
				stop = callback(Event{
					Type:                  eventType,
					UncachedInputTokens:   tu.UncachedInputTokens,
					CacheReadInputTokens:  tu.CacheReadInputTokens,
					CacheWriteInputTokens: tu.CacheWriteInputTokens,
					OutputTokens:          tu.OutputTokens,
					TotalTokens:           tu.TotalTokens,
					InputTokens:           tu.UncachedInputTokens + tu.CacheReadInputTokens,
				})
			} else {
				slog.Warn("kiro: failed to decode event", "type", eventType, "err", err)
			}

		case EventMetering:
			var m struct {
				Usage        float64 `json:"usage"`
				InputTokens  int     `json:"inputTokens"`
				OutputTokens int     `json:"outputTokens"`
			}
			if err := json.Unmarshal(payload, &m); err == nil {
				stop = callback(Event{
					Type:         eventType,
					Credits:      m.Usage,
					InputTokens:  m.InputTokens,
					OutputTokens: m.OutputTokens,
				})
			} else {
				slog.Warn("kiro: failed to decode event", "type", eventType, "err", err)
			}

		case EventInvalidState:
			var m struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(payload, &m); err != nil {
				return fmt.Errorf("decode %s: %w", eventType, err)
			}
			stop = callback(Event{
				Type:               eventType,
				InvalidStateReason: m.Reason,
				ErrorMessage:       m.Message,
			})

		case EventMessageMetadata:
			var m struct {
				ConversationID string `json:"conversationId"`
				UtteranceID    string `json:"utteranceId"`
			}
			if err := json.Unmarshal(payload, &m); err == nil {
				stop = callback(Event{
					Type:           eventType,
					ConversationID: m.ConversationID,
					UtteranceID:    m.UtteranceID,
				})
			} else {
				slog.Warn("kiro: failed to decode event", "type", eventType, "err", err)
			}

		case EventContextUsage:
			var m struct {
				ContextUsagePercentage float64 `json:"contextUsagePercentage"`
			}
			if err := json.Unmarshal(payload, &m); err == nil {
				stop = callback(Event{
					Type:                   eventType,
					ContextUsagePercentage: m.ContextUsagePercentage,
				})
			} else {
				slog.Warn("kiro: failed to decode event", "type", eventType, "err", err)
			}

		case EventFollowupPrompt, EventCitation, EventCode, EventCodeReference,
			EventSupplementaryLinks, EventIntents, EventInteractionComps,
			EventDryRunSucceed:
			// Known no-op events — silently ignore.

		default:
			if eventType != "" {
				p := string(payload[:min(len(payload), 200)])
				if len(payload) > 200 {
					p += "..."
				}
				slog.Warn("kiro: unknown event type (ignored)", "type", eventType, "payload", p)
			}
		}

		if stop {
			return nil
		}
	}
}
