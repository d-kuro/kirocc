package safeguard

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/d-kuro/kirocc/internal/respconv"
)

// Recorder appends a replayable record of each real Jev judgment. It exists so
// a block can be second-guessed after the fact and the classifier iteratively
// tuned: every field needed to re-issue the exact same call is captured, so a
// recorded line replays deterministically (see the -replay-jev mode). Only
// actual Jev calls are recorded -- a local fast-path clear or a cache hit makes
// no judgment to replay.
type Recorder interface {
	record(r jevRecord)
}

// jevRecord is one Jev judgment, complete enough to replay. Request holds the
// exact eval request body (model, state, questions) so re-POSTing it to the
// endpoint reproduces the call; Answers is what came back; Resolved is the
// verdict decide() produced from it.
type jevRecord struct {
	Timestamp  time.Time         `json:"ts"`
	Endpoint   string            `json:"endpoint"`
	Tool       string            `json:"tool"`
	Input      string            `json:"input"`
	PolicyHash string            `json:"policy_hash"`
	Request    evalRequest       `json:"request"`
	Answers    map[string]answer `json:"answers"`
	Resolved   resolvedVerdict   `json:"resolved"`
}

// resolvedVerdict is decide()'s output, flattened for the record.
type resolvedVerdict struct {
	Outcome       string `json:"outcome"` // not_flagged | flagged | skipped
	Explanation   string `json:"explanation,omitempty"`
	Generalizable bool   `json:"generalizable"`
}

func resolveOf(st respconv.SafeguardStatus, generalizable bool) resolvedVerdict {
	out := "skipped"
	if !st.Skip && st.Outcome != "" {
		out = st.Outcome
	}
	return resolvedVerdict{Outcome: out, Explanation: st.Explanation, Generalizable: generalizable}
}

// FileRecorder appends one JSON line per judgment to a file. Writes are
// serialised and best-effort: a recording error never affects the verdict.
type FileRecorder struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileRecorder opens path for appending. The caller owns closing via Close.
func NewFileRecorder(path string) (*FileRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileRecorder{f: f}, nil
}

func (r *FileRecorder) record(rec jevRecord) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')
	r.mu.Lock()
	_, _ = r.f.Write(b)
	r.mu.Unlock()
}

// Close flushes and closes the underlying file.
func (r *FileRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}

// WithRecorder sets the judgment recorder. A nil recorder (the default) records
// nothing.
func WithRecorder(rec Recorder) Option { return func(c *Client) { c.recorder = rec } }

// buildRequest assembles the eval request for a tool use, factored out of judge
// so both the live path and a replay can construct it identically. When
// includePolicy is false the policy is omitted -- used to retry after Jev
// rejects an oversized request. It reports whether the policy was actually
// included (false when absent or dropped for size), so the caller knows whether
// a policy-less retry is even possible. Questions come from override when
// non-empty, else the built-in default set.
func buildRequest(model string, policy jsontext.Value, u ToolUse, includePolicy bool, override map[string]question) (evalRequest, bool) {
	state := map[string]any{"pending_tool": u.Name}
	if v := jsontext.Value(u.Input); len(v) > 0 && v.IsValid() {
		state["pending_input"] = v
	} else if u.Input != "" {
		state["pending_input"] = u.Input
	}
	withPolicy := false
	if includePolicy && len(policy) > 0 {
		if p := trimPolicy(policy); len(p) > 0 && len(p) <= maxPolicyBytes {
			state["policy"] = p
			withPolicy = true
		}
	}
	qs := questions()
	if len(override) > 0 {
		qs = override
	}
	return evalRequest{Model: model, State: state, Questions: qs}, withPolicy
}

// buildRequestForRecord builds the request body for a judgment record: the
// policy-trimmed request as sent, using the given model. It mirrors what the
// provider was sent (a provider that dropped the policy for size still records
// the trimmed form), which is enough to replay.
func buildRequestForRecord(model string, policy jsontext.Value, u ToolUse, override map[string]question) evalRequest {
	req, _ := buildRequest(model, policy, u, true, override)
	return req
}

// policyBloatFields are policy members that grow unboundedly over a session and
// carry no signal the questions use. Claude Code's ClassifierContext accumulates
// a per-turn `prior_turn_context` list that reached ~124 KB of a 120 KB policy
// in a long session, overrunning Jev's context; the useful part (permission
// mode, trusted directories, rules, git state) is a few KB. Stripping these
// keeps the signal while cutting the bulk.
var policyBloatFields = []string{"prior_turn_context"}

