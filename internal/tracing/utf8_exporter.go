package tracing

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// validUTF8Exporter replaces invalid UTF-8 in span strings before handing the
// batch to the wrapped exporter. OTLP's protobuf encoding rejects invalid
// UTF-8 and the exporter then fails the whole batch, so one bad string would
// drop every span exported with it.
//
// It covers the span name, status description, and string attributes on the
// span and its events. kirocc creates no links, and Init cleans the resource
// once.
type validUTF8Exporter struct {
	sdktrace.SpanExporter
}

func (e validUTF8Exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	valid := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		valid[i] = validUTF8Span{s}
	}
	return e.SpanExporter.ExportSpans(ctx, valid)
}

// validUTF8Span overrides the string-bearing accessors of a ReadOnlySpan.
// Embedding the interface also satisfies its unexported method.
type validUTF8Span struct {
	sdktrace.ReadOnlySpan
}

func (s validUTF8Span) Name() string {
	return validUTF8(s.ReadOnlySpan.Name())
}

func (s validUTF8Span) Status() sdktrace.Status {
	st := s.ReadOnlySpan.Status()
	st.Description = validUTF8(st.Description)
	return st
}

func (s validUTF8Span) Attributes() []attribute.KeyValue {
	return validUTF8Attrs(s.ReadOnlySpan.Attributes())
}

func (s validUTF8Span) Events() []sdktrace.Event {
	events := slices.Clone(s.ReadOnlySpan.Events())
	for i := range events {
		events[i].Attributes = validUTF8Attrs(events[i].Attributes)
	}
	return events
}

// validUTF8Attrs returns a copy of attrs with invalid UTF-8 replaced in string
// and string-slice values, the only string-bearing kinds kirocc records.
func validUTF8Attrs(attrs []attribute.KeyValue) []attribute.KeyValue {
	out := slices.Clone(attrs)
	for i, kv := range out {
		switch kv.Value.Type() {
		case attribute.STRING:
			out[i].Value = attribute.StringValue(validUTF8(kv.Value.AsString()))
		case attribute.STRINGSLICE:
			ss := kv.Value.AsStringSlice()
			for j := range ss {
				ss[j] = validUTF8(ss[j])
			}
			out[i].Value = attribute.StringSliceValue(ss)
		}
	}
	return out
}

func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}
