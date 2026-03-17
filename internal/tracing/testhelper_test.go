package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// setupTestExporter creates an InMemoryExporter with a synchronous TracerProvider
// and restores the global TracerProvider on cleanup.
func setupTestExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return exporter
}

// findSpan finds a span by name in SpanStubs.
func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not found in %d spans", name, len(spans))
	return tracetest.SpanStub{}
}

// findEvent finds an event by name in a SpanStub.
func findEvent(t *testing.T, span tracetest.SpanStub, name string) sdktrace.Event {
	t.Helper()
	for _, e := range span.Events {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("event %q not found in span %q", name, span.Name)
	return sdktrace.Event{}
}

// eventAttr retrieves an attribute value by key from an event.
func eventAttr(event sdktrace.Event, key string) (attribute.Value, bool) {
	for _, a := range event.Attributes {
		if string(a.Key) == key {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}
