package reqconv

import (
	"crypto/sha256"
	"encoding/hex"
)

const maxToolNameLen = 64

// ToolNameMap provides bidirectional mapping between original and shortened tool names.
// All methods are safe to call on a nil receiver (no-op passthrough).
type ToolNameMap struct {
	toShort    map[string]string
	toOriginal map[string]string
}

// NewToolNameMap creates a new ToolNameMap. Maps are lazily allocated on first use.
func NewToolNameMap() *ToolNameMap {
	return &ToolNameMap{}
}

// Shorten returns name as-is if <= 64 chars. Otherwise shortens, registers mapping,
// and returns the shortened name. Safe to call on nil receiver.
func (m *ToolNameMap) Shorten(name string) string {
	if m == nil || len(name) <= maxToolNameLen {
		return name
	}
	if short, ok := m.toShort[name]; ok {
		return short
	}
	if m.toShort == nil {
		m.toShort = make(map[string]string)
		m.toOriginal = make(map[string]string)
	}
	h := sha256.Sum256([]byte(name))
	short := name[:50] + "_" + hex.EncodeToString(h[:])[:13]
	m.toShort[name] = short
	m.toOriginal[short] = name
	return short
}

// Restore returns the original name for a shortened name, or name itself if not mapped.
// Safe to call on nil receiver.
func (m *ToolNameMap) Restore(name string) string {
	if m == nil {
		return name
	}
	if orig, ok := m.toOriginal[name]; ok {
		return orig
	}
	return name
}

// ReverseMap returns the short→original map for use in the response path.
// Returns nil if no mappings exist.
func (m *ToolNameMap) ReverseMap() map[string]string {
	if m == nil || len(m.toOriginal) == 0 {
		return nil
	}
	return m.toOriginal
}
