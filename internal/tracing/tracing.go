package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the OTel service name used for resource and tracer identification.
const ServiceName = "kirocc"

// Init initializes the OTel TracerProvider with an OTLP HTTP exporter.
// The OTLP endpoint is configured via the standard OTEL_EXPORTER_OTLP_ENDPOINT
// environment variable (defaults to http://localhost:4318).
// Returns a shutdown function that flushes and shuts down the TracerProvider.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	res, err := newResource(resource.Default())
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(validUTF8Exporter{exporter}),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// newResource names the service on top of base. It also replaces invalid
// UTF-8 in the attributes, which validUTF8Exporter leaves alone: the default
// resource includes OTEL_RESOURCE_ATTRIBUTES, which the SDK percent-decodes
// without validating, and a bad value there would fail every export.
func newResource(base *resource.Resource) (*resource.Resource, error) {
	res, err := resource.Merge(base, resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
	))
	if err != nil {
		return nil, err
	}
	return resource.NewWithAttributes(res.SchemaURL(), validUTF8Attrs(res.Attributes())...), nil
}

// Tracer returns the package-level OTel tracer.
func Tracer() trace.Tracer {
	return otel.Tracer(ServiceName)
}

// RecordError records an error on the span in the given context.
// Safe to call even if no active span exists.
func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err)
}
