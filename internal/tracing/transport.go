package tracing

import (
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// WrapTransport wraps an http.RoundTripper with OTel tracing that creates
// client spans for each outgoing request, recording headers and body as span events.
func WrapTransport(base http.RoundTripper, bodyLimit int) http.RoundTripper {
	return &tracingTransport{base: base, bodyLimit: bodyLimit}
}

type tracingTransport struct {
	base      http.RoundTripper
	bodyLimit int
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, span := Tracer().Start(req.Context(),
		req.Method+" "+req.URL.Path,
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()
	req = req.WithContext(ctx)

	// Record request attributes.
	span.SetAttributes(
		semconv.HTTPRequestMethodKey.String(req.Method),
		semconv.URLFull(req.URL.String()),
	)

	// Record request headers.
	span.AddEvent("kiro.request", trace.WithAttributes(sanitizedHeaderAttrs("kiro.request.header.", req.Header)...))

	// Capture request body via GetBody (read only up to bodyLimit bytes).
	if req.GetBody != nil {
		bodyReader, err := req.GetBody()
		if err == nil {
			readLimit := t.bodyLimit
			if readLimit <= 0 {
				readLimit = int(req.ContentLength)
			}
			if readLimit <= 0 {
				readLimit = 32 * 1024 // fallback for unknown ContentLength
			}
			buf, _ := io.ReadAll(io.LimitReader(bodyReader, int64(readLimit)))
			_ = bodyReader.Close()
			totalSize := int(req.ContentLength)
			if totalSize < 0 {
				totalSize = len(buf)
			}
			recordBodyEvent(span, "kiro.request", buf, t.bodyLimit > 0 && totalSize > t.bodyLimit, totalSize)
		}
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		RecordError(ctx, err)
		return nil, err
	}

	span.SetAttributes(semconv.HTTPResponseStatusCode(resp.StatusCode))

	// Record response headers.
	span.AddEvent("kiro.response", trace.WithAttributes(sanitizedHeaderAttrs("kiro.response.header.", resp.Header)...))

	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}

	return resp, nil
}
