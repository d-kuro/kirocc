package messages

import (
	"context"
	"log/slog"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/respconv"
	"github.com/d-kuro/kirocc/internal/safeguard"
)

// safeguardTarget is the part of a response writer that safeguard verdicts need:
// the tool calls this round produced, and somewhere to hang the answer. Both
// SSEWriter and NonStreamingAccumulator satisfy it, so streaming, non-streaming
// and the server-tool loop all use one code path.
type safeguardTarget interface {
	ToolCalls() []respconv.ToolCall
	SetSafeguardResults([]any)
}

// collectedSafeguardTarget adapts an explicit tool-call list to
// safeguardTarget. The server-tool loop's non-streaming path assembles its
// response from per-round accumulators rather than one, so its calls are
// gathered as it goes and the verdicts read back off this holder.
type collectedSafeguardTarget struct {
	calls   []respconv.ToolCall
	results []any
}

func (c *collectedSafeguardTarget) ToolCalls() []respconv.ToolCall { return c.calls }

func (c *collectedSafeguardTarget) SetSafeguardResults(r []any) { c.results = r }

// degradeWarnAt is the number of consecutive responses that fell back wholesale
// to the client's classifier before we say so out loud. One or two is ordinary
// noise -- a timeout, a blip. A sustained run means the field is effectively off
// for the session and the saving is gone, which is worth a line in the log.
const degradeWarnAt = 3

// noteClassified records a response whose tool uses were judged locally or by
// Jev, ending any degraded streak.
func (s *Service) noteClassified() { s.degraded.Store(0) }

// noteDegraded records a response handed wholesale to the client's classifier
// and warns once when a streak crosses the threshold. It is the only visible
// signal that the safeguard field has stopped saving anything.
func (s *Service) noteDegraded(ctx context.Context, reason string, err error) {
	n := s.degraded.Add(1)
	if n == degradeWarnAt {
		_, short := logging.TraceIDs(ctx)
		slog.WarnContext(ctx, "safeguard degraded: deferring every action to the client's own classifier",
			"trace_id", short, "consecutive", n, "reason", reason, "err", err)
	}
}

// attachSafeguards answers auto mode's `safeguards` request field, when the
// client asked. Call it once per response, after the round's tool calls are
// known and before the response is finalised.
//
// A local pass clears unambiguous reads first, with no network and no key, so
// the commonest actions never reach a paid judgment and a reader stays
// unblocked even when every remote classifier is down. Whatever the local pass
// cannot vouch for goes to Jev when one is configured; anything still
// unresolved is left for the client's own classifier.
//
// Gating is on the request field rather than the "dangerous-tool-use-*" beta
// header: the field is what actually carries the question, and it stays in
// scope here while the header does not. The client sends both or neither.
func (s *Service) attachSafeguards(ctx context.Context, req *anthropic.Request, target safeguardTarget) {
	sg := req.DangerousToolUseSafeguard()
	if sg == nil {
		return
	}
	_, short := logging.TraceIDs(ctx)

	// Safeguards disabled (-safeguard=false): leave the field unanswered. The
	// client then sees server_no_result and falls back to its own classifier,
	// exactly as it did before kirocc answered the field at all. This is a
	// deliberate omission, distinct from an empty answer -- an empty result
	// latches the client off for the session; omitting it does not.
	if s.safeguard == nil {
		return
	}

	calls := target.ToolCalls()

	// Answer even with no tool calls. An empty "available" result is the honest
	// reply, and it matters: a response that omits the key entirely reads to the
	// client as server_no_result, which makes it stop asking for the rest of the
	// session and go back to billing its own classifier.
	if len(calls) == 0 {
		target.SetSafeguardResults(respconv.SafeguardResults(nil))
		return
	}

	// Local pass: clear what is unambiguously read-only, and collect the rest
	// for Jev.
	statuses := make(map[string]respconv.SafeguardStatus, len(calls))
	var remaining []safeguard.ToolUse
	cleared := 0
	for _, tc := range calls {
		if st, ok := safeguard.LocalVerdict(tc.Name, tc.Input); ok {
			statuses[tc.ID] = st
			cleared++
			continue
		}
		remaining = append(remaining, safeguard.ToolUse{ID: tc.ID, Name: tc.Name, Input: tc.Input})
	}

	// Everything cleared locally: a fully answered result, no classifier needed.
	if len(remaining) == 0 {
		s.noteClassified()
		slog.InfoContext(ctx, "safeguard verdicts", "trace_id", short,
			"tool_uses", len(calls), "cleared_local", cleared, "flagged", 0, "deferred", 0)
		target.SetSafeguardResults(respconv.SafeguardResults(statuses))
		return
	}

	// Bound the judgment so a slow classifier cannot hold the response open.
	// Timing out defers the remaining actions rather than failing them.
	cctx, cancel := context.WithTimeout(ctx, safeguard.DefaultTimeout)
	defer cancel()

	judged, err := s.safeguard.Classify(cctx, sg.ClassifierContext, remaining)
	if err != nil {
		for _, u := range remaining {
			statuses[u.ID] = respconv.SafeguardStatus{Skip: true}
		}
		s.noteDegraded(ctx, "classifier unavailable", err)
		target.SetSafeguardResults(respconv.SafeguardResults(statuses))
		return
	}

	flagged, skipped := 0, 0
	for id, st := range judged {
		statuses[id] = st
		switch {
		case st.Skip:
			skipped++
		case st.Outcome == respconv.SafeguardFlagged:
			flagged++
		}
	}
	// A response is degraded only when every judged action skipped; a mix still
	// saved calls, so it ends the streak.
	if skipped == len(remaining) {
		s.noteDegraded(ctx, "all deferred", nil)
	} else {
		s.noteClassified()
	}
	slog.InfoContext(ctx, "safeguard verdicts", "trace_id", short,
		"tool_uses", len(calls), "cleared_local", cleared, "flagged", flagged, "deferred", skipped)
	target.SetSafeguardResults(respconv.SafeguardResults(statuses))
}
