package kiroproto

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// Constants for Kiro API field values.
const (
	OriginKiroCLI           = "KIRO_CLI"
	ChatTriggerTypeManual   = "MANUAL"
	AgentTaskTypeVibe       = "vibe"
	ToolResultStatusSuccess = "success"
	ToolResultStatusError   = "error"
	ThinkingToolName        = "thinking"
)

// Payload is the top-level request body for the Kiro API.
type Payload struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileARN        string            `json:"profileArn,omitempty"`
}

// ConversationState holds the conversation context for the Kiro API.
type ConversationState struct {
	ConversationID  string         `json:"conversationId,omitempty"`
	ChatTriggerType string         `json:"chatTriggerType"`
	AgentTaskType   string         `json:"agentTaskType"`
	CurrentMessage  CurrentMessage `json:"currentMessage,omitzero"`
	History         []HistoryEntry `json:"history,omitempty"`
}

// CurrentMessage wraps the current user input message.
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage represents a user's input in the Kiro API.
type UserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId,omitempty"`
	Origin                  string                   `json:"origin,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
	Images                  []Image                  `json:"images,omitempty"`
	CachePoint              *CachePoint              `json:"cachePoint,omitempty"`
}

// UserInputMessageContext holds tools and tool results.
type UserInputMessageContext struct {
	Tools       []ToolEntry  `json:"tools,omitempty"`
	ToolResults []ToolResult `json:"toolResults,omitempty"`
}

// ToolEntry is a union type in the tools array: either a toolSpecification or a cachePoint.
type ToolEntry struct {
	ToolSpecification *ToolSpecification `json:"toolSpecification,omitempty"`
	CachePoint        *CachePoint        `json:"cachePoint,omitempty"`
}

// MarshalJSONTo produces either {"toolSpecification": ...} or {"cachePoint": ...}.
func (te ToolEntry) MarshalJSONTo(enc *jsontext.Encoder) error {
	if te.CachePoint != nil {
		return json.MarshalEncode(enc, struct {
			CachePoint *CachePoint `json:"cachePoint"`
		}{CachePoint: te.CachePoint})
	}
	return json.MarshalEncode(enc, struct {
		ToolSpecification *ToolSpecification `json:"toolSpecification"`
	}{ToolSpecification: te.ToolSpecification})
}

// ToolSpecification defines a tool in the Kiro API.
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema wraps the JSON schema for a tool's input.
type InputSchema struct {
	JSON map[string]any `json:"json"`
}

// ToolResult represents a tool execution result.
type ToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Status    string              `json:"status"`
	Content   []ToolResultContent `json:"content"`
}

// ToolResultContent holds the content of a tool result.
// Exactly one of Text or JSON should be set.
type ToolResultContent struct {
	Text string         `json:"text,omitempty"`
	JSON map[string]any `json:"json,omitempty"`
}

// Image represents an image in the Kiro API.
type Image struct {
	Format string      `json:"format"`
	Source ImageSource `json:"source,omitzero"`
}

// ImageSource holds the base64-encoded image bytes.
type ImageSource struct {
	Bytes string `json:"bytes"`
}

// CachePoint represents a cache point marker.
type CachePoint struct {
	Type string `json:"type"`
}

// HistoryEntry is a union: either userInputMessage or assistantResponseMessage.
type HistoryEntry struct {
	UserInputMessage         *HistoryUserInputMessage  `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// MarshalJSONTo produces either {"userInputMessage": ...} or {"assistantResponseMessage": ...}.
func (he HistoryEntry) MarshalJSONTo(enc *jsontext.Encoder) error {
	if he.AssistantResponseMessage != nil {
		return json.MarshalEncode(enc, struct {
			AssistantResponseMessage *AssistantResponseMessage `json:"assistantResponseMessage"`
		}{AssistantResponseMessage: he.AssistantResponseMessage})
	}
	return json.MarshalEncode(enc, struct {
		UserInputMessage *HistoryUserInputMessage `json:"userInputMessage"`
	}{UserInputMessage: he.UserInputMessage})
}

// HistoryUserInputMessage is a user message within history.
type HistoryUserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId,omitempty"`
	Origin                  string                   `json:"origin,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
	CachePoint              *CachePoint              `json:"cachePoint,omitempty"`
}

// AssistantResponseMessage is an assistant message within history.
type AssistantResponseMessage struct {
	MessageID  string           `json:"messageId,omitempty"`
	Content    string           `json:"content"`
	ToolUses   []HistoryToolUse `json:"toolUses,omitempty"`
	CachePoint *CachePoint      `json:"cachePoint,omitempty"`
}

// HistoryToolUse represents a tool call in an assistant history message.
type HistoryToolUse struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	Input     any    `json:"input"`
}
