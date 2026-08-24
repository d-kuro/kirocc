package messages

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/kiroclient"
)

// Re-exports of httpx error type constants so in-package callers stay concise.
const (
	errTypeInvalidRequest = httpx.ErrTypeInvalidRequest
	errTypeAPI            = httpx.ErrTypeAPI
	ErrTypeAuthentication = httpx.ErrTypeAuthentication
	errTypeStreamError    = httpx.ErrTypeStream
)

// retryableInvalidStateReasons are invalidStateEvent reasons that can be resolved
// by clearing the conversation ID and retrying.
var retryableInvalidStateReasons = map[string]struct{}{
	"CONTENT_LENGTH_EXCEEDS_THRESHOLD": {},
	"INVALID_CONVERSATION_STATE":       {},
	"STALE_CONVERSATION":               {},
}

type upstreamErrorClassification struct {
	retryReason string
	final       streamFinalError
}

type streamFinalError struct {
	status      int
	jsonType    string
	jsonMessage string
	sseType     string
	sseMessage  string
}

func newStreamFinalError(status int, errType, message string) streamFinalError {
	return streamFinalError{
		status:      status,
		jsonType:    errType,
		jsonMessage: message,
		sseType:     errType,
		sseMessage:  message,
	}
}

// classifyUpstreamError separates upstream event classification from response
// writing so streaming callers can choose JSON or SSE based on session state.
func classifyUpstreamError(isException bool, invalidReason, upstreamMessage string) upstreamErrorClassification {
	if isException {
		final := newStreamFinalError(http.StatusBadGateway, errTypeAPI, "upstream exception")
		if upstreamMessage != "" {
			final.sseMessage = upstreamMessage
		}
		return upstreamErrorClassification{final: final}
	}
	final := newStreamFinalError(
		http.StatusBadRequest,
		errTypeInvalidRequest,
		"invalid state: request rejected by upstream",
	)
	final.sseType = "invalid_state"
	if upstreamMessage != "" {
		final.sseMessage = upstreamMessage
	}
	classification := upstreamErrorClassification{
		final: final,
	}
	if _, ok := retryableInvalidStateReasons[invalidReason]; ok {
		classification.retryReason = invalidReason
	}
	return classification
}

// logUpstreamError logs a "kiro api error" with structured attributes when the
// error is an *UpstreamError. Falls back to plain err logging otherwise.
func logUpstreamError(ctx context.Context, short string, err error, extra ...any) {
	attrs := []any{"trace_id", short, "err", err}
	attrs = append(attrs, extra...)
	if ue, ok := errors.AsType[*kiroclient.UpstreamError](err); ok {
		attrs = append(attrs,
			"status", ue.Status,
			"content_type", ue.ContentType,
			"exception", ue.Exception,
			"body", ue.Body,
		)
	}
	slog.ErrorContext(ctx, "kiro api error", attrs...)
}
