package messages

import (
	"context"
	"testing"

	"github.com/d-kuro/kirocc/internal/kiroproto"
)

func TestNewUpstreamAttemptCapture_DisabledReturnsNil(t *testing.T) {
	payload := &kiroproto.Payload{}
	got := newUpstreamAttemptCapture(context.Background(), false, payload, "model", false, true, 1)
	if got != nil {
		t.Errorf("expected nil when disabled, got %+v", got)
	}
}

func TestNewUpstreamAttemptCapture_EnabledReturnsNonNil(t *testing.T) {
	payload := &kiroproto.Payload{}
	got := newUpstreamAttemptCapture(context.Background(), true, payload, "model", false, true, 1)
	if got == nil {
		t.Fatal("expected non-nil capture when enabled")
	}
}
