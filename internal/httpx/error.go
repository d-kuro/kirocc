package httpx

import (
	"encoding/json/v2"
	"log/slog"
	"net/http"
)

// Anthropic-compatible error type constants.
const (
	ErrTypeInvalidRequest = "invalid_request_error"
	ErrTypeAPI            = "api_error"
	ErrTypeAuthentication = "authentication_error"
	ErrTypeStream         = "stream_error"
)

// WriteError writes an Anthropic-compatible JSON error response.
func WriteError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	}); err != nil {
		slog.Error("httpx: write error response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, body); err != nil {
		slog.Error("httpx: write json response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}
