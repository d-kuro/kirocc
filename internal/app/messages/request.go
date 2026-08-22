package messages

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/models"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/tokencount"
)

// requestTooLargeError reports a client body that exceeded the configured cap.
// It is distinct from a malformed-JSON error so callers can answer 413 instead
// of 400: "too big" is actionable (drop images, /compact), "malformed" is not,
// and a client that cannot tell them apart retries the same oversized body
// forever.
type requestTooLargeError struct {
	// size is the body size in bytes: exact when Content-Length was present or
	// the body was drained to EOF, otherwise a lower bound.
	size int64
	// exact reports whether size is the full body size rather than a lower bound.
	exact bool
	limit int64
}

func (e *requestTooLargeError) Error() string {
	if e.exact {
		return fmt.Sprintf("request body too large: %d bytes exceeds the %d byte limit; "+
			"remove images from the conversation, run /compact, or raise -max-request-bytes",
			e.size, e.limit)
	}
	return fmt.Sprintf("request body too large: exceeded the %d byte limit (read %d bytes before aborting); "+
		"remove images from the conversation, run /compact, or raise -max-request-bytes",
		e.limit, e.size)
}

// countingReader tracks how many bytes were read from the underlying body, so
// the size is known both for the "--> POST /v1/messages" log line and for the
// too-large error when Content-Length was absent.
type countingReader struct {
	r io.ReadCloser
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *countingReader) Close() error { return c.r.Close() }

// maxDrainBytes bounds how much of a rejected body is read and discarded before
// answering. It is not a tuning knob; it exists so the error is deliverable.
// Many HTTP clients write the whole request before reading any of the response
// (Python's urllib among them). Answering an oversized request without reading
// it leaves unread bytes on the connection, and closing then reaches the client
// as a connection reset rather than the 413 — a transport failure in place of
// the diagnosable error this path exists to produce. Reading the rest first lets
// the client finish writing and see the answer; nginx solves the same problem
// the same way, with lingering_close. Bounded so a hostile body cannot make
// kirocc read forever.
const maxDrainBytes = 64 << 20

// drainRejected discards up to maxDrainBytes of a rejected request body,
// reporting how much it discarded and whether it reached EOF. It must be given
// the reader beneath the cap: a MaxBytesReader that has already tripped returns
// its error forever and would discard nothing.
func drainRejected(body io.Reader) (discarded int64, atEOF bool) {
	// One byte past the bound distinguishes "drained it all" from "there was
	// more", which is what decides whether the reported size is exact.
	n, err := io.CopyN(io.Discard, body, maxDrainBytes+1)
	if err == nil {
		return n, false
	}
	return n, errors.Is(err, io.EOF)
}

// HandleCountTokens serves POST /v1/messages/count_tokens.
func (s *Service) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	req, _, err := s.parseAndValidateRequest(r.Context(), w, r)
	if err != nil {
		writeRequestError(w, err)
		return
	}

	profileARN := ""
	if creds, err := s.auth.GetToken(r.Context()); err == nil {
		profileARN = creds.ProfileARN
	} else {
		slog.DebugContext(r.Context(), "count_tokens proceeding without credentials", "err", err)
	}

	kiroModel, thinking, _, _ := models.Resolve(req.Model, anthropic.HasContext1MBeta(r.Header))
	if req.IsThinkingEnabled() {
		thinking = true
	}

	ccSessionID := r.Header.Get(headerCCSessionID)

	// Mirror the live send path so token counts include effort (envState is
	// derived inside BuildPayload from the system prompt) and the same tool
	// entries: server-side tool definitions are filtered and replaced by their
	// synthetic Kiro tools exactly as the live /v1/messages payload does.
	effort := resolveEffort(r.Context(), kiroModel, req, thinking)
	tsCtx, advCtx := newServerToolContexts(req)

	payload, _, err := reqconv.BuildPayload(req, reqconv.BuildOptions{
		ProfileARN:     profileARN,
		ModelID:        kiroModel,
		ConversationID: ccSessionID,
		Effort:         effort,
		ToolSearchCtx:  tsCtx,
		AdvisorCtx:     advCtx,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, errTypeAPI, "failed to serialize payload")
		return
	}

	n, err := tokencount.CountBytes(data)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, errTypeAPI, "token counting unavailable")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, map[string]int{"input_tokens": n}); err != nil {
		slog.ErrorContext(r.Context(), "write count_tokens response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}

// writeRequestError answers a parseAndValidateRequest failure, mapping an
// oversized body to 413 request_too_large and everything else to 400.
func writeRequestError(w http.ResponseWriter, err error) {
	var tooLarge *requestTooLargeError
	if errors.As(err, &tooLarge) {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, httpx.ErrTypeRequestTooLarge, err.Error())
		return
	}
	httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
}

// parseAndValidateRequest decodes and validates an Anthropic request from the
// HTTP body, returning the number of body bytes read alongside it. A body over
// s.maxRequestBytes yields a *requestTooLargeError; a zero limit means unlimited.
func (s *Service) parseAndValidateRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) (*anthropic.Request, int64, error) {
	limit := int64(s.maxRequestBytes)
	// Content-Length is advisory, but when the client sends one — Claude Code
	// always does — an oversized body can be rejected before reading a byte of
	// it, and the reported size is the real one rather than a lower bound.
	if limit > 0 && r.ContentLength > limit {
		drainRejected(r.Body)
		return nil, r.ContentLength, &requestTooLargeError{size: r.ContentLength, exact: true, limit: limit}
	}
	// The counter sits *under* the cap, so it sees every byte consumed from the
	// body: MaxBytesReader reads one byte past its limit to detect the overflow
	// but does not hand that byte back, and counting above the cap would lose
	// it. It also makes the drain below self-accounting, leaving counter.n the
	// single source of truth for the size.
	counter := &countingReader{r: r.Body}
	var body io.ReadCloser = counter
	if limit > 0 {
		body = http.MaxBytesReader(w, counter, limit)
	}
	r.Body = body

	toError := func(err error) error {
		// MaxBytesReader's error survives the jsontext decoder's wrapping, so
		// errors.As is the primary signal; the byte count is the fallback for a
		// reader that reports the truncation differently.
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) || (limit > 0 && counter.n >= limit) {
			// Draining yields the true total when it reaches the end, so a body
			// sent without a Content-Length still gets its real size reported.
			// It reads through the counter, so counter.n covers the drain too.
			_, atEOF := drainRejected(counter)
			return &requestTooLargeError{size: counter.n, exact: atEOF, limit: limit}
		}
		return fmt.Errorf("invalid request: %w", err)
	}

	var req anthropic.Request
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, counter.n, toError(err)
		}
		slog.DebugContext(ctx, "client request body", "request_body", jsontext.Value(raw))
		if err := json.UnmarshalDecode(jsontext.NewDecoder(bytes.NewReader(raw)), &req); err != nil {
			return nil, counter.n, toError(err)
		}
	} else {
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
			return nil, counter.n, toError(err)
		}
	}
	if len(req.Messages) == 0 {
		return nil, counter.n, fmt.Errorf("messages must not be empty")
	}
	return &req, counter.n, nil
}
