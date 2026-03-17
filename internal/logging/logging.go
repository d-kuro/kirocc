package logging

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/lmittmann/tint"
)

type traceIDKey struct{}

// NewTraceID generates a new UUID v4 trace ID.
func NewTraceID() string {
	return uuid.New().String()
}

// ShortTraceID returns the first 8 characters of a trace ID.
// If the ID is shorter than 8 characters, it is returned as-is.
func ShortTraceID(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// WithTraceID stores a trace ID in the context.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceIDFromContext retrieves the trace ID from the context.
// Returns "" if not set.
func TraceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey{}).(string)
	return id
}

// OTelTraceID converts a UUID trace ID to OTel format (32 hex chars, no hyphens).
func OTelTraceID(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

// sensitiveHeaders lists header names whose values must be redacted in logs.
var sensitiveHeaders = map[string]bool{
	"Authorization":        true,
	"Proxy-Authorization":  true,
	"X-Api-Key":            true,
	"Cookie":               true,
	"Set-Cookie":           true,
	"X-Amz-Security-Token": true,
}

// IsSensitiveHeader returns true if the canonicalized header name should be redacted.
func IsSensitiveHeader(name string) bool {
	if sensitiveHeaders[name] {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "-token") || strings.HasSuffix(lower, "-secret")
}

// SafeHeaders wraps http.Header as a slog.LogValuer so that sanitization
// (map allocation + string joins) is deferred until the log record is
// actually emitted. When debug logging is disabled, zero work is done.
type SafeHeaders struct{ H http.Header }

// LogValue implements slog.LogValuer.
func (s SafeHeaders) LogValue() slog.Value {
	attrs := make([]slog.Attr, 0, len(s.H))
	for k, vs := range s.H {
		if IsSensitiveHeader(k) {
			attrs = append(attrs, slog.String(k, "[REDACTED]"))
		} else {
			attrs = append(attrs, slog.String(k, strings.Join(vs, ", ")))
		}
	}
	return slog.GroupValue(attrs...)
}

// debug=false: tint colored handler (INFO+)
// debug=true: OTel JSON Lines handler (DEBUG+)
func NewHandler(debug bool) slog.Handler {
	if debug {
		return NewOTelHandler(os.Stderr)
	}
	return tint.NewHandler(os.Stderr, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: "2006-01-02 15:04:05",
	})
}
