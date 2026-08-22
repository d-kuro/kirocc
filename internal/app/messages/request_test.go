package messages

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// validMessageBody is a minimal Anthropic request that parseAndValidateRequest accepts.
const validMessageBody = `{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"hi"}]}`

func TestParseAndValidateRequest_ContentLengthFastPath(t *testing.T) {
	const limit = 100
	body := strings.Repeat("a", limit+50)
	s := New(nil, nil, WithMaxRequestBytes(limit))

	// httptest.NewRequest sets ContentLength from a *strings.Reader automatically.
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()

	_, n, err := s.parseAndValidateRequest(context.Background(), w, r)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var tooLarge *requestTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v (%T), want *requestTooLargeError", err, err)
	}
	if !tooLarge.exact {
		t.Errorf("exact = false, want true (Content-Length path)")
	}
	if tooLarge.size != int64(len(body)) {
		t.Errorf("size = %d, want %d", tooLarge.size, len(body))
	}
	if tooLarge.limit != limit {
		t.Errorf("limit = %d, want %d", tooLarge.limit, limit)
	}
	if n != int64(len(body)) {
		t.Errorf("returned byte count = %d, want %d", n, len(body))
	}

	msg := tooLarge.Error()
	if !strings.Contains(msg, fmt.Sprint(len(body))) {
		t.Errorf("message %q should contain the observed size %d", msg, len(body))
	}
	if !strings.Contains(msg, fmt.Sprint(limit)) {
		t.Errorf("message %q should contain the limit %d", msg, limit)
	}
}

// TestParseAndValidateRequest_ChunkedPath proves the errors.As(err, &maxErr)
// detection through the jsontext decoder works when Content-Length is
// unknown (e.g. chunked transfer encoding), not just the fast Content-Length
// path above.
//
// The body must be well-formed-looking JSON (not garbage) so the decoder
// keeps pulling bytes instead of failing fast on a syntax error before ever
// reaching the byte that trips MaxBytesReader.
func TestParseAndValidateRequest_ChunkedPath(t *testing.T) {
	const limit = 200
	pad := strings.Repeat("a", 1000)
	body := `{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"` + pad + `"}]}`
	s := New(nil, nil, WithMaxRequestBytes(limit))

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.ContentLength = -1 // simulate chunked / unknown-length body
	w := httptest.NewRecorder()

	_, _, err := s.parseAndValidateRequest(context.Background(), w, r)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var tooLarge *requestTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %v (%T), want *requestTooLargeError", err, err)
	}
	// Draining the rejected body reaches EOF here, so the size is the real
	// total even though Content-Length was absent.
	if !tooLarge.exact {
		t.Errorf("exact = false, want true (drain reached EOF)")
	}
	if got, want := tooLarge.size, int64(len(body)); got != want {
		t.Errorf("size = %d, want %d", got, want)
	}
	if tooLarge.limit != limit {
		t.Errorf("limit = %d, want %d", tooLarge.limit, limit)
	}
}

// TestParseAndValidateRequest_DrainsRejectedBody covers what makes the 413
// deliverable: a rejected body is read to the end rather than left on the
// connection. A client that writes its whole request before reading the
// response — Python's urllib does — otherwise gets a connection reset instead
// of the error. Asserted on both rejection paths, since the fast
// Content-Length path answers before touching the body at all.
func TestParseAndValidateRequest_DrainsRejectedBody(t *testing.T) {
	pad := strings.Repeat("a", 1000)
	body := `{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"` + pad + `"}]}`

	tests := []struct {
		name          string
		contentLength int64
	}{
		{"content-length fast path", int64(len(body))},
		{"unknown length", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(nil, nil, WithMaxRequestBytes(200))
			// Held directly, because r.Body is wrapped in a MaxBytesReader that
			// keeps returning its error once tripped no matter what remains
			// beneath it — only the source shows whether the body was drained.
			src := strings.NewReader(body)
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", src)
			r.ContentLength = tt.contentLength
			w := httptest.NewRecorder()

			if _, _, err := s.parseAndValidateRequest(context.Background(), w, r); err == nil {
				t.Fatal("expected error, got nil")
			}
			if left := src.Len(); left != 0 {
				t.Errorf("body not drained: %d bytes left unread, want 0", left)
			}
		})
	}
}

// TestDrainRejected_BoundedForHostileBody confirms the drain cannot be used to
// make kirocc read forever: an endless body stops at maxDrainBytes and reports
// the size as a lower bound rather than the truth.
func TestDrainRejected_BoundedForHostileBody(t *testing.T) {
	endless := endlessReader{}
	discarded, atEOF := drainRejected(endless)
	if atEOF {
		t.Error("atEOF = true, want false for an endless body")
	}
	if discarded != maxDrainBytes+1 {
		t.Errorf("discarded = %d, want %d", discarded, int64(maxDrainBytes)+1)
	}
}

// endlessReader yields bytes forever, standing in for a body that never ends.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestParseAndValidateRequest_UnderLimit(t *testing.T) {
	s := New(nil, nil, WithMaxRequestBytes(1<<20))

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(validMessageBody))
	w := httptest.NewRecorder()

	req, n, err := s.parseAndValidateRequest(context.Background(), w, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req == nil {
		t.Fatal("expected a non-nil request")
	}
	if n != int64(len(validMessageBody)) {
		t.Errorf("returned byte count = %d, want %d", n, len(validMessageBody))
	}
}

// TestParseAndValidateRequest_Unlimited verifies that a zero limit (unlimited)
// does not reject a body over the default 32 MiB cap for size. It may still
// fail for another reason (malformed JSON), so the assertion is specifically
// that any error is not *requestTooLargeError.
func TestParseAndValidateRequest_Unlimited(t *testing.T) {
	const size = 33 << 20 // 33 MiB, just over the default 32 MiB default cap
	body := strings.Repeat("a", size)
	s := New(nil, nil, WithMaxRequestBytes(0))

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()

	_, _, err := s.parseAndValidateRequest(context.Background(), w, r)
	if err == nil {
		// The body is not valid JSON for an anthropic.Request, so some error is
		// expected; but if the decoder is ever relaxed this is still a pass.
		return
	}
	var tooLarge *requestTooLargeError
	if errors.As(err, &tooLarge) {
		t.Fatalf("unlimited (limit=0) rejected body for size: %v", err)
	}
}

func TestParseAndValidateRequest_MalformedJSON(t *testing.T) {
	s := New(nil, nil, WithMaxRequestBytes(1<<20))

	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("not json"))
	w := httptest.NewRecorder()

	_, _, err := s.parseAndValidateRequest(context.Background(), w, r)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	var tooLarge *requestTooLargeError
	if errors.As(err, &tooLarge) {
		t.Fatalf("malformed JSON under the limit should not be *requestTooLargeError, got %v", err)
	}
}

func TestWriteRequestError(t *testing.T) {
	t.Run("request too large maps to 413", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeRequestError(rec, &requestTooLargeError{size: 200, exact: true, limit: 100})

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
		}
		var payload struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload.Error.Type != "request_too_large" {
			t.Errorf("error.type = %q, want %q", payload.Error.Type, "request_too_large")
		}
	})

	t.Run("other error maps to 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeRequestError(rec, errors.New("boom"))

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		var payload struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(rec.Body.String())), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload.Error.Type != "invalid_request_error" {
			t.Errorf("error.type = %q, want %q", payload.Error.Type, "invalid_request_error")
		}
	})
}
