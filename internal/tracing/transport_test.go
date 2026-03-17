package tracing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func TestWrapTransport(t *testing.T) {
	tests := []struct {
		name           string
		serverStatus   int
		bodyLimit      int
		wantSpanStatus codes.Code
	}{
		{"success", http.StatusOK, 1024, codes.Unset},
		{"server error", http.StatusInternalServerError, 1024, codes.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := setupTestExporter(t)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.serverStatus)
			}))
			t.Cleanup(server.Close)

			tracer := otel.Tracer("test")
			ctx, parentSpan := tracer.Start(context.Background(), "parent")
			defer parentSpan.End()

			transport := WrapTransport(http.DefaultTransport, tt.bodyLimit)
			req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/generateAssistantResponse", bytes.NewReader([]byte("test body")))
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer token")

			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip failed: %v", err)
			}
			_ = resp.Body.Close()

			spans := exporter.GetSpans()

			// Find the client span.
			var found bool
			for _, s := range spans {
				if s.SpanKind != trace.SpanKindClient {
					continue
				}
				found = true

				if s.Status.Code != tt.wantSpanStatus {
					t.Errorf("span status = %v; want %v", s.Status.Code, tt.wantSpanStatus)
				}

				// Verify request event with sanitized headers.
				reqEvent := findEvent(t, s, "kiro.request")
				authVal, ok := eventAttr(reqEvent, "kiro.request.header.Authorization")
				if !ok {
					t.Fatal("Authorization header not found in request event")
				}
				if authVal.AsString() != "[REDACTED]" {
					t.Errorf("Authorization not redacted: %q", authVal.AsString())
				}

				// Verify body capture event.
				findEvent(t, s, "kiro.request.body")

				// Verify response event.
				findEvent(t, s, "kiro.response")
				break
			}
			if !found {
				t.Fatal("no client span found")
			}
		})
	}
}
