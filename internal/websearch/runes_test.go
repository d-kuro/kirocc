package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"

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

// Snippets and provider error lines are capped in runes, never bytes: slicing
// raw bytes would split a multi-byte character and emit invalid UTF-8.
func TestTruncation(t *testing.T) {
	firstLineOf := func(s string) string { return firstLine([]byte(s)) }
	truncateTo3 := func(s string) string { return truncateRunes(s, 3) }
	cases := []struct {
		name string
		fn   func(string) string
		in   string
		want string
	}{
		{"truncateRunes under the limit", truncateTo3, "ab", "ab"},
		{"truncateRunes at the limit", truncateTo3, "あいう", "あいう"},
		{"truncateRunes cuts on a rune boundary", truncateTo3, "あいうえ", "あいう…"},
		{"truncateRunes trims space before the ellipsis", truncateTo3, "ab cd", "ab…"},
		{"collapseSpace collapses whitespace", collapseSpace, "  a   b  ", "a b"},
		{"collapseSpace caps at maxSnippetLen", collapseSpace, strings.Repeat("あ", maxSnippetLen+50), strings.Repeat("あ", maxSnippetLen) + "…"},
		{"firstLine keeps only the first line", firstLineOf, "  bad request\nstack trace  ", "bad request"},
		{"firstLine caps at maxErrorLineLen", firstLineOf, strings.Repeat("あ", maxErrorLineLen+50) + "\nsecond line", strings.Repeat("あ", maxErrorLineLen) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
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
