package reqconv

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/anthropic"
)

func TestConvertTools_Basic(t *testing.T) {
	tools := []anthropic.Tool{
		{
			Name:        "get_weather",
			Description: "Get weather",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		},
	}
	entries := ConvertTools(tools, nil)
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	spec := entries[0].ToolSpecification
	if spec.Name != "get_weather" || spec.Description != "Get weather" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

func TestConvertTools_EmptyDescription(t *testing.T) {
	tools := []anthropic.Tool{{Name: "my_tool", InputSchema: map[string]any{}}}
	entries := ConvertTools(tools, nil)
	if entries[0].ToolSpecification.Description != "Tool: my_tool" {
		t.Fatalf("got %q", entries[0].ToolSpecification.Description)
	}
}

func TestConvertTools_LongDescription(t *testing.T) {
	longDesc := strings.Repeat("x", 50001)
	tools := []anthropic.Tool{{Name: "Bash", Description: longDesc, InputSchema: map[string]any{}}}
	entries := ConvertTools(tools, nil)
	if entries[0].ToolSpecification.Description != longDesc {
		t.Fatal("long description should be kept as-is")
	}
}

func TestConvertTools_LongNameShortened(t *testing.T) {
	longName := strings.Repeat("a", 65)
	tools := []anthropic.Tool{{Name: longName, InputSchema: map[string]any{}}}
	nameMap := NewToolNameMap()
	entries := ConvertTools(tools, nameMap)
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	short := entries[0].ToolSpecification.Name
	if len(short) > maxToolNameLen {
		t.Fatalf("shortened name still too long: %d chars", len(short))
	}
	if nameMap.Restore(short) != longName {
		t.Fatal("reverse mapping failed")
	}
}

func TestSanitizeJSONSchema_RemovesAdditionalProperties(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"x": map[string]any{"type": "string", "additionalProperties": true}},
	}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["additionalProperties"]; ok {
		t.Fatal("additionalProperties should be removed")
	}
	props := got["properties"].(map[string]any)
	x := props["x"].(map[string]any)
	if _, ok := x["additionalProperties"]; ok {
		t.Fatal("nested additionalProperties should be removed")
	}
}

func TestSanitizeJSONSchema_RemovesEmptyRequired(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{}}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["required"]; ok {
		t.Fatal("empty required should be removed")
	}
}

func TestSanitizeJSONSchema_KeepsNonEmptyRequired(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"x"}}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["required"]; !ok {
		t.Fatal("non-empty required should be kept")
	}
}

func TestSanitizeJSONSchema_ConstToEnum(t *testing.T) {
	schema := map[string]any{"const": "hello"}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["const"]; ok {
		t.Fatal("const should be removed")
	}
	enum, ok := got["enum"].([]any)
	if !ok || len(enum) != 1 || enum[0] != "hello" {
		t.Fatalf("expected enum: [hello], got %v", got["enum"])
	}
}

func TestSanitizeJSONSchema_Nil(t *testing.T) {
	got := SanitizeJSONSchema(nil)
	if got == nil {
		t.Fatal("should return empty map, not nil")
	}
}

func TestSanitizeJSONSchema_FlattensAnyOfEnums(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{
				"anyOf": []any{
					map[string]any{"enum": []any{"pending", "in_progress", "completed"}, "type": "string"},
					map[string]any{"enum": []any{"deleted"}, "type": "string"},
				},
			},
		},
	}
	got := SanitizeJSONSchema(schema)
	props := got["properties"].(map[string]any)
	status := props["status"].(map[string]any)
	if _, ok := status["anyOf"]; ok {
		t.Fatal("anyOf should be flattened")
	}
	enum, ok := status["enum"].([]any)
	if !ok {
		t.Fatal("expected enum field")
	}
	if len(enum) != 4 {
		t.Fatalf("expected 4 enum values, got %d: %v", len(enum), enum)
	}
	if status["type"] != "string" {
		t.Fatalf("expected type string, got %v", status["type"])
	}
}

func TestSanitizeJSONSchema_AnyOfNullable_NoWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "null"},
		},
	}
	got := SanitizeJSONSchema(schema)

	if got["type"] != "string" {
		t.Fatalf("expected type string, got %v", got["type"])
	}
	if _, ok := got["anyOf"]; ok {
		t.Fatal("anyOf should be removed")
	}
	if buf.Len() > 0 {
		t.Fatalf("expected no warning for nullable anyOf, got: %q", buf.String())
	}
}

func TestSanitizeJSONSchema_OneOfNullable_NoWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	schema := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "null"},
			map[string]any{"type": "integer", "description": "count"},
		},
	}
	got := SanitizeJSONSchema(schema)

	if got["type"] != "integer" {
		t.Fatalf("expected type integer, got %v", got["type"])
	}
	if got["description"] != "count" {
		t.Fatalf("expected description preserved, got %v", got["description"])
	}
	if buf.Len() > 0 {
		t.Fatalf("expected no warning for nullable oneOf, got: %q", buf.String())
	}
}

