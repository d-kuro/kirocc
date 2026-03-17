package tracing

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// ExtractTraceID returns the OTel trace ID from the context as a 32-char hex string.
// Returns an empty string if no valid OTel span is present (e.g., when OTel is disabled).
func ExtractTraceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.TraceID().IsValid() {
		return ""
	}
	return sc.TraceID().String()
}
