package anthropic

import (
	"net/http"
	"strings"
)

// hasBetaPrefix reports whether any comma-separated Anthropic-Beta value starts
// with prefix. Beta values carry a dated suffix that changes between releases,
// so callers match on the stable prefix rather than the whole string.
func hasBetaPrefix(h http.Header, prefix string) bool {
	for _, v := range h["Anthropic-Beta"] {
		for beta := range strings.SplitSeq(v, ",") {
			if strings.HasPrefix(strings.TrimSpace(beta), prefix) {
				return true
			}
		}
	}
	return false
}

// HasContext1MBeta reports whether the Anthropic-Beta header set contains a
// context-1m flag. Matches any value with the "context-1m" prefix (e.g.
// "context-1m-2025-10-22").
func HasContext1MBeta(h http.Header) bool {
	return hasBetaPrefix(h, "context-1m")
}

// HasDangerousToolUseBeta reports whether the client asked for server-side
// auto-mode classification (e.g. "dangerous-tool-use-2026-09-03"). The beta
// travels with the `safeguards` request field: Claude Code sends both or
// neither, and expects `safeguard_results` back on the same response.
func HasDangerousToolUseBeta(h http.Header) bool {
	return hasBetaPrefix(h, "dangerous-tool-use")
}
