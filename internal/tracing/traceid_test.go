package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestExtractTraceID(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) context.Context
		wantLen int
	}{
		{
			name:    "no span in context",
			setup:   func(t *testing.T) context.Context { return context.Background() },
			wantLen: 0,
		},
		{
			name: "with valid span",
			setup: func(t *testing.T) context.Context {
				setupTestExporter(t)
				tracer := otel.Tracer("test")
				ctx, span := tracer.Start(context.Background(), "test-span")
				t.Cleanup(func() { span.End() })
				return ctx
			},
			wantLen: 32,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setup(t)
			got := ExtractTraceID(ctx)
			if len(got) != tt.wantLen {
				t.Errorf("ExtractTraceID() len = %d; want %d (value: %q)", len(got), tt.wantLen, got)
			}
		})
	}
}