// trimPolicy removes the known unbounded fields from the policy document,
// returning a compacted copy. If the policy is not a JSON object (schema drift
// or a future shape), it is returned unchanged and the size budget in the
// caller is the only guard -- degrading to a possible policy drop, which is
// cost, never safety.
func trimPolicy(policy jsontext.Value) jsontext.Value {
	var m map[string]jsontext.Value
	if err := json.Unmarshal(policy, &m); err != nil {
		return policy // not an object we recognise; let the size budget decide
	}
	stripped := false
	for _, f := range policyBloatFields {
		if _, ok := m[f]; ok {
			delete(m, f)
			stripped = true
		}
	}
	if !stripped {
		return policy
	}
	out, err := json.Marshal(m)
	if err != nil {
		return policy
	}
	return jsontext.Value(out)
}

// ReplayResult is the outcome of re-issuing a recorded Jev judgment.
type ReplayResult struct {
	Tool         string            `json:"tool"`
	Input        string            `json:"input"`
	Recorded     resolvedVerdict   `json:"recorded"`      // the original verdict
	Fresh        resolvedVerdict   `json:"fresh"`         // the verdict now
	FreshAnswers map[string]answer `json:"fresh_answers"` // raw Jev answers now
	Changed      bool              `json:"changed"`       // outcome differs
}

// Question is the exported form of a classification question, so a caller (the
// replay tool) can supply a modified questions map to A/B a proposed prompt.
type Question = question

// LoadQuestions reads a questions map from a JSON file: an object keyed by
// question id, each value a {type, instructions, criteria} object matching the
// wire shape. It is how a prompt variant is supplied without a recompile -- to
// WithQuestions on a live client, or to the replay tool's -questions. An empty
// path returns (nil, nil) so callers can treat "no override" uniformly.
func LoadQuestions(path string) (map[string]Question, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read questions %s: %w", path, err)
	}
	var qs map[string]Question
	if err := json.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("parse questions %s: %w", path, err)
	}
	return qs, nil
}

// ReplayFile loads the replay file, replays record number line (1-based; 0 =
// last) against the live endpoint, and returns the original and fresh verdicts.
// A non-nil override replaces the questions map for the replayed call, leaving
// the recorded command and policy untouched -- that is how a proposed prompt is
// tried against a captured case. It makes a live, billed Jev call.
func (c *Client) ReplayFile(ctx context.Context, path string, line int, override map[string]Question) (ReplayResult, error) {
	recs, err := LoadRecords(path)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(recs) == 0 {
		return ReplayResult{}, os.ErrNotExist
	}
	idx := len(recs) - 1
	if line > 0 {
		if line > len(recs) {
			return ReplayResult{}, os.ErrInvalid
		}
		idx = line - 1
	}
	return c.replay(ctx, recs[idx], override)
}

// replay re-issues one recorded Jev judgment. See ReplayFile.
func (c *Client) replay(ctx context.Context, rec jevRecord, override map[string]question) (ReplayResult, error) {
	// Replay targets the primary provider (the one the chain tries first).
	p := c.providers[0]
	req := rec.Request
	switch {
	case override != nil:
		// An explicit questions map wins: the caller (the replay tool's
		// -questions) is A/B-ing a proposed prompt against the recorded inputs.
		req.Questions = override
	case len(c.questionsOverride) > 0:
		// A client-level prompt override (WithQuestions) applies next.
		req.Questions = c.questionsOverride
	}
	if req.Model == "" {
		req.Model = p.model
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ReplayResult{}, err
	}
	answers, err := c.post(ctx, p, body)
	if err != nil && retryable(err) && ctx.Err() == nil {
		answers, err = c.post(ctx, p, body)
	}
	if err != nil {
		return ReplayResult{}, err
	}
	st, generalizable, err := c.resolve(answers)
	if err != nil {
		return ReplayResult{}, err
	}
	fresh := resolveOf(st, generalizable)
	return ReplayResult{
		Tool:         rec.Tool,
		Input:        rec.Input,
		Recorded:     rec.Resolved,
		Fresh:        fresh,
		FreshAnswers: answers,
		Changed:      fresh.Outcome != rec.Resolved.Outcome,
	}, nil
}

// LoadRecords reads all jevRecord lines from a replay file.
func LoadRecords(path string) ([]jevRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recs []jevRecord
	for _, line := range splitLines(b) {
		if len(line) == 0 {
			continue
		}
		var r jevRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue // skip a malformed line rather than fail the whole load
		}
		recs = append(recs, r)
	}
	return recs, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
