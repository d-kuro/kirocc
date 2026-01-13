package logging

import (
	"context"
	"log/slog"
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

// NewHandler creates a slog.Handler based on the debug flag.
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
