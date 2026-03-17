package tracing

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

func TestInit(t *testing.T) {
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	tracer := otel.Tracer("test")
	_, span := tracer.Start(context.Background(), "test")
	defer span.End()

	if !span.SpanContext().TraceID().IsValid() {
		t.Error("expected valid trace ID from initialized provider")
	}
}

func TestTracer(t *testing.T) {
	setupTestExporter(t)

	tracer := Tracer()
	_, span := tracer.Start(context.Background(), "test-tracer")
	defer span.End()

	if !span.SpanContext().TraceID().IsValid() {
		t.Error("expected valid trace ID from Tracer()")
	}
}

func TestRecordError(t *testing.T) {
	t.Run("records error on active span", func(t *testing.T) {
		exporter := setupTestExporter(t)

		ctx, span := Tracer().Start(context.Background(), "test-record-error")
		testErr := errors.New("something went wrong")
		RecordError(ctx, testErr)
		span.End()

		spans := exporter.GetSpans()
		s := findSpan(t, spans, "test-record-error")

		if s.Status.Code != codes.Error {
			t.Errorf("span status = %v; want %v", s.Status.Code, codes.Error)
		}
		if s.Status.Description != testErr.Error() {
			t.Errorf("span status description = %q; want %q", s.Status.Description, testErr.Error())
		}
	})

	t.Run("noop on background context", func(t *testing.T) {
		// Should not panic with no active span.
		RecordError(context.Background(), errors.New("ignored"))
	})
}
