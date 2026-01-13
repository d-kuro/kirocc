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
