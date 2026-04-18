package auth

import (
	"encoding/json/v2"
	"strconv"
	"time"
)

// coalesce returns the first non-empty string.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parseExpiresAt extracts an int64 timestamp from values that may be int, float, or string.
func parseExpiresAt(vals ...any) int64 {
	for _, v := range vals {
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			if t > 0 {
				return int64(t)
			}
		case string:
			if t == "" {
				continue
			}
			// Try parsing as unix timestamp string.
			if n, err := strconv.ParseInt(t, 10, 64); err == nil && n > 0 {
				return n
			}
			if n, err := strconv.ParseFloat(t, 64); err == nil && n > 0 {
				return int64(n)
			}
			// Try parsing as ISO 8601 timestamp.
			if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return ts.Unix()
			}
			if ts, err := time.Parse(time.RFC3339, t); err == nil {
				return ts.Unix()
			}
		}
	}
	return 0
}

// extractProfileARN extracts the ARN from a profile value that may be:
// - a plain ARN string
// - a JSON object like {"arn":"...","profile_name":"..."}
func extractProfileARN(raw string) string {
	if raw == "" {
		return ""
	}
	// Try as JSON object with "arn" field.
	var obj struct {
		ARN string `json:"arn"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err == nil && obj.ARN != "" {
		return obj.ARN
	}
	return raw
}
