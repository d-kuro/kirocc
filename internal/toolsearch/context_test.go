package toolsearch

import (
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestNewContext(t *testing.T) {
	tests := []struct {
		name             string
		tools            []anthropic.Tool
		wantNil          bool
		wantSearchType   string
		wantSearchName   string
		wantDeferredLen  int
		wantActiveLen    int
		wantDeferredName string // check one deferred tool name if non-empty
		wantActiveName   string // check one active tool name if non-empty
	}{
		{
			name:    "no_tool_search_tool",
			tools:   []anthropic.Tool{{Name: "Read"}, {Name: "Edit"}},
			wantNil: true,
		},
		{
			name: "regex_type",
			tools: []anthropic.Tool{
				{Type: anthropic.ToolTypeSearchRegex, Name: "tool_search_tool_regex"},
				{Name: "Read"},
			},
			wantSearchType: SearchTypeRegex,
			wantSearchName: "tool_search_tool_regex",
			wantActiveLen:  1,
			wantActiveName: "Read",
		},
		{
			name: "bm25_type",
			tools: []anthropic.Tool{
				{Type: anthropic.ToolTypeSearchBM25, Name: "tool_search_bm25"},
				{Name: "Grep"},
			},
			wantSearchType: SearchTypeBM25,
			wantSearchName: "tool_search_bm25",
			wantActiveLen:  1,
		},
		{
			name: "partitioning",
			tools: []anthropic.Tool{
				{Type: anthropic.ToolTypeSearchRegex, Name: "ts"},
				{Name: "Active1"},
				{Name: "Deferred1", DeferLoading: true},
				{Name: "Active2"},
				{Name: "Deferred2", DeferLoading: true},
			},
			wantSearchType:   SearchTypeRegex,
			wantDeferredLen:  2,
			wantActiveLen:    2,
			wantDeferredName: "Deferred1",
			wantActiveName:   "Active1",
		},
		{
			name: "search_tool_excluded_from_both",
			tools: []anthropic.Tool{
				{Type: anthropic.ToolTypeSearchRegex, Name: "ts"},
			},
			wantSearchType:  SearchTypeRegex,
			wantDeferredLen: 0,
			wantActiveLen:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewContext(tt.tools)
			if tt.wantNil {
				if ctx != nil {
					t.Fatalf("expected nil, got %+v", ctx)
				}
				return
			}
			if ctx == nil {
				t.Fatal("expected non-nil context")
			}
			if tt.wantSearchType != "" && ctx.SearchType != tt.wantSearchType {
				t.Fatalf("SearchType = %q, want %q", ctx.SearchType, tt.wantSearchType)
			}
			if tt.wantSearchName != "" && ctx.SearchToolName != tt.wantSearchName {
				t.Fatalf("SearchToolName = %q, want %q", ctx.SearchToolName, tt.wantSearchName)
			}
			if len(ctx.DeferredTools) != tt.wantDeferredLen {
				t.Fatalf("DeferredTools len = %d, want %d", len(ctx.DeferredTools), tt.wantDeferredLen)
			}
			if len(ctx.ActiveTools) != tt.wantActiveLen {
				t.Fatalf("ActiveTools len = %d, want %d", len(ctx.ActiveTools), tt.wantActiveLen)
			}
			if tt.wantDeferredName != "" {
				if _, ok := ctx.DeferredTools[tt.wantDeferredName]; !ok {
					t.Fatalf("DeferredTools missing %q", tt.wantDeferredName)
				}
			}
			if tt.wantActiveName != "" && ctx.ActiveTools[0].Name != tt.wantActiveName {
				t.Fatalf("ActiveTools[0].Name = %q, want %q", ctx.ActiveTools[0].Name, tt.wantActiveName)
			}
		})
	}
}

func TestPromoteTools(t *testing.T) {
	tests := []struct {
		name            string
		promote         []string
		wantActiveLen   int
		wantDeferredLen int
		wantActiveLast  string // name of last active tool after promotion
	}{
		{
			name:            "promote_existing",
			promote:         []string{"D1"},
			wantActiveLen:   2,
			wantDeferredLen: 1,
			wantActiveLast:  "D1",
		},
		{
			name:            "non_existing_noop",
			promote:         []string{"NoSuchTool"},
			wantActiveLen:   1,
			wantDeferredLen: 2,
		},
		{
			name:            "multiple",
			promote:         []string{"D1", "D2"},
			wantActiveLen:   3,
			wantDeferredLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewContext([]anthropic.Tool{
				{Type: anthropic.ToolTypeSearchRegex, Name: "ts"},
				{Name: "A1"},
				{Name: "D1", DeferLoading: true},
				{Name: "D2", DeferLoading: true},
			})
			ctx.PromoteTools(tt.promote)
			if len(ctx.ActiveTools) != tt.wantActiveLen {
				t.Fatalf("ActiveTools len = %d, want %d", len(ctx.ActiveTools), tt.wantActiveLen)
			}
			if len(ctx.DeferredTools) != tt.wantDeferredLen {
				t.Fatalf("DeferredTools len = %d, want %d", len(ctx.DeferredTools), tt.wantDeferredLen)
			}
			if tt.wantActiveLast != "" {
				last := ctx.ActiveTools[len(ctx.ActiveTools)-1].Name
				if last != tt.wantActiveLast {
					t.Fatalf("last active = %q, want %q", last, tt.wantActiveLast)
				}
			}
		})
	}
}

func TestToolRefMaps(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  int
	}{
		{
			name:  "empty",
			names: nil,
			want:  0,
		},
		{
			name:  "multiple",
			names: []string{"Read", "Edit"},
			want:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToolRefMaps(tt.names)
			if len(got) != tt.want {
				t.Fatalf("len = %d, want %d", len(got), tt.want)
			}
			for i, m := range got {
				if m["type"] != anthropic.BlockTypeToolReference {
					t.Fatalf("got[%d][\"type\"] = %v, want %q", i, m["type"], anthropic.BlockTypeToolReference)
				}
				if m["tool_name"] != tt.names[i] {
					t.Fatalf("got[%d][\"tool_name\"] = %v, want %q", i, m["tool_name"], tt.names[i])
				}
			}
		})
	}
}
