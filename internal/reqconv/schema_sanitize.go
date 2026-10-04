package reqconv

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
)

// unsupportedKeywords lists JSON Schema keywords that Kiro API rejects.
var unsupportedKeywords = map[string]struct{}{
	"additionalProperties":  {},
	"$schema":               {},
	"propertyNames":         {},
	"default":               {},
	"exclusiveMinimum":      {},
	"exclusiveMaximum":      {},
	"$defs":                 {},
	"$ref":                  {},
	"patternProperties":     {},
	"if":                    {},
	"then":                  {},
	"else":                  {},
	"dependentRequired":     {},
	"dependentSchemas":      {},
	"prefixItems":           {},
	"unevaluatedProperties": {},
	"unevaluatedItems":      {},
	"contentMediaType":      {},
	"contentEncoding":       {},
	"format":                {},
	"pattern":               {},
	"minLength":             {},
	"maxLength":             {},
	"minimum":               {},
	"maximum":               {},
	"minItems":              {},
	"maxItems":              {},
	"uniqueItems":           {},
	"multipleOf":            {},
	"not":                   {},
}

// SanitizeJSONSchema recursively removes fields that Kiro API rejects.
func SanitizeJSONSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(schema))

	// First pass: process all non-combinator keys.
	for key, value := range schema {
		if _, drop := unsupportedKeywords[key]; drop {
			continue
		}
		switch key {
		case "const":
			result["enum"] = []any{value}
		case "required":
			if arr, ok := value.([]any); ok && len(arr) == 0 {
				continue
			}
			result[key] = value
		case "anyOf", "oneOf", "allOf":
			// Handled in second pass.
		default:
			switch v := value.(type) {
			case map[string]any:
				result[key] = SanitizeJSONSchema(v)
			case []any:
				sanitized := make([]any, len(v))
				for i, item := range v {
					if m, ok := item.(map[string]any); ok {
						sanitized[i] = SanitizeJSONSchema(m)
					} else {
						sanitized[i] = item
					}
				}
				result[key] = sanitized
			default:
				result[key] = value
			}
		}
	}

	// Second pass: apply combinators last so they deterministically override.
	for key, value := range schema {
		switch key {
		case "anyOf", "oneOf":
			if arr, ok := value.([]any); ok && len(arr) > 0 {
				if merged := flattenEnumBranches(arr); merged != nil {
					maps.Copy(result, merged)
				} else if nonNull := dropNullBranches(arr); len(nonNull) == 1 {
					if m, ok := nonNull[0].(map[string]any); ok {
						maps.Copy(result, SanitizeJSONSchema(m))
					}
				} else if merged := mergeBranches(nonNull); merged != nil {
					// Keep every branch instead of silently using the first one. A widened
					// (untyped) merge must also drop a sibling "type", or it would re-narrow the schema.
					if _, typed := merged["type"]; !typed {
						delete(result, "type")
					}
					maps.Copy(result, merged)
				}
			}
		case "allOf":
			if arr, ok := value.([]any); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						// Deep-merge properties/required instead of letting a later
						// branch's "properties" replace an earlier one wholesale.
						mergeObjectInto(result, SanitizeJSONSchema(m), true)
					}
				}
			}
		}
	}

	return result
}

// EnsureObjectRoot wraps a sanitized schema in an object envelope if its root
// type is not "object". Call this on the final schema passed to Kiro, not during
// recursive sanitization of nested properties.
//
// Kiro/Bedrock rejects any tool whose inputSchema.json.type is not "object":
//
//	ValidationException: The value at toolConfig.tools.0.toolSpec.inputSchema.json.type
//	must be one of the following: object. reason: TOOL_SCHEMA_INVALID
//
// Anthropic's API has no such constraint, so clients (including Claude Code's
// built-in tools like WebSearch) may send schemas with type:"string" or no type
// at all. This wrapper satisfies the validation without altering semantics for
// the model.
func EnsureObjectRoot(schema map[string]any) map[string]any {
	if len(schema) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	t, _ := schema["type"].(string)
	if t == "object" {
		return schema
	}
	if t == "" {
		// No type declared — add it rather than wrapping.
		schema["type"] = "object"
		return schema
	}
	// Non-object type: wrap in an object envelope.
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"input": schema,
		},
	}
}

// dropNullBranches returns branches that are not {type: "null"}.
func dropNullBranches(branches []any) []any {
	var result []any
	for _, b := range branches {
		m, ok := b.(map[string]any)
		if !ok || m["type"] != "null" {
			result = append(result, b)
		}
	}
	return result
}

// flattenEnumBranches merges anyOf/oneOf branches when all branches have enum values.
// Each branch is sanitized exactly once and the sanitized result is reused for
// enum/type extraction, avoiding the double SanitizeJSONSchema call that the
// previous combinator pass performed per branch.
// Returns a merged schema with combined enum, or nil if not all branches are enum-based.
func flattenEnumBranches(branches []any) map[string]any {
	if len(branches) == 0 {
		return nil
	}
	var allEnums []any
	var typ string
	typConsistent := true
	for _, branch := range branches {
		m, ok := branch.(map[string]any)
		if !ok {
			return nil
		}
		sanitized := SanitizeJSONSchema(m)
		enumVal, hasEnum := sanitized["enum"]
		if !hasEnum {
			return nil
		}
		arr, ok := enumVal.([]any)
		if !ok {
			return nil
		}
		allEnums = append(allEnums, arr...)
		if t, ok := sanitized["type"].(string); ok {
			if typ == "" {
				typ = t
			} else if typ != t {
				typConsistent = false
			}
		} else {
			typConsistent = false
		}
	}
	merged := map[string]any{"enum": allEnums}
	if typ != "" && typConsistent {
		merged["type"] = typ
	}
	return merged
}

