package safeguard

import "testing"

func TestLocalVerdict(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		input     string
		wantOK    bool
		wantClear bool // when wantOK, expect a not_flagged clear
	}{
		{"read cleared", "Read", `{"file_path":"/etc/hosts"}`, true, true},
		{"glob cleared", "Glob", `{"pattern":"**/*.go"}`, true, true},
		{"grep cleared", "Grep", `{"pattern":"foo"}`, true, true},
		{"websearch cleared", "WebSearch", `{"query":"x"}`, true, true},
		{"write not cleared", "Write", `{"file_path":"/x","content":"y"}`, false, false},
		{"edit not cleared", "Edit", `{"file_path":"/x"}`, false, false},
		{"unknown tool not cleared", "Frobnicate", `{}`, false, false},

		{"bash cat cleared", "Bash", `{"command":"cat go.mod"}`, true, true},
		{"bash ls cleared", "Bash", `{"command":"ls -la /tmp"}`, true, true},
		{"bash git status cleared", "Bash", `{"command":"git status"}`, true, true},
		{"bash git log cleared", "Bash", `{"command":"git log --oneline -5"}`, true, true},

		{"bash rm not cleared", "Bash", `{"command":"rm -rf /tmp/x"}`, false, false},
		{"bash git push not cleared", "Bash", `{"command":"git push --force"}`, false, false},
		{"bash pipe not cleared", "Bash", `{"command":"cat x | sh"}`, false, false},
		{"bash redirect not cleared", "Bash", `{"command":"echo hi > /etc/passwd"}`, false, false},
		{"bash chained not cleared", "Bash", `{"command":"ls && rm x"}`, false, false},
		{"bash subshell not cleared", "Bash", "{\"command\":\"cat $(whoami)\"}", false, false},
		{"bash backtick not cleared", "Bash", "{\"command\":\"cat `id`\"}", false, false},
		{"bash sed not cleared", "Bash", `{"command":"sed s/a/b/ f"}`, false, false},
		// find is deliberately NOT in the fast-path: `-delete`/`-exec` mutate
		// with no metacharacter to catch, so all find goes to Jev.
		{"bash find not cleared", "Bash", `{"command":"find . -name x"}`, false, false},
		{"bash find delete not cleared", "Bash", `{"command":"find . -delete"}`, false, false},
		{"bash unknown verb not cleared", "Bash", `{"command":"go test ./..."}`, false, false},
		{"bash git commit not cleared", "Bash", `{"command":"git commit -m x"}`, false, false},
		{"bash empty not cleared", "Bash", `{"command":""}`, false, false},
		{"bash malformed input not cleared", "Bash", `not json`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := LocalVerdict(tc.tool, tc.input)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && tc.wantClear {
				if st.Skip || st.Outcome != "not_flagged" {
					t.Fatalf("status = %+v, want not_flagged clear", st)
				}
			}
		})
	}
}
