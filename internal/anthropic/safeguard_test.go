package anthropic

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
)

// A trimmed but structurally faithful sample of what Claude Code 2.1.278 sends
// in auto mode. The context is policy and environment state only — there are no
// pending tool calls in it, which is why the verdicts are keyed by the tool_use
// ids of the response rather than by anything echoed from here.
const safeguardRequestJSON = `{
  "model": "claude-opus-5",
  "max_tokens": 32000,
  "stream": true,
  "messages": [{"role": "user", "content": "run the tests"}],
  "safeguards": [{
    "type": "dangerous_tool_use",
    "classifier_context": {
      "v": 1,
      "platform": "macos",
      "permission_mode": "auto",
      "user_identity": "dev",
      "live_cwd": "/repo",
      "git_state": {"branch": "main", "default_branch": "main"},
      "rules": {"allow": [{"rule": "WebSearch", "source": "localSettings"}], "deny": [], "ask": []},
      "auto_mode": {"hard_deny": [], "soft_deny": [], "allow": [], "environment": []},
      "classify_all_shell": false
    }
  }]
}`

func TestRequestDecodesSafeguards(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(safeguardRequestJSON), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	sg := req.DangerousToolUseSafeguard()
	if sg == nil {
		t.Fatal("DangerousToolUseSafeguard returned nil; the request asked for server-side classification")
	}
	if sg.Type != SafeguardDangerousToolUse {
		t.Fatalf("type = %q, want %q", sg.Type, SafeguardDangerousToolUse)
	}

	// The context is forwarded to the classifier verbatim, so it has to survive
	// the round trip as valid JSON rather than being reshaped into our own
	// structs — its schema belongs to the client and is versioned by its own
	// `v` field.
	if !sg.ClassifierContext.IsValid() {
		t.Fatal("classifier_context did not survive decoding as valid JSON")
	}
	if !bytes.Contains(sg.ClassifierContext, []byte(`"permission_mode"`)) {
		t.Errorf("classifier_context lost fields: %s", sg.ClassifierContext)
	}
	var ctx struct {
		V              int    `json:"v"`
		PermissionMode string `json:"permission_mode"`
	}
	if err := json.Unmarshal(sg.ClassifierContext, &ctx); err != nil {
		t.Fatalf("classifier_context is not decodable: %v", err)
	}
	if ctx.V != 1 || ctx.PermissionMode != "auto" {
		t.Errorf("context = %+v, want v=1 permission_mode=auto", ctx)
	}
}

func TestDangerousToolUseSafeguardAbsent(t *testing.T) {
	tests := map[string]string{
		"no safeguards field": `{"messages":[],"safeguards":null}`,
		"empty list":          `{"messages":[],"safeguards":[]}`,
		"another type only":   `{"messages":[],"safeguards":[{"type":"something_else"}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			var req Request
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := req.DangerousToolUseSafeguard(); got != nil {
				t.Fatalf("want nil, got %+v", got)
			}
		})
	}
}

// Requests that carry no safeguards must not grow the field on the way out.
func TestRequestOmitsEmptySafeguards(t *testing.T) {
	b, err := json.Marshal(Request{Safeguards: nil})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(b, []byte("safeguards")) {
		t.Fatalf("safeguards must be omitted when unset: %s", b)
	}
}

func TestSafeguardRoundTrip(t *testing.T) {
	in := Safeguard{
		Type:              SafeguardDangerousToolUse,
		ClassifierContext: jsontext.Value(`{"v":1}`),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Safeguard
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Type != in.Type {
		t.Errorf("type = %q, want %q", out.Type, in.Type)
	}
	if string(out.ClassifierContext) != `{"v":1}` {
		t.Errorf("context = %s, want {\"v\":1}", out.ClassifierContext)
	}
}
