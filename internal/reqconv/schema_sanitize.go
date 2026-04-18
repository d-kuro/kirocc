package reqconv

import (
	"log/slog"
	"maps"
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
				} else if first, ok := arr[0].(map[string]any); ok {
					slog.Warn("lossy schema conversion: using first branch only",
						"combinator", key, "branches", len(arr))
					maps.Copy(result, SanitizeJSONSchema(first))
				}
			}
		case "allOf":
			if arr, ok := value.([]any); ok {
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						maps.Copy(result, SanitizeJSONSchema(m))
					}
				}
			}
		}
	}

	return result
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
