package toolsearch

import (
	"cmp"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

var (
	ErrInvalidPattern = errors.New("invalid_pattern")
	ErrPatternTooLong = errors.New("pattern_too_long")
)

// ErrorCode returns the API error code string for a search error.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidPattern):
		return "invalid_pattern"
	case errors.Is(err, ErrPatternTooLong):
		return "pattern_too_long"
	default:
		return "unavailable"
	}
}

const defaultMaxResults = 5

type scored struct {
	name  string
	score float64
}

// topNames sorts scored results by descending score and returns up to maxResults names.
func topNames(results []scored, maxResults int) []string {
	slices.SortFunc(results, func(a, b scored) int {
		return cmp.Compare(b.score, a.score)
	})
	n := min(maxResults, len(results))
	names := make([]string, n)
	for i := range n {
		names[i] = results[i].name
	}
	return names
}

// Search finds tools matching query. searchType must be "regex" or "bm25".
// Supports "select:Name1,Name2" syntax for exact tool selection.
func Search(query string, tools map[string]anthropic.Tool, searchType string, maxResults int) ([]string, error) {
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	// Handle "select:Tool1,Tool2" exact selection syntax.
	if after, ok := strings.CutPrefix(query, "select:"); ok {
		var names []string
		for name := range strings.SplitSeq(after, ",") {
			name = strings.TrimSpace(name)
			if _, exists := tools[name]; exists && name != "" {
				names = append(names, name)
			}
		}
		return names, nil
	}
	if searchType == SearchTypeBM25 {
		return searchBM25(query, tools, maxResults), nil
	}
	return searchRegex(query, tools, maxResults)
}

// searchRegex matches tools by regex against name and description.
func searchRegex(pattern string, tools map[string]anthropic.Tool, maxResults int) ([]string, error) {
	if len(pattern) > 200 {
		return nil, ErrPatternTooLong
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, ErrInvalidPattern
	}

	// Also build a word-level OR pattern for natural language queries like "read file".
	// This matches tools where any word in the query appears in name or description.
	words := strings.Fields(strings.ToLower(pattern))
	var wordRe *regexp.Regexp
	if len(words) > 1 {
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = regexp.QuoteMeta(w)
		}
		wordRe, _ = regexp.Compile("(?i)(" + strings.Join(quoted, "|") + ")")
	}

	var results []scored

	for name, t := range tools {
		if name == pattern {
			results = append(results, scored{name, 3})
		} else if re.MatchString(name) {
			results = append(results, scored{name, 2})
		} else if re.MatchString(t.Description) {
			results = append(results, scored{name, 1})
		} else if wordRe != nil && (wordRe.MatchString(name) || wordRe.MatchString(t.Description)) {
			results = append(results, scored{name, 1})
		}
	}

	return topNames(results, maxResults), nil
}

// searchBM25 ranks tools using BM25 scoring on name + description.
func searchBM25(query string, tools map[string]anthropic.Tool, maxResults int) []string {
	const (
		k1 = 1.2
		b  = 0.75
	)

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	// Build documents and compute document frequency in a single pass.
	type doc struct {
		name     string
		tokens   []string
		tokenSet map[string]struct{}
	}
	docs := make([]doc, 0, len(tools))
	var totalLen float64
	df := make(map[string]int, len(queryTokens))
	querySet := make(map[string]struct{}, len(queryTokens))
	for _, qt := range queryTokens {
		querySet[qt] = struct{}{}
	}
	for name, t := range tools {
		toks := tokenize(name + " " + t.Description)
		ts := make(map[string]struct{}, len(toks))
		for _, tok := range toks {
			ts[tok] = struct{}{}
		}
		for qt := range querySet {
			if _, ok := ts[qt]; ok {
				df[qt]++
			}
		}
		docs = append(docs, doc{name, toks, ts})
		totalLen += float64(len(toks))
	}
	if len(docs) == 0 {
		return nil
	}
	avgDL := totalLen / float64(len(docs))

	// Score each document.
	n := float64(len(docs))
	results := make([]scored, 0, len(docs))
	for _, d := range docs {
		tf := make(map[string]int, len(d.tokens))
		for _, t := range d.tokens {
			tf[t]++
		}
		var score float64
		dl := float64(len(d.tokens))
		for _, qt := range queryTokens {
			f := float64(tf[qt])
			if f == 0 {
				continue
			}
			idf := math.Log((n-float64(df[qt])+0.5)/(float64(df[qt])+0.5) + 1)
			score += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*dl/avgDL))
		}
		if score > 0 {
			results = append(results, scored{d.name, score})
		}
	}

	return topNames(results, maxResults)
}

// tokenize splits text by spaces and underscores, lowercases, and removes empty tokens.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", " ")
	parts := strings.Fields(s)
	return parts
}
