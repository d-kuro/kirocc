package tracing

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/d-kuro/kirocc/internal/logging"
)

// Middleware returns an HTTP middleware that creates OTel server spans and
// records request/response headers and request body as span events.
func Middleware(next http.Handler, bodyLimit int) http.Handler {
	otelMW := otelhttp.NewMiddleware(ServiceName+"-server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)

	return otelMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())

		// Record request headers as span event.
		span.AddEvent("http.request", trace.WithAttributes(sanitizedHeaderAttrs("http.request.header.", r.Header)...))

		// Wrap body for capture.
		if r.Body != nil && r.Body != http.NoBody {
			cap := newBodyCaptureReader(r.Body, bodyLimit)
			r.Body = cap
			defer func() {
				body, truncated, totalSize := cap.captured()
				attrs := []attribute.KeyValue{
					attribute.String("http.request.body", toValidUTF8(body)),
					attribute.Bool("http.request.body.truncated", truncated),
					attribute.Int("http.request.body.size", totalSize),
				}
				span.AddEvent("http.request.body", trace.WithAttributes(attrs...))
			}()
		}

		next.ServeHTTP(w, r)

		// Record response headers as span event.
		span.AddEvent("http.response", trace.WithAttributes(sanitizedHeaderAttrs("http.response.header.", w.Header())...))
	}))
}

// sanitizedHeaderAttrs converts HTTP headers to OTel attributes, redacting sensitive values.
func sanitizedHeaderAttrs(prefix string, h http.Header) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(h))
	for k, vs := range h {
		var val string
		if logging.IsSensitiveHeader(k) {
			val = "[REDACTED]"
		} else if len(vs) == 1 {
			val = vs[0]
		} else {
			val = strings.Join(vs, ", ")
		}
		attrs = append(attrs, attribute.String(prefix+k, val))
	}
	return attrs
}

// bodyCaptureReader wraps an io.ReadCloser and captures the first N bytes read.
type bodyCaptureReader struct {
	r     io.ReadCloser
	buf   bytes.Buffer
	limit int // 0 = unlimited
	total int
}

func newBodyCaptureReader(r io.ReadCloser, limit int) *bodyCaptureReader {
	bcr := &bodyCaptureReader{r: r, limit: limit}
	if limit > 0 {
		bcr.buf.Grow(limit)
	}
	return bcr
}

func (b *bodyCaptureReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.total += n
	if b.limit == 0 || b.buf.Len() < b.limit {
		remaining := n
		if b.limit > 0 {
			remaining = min(n, b.limit-b.buf.Len())
		}
		b.buf.Write(p[:remaining])
	}
	return n, err
}

func (b *bodyCaptureReader) Close() error {
	return b.r.Close()
}

// captured returns the captured body, whether it was truncated, and total bytes read.
func (b *bodyCaptureReader) captured() (body []byte, truncated bool, totalSize int) {
	return b.buf.Bytes(), b.limit > 0 && b.total > b.limit, b.total
}

// toValidUTF8 returns the byte slice as a valid UTF-8 string,
// replacing invalid sequences with the Unicode replacement character.
func toValidUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}
