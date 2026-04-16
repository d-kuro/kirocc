package messages

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/kiroclient"
)

// Anthropic API error type constants.
const (
	errTypeInvalidRequest = "invalid_request_error"
	errTypeAPI            = "api_error"
	ErrTypeAuthentication = "authentication_error"
	errTypeStreamError    = "stream_error"
)

// retryableInvalidStateReasons are invalidStateEvent reasons that can be resolved
// by clearing the conversation ID and retrying.
var retryableInvalidStateReasons = map[string]struct{}{
	"CONTENT_LENGTH_EXCEEDS_THRESHOLD": {},
	"INVALID_CONVERSATION_STATE":       {},
	"STALE_CONVERSATION":               {},
}

// handleUpstreamError writes the appropriate error response for upstream failures.
// Returns a non-empty reason string if the error is retryable, or "" if a final error was written.
func handleUpstreamError(w http.ResponseWriter, isException bool, invalidReason string) string {
	if isException {
		WriteErrorJSON(w, http.StatusBadGateway, errTypeAPI, "upstream exception")
		return ""
	}
	if _, ok := retryableInvalidStateReasons[invalidReason]; ok {
		return invalidReason
	}
	WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, "invalid state: request rejected by upstream")
	return ""
}

// logUpstreamError logs a "kiro api error" with structured attributes when the
// error is an *UpstreamError. Falls back to plain err logging otherwise.
func logUpstreamError(ctx context.Context, short string, err error, extra ...any) {
	attrs := []any{"trace_id", short, "err", err}
	attrs = append(attrs, extra...)
	var ue *kiroclient.UpstreamError
	if errors.As(err, &ue) {
		attrs = append(attrs,
			"status", ue.Status,
			"content_type", ue.ContentType,
			"exception", ue.Exception,
			"body", ue.Body,
		)
	}
	slog.ErrorContext(ctx, "kiro api error", attrs...)
}

// WriteErrorJSON writes an Anthropic-compatible JSON error response.
func WriteErrorJSON(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	}); err != nil {
		slog.Error("write message error response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}
