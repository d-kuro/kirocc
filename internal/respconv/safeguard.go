package respconv

import "github.com/d-kuro/kirocc/internal/anthropic"

// Auto mode's server-side classifier channel.
//
// When Claude Code runs in auto mode against a custom base URL it asks whoever
// answers /v1/messages to classify the tool uses that response is about to emit,
// by sending the "dangerous-tool-use-*" beta and a `safeguards` request field.
// It expects a `safeguard_results` payload back on the same response, keyed by
// the tool_use ids in that response. When it gets one it does not issue its own
// classifier request, which is the point: those requests are billed, pin Claude
// Sonnet 5 regardless of the session's model, and on a Kiro-backed session they
// are the single largest line of spend.
//
// The shapes below are the ones Claude Code 2.1.278 parses. They are not
// documented, so treat them as version-pinned: a release that changes the schema
// makes the client log "a completed response carried no classification result"
// and fall back to its own classifier. That degrades cost, never safety.
const (
	// SafeguardNotFlagged lets the action run.
	SafeguardNotFlagged = "not_flagged"
	// SafeguardFlagged blocks it.
	SafeguardFlagged = "flagged"
)

// SafeguardStatus is the verdict for a single tool use.
type SafeguardStatus struct {
	// Skip hands this one action back to the client's own classifier. It is the
	// right answer for "not confident", and it matters that it is distinct from
	// returning nothing: the client's latch (X6t) fires only when the whole
	// result is absent, unsupported or unavailable, so a skipped entry costs one
	// local classifier call for that action while leaving the session willing to
	// ask again on the next turn. Omitting the payload instead stops it asking
	// for the rest of the session.
	Skip bool

	// Outcome is SafeguardNotFlagged or SafeguardFlagged. Ignored when Skip.
	Outcome string

	// Explanation is surfaced to the user on a flagged action. Without it the
	// client says "it gave no explanation", so populate it whenever a verdict
	// blocks something.
	Explanation string
}

// SafeguardResults builds the `safeguard_results` payload for a set of tool-use
// verdicts, keyed by tool_use id. An empty map still yields a well-formed
// "available" result with no entries, which is the honest answer for a response
// that emitted no tool uses.
func SafeguardResults(perTool map[string]SafeguardStatus) []any {
	toolUses := make(map[string]any, len(perTool))
	for id, st := range perTool {
		if st.Skip {
			toolUses[id] = map[string]any{"type": "skipped"}
			continue
		}
		entry := map[string]any{
			"type":    "evaluated",
			"outcome": st.Outcome,
		}
		if st.Explanation != "" {
			entry["explanation"] = st.Explanation
		}
		toolUses[id] = entry
	}
	return []any{map[string]any{
		"type": anthropic.SafeguardDangerousToolUse,
		"status": map[string]any{
			"type":      "available",
			"tool_uses": toolUses,
		},
	}}
}

// SafeguardSkipAll answers with every tool use skipped, which is what to send
// when the judgment could not be obtained — a classifier outage, a timeout, a
// malformed answer. The client runs its own classifier for these actions and
// keeps asking on later turns, so a transient failure costs one turn's saving
// rather than the session's.
func SafeguardSkipAll(toolUseIDs []string) []any {
	perTool := make(map[string]SafeguardStatus, len(toolUseIDs))
	for _, id := range toolUseIDs {
		perTool[id] = SafeguardStatus{Skip: true}
	}
	return SafeguardResults(perTool)
}
