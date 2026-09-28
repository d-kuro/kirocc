package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

// A multi-byte query must be measured in runes, not bytes, or a CJK question
// is rejected at roughly a third of its real length.
func TestSearchQueryLimitCountsRunes(t *testing.T) {
	p := &stubProvider{results: []Result{{Title: "t", URL: "https://example.com/t"}}}
	c := NewContext([]anthropic.Tool{webSearchTool()}, p, 0)

	// maxQueryLen runes of a 3-byte character: well over the byte limit, but at
	// the rune limit, so it must be accepted.
	q := strings.Repeat("あ", maxQueryLen)
	if _, err := c.Search(context.Background(), q); err != nil {
		t.Fatalf("query of %d runes (%d bytes) rejected: %v", maxQueryLen, len(q), err)
	}
	if p.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", p.calls)
	}

	// One rune over the limit must be rejected.
	if _, err := c.Search(context.Background(), strings.Repeat("あ", maxQueryLen+1)); !errors.Is(err, ErrQueryTooLong) {
		t.Errorf("over-length query err = %v, want ErrQueryTooLong", err)
	}
}

// collapseSpace must truncate on a rune boundary; slicing raw bytes would split
// a multi-byte character and emit invalid UTF-8.
func TestCollapseSpaceTruncatesOnRuneBoundary(t *testing.T) {
	got := collapseSpace(strings.Repeat("あ", maxSnippetLen+50))
	if !utf8.ValidString(got) {
		t.Fatalf("collapseSpace produced invalid UTF-8: %q", got)
	}
	trimmed := strings.TrimSuffix(got, "…")
	if n := utf8.RuneCountInString(trimmed); n != maxSnippetLen {
		t.Errorf("truncated to %d runes, want %d", n, maxSnippetLen)
	}
	// A snippet already within the limit is returned unchanged (spaces
	// collapsed), with no ellipsis.
	if got := collapseSpace("  a   b  "); got != "a b" {
		t.Errorf("collapseSpace(short) = %q, want %q", got, "a b")
	}
}

// LiveResultText is what the executor model reads for the next round, so it must
// carry the snippet — the whole reason results are fed back — alongside title
// and URL.
func TestLiveResultText(t *testing.T) {
	got := LiveResultText([]Result{
		{Title: "Selters weather", URL: "https://example.com/w", Snippet: "17 °C, light rain", PageAge: "2 hours ago"},
		{URL: "https://example.com/bare"},
	})
	for _, want := range []string{
		"Selters weather",
		"https://example.com/w",
		"17 °C, light rain",
		"2 hours ago",
		"https://example.com/bare",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("LiveResultText missing %q in:\n%s", want, got)
		}
	}

	// No results has to read as a definite empty answer, not a failed call.
	if got := LiveResultText(nil); !strings.Contains(strings.ToLower(got), "no web search results") {
		t.Errorf("LiveResultText(nil) = %q, want a no-results message", got)
	}
}
