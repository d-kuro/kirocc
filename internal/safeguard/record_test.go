package safeguard

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestTrimPolicyDropsBloatKeepsSignal(t *testing.T) {
	// A policy shaped like Claude Code's, with a huge prior_turn_context and a
	// small useful remainder.
	big := strings.Repeat("x", 200_000)
	policy := jsontext.Value(`{"permission_mode":"auto","trusted_directories":{"primary":"/repo"},"rules":{"allow":["a"]},"prior_turn_context":["` + big + `"]}`)

	trimmed := trimPolicy(policy)
	if len(trimmed) >= len(policy) {
		t.Fatalf("trim did not shrink: %d -> %d", len(policy), len(trimmed))
	}
	var m map[string]jsontext.Value
	if err := json.Unmarshal(trimmed, &m); err != nil {
		t.Fatalf("trimmed policy not valid JSON: %v", err)
	}
	if _, ok := m["prior_turn_context"]; ok {
		t.Error("prior_turn_context should have been stripped")
	}
	for _, keep := range []string{"permission_mode", "trusted_directories", "rules"} {
		if _, ok := m[keep]; !ok {
			t.Errorf("useful field %q was dropped", keep)
		}
	}
}

func TestTrimPolicyPassesThroughNonObject(t *testing.T) {
	// A non-object policy (schema drift) is returned unchanged.
	policy := jsontext.Value(`"just a string"`)
	if got := trimPolicy(policy); string(got) != string(policy) {
		t.Errorf("non-object policy changed: %s", got)
	}
	// An object without bloat fields is returned unchanged (same bytes).
	plain := jsontext.Value(`{"permission_mode":"auto"}`)
	if got := trimPolicy(plain); string(got) != string(plain) {
		t.Errorf("plain policy changed: %s", got)
	}
}

func TestBuildRequestDropsOversizePolicy(t *testing.T) {
	u := ToolUse{Name: "Bash", Input: `{"command":"go build"}`}

	// A policy that is oversized even after trimming (no bloat field to strip)
	// is dropped, and withPolicy is false.
	huge := jsontext.Value(`{"blob":"` + strings.Repeat("y", maxPolicyBytes) + `"}`)
	req, withPolicy := buildRequest("jev-latest", huge, u, true, nil)
	if withPolicy {
		t.Error("oversized policy should have been dropped")
	}
	if _, ok := req.State["policy"]; ok {
		t.Error("state should not carry an oversized policy")
	}

	// A small policy is kept.
	small := jsontext.Value(`{"permission_mode":"auto"}`)
	_, withPolicy = buildRequest("jev-latest", small, u, true, nil)
	if !withPolicy {
		t.Error("small policy should have been included")
	}

	// includePolicy=false always omits it.
	_, withPolicy = buildRequest("jev-latest", small, u, false, nil)
	if withPolicy {
		t.Error("includePolicy=false must omit the policy")
	}
}

// buildRequest sends the default Choice verdict question, and an override
// replaces it.
func TestBuildRequestQuestions(t *testing.T) {
	u := ToolUse{Name: "Bash", Input: `{"command":"go build"}`}

	def, _ := buildRequest("jev-latest", nil, u, true, nil)
	q, ok := def.Questions[questionVerdict]
	if !ok {
		t.Fatal("default must ask the Choice verdict")
	}
	if q.Type != "choice" {
		t.Errorf("verdict question type = %q, want choice", q.Type)
	}

	override := map[string]question{
		questionVerdict: {Type: "choice", Instructions: "custom", Criteria: map[string]string{verdictNotFlagged: "x", verdictFlagged: "y", verdictUncertain: "z"}},
	}
	got, _ := buildRequest("jev-latest", nil, u, true, override)
	if got.Questions[questionVerdict].Instructions != "custom" {
		t.Error("override must replace the default questions")
	}
}
