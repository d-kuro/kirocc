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
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
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

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(ServiceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
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
