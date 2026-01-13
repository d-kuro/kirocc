package kiroproto

import (
	"encoding/json/v2"
	"testing"
)

func TestPayload_MarshalJSON_Minimal(t *testing.T) {
	p := Payload{
		ConversationState: ConversationState{
			ChatTriggerType: "MANUAL",
			AgentTaskType:   "vibe",
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: "Hello",
					ModelID: "claude-sonnet-4.6",
					Origin:  "AI_EDITOR",
				},
			},
		},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// profileArn should be omitted
	if _, ok := m["profileArn"]; ok {
		t.Fatal("profileArn should be omitted when empty")
	}
	cs := m["conversationState"].(map[string]any)
	if cs["agentTaskType"] != "vibe" {
		t.Fatalf("agentTaskType = %v", cs["agentTaskType"])
	}
	if cs["chatTriggerType"] != "MANUAL" {
		t.Fatalf("chatTriggerType = %v", cs["chatTriggerType"])
	}
	// history should be omitted
	if _, ok := cs["history"]; ok {
		t.Fatal("history should be omitted when nil")
	}
}

func TestPayload_MarshalJSON_WithHistory(t *testing.T) {
	p := Payload{
		ConversationState: ConversationState{
			ConversationID:  "test-id",
			ChatTriggerType: "MANUAL",
			AgentTaskType:   "vibe",
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: "Continue",
					ModelID: "claude-sonnet-4.6",
					Origin:  "AI_EDITOR",
				},
			},
			History: []HistoryEntry{
				{
					UserInputMessage: &HistoryUserInputMessage{
						Content: "Hello",
						ModelID: "claude-sonnet-4.6",
						Origin:  "AI_EDITOR",
					},
				},
				{
					AssistantResponseMessage: &AssistantResponseMessage{
						Content: "Hi there",
					},
				},
			},
		},
		ProfileARN: "arn:aws:test",
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["profileArn"] != "arn:aws:test" {
		t.Fatalf("profileArn = %v", m["profileArn"])
	}
	cs := m["conversationState"].(map[string]any)
	history := cs["history"].([]any)
	if len(history) != 2 {
		t.Fatalf("history len = %d", len(history))
	}
	// First entry should have userInputMessage
	h0 := history[0].(map[string]any)
	if _, ok := h0["userInputMessage"]; !ok {
		t.Fatal("history[0] should have userInputMessage")
	}
	if _, ok := h0["assistantResponseMessage"]; ok {
		t.Fatal("history[0] should not have assistantResponseMessage")
	}
	// Second entry should have assistantResponseMessage
	h1 := history[1].(map[string]any)
	if _, ok := h1["assistantResponseMessage"]; !ok {
		t.Fatal("history[1] should have assistantResponseMessage")
	}
	if _, ok := h1["userInputMessage"]; ok {
		t.Fatal("history[1] should not have userInputMessage")
	}
}

func TestToolEntry_MarshalJSON_Specification(t *testing.T) {
	te := ToolEntry{
		ToolSpecification: &ToolSpecification{
			Name:        "get_weather",
			Description: "Get weather",
			InputSchema: InputSchema{JSON: map[string]any{"type": "object"}},
		},
	}
	data, err := json.Marshal(te)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["toolSpecification"]; !ok {
		t.Fatal("expected toolSpecification key")
	}
	if _, ok := m["cachePoint"]; ok {
		t.Fatal("should not have cachePoint")
	}
}

func TestToolEntry_MarshalJSON_CachePoint(t *testing.T) {
	te := ToolEntry{
		CachePoint: &CachePoint{Type: "default"},
	}
	data, err := json.Marshal(te)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["cachePoint"]; !ok {
		t.Fatal("expected cachePoint key")
	}
	if _, ok := m["toolSpecification"]; ok {
		t.Fatal("should not have toolSpecification")
	}
}

func TestAssistantResponseMessage_WithToolUses(t *testing.T) {
	arm := AssistantResponseMessage{
		Content: "I'll check.",
		ToolUses: []HistoryToolUse{
			{ToolUseID: "toolu_01", Name: "get_weather", Input: map[string]any{"city": "Tokyo"}},
		},
	}
	data, err := json.Marshal(arm)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	toolUses := m["toolUses"].([]any)
	if len(toolUses) != 1 {
		t.Fatalf("toolUses len = %d", len(toolUses))
	}
	tu := toolUses[0].(map[string]any)
	if tu["toolUseId"] != "toolu_01" {
		t.Fatalf("toolUseId = %v", tu["toolUseId"])
	}
}

func TestUserInputMessage_OmitEmpty(t *testing.T) {
	uim := UserInputMessage{
		Content: "Hello",
		ModelID: "model",
		Origin:  "AI_EDITOR",
	}
	data, err := json.Marshal(uim)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// These should be omitted
	for _, key := range []string{"userInputMessageContext", "images", "cachePoint"} {
		if _, ok := m[key]; ok {
			t.Fatalf("%s should be omitted when empty", key)
		}
	}
}
