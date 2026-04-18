package httpx_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d-kuro/kirocc/internal/httpx"
)

func TestWriteError_AuthenticationError(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteError(rec, http.StatusUnauthorized, httpx.ErrTypeAuthentication, "invalid API key")

	if got, want := rec.Code, http.StatusUnauthorized; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("content-type: got %q, want %q", got, want)
	}
	body := rec.Body.String()
	if !strings.HasSuffix(body, "\n") {
		t.Errorf("body should end with newline, got %q", body)
	}

	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Type != "error" {
		t.Errorf("payload.type: got %q, want %q", payload.Type, "error")
	}
	if payload.Error.Type != httpx.ErrTypeAuthentication {
		t.Errorf("error.type: got %q, want %q", payload.Error.Type, httpx.ErrTypeAuthentication)
	}
	if payload.Error.Message != "invalid API key" {
		t.Errorf("error.message: got %q, want %q", payload.Error.Message, "invalid API key")
	}
}

func TestWriteJSON_Map(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteJSON(rec, http.StatusOK, map[string]string{"status": "ok"})

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("content-type: got %q, want %q", got, want)
	}
	body := strings.TrimSpace(rec.Body.String())
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["status"] != "ok" {
		t.Errorf("status: got %q, want %q", m["status"], "ok")
	}
}
