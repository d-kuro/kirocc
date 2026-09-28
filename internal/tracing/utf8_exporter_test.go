package tracing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const cut = "\xe3\x81" // the first two bytes of a three-byte character

// OTLP's protobuf encoding rejects invalid UTF-8 and fails the whole batch, so
// a clean span exported alongside one carrying invalid UTF-8 would be lost too.
func TestValidUTF8Exporter(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("test")
	_, clean := tracer.Start(context.Background(), "clean")
	clean.End()
	ctx, bad := tracer.Start(context.Background(), "GET /v1/messages"+cut)
	bad.SetAttributes(
		attribute.String("url.path", "/v1/messages"+cut),
		attribute.StringSlice("list", []string{"ok", "x" + cut}),
	)
	bad.AddEvent("http.request", trace.WithAttributes(attribute.String("user_agent", "curl"+cut)))
	RecordError(ctx, errors.New("status 500: "+cut))
	bad.End()
	spans := rec.Ended()

	t.Run("the batch encodes for OTLP", func(t *testing.T) {
		collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer collector.Close()
		otlp, err := otlptracehttp.New(context.Background(),
			otlptracehttp.WithEndpointURL(collector.URL+"/v1/traces"),
			otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
		if err != nil {
			t.Fatal(err)
		}
		if err := (validUTF8Exporter{otlp}).ExportSpans(context.Background(), spans); err != nil {
			t.Fatalf("ExportSpans: %v", err)
		}
	})

	t.Run("invalid bytes are replaced", func(t *testing.T) {
		mem := tracetest.NewInMemoryExporter()
		if err := (validUTF8Exporter{mem}).ExportSpans(context.Background(), spans); err != nil {
			t.Fatalf("ExportSpans: %v", err)
		}
		findSpan(t, mem.GetSpans(), "clean")
		got := findSpan(t, mem.GetSpans(), "GET /v1/messages\ufffd")
		attrs := attribute.NewSet(got.Attributes...)
		path, _ := attrs.Value("url.path")
		list, _ := attrs.Value("list")
		ua, _ := eventAttr(findEvent(t, got, "http.request"), "user_agent")
		msg, _ := eventAttr(findEvent(t, got, "exception"), "exception.message")
		for _, tt := range []struct{ name, got, want string }{
			{"string attribute", path.AsString(), "/v1/messages\ufffd"},
			{"string slice attribute", strings.Join(list.AsStringSlice(), ","), "ok,x\ufffd"},
			{"status description", got.Status.Description, "status 500: \ufffd"},
			{"event attribute", ua.AsString(), "curl\ufffd"},
			{"exception message", msg.AsString(), "status 500: \ufffd"},
		} {
			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		}

		// The SDK's own snapshot must be left untouched.
		origAttrs := attribute.NewSet(spans[1].Attributes()...)
		if orig, _ := origAttrs.Value("url.path"); orig.AsString() != "/v1/messages"+cut {
			t.Errorf("original url.path = %q, want it unchanged", orig.AsString())
		}
	})
}

func TestNewResourceReplacesInvalidUTF8(t *testing.T) {
	res, err := newResource(resource.NewSchemaless(attribute.String("deployment.environment", cut)))
	if err != nil {
		t.Fatal(err)
	}
	attrs := res.Set()
	if env, _ := attrs.Value("deployment.environment"); env.AsString() != "\ufffd" {
		t.Errorf("deployment.environment = %q, want %q", env.AsString(), "\ufffd")
	}
	if name, _ := attrs.Value("service.name"); name.AsString() != ServiceName {
		t.Errorf("service.name = %q, want %q", name.AsString(), ServiceName)
	}
}
