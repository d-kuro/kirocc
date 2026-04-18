package tracing

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/d-kuro/kirocc/internal/logging"
)

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

// recordBodyEvent adds a "{prefix}.body" span event with the captured body,
// a truncation flag, and the total byte size.
// prefix examples: "http.request", "http.response", "kiro.request".
func recordBodyEvent(span trace.Span, prefix string, body []byte, truncated bool, totalSize int) {
	span.AddEvent(prefix+".body", trace.WithAttributes(
		attribute.String(prefix+".body", toValidUTF8(body)),
		attribute.Bool(prefix+".body.truncated", truncated),
		attribute.Int(prefix+".body.size", totalSize),
	))
}
