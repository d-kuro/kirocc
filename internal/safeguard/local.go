package safeguard

import (
	"strings"

	"github.com/d-kuro/kirocc/internal/respconv"
)

// LocalVerdict is a zero-latency fast-path for tool calls that are unambiguously
// read-only. It returns (status, true) only when a call is safe beyond doubt;
// every other case returns (_, false) so the caller falls through to Jev.
//
// This is a latency/cost fast-path and cache pre-seed, not a safety floor: it
// spares the commonest actions -- reads and searches -- a Jev round-trip even on
// their first occurrence, before the verdict cache is warm. It is deliberately
// small; breadth (learning that other commands are safe across their arguments)
// belongs to Jev plus the verdict cache, not to an ever-growing local list. It
// never clears a mutation; "not sure" is always false.
func LocalVerdict(name, input string) (respconv.SafeguardStatus, bool) {
	if readOnlyTool[name] {
		return respconv.SafeguardStatus{Outcome: respconv.SafeguardNotFlagged}, true
	}
	if name == "Bash" && isReadOnlyShell(input) {
		return respconv.SafeguardStatus{Outcome: respconv.SafeguardNotFlagged}, true
	}
	return respconv.SafeguardStatus{}, false
}

// readOnlyTool is the set of client tools whose every invocation only observes.
// A tool earns a place here only if no input can make it mutate state; anything
// that can write, delete, fetch-and-execute, or reach outside the workspace is
// left off and judged by Jev.
var readOnlyTool = map[string]bool{
	"Read":         true,
	"Glob":         true,
	"Grep":         true,
	"NotebookRead": true,
	"TodoWrite":    true, // writes only the local scratch todo list
	"WebFetch":     true, // retrieves and summarises; cannot act
	"WebSearch":    true,
}

// isReadOnlyShell reports whether a Bash command is confidently a pure read. It
// is deliberately strict: any shell construct that could chain, redirect or
// substitute, or a verb not on the tiny read-only list, makes it return false
// and the command goes to Jev. A false negative costs one Jev call; a false
// positive clears a mutation, so the bias is entirely toward false.
func isReadOnlyShell(input string) bool {
	cmd := parseBashCommand(input)
	if cmd == "" {
		return false
	}
	// Any metacharacter that could sequence, redirect, background or substitute
	// takes the command out of "single read" territory.
	if strings.ContainsAny(cmd, "|&;<>`$(){}\\\n") {
		return false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "cat", "head", "tail", "less", "more", "ls", "pwd", "wc",
		"grep", "rg", "which", "file", "stat", "echo", "date":
		// Only verbs that cannot mutate under ANY flag. Deliberately absent:
		//   - find: `-delete`/`-exec` mutate, with no metacharacter to catch;
		//   - sed/awk: `-i` rewrites in place with no metacharacter;
		// those go to Jev rather than have the fast-path parse their flags.
		return true
	case "git":
		return len(fields) > 1 && gitReadSubcmd[fields[1]]
	}
	return false
}

// gitReadSubcmd is the set of git subcommands that never change repository or
// remote state.
var gitReadSubcmd = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true,
	"branch": true, "remote": true, "rev-parse": true, "describe": true,
	"blame": true, "config": true, "ls-files": true, "for-each-ref": true,
}

// parseBashCommand pulls the shell command string out of a Bash tool input,
// which is JSON like {"command":"…"}. It returns "" when the shape is not what
// it expects, so an unparseable input is judged rather than cleared.
func parseBashCommand(input string) string {
	// A tiny, allocation-light extraction: the input is trusted to be the tool
	// call the accumulator recorded, but a malformed one must fail closed.
	const key = `"command"`
	_, rest, ok := strings.Cut(input, key)
	if !ok {
		return ""
	}
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	rest = rest[j+1:]
	// Find the closing quote, honouring backslash escapes.
	var b strings.Builder
	for k := 0; k < len(rest); k++ {
		c := rest[k]
		if c == '\\' && k+1 < len(rest) {
			b.WriteByte(rest[k+1])
			k++
			continue
		}
		if c == '"' {
			return b.String()
		}
		b.WriteByte(c)
	}
	return ""
}
