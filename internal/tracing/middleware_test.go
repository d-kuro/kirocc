package tracing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddleware(t *testing.T) {
	tests := []struct {
		name          string
		bodyLimit     int
		requestBody   string
		wantTruncated bool
	}{
		{"body within limit", 1024, "small body", false},
		{"body exceeds limit", 16, "this body exceeds the limit", true},
		{"unlimited capture", 0, strings.Repeat("x", 1000), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := setupTestExporter(t)

			// Echo handler that reads the full body.
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			})
			handler := Middleware(inner, tt.bodyLimit)

			req := httptest.NewRequest("POST", "/test", strings.NewReader(tt.requestBody))
			req.Header.Set("Authorization", "Bearer secret-token")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			spans := exporter.GetSpans()
			if len(spans) == 0 {
				t.Fatal("no spans recorded")
			}

			span := findSpan(t, spans, "POST /test")

			// Verify request headers event.
			reqEvent := findEvent(t, span, "http.request")
			authVal, ok := eventAttr(reqEvent, "http.request.header.Authorization")
			if !ok {
				t.Fatal("Authorization header attribute not found")
			}
			if authVal.AsString() != "[REDACTED]" {
				t.Errorf("Authorization header not redacted: got %q", authVal.AsString())
			}
			ctVal, ok := eventAttr(reqEvent, "http.request.header.Content-Type")
			if !ok {
				t.Fatal("Content-Type header attribute not found")
			}
			if ctVal.AsString() != "application/json" {
				t.Errorf("Content-Type = %q; want %q", ctVal.AsString(), "application/json")
			}

			// Verify body capture event.
			bodyEvent := findEvent(t, span, "http.request.body")
			truncated, ok := eventAttr(bodyEvent, "http.request.body.truncated")
			if !ok {
				t.Fatal("body.truncated attribute not found")
			}
			if truncated.AsBool() != tt.wantTruncated {
				t.Errorf("body.truncated = %v; want %v", truncated.AsBool(), tt.wantTruncated)
			}

			bodySize, ok := eventAttr(bodyEvent, "http.request.body.size")
			if !ok {
				t.Fatal("body.size attribute not found")
			}
			if int(bodySize.AsInt64()) != len(tt.requestBody) {
				t.Errorf("body.size = %d; want %d", bodySize.AsInt64(), len(tt.requestBody))
			}

			// Verify response headers event.
			findEvent(t, span, "http.response")
		})
	}
}

func TestMiddleware_FlusherPassthrough(t *testing.T) {
	setupTestExporter(t)

	var hasFlusher bool
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasFlusher = w.(http.Flusher)
	}), 1024)
	req := httptest.NewRequest("GET", "/test", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !hasFlusher {
		t.Error("ResponseWriter does not implement http.Flusher through OTel middleware")
	}
}