// mergeBranches approximates alternatives without retaining only the first branch.
// Objects union their properties and intersect required keys; shared properties are
// merged recursively. A shared primitive type is retained, while mixed types are
// widened and described for the model. This necessarily loses union constraints.
func mergeBranches(branches []any) map[string]any {
	var sanitized []map[string]any
	for _, b := range branches {
		m, ok := b.(map[string]any)
		if !ok {
			return nil
		}
		sanitized = append(sanitized, SanitizeJSONSchema(m))
	}
	if len(sanitized) == 0 {
		return nil
	}
	types := map[string]bool{}
	for _, s := range sanitized {
		t, _ := s["type"].(string)
		if t == "" && s["properties"] != nil {
			t = "object"
		}
		types[t] = true
	}
	if len(types) == 1 {
		var only string
		for t := range types {
			only = t
		}
		result := map[string]any{}
		if only == "object" {
			for i, s := range sanitized {
				mergeObjectInto(result, s, i == 0)
			}
			// A property defined by several branches (a discriminator such as kind: fast | full) must
			// accept every branch's value; keeping the first branch's definition would re-introduce the
			// exact truncation this merge exists to remove.
			if props, ok := result["properties"].(map[string]any); ok {
				for name := range props {
					var defs []any
					for _, s := range sanitized {
						if sp, ok := s["properties"].(map[string]any); ok {
							if d, ok := sp[name]; ok {
								defs = append(defs, d)
							}
						}
					}
					if len(defs) > 1 {
						props[name] = mergePropertyDefs(defs)
					}
				}
			}
			// Keep only keys every branch requires: a key required by one branch alone would make
			// the other branches' valid inputs fail.
			result["required"] = intersectRequired(sanitized)
			if req, ok := result["required"].([]any); !ok || len(req) == 0 {
				delete(result, "required")
			}
			result["type"] = "object"
			return result
		}
		for _, s := range sanitized {
			for k, v := range s {
				if k == "enum" {
					continue
				}
				if _, seen := result[k]; !seen {
					result[k] = v
				}
			}
		}
		if enums := unionEnums(sanitized); enums != nil {
			result["enum"] = enums
		}
		if only != "" {
			result["type"] = only
		}
		return result
	}
	slog.Debug("schema conversion: mixed-type anyOf/oneOf widened to an untyped schema", "types", len(types))
	names := make([]string, 0, len(types))
	for t := range types {
		if t != "" {
			names = append(names, t)
		}
	}
	slices.Sort(names)
	result := map[string]any{}
	for _, s := range sanitized {
		if d, ok := s["description"].(string); ok && d != "" {
			result["description"] = d
			break
		}
	}
	if len(names) > 0 {
		note := "Accepts one of: " + strings.Join(names, ", ") + "."
		if d, ok := result["description"].(string); ok {
			note = d + " " + note
		}
		result["description"] = note
	}
	return result
}

// mergePropertyDefs merges the definitions one property has in several anyOf/oneOf branches, using
// the same rules as the branches themselves: enums unioned, one shared type kept, mixed types widened.
func mergePropertyDefs(defs []any) any {
	for _, d := range defs {
		if _, ok := d.(map[string]any); !ok {
			return defs[0]
		}
	}
	if merged := flattenEnumBranches(defs); merged != nil {
		return merged
	}
	if merged := mergeBranches(defs); merged != nil {
		return merged
	}
	return defs[0]
}

// mergeObjectInto merges src into dst: properties are unioned (existing keys kept), other keys are
// copied when absent. When unionRequired is true, required lists are unioned (allOf semantics).
func mergeObjectInto(dst, src map[string]any, unionRequired bool) {
	for k, v := range src {
		switch k {
		case "properties":
			props, _ := dst["properties"].(map[string]any)
			if props == nil {
				props = map[string]any{}
			}
			if sp, ok := v.(map[string]any); ok {
				for pk, pv := range sp {
					if _, exists := props[pk]; !exists {
						props[pk] = pv
					}
				}
			}
			dst["properties"] = props
		case "required":
			if !unionRequired {
				continue
			}
			seen := map[string]bool{}
			var out []any
			for _, list := range []any{dst["required"], v} {
				if arr, ok := list.([]any); ok {
					for _, r := range arr {
						if s, ok := r.(string); ok && !seen[s] {
							seen[s] = true
							out = append(out, s)
						}
					}
				}
			}
			if len(out) > 0 {
				dst["required"] = out
			}
		default:
			if _, exists := dst[k]; !exists {
				dst[k] = v
			}
		}
	}
}

func intersectRequired(schemas []map[string]any) []any {
	count := map[string]int{}
	var order []string
	for _, s := range schemas {
		arr, _ := s["required"].([]any)
		seen := map[string]bool{}
		for _, r := range arr {
			if str, ok := r.(string); ok && !seen[str] {
				seen[str] = true
				if count[str] == 0 {
					order = append(order, str)
				}
				count[str]++
			}
		}
	}
	var out []any
	for _, k := range order {
		if count[k] == len(schemas) {
			out = append(out, k)
		}
	}
	return out
}

// unionEnums returns the union of enum values when every schema has an enum, else nil (an
// unconstrained branch means any value is allowed, so no enum may be kept).
func unionEnums(schemas []map[string]any) []any {
	var out []any
	for _, s := range schemas {
		arr, ok := s["enum"].([]any)
		if !ok {
			return nil
		}
		// Repeated values are harmless, and enum values may be non-comparable objects
		// or arrays. Match flattenEnumBranches rather than comparing interface values.
		out = append(out, arr...)
	}
	return out
}
