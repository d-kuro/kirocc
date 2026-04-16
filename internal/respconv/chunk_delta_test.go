package respconv

import (
	"testing"
)

func TestNormalizeChunk(t *testing.T) {
	tests := []struct {
		name     string
		chunk    string
		previous string
		want     string
	}{
		{"empty previous", "Hello", "", "Hello"},
		{"same text", "Hello", "Hello", ""},
		{"prefix extension", "Hello world", "Hello", " world"},
		{"shrunk text", "Hel", "Hello", ""},
		{"overlap", "world!", "Hello world", "!"},
		{"no overlap", "Goodbye", "Hello", "Goodbye"},
		{"empty chunk", "", "Hello", ""},
		// Multi-byte UTF-8 overlap: previous ends with "日本", chunk starts with
		// "日本". Overlap detection must match all 6 bytes of "日本" (not only
		// the rune-start bytes) or the resulting delta begins mid-rune and
		// emits invalid UTF-8 into the SSE stream.
		{"multibyte overlap", "日本語のテスト", "こんにちは日本", "語のテスト"},
		{"multibyte overlap kanji only", "日本語", "日本", "語"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeChunk(tt.chunk, tt.previous)
			if got != tt.want {
				t.Errorf("NormalizeChunk(%q, %q) = %q, want %q", tt.chunk, tt.previous, got, tt.want)
			}
		})
	}
}