// Multi-branch anyOf/oneOf is merged instead of truncated to the first branch.
func TestSanitizeJSONSchema_AnyOfNullableMultiNonNull_WidensWithoutWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
			map[string]any{"type": "null"},
		},
	}
	got := SanitizeJSONSchema(schema)

	if _, ok := got["type"]; ok {
		t.Fatalf("string|integer must not be narrowed to one type, got %v", got["type"])
	}
	if d, _ := got["description"].(string); !strings.Contains(d, "integer") || !strings.Contains(d, "string") {
		t.Fatalf("description should list the alternatives, got %q", d)
	}
	if buf.Len() > 0 {
		t.Fatalf("expected no warning, got: %q", buf.String())
	}
}

func TestSanitizeJSONSchema_OneOfObjects_UnionPropertiesIntersectRequired(t *testing.T) {
	schema := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}},
				"required": []any{"path", "mode"}},
			map[string]any{"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}},
				"required": []any{"path"}},
		},
	}
	got := SanitizeJSONSchema(schema)
	props, _ := got["properties"].(map[string]any)
	for _, k := range []string{"path", "mode", "url"} {
		if _, ok := props[k]; !ok {
			t.Fatalf("property %q from some branch was dropped: %v", k, props)
		}
	}
	req, _ := got["required"].([]any)
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("only keys every branch requires may stay required, got %v", got["required"])
	}
	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}
}

// A shared discriminator must accept values from every branch, not only the first.
func TestSanitizeJSONSchema_OneOfObjects_MergesSharedDiscriminator(t *testing.T) {
	schema := map[string]any{"oneOf": []any{
		map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "fast"}}, "required": []any{"kind"}},
		map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "full"}, "depth": map[string]any{"type": "integer"}}, "required": []any{"kind"}},
	}}
	got := SanitizeJSONSchema(schema)
	props, _ := got["properties"].(map[string]any)
	kind, _ := props["kind"].(map[string]any)
	enum, _ := kind["enum"].([]any)
	if len(enum) != 2 || !slices.Contains(enum, any("fast")) || !slices.Contains(enum, any("full")) {
		t.Fatalf("kind must accept both branch values, got %v", props["kind"])
	}
	if props["depth"] == nil {
		t.Fatal("depth from the second branch was dropped")
	}
	req, _ := got["required"].([]any)
	if len(req) != 1 || req[0] != "kind" {
		t.Fatalf("required = %v, want [kind]", got["required"])
	}
}

func TestUnionEnums_CompoundValues(t *testing.T) {
	values := []any{map[string]any{"mode": "fast"}, []any{"full", "deep"}}
	got := unionEnums([]map[string]any{{"enum": values}, {"enum": values}})
	if len(got) != 4 {
		t.Fatalf("compound enum values lost: %v", got)
	}
	if got := unionEnums([]map[string]any{{"enum": values}, {}}); got != nil {
		t.Fatalf("unconstrained branch must remove enum restriction: %v", got)
	}
}

func TestSanitizeJSONSchema_AnyOfSamePrimitive_KeepsType(t *testing.T) {
	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string", "description": "a path"},
			map[string]any{"type": "string", "description": "a glob"},
		},
	}
	got := SanitizeJSONSchema(schema)
	if got["type"] != "string" {
		t.Fatalf("type = %v, want string", got["type"])
	}
	if _, ok := got["enum"]; ok {
		t.Fatal("unconstrained branches must not produce an enum")
	}
}

func TestSanitizeJSONSchema_AnyOfEnum_NoWarning(t *testing.T) {
	// When all branches are enum-based, no warning should be logged.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"enum": []any{"a"}, "type": "string"},
			map[string]any{"enum": []any{"b"}, "type": "string"},
		},
	}
	SanitizeJSONSchema(schema)

	if buf.Len() > 0 {
		t.Fatalf("expected no warning for enum-based anyOf, got: %q", buf.String())
	}
}

func TestSanitizeJSONSchema_AnyOfNonEnum_KeepsEveryBranchValid(t *testing.T) {
	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string", "description": "a string"},
			map[string]any{"type": "number"},
		},
	}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["anyOf"]; ok {
		t.Fatal("anyOf should be removed")
	}
	if _, ok := got["type"]; ok {
		t.Fatalf("string|number must not be narrowed to the first branch, got type %v", got["type"])
	}
}

func TestSanitizeJSONSchema_AnyOfConstBranches(t *testing.T) {
	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"const": "A"},
			map[string]any{"const": "B"},
			map[string]any{"const": "C"},
		},
	}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["anyOf"]; ok {
		t.Fatal("anyOf should be flattened")
	}
	enum, ok := got["enum"].([]any)
	if !ok {
		t.Fatalf("expected enum field, got %v", got)
	}
	if len(enum) != 3 {
		t.Fatalf("expected 3 enum values, got %d: %v", len(enum), enum)
	}
}

func TestSanitizeJSONSchema_AnyOfMixedTypes_NoType(t *testing.T) {
	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"enum": []any{"hello"}, "type": "string"},
			map[string]any{"enum": []any{42}, "type": "integer"},
		},
	}
	got := SanitizeJSONSchema(schema)
	enum, ok := got["enum"].([]any)
	if !ok {
		t.Fatalf("expected enum field, got %v", got)
	}
	if len(enum) != 2 {
		t.Fatalf("expected 2 enum values, got %d: %v", len(enum), enum)
	}
	if _, ok := got["type"]; ok {
		t.Fatal("type should be omitted for mixed-type enums")
	}
}

