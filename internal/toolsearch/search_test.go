package toolsearch

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func testTools() map[string]anthropic.Tool {
	return map[string]anthropic.Tool{
		"Read":  {Name: "Read", Description: "Reads a file from the filesystem"},
		"Edit":  {Name: "Edit", Description: "Edits a file using a diff"},
		"Grep":  {Name: "Grep", Description: "Search for patterns in files"},
		"Bash":  {Name: "Bash", Description: "Executes a bash command"},
		"Write": {Name: "Write", Description: "Writes a file to the filesystem"},
	}
}

func TestSearch(t *testing.T) {
	tools := testTools()

	tests := []struct {
		name       string
		query      string
		searchType string
		maxResults int
		want       []string
		wantErr    bool
	}{
		{
			name:       "select_exact",
			query:      "select:Read,Edit,Grep",
			searchType: SearchTypeRegex,
			maxResults: 5,
			want:       []string{"Read", "Edit", "Grep"},
		},
		{
			name:       "select_skips_nonexistent",
			query:      "select:Read,NonExistent,Grep",
			searchType: SearchTypeRegex,
			maxResults: 5,
			want:       []string{"Read", "Grep"},
		},
		{
			name:       "select_trims_spaces",
			query:      "select: Read , Edit ",
			searchType: SearchTypeRegex,
			maxResults: 5,
			want:       []string{"Read", "Edit"},
		},
		{
			name:       "dispatches_to_regex",
			query:      "Read",
			searchType: SearchTypeRegex,
			maxResults: 5,
			want:       []string{"Read"},
		},
		{
			name:       "dispatches_to_bm25",
			query:      "file",
			searchType: SearchTypeBM25,
			maxResults: 5,
		},
		{
			name:       "maxResults_zero_defaults_to_5",
			query:      "select:Read,Edit,Grep,Bash,Write",
			searchType: SearchTypeRegex,
			maxResults: 0,
			want:       []string{"Read", "Edit", "Grep", "Bash", "Write"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Search(tt.query, tools, tt.searchType, tt.maxResults)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != nil {
				slices.Sort(got)
				sorted := slices.Clone(tt.want)
				slices.Sort(sorted)
				if !slices.Equal(got, sorted) {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSearchRegex(t *testing.T) {
	tools := testTools()

	tests := []struct {
		name       string
		pattern    string
		maxResults int
		wantFirst  string
		wantErr    error
		wantEmpty  bool
	}{
		{
			name:       "exact_name_highest_score",
			pattern:    "Read",
			maxResults: 5,
			wantFirst:  "Read",
		},
		{
			name:       "name_regex_match",
			pattern:    "Re.d",
			maxResults: 5,
			wantFirst:  "Read",
		},
		{
			name:       "description_regex_match",
			pattern:    "bash command",
			maxResults: 5,
			wantFirst:  "Bash",
		},
		{
			name:       "word_or_fallback",
			pattern:    "search patterns",
			maxResults: 5,
			wantFirst:  "Grep",
		},
		{
			name:       "pattern_too_long",
			pattern:    strings.Repeat("a", 201),
			maxResults: 5,
			wantErr:    ErrPatternTooLong,
		},
		{
			name:       "invalid_regex",
			pattern:    "[invalid",
			maxResults: 5,
			wantErr:    ErrInvalidPattern,
		},
		{
			name:       "maxResults_limiting",
			pattern:    ".*",
			maxResults: 2,
		},
		{
			name:       "no_matches",
			pattern:    "zzzznotfound",
			maxResults: 5,
			wantEmpty:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := searchRegex(tt.pattern, tools, tt.maxResults)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got error %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantEmpty {
				if len(got) != 0 {
					t.Fatalf("expected empty, got %v", got)
				}
				return
			}
			if tt.name == "maxResults_limiting" {
				if len(got) > tt.maxResults {
					t.Fatalf("got %d results, want at most %d", len(got), tt.maxResults)
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("expected results, got none")
			}
			if got[0] != tt.wantFirst {
				t.Fatalf("first result = %q, want %q", got[0], tt.wantFirst)
			}
		})
	}
}

func TestSearchBM25(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		tools      map[string]anthropic.Tool
		maxResults int
		wantFirst  string
		wantNil    bool
	}{
		{
			name:  "higher_term_frequency_ranks_higher",
			query: "file",
			tools: map[string]anthropic.Tool{
				"Read":  {Name: "Read", Description: "Reads a file from the filesystem"},
				"Write": {Name: "Write", Description: "Writes a file to the filesystem file file"},
			},
			maxResults: 5,
			wantFirst:  "Write",
		},
		{
			name:       "empty_query",
			query:      "",
			tools:      testTools(),
			maxResults: 5,
			wantNil:    true,
		},
		{
			name:       "empty_tools",
			query:      "file",
			tools:      map[string]anthropic.Tool{},
			maxResults: 5,
			wantNil:    true,
		},
		{
			name:       "maxResults_limiting",
			query:      "file",
			tools:      testTools(),
			maxResults: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchBM25(tt.query, tt.tools, tt.maxResults)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if tt.name == "maxResults_limiting" {
				if len(got) > tt.maxResults {
					t.Fatalf("got %d results, want at most %d", len(got), tt.maxResults)
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("expected results, got none")
			}
			if got[0] != tt.wantFirst {
				t.Fatalf("first result = %q, want %q", got[0], tt.wantFirst)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "lowercases",
			input: "ReadFile",
			want:  []string{"readfile"},
		},
		{
			name:  "splits_underscores",
			input: "read_file",
			want:  []string{"read", "file"},
		},
		{
			name:  "splits_whitespace",
			input: "  read   file  ",
			want:  []string{"read", "file"},
		},
		{
			name:  "combined",
			input: "Read_File Test",
			want:  []string{"read", "file", "test"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid_pattern",
			err:  ErrInvalidPattern,
			want: "invalid_pattern",
		},
		{
			name: "pattern_too_long",
			err:  ErrPatternTooLong,
			want: "pattern_too_long",
		},
		{
			name: "unknown_error",
			err:  errors.New("something else"),
			want: "unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ErrorCode(tt.err)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTopNames(t *testing.T) {
	tests := []struct {
		name       string
		results    []scored
		maxResults int
		want       []string
	}{
		{
			name: "sorts_descending_and_limits",
			results: []scored{
				{"A", 1},
				{"B", 3},
				{"C", 2},
			},
			maxResults: 2,
			want:       []string{"B", "C"},
		},
		{
			name:       "empty_input",
			results:    nil,
			maxResults: 5,
			want:       []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := topNames(tt.results, tt.maxResults)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
