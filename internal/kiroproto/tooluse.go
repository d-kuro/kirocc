package kiroproto

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// toolUseAccumulator manages state across multiple toolUseEvent frames.
type toolUseAccumulator struct {
	toolName  string
	toolUseID string
	toolInput strings.Builder
}

// update processes a raw toolUseEvent payload. Returns completed events.
// A stop frame produces one event. A new toolUseId arriving while a previous
// tool is in-progress (missing stop) also flushes the previous tool.
func (a *toolUseAccumulator) update(raw map[string]jsontext.Value) []Event {
	// Extract toolUseId to detect new tool calls.
	var currentID string
	if idRaw, ok := raw["toolUseId"]; ok {
		if err := json.Unmarshal(idRaw, &currentID); err != nil {
			slog.Warn("kiro: failed to decode toolUseId", "err", err)
		}
	}

	// Detect new tool call.
	isNewTool := false
	if currentID != "" && currentID != a.toolUseID {
		isNewTool = true
	} else if currentID == "" && a.toolUseID == "" {
		if _, hasName := raw["name"]; hasName {
			isNewTool = true
			currentID = uuid.New().String()
		}
	}

	var events []Event

	if isNewTool {
		// Flush previous in-progress tool if it never received a stop frame.
		if a.toolUseID != "" {
			slog.Info("kiro: toolUseEvent missing stop frame, flushing",
				"tool_name", a.toolName, "tool_use_id", a.toolUseID)
			events = append(events, a.buildAndReset())
		}
		a.toolUseID = currentID
		a.toolInput.Reset()
		a.toolName = "" // Reset to avoid leaking previous tool's name.
	}

	// Always update name when present (may arrive in a later frame).
	if nameRaw, ok := raw["name"]; ok {
		if err := json.Unmarshal(nameRaw, &a.toolName); err != nil {
			slog.Warn("kiro: failed to decode tool name", "err", err)
		}
	}

	if inputRaw, ok := raw["input"]; ok {
		if len(inputRaw) > 0 && inputRaw[0] == '"' {
			var s string
			if err := json.Unmarshal(inputRaw, &s); err == nil {
				a.toolInput.WriteString(s)
			}
		} else {
			a.toolInput.Reset()
			a.toolInput.Write(inputRaw)
		}
	}

	if stopRaw, ok := raw["stop"]; ok {
		var stop bool
		if err := json.Unmarshal(stopRaw, &stop); err == nil && stop {
			events = append(events, a.buildAndReset())
		}
	}

	return events
}

// flush returns a synthetic stop event for any in-progress tool call.
// Called at stream EOF when the upstream omits the stop frame (e.g. ExitPlanMode with empty input).
func (a *toolUseAccumulator) flush() (Event, bool) {
	if a.toolUseID == "" {
		return Event{}, false
	}
	slog.Info("kiro: toolUseEvent missing stop frame, flushing",
		"tool_name", a.toolName, "tool_use_id", a.toolUseID)
	return a.buildAndReset(), true
}

// buildAndReset emits the current tool as a completed event and resets accumulator state.
func (a *toolUseAccumulator) buildAndReset() Event {
	event := Event{
		Type:      EventToolUse,
		ToolName:  a.toolName,
		ToolUseID: a.toolUseID,
		ToolInput: a.toolInput.String(),
		ToolStop:  true,
	}
	a.toolName, a.toolUseID = "", ""
	a.toolInput.Reset()
	return event
}