func TestSanitizeJSONSchema_AllOfMerged(t *testing.T) {
	schema := map[string]any{
		"allOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
			map[string]any{"required": []any{"a"}},
		},
	}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["allOf"]; ok {
		t.Fatal("allOf should be removed")
	}
	if got["type"] != "object" {
		t.Fatalf("expected type object, got %v", got["type"])
	}
	req, ok := got["required"].([]any)
	if !ok || len(req) != 1 {
		t.Fatalf("expected required [a], got %v", got["required"])
	}
}

// allOf must not let a later branch's properties replace earlier ones.
func TestSanitizeJSONSchema_AllOfDeepMergesProperties(t *testing.T) {
	schema := map[string]any{
		"allOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}},
			map[string]any{"properties": map[string]any{"b": map[string]any{"type": "integer"}}, "required": []any{"b"}},
		},
	}
	got := SanitizeJSONSchema(schema)
	props, _ := got["properties"].(map[string]any)
	if props["a"] == nil || props["b"] == nil {
		t.Fatalf("both branches' properties must survive, got %v", props)
	}
	req, _ := got["required"].([]any)
	if len(req) != 2 {
		t.Fatalf("allOf requires the union, got %v", got["required"])
	}
}

func TestSanitizeJSONSchema_RemovesValidationKeywords(t *testing.T) {
	keywords := []string{
		"format", "pattern",
		"minLength", "maxLength",
		"minimum", "maximum",
		"minItems", "maxItems",
		"uniqueItems", "multipleOf",
		"not",
	}
	for _, kw := range keywords {
		schema := map[string]any{"type": "string", kw: "value"}
		got := SanitizeJSONSchema(schema)
		if _, ok := got[kw]; ok {
			t.Fatalf("%q should be removed", kw)
		}
		if got["type"] != "string" {
			t.Fatalf("type should be preserved when removing %q", kw)
		}
	}
}

func TestSanitizeJSONSchema_RemovesDollarSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "$schema": "http://json-schema.org/draft-07/schema#"}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["$schema"]; ok {
		t.Fatal("$schema should be removed")
	}
}

func TestSanitizeJSONSchema_RemovesPatternProperties(t *testing.T) {
	schema := map[string]any{"type": "object", "patternProperties": map[string]any{}}
	got := SanitizeJSONSchema(schema)
	if _, ok := got["patternProperties"]; ok {
		t.Fatal("patternProperties should be removed")
	}
}

func TestSanitizeJSONSchema_AnyOfOverridesType_Deterministic(t *testing.T) {
	// A widened string|number anyOf must drop the sibling type "object" every time, regardless of map
	// iteration order: keeping it would reject every valid value.
	schema := map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"type": "string", "description": "a string"},
			map[string]any{"type": "number"},
		},
	}
	for i := range 100 {
		got := SanitizeJSONSchema(schema)
		if _, ok := got["type"]; ok {
			t.Fatalf("iteration %d: type = %v, want none (widened union)", i, got["type"])
		}
	}
}

func TestEnsureObjectRoot_AlreadyObject(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	got := EnsureObjectRoot(schema)
	if got["type"] != "object" || got["properties"] == nil {
		t.Fatalf("should be unchanged: %v", got)
	}
}

func TestEnsureObjectRoot_StringType_Wraps(t *testing.T) {
	schema := map[string]any{"type": "string", "description": "search query"}
	got := EnsureObjectRoot(schema)
	if got["type"] != "object" {
		t.Fatalf("expected object root, got %v", got["type"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	input, ok := props["input"].(map[string]any)
	if !ok {
		t.Fatal("expected input property")
	}
	if input["type"] != "string" {
		t.Fatalf("wrapped schema lost type: %v", input)
	}
}

func TestEnsureObjectRoot_NoType_AddsObject(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{"x": map[string]any{}}}
	got := EnsureObjectRoot(schema)
	if got["type"] != "object" {
		t.Fatalf("expected type added, got %v", got["type"])
	}
	// Should not wrap — just add the type field.
	if _, ok := got["properties"].(map[string]any)["x"]; !ok {
		t.Fatal("original properties lost")
	}
}

func TestEnsureObjectRoot_Empty(t *testing.T) {
	got := EnsureObjectRoot(map[string]any{})
	if got["type"] != "object" {
		t.Fatalf("expected object for empty schema, got %v", got)
	}
}

func TestEnsureObjectRoot_ArrayType_Wraps(t *testing.T) {
	schema := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	got := EnsureObjectRoot(schema)
	if got["type"] != "object" {
		t.Fatalf("expected object root, got %v", got["type"])
	}
	props := got["properties"].(map[string]any)
	input := props["input"].(map[string]any)
	if input["type"] != "array" {
		t.Fatalf("wrapped schema lost type: %v", input)
	}
}
