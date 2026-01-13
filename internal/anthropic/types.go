package anthropic

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// Request represents an incoming Anthropic Messages API request.
type Request struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	System        SystemPrompt    `json:"system"`
	Tools         []Tool          `json:"tools,omitempty"`
	MaxTokens     int             `json:"max_tokens"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream"`
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`
}

// ThinkingConfig represents the thinking configuration in the Anthropic API.
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitzero"`
	// reasoning_effort is sent by Claude Code (e.g. --effort high/low).
	// Values: "high", "medium", "low" (or empty).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Thinking type constants.
const (
	ThinkingTypeEnabled  = "enabled"
	ThinkingTypeAdaptive = "adaptive"
)

// ReasoningEffort constants (sent by Claude Code via --effort flag).
const (
	ReasoningEffortHigh   = "high"
	ReasoningEffortMedium = "medium"
	ReasoningEffortLow    = "low"
)

// Thinking budget tokens per reasoning effort level.
const (
	ThinkingBudgetHigh   = 31999
	ThinkingBudgetMedium = 10000
	ThinkingBudgetLow    = 4000
)

// IsThinkingEnabled reports whether extended thinking is enabled in the request.
// Anthropic API supports type "enabled" and "adaptive".
func (r *Request) IsThinkingEnabled() bool {
	if r.Thinking == nil {
		return false
	}
	return r.Thinking.Type == ThinkingTypeEnabled || r.Thinking.Type == ThinkingTypeAdaptive
}

// Message represents a single message in the conversation.
type Message struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

// MessageContent is a union type: either a plain string or []ContentBlock.
type MessageContent struct {
	Text   string         // set when content is a plain string
	Blocks []ContentBlock // set when content is an array of content blocks
}

// IsString reports whether the content is a plain string.
func (mc MessageContent) IsString() bool {
	return mc.Blocks == nil
}

// String returns the text representation. For blocks, joins text blocks with space.
func (mc MessageContent) String() string {
	if mc.IsString() {
		return mc.Text
	}
	var s string
	for _, b := range mc.Blocks {
		if b.Type == "text" {
			if s != "" {
				s += " "
			}
			s += b.Text
		}
	}
	return s
}

func (mc MessageContent) MarshalJSONTo(enc *jsontext.Encoder) error {
	if mc.IsString() {
		return json.MarshalEncode(enc, mc.Text)
	}
	return json.MarshalEncode(enc, mc.Blocks)
}

func (mc *MessageContent) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	switch dec.PeekKind() {
	case '"':
		return json.UnmarshalDecode(dec, &mc.Text)
	case '[':
		return json.UnmarshalDecode(dec, &mc.Blocks)
	default:
		return fmt.Errorf("unexpected content type: %v", dec.PeekKind())
	}
}

// ContentBlock represents a single content block within a message.
type ContentBlock struct {
	Type string `json:"type"`

	// text block
	Text string `json:"text,omitempty"`

	// thinking block
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// tool_use block (assistant)
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result block (user)
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   MessageContent `json:"content"`
	IsError   bool           `json:"is_error,omitzero"`

	// image block
	Source *ImageSource `json:"source,omitempty"`

	// tool_reference block
	ToolName string `json:"tool_name,omitempty"`

	// cache_control (prompt caching)
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ImageSource represents the source of an image content block.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// CacheControl represents cache control settings for prompt caching.
type CacheControl struct {
	Type string `json:"type"`
}

// Tool represents a tool definition in the Anthropic API.
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl *CacheControl  `json:"cache_control,omitempty"`
}

// SystemPrompt is a union type: either a plain string or []SystemBlock.
type SystemPrompt struct {
	Text   string        // set when system is a plain string
	Blocks []SystemBlock // set when system is an array
}

// IsEmpty reports whether the system prompt is empty.
func (sp SystemPrompt) IsEmpty() bool {
	return sp.Text == "" && len(sp.Blocks) == 0
}

func (sp SystemPrompt) MarshalJSONTo(enc *jsontext.Encoder) error {
	if len(sp.Blocks) > 0 {
		return json.MarshalEncode(enc, sp.Blocks)
	}
	if sp.Text != "" {
		return json.MarshalEncode(enc, sp.Text)
	}
	return enc.WriteToken(jsontext.Null)
}

func (sp *SystemPrompt) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	switch dec.PeekKind() {
	case 'n':
		_, err := dec.ReadToken()
		return err
	case '"':
		return json.UnmarshalDecode(dec, &sp.Text)
	case '[':
		return json.UnmarshalDecode(dec, &sp.Blocks)
	default:
		return fmt.Errorf("unexpected system type: %v", dec.PeekKind())
	}
}

// SystemBlock represents a single block in an array-form system prompt.
type SystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}
