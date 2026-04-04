package reqconv

import (
	"strings"
	"testing"
)

func TestToolNameMap_Shorten_Short(t *testing.T) {
	m := NewToolNameMap()
	name := "get_weather"
	if got := m.Shorten(name); got != name {
		t.Fatalf("short name should pass through, got %q", got)
	}
}

func TestToolNameMap_Shorten_Exact64(t *testing.T) {
	m := NewToolNameMap()
	name := strings.Repeat("a", 64)
	if got := m.Shorten(name); got != name {
		t.Fatal("64-char name should pass through")
	}
}

func TestToolNameMap_Shorten_Long(t *testing.T) {
	m := NewToolNameMap()
	name := "mcp__plugin_chrome-devtools-mcp_chrome-devtools__get_network_request"
	short := m.Shorten(name)
	if len(short) != maxToolNameLen {
		t.Fatalf("shortened name should be %d chars, got %d", maxToolNameLen, len(short))
	}
	if !strings.HasPrefix(short, name[:50]) {
		t.Fatal("shortened name should start with first 50 chars of original")
	}
}

func TestToolNameMap_Shorten_Deterministic(t *testing.T) {
	m := NewToolNameMap()
	name := strings.Repeat("x", 100)
	a := m.Shorten(name)
	b := m.Shorten(name)
	if a != b {
		t.Fatal("same input should produce same output")
	}
}

func TestToolNameMap_Shorten_NilReceiver(t *testing.T) {
	var m *ToolNameMap
	long := strings.Repeat("a", 100)
	if got := m.Shorten(long); got != long {
		t.Fatal("nil receiver should return name unchanged")
	}
	if got := m.Shorten("short"); got != "short" {
		t.Fatal("nil receiver should return short name unchanged")
	}
}

func TestToolNameMap_Restore(t *testing.T) {
	m := NewToolNameMap()
	name := strings.Repeat("b", 80)
	short := m.Shorten(name)
	if got := m.Restore(short); got != name {
		t.Fatalf("Restore(%q) = %q, want original", short, got)
	}
}

func TestToolNameMap_Restore_Unknown(t *testing.T) {
	m := NewToolNameMap()
	if got := m.Restore("unknown"); got != "unknown" {
		t.Fatal("unknown name should pass through")
	}
}

func TestToolNameMap_Restore_NilReceiver(t *testing.T) {
	var m *ToolNameMap
	if got := m.Restore("anything"); got != "anything" {
		t.Fatal("nil receiver should return name unchanged")
	}
}

func TestToolNameMap_ReverseMap_Empty(t *testing.T) {
	m := NewToolNameMap()
	if got := m.ReverseMap(); got != nil {
		t.Fatal("empty map should return nil")
	}
}

func TestToolNameMap_ReverseMap_WithMappings(t *testing.T) {
	m := NewToolNameMap()
	name := strings.Repeat("c", 70)
	short := m.Shorten(name)
	rm := m.ReverseMap()
	if rm == nil {
		t.Fatal("should return non-nil map")
	}
	if rm[short] != name {
		t.Fatal("reverse map should contain short→original")
	}
}

func TestToolNameMap_LazyInit(t *testing.T) {
	m := NewToolNameMap()
	// Maps should be nil before first long name.
	if m.toShort != nil || m.toOriginal != nil {
		t.Fatal("maps should be nil before first use")
	}
	m.Shorten("short_name")
	if m.toShort != nil {
		t.Fatal("maps should stay nil for short names")
	}
	m.Shorten(strings.Repeat("z", 65))
	if m.toShort == nil {
		t.Fatal("maps should be initialized after first long name")
	}
}
