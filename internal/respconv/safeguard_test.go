package respconv

import (
	"encoding/json/v2"
	"testing"
)

// The assertions here mirror Claude Code 2.1.278's own parser: it reads
// safeguard_results as an array, keeps exactly the one entry whose type is
// "dangerous_tool_use" (zero or more than one is treated as no result), then
// requires status.type "available" with a tool_uses map. Each entry must be
// "evaluated" with outcome "flagged"/"not_flagged", "skipped", or
// "unavailable". Anything else the client reads as unrecognised and falls back
// to its own classifier.
func TestSafeguardResultsShape(t *testing.T) {
	got := SafeguardResults(map[string]SafeguardStatus{
		"toolu_1": {Outcome: SafeguardNotFlagged},
		"toolu_2": {Outcome: SafeguardFlagged, Explanation: "rated potential harm 2.40 of 3"},
		"toolu_3": {Skip: true},
	})

	if len(got) != 1 {
		t.Fatalf("want exactly one entry (the client discards a list with more), got %d", len(got))
	}
	entry, ok := got[0].(map[string]any)
	if !ok {
		t.Fatalf("entry is %T, want map", got[0])
	}
	if entry["type"] != "dangerous_tool_use" {
		t.Fatalf("type = %v, want dangerous_tool_use", entry["type"])
	}
	status, ok := entry["status"].(map[string]any)
	if !ok {
		t.Fatalf("status is %T, want map", entry["status"])
	}
	if status["type"] != "available" {
		t.Fatalf("status.type = %v, want available", status["type"])
	}
	toolUses, ok := status["tool_uses"].(map[string]any)
	if !ok {
		t.Fatalf("tool_uses is %T, want map", status["tool_uses"])
	}
	if len(toolUses) != 3 {
		t.Fatalf("tool_uses has %d entries, want 3", len(toolUses))
	}

	cleared := toolUses["toolu_1"].(map[string]any)
	if cleared["type"] != "evaluated" || cleared["outcome"] != "not_flagged" {
		t.Errorf("cleared entry = %v", cleared)
	}
	if _, present := cleared["explanation"]; present {
		t.Error("an explanation-free verdict must omit the key, not send an empty string")
	}

	blocked := toolUses["toolu_2"].(map[string]any)
	if blocked["type"] != "evaluated" || blocked["outcome"] != "flagged" {
		t.Errorf("blocked entry = %v", blocked)
	}
	if blocked["explanation"] != "rated potential harm 2.40 of 3" {
		t.Errorf("explanation = %v", blocked["explanation"])
	}

	deferred := toolUses["toolu_3"].(map[string]any)
	if deferred["type"] != "skipped" {
		t.Errorf("deferred entry = %v, want type skipped", deferred)
	}
	if _, present := deferred["outcome"]; present {
		t.Error("a skipped entry must not carry an outcome")
	}
}

// A response with no tool calls still answers. Omitting the key entirely reads
// to the client as server_no_result, which stops it asking for the rest of the
// session — so the empty case has to serialise as a well-formed available
// result with an empty (not null) map.
func TestSafeguardResultsEmptyIsStillAvailable(t *testing.T) {
	b, err := json.Marshal(SafeguardResults(nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Compared after decoding: map key order is not part of the contract, and
	// asserting on it would break on an unrelated encoder change.
	var got []struct {
		Type   string `json:"type"`
		Status struct {
			Type     string         `json:"type"`
			ToolUses map[string]any `json:"tool_uses"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	if len(got) != 1 {
		t.Fatalf("want one entry, got %d: %s", len(got), b)
	}
	if got[0].Type != "dangerous_tool_use" || got[0].Status.Type != "available" {
		t.Fatalf("unexpected shape: %s", b)
	}
	if got[0].Status.ToolUses == nil {
		t.Fatalf("tool_uses must serialise as an empty object, not null: %s", b)
	}
	if len(got[0].Status.ToolUses) != 0 {
		t.Fatalf("want no entries, got %d: %s", len(got[0].Status.ToolUses), b)
	}
}

func TestSafeguardSkipAll(t *testing.T) {
	got := SafeguardSkipAll([]string{"toolu_a", "toolu_b"})
	toolUses := got[0].(map[string]any)["status"].(map[string]any)["tool_uses"].(map[string]any)
	if len(toolUses) != 2 {
		t.Fatalf("want 2 entries, got %d", len(toolUses))
	}
	for id, v := range toolUses {
		if v.(map[string]any)["type"] != "skipped" {
			t.Errorf("%s = %v, want skipped", id, v)
		}
	}
}

// Round-trips through JSON the way the client will read it, so a change to the
// builders that breaks the parser's expectations shows up here.
func TestSafeguardResultsSurvivesJSON(t *testing.T) {
	b, err := json.Marshal(SafeguardResults(map[string]SafeguardStatus{
		"toolu_1": {Outcome: SafeguardNotFlagged},
		"toolu_2": {Skip: true},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []struct {
		Type   string `json:"type"`
		Status struct {
			Type     string `json:"type"`
			ToolUses map[string]struct {
				Type        string `json:"type"`
				Outcome     string `json:"outcome"`
				Explanation string `json:"explanation"`
			} `json:"tool_uses"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	if len(got) != 1 || got[0].Type != "dangerous_tool_use" || got[0].Status.Type != "available" {
		t.Fatalf("unexpected shape: %s", b)
	}
	if e := got[0].Status.ToolUses["toolu_1"]; e.Type != "evaluated" || e.Outcome != "not_flagged" {
		t.Errorf("toolu_1 = %+v", e)
	}
	if e := got[0].Status.ToolUses["toolu_2"]; e.Type != "skipped" || e.Outcome != "" {
		t.Errorf("toolu_2 = %+v", e)
	}
}
