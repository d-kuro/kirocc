package anthropic

import (
	"net/http"
	"strings"
)

// HasContext1MBeta reports whether the Anthropic-Beta header set contains a
// context-1m flag. Matches any value with the "context-1m" prefix (e.g.
// "context-1m-2025-10-22").
func HasContext1MBeta(h http.Header) bool {
	for _, v := range h["Anthropic-Beta"] {
		for beta := range strings.SplitSeq(v, ",") {
			if strings.HasPrefix(strings.TrimSpace(beta), "context-1m") {
				return true
			}
		}
	}
	return false
}
