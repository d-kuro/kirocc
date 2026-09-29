package kiroclient

import (
	"net/http"
	"testing"
	"time"
)

func headerTimeoutOf(t *testing.T, c *HTTPClient) time.Duration {
	t.Helper()
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", c.httpClient.Transport)
	}
	return tr.ResponseHeaderTimeout
}

func TestResponseHeaderTimeout(t *testing.T) {
	if got := headerTimeoutOf(t, NewHTTPClient()); got != 30*time.Second {
		t.Fatalf("default = %s, want 30s", got)
	}
	if got := headerTimeoutOf(t, NewHTTPClient(WithResponseHeaderTimeout(90*time.Second))); got != 90*time.Second {
		t.Fatalf("override = %s, want 90s", got)
	}
	if got := headerTimeoutOf(t, NewHTTPClient(WithResponseHeaderTimeout(0))); got != 0 {
		t.Fatalf("zero = %s, want 0 (no limit)", got)
	}
}
