package messages

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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

// HandleCountTokens serves POST /v1/messages/count_tokens.
func (s *Service) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	req, err := parseAndValidateRequest(r.Context(), w, r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
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

	payload, _, err := reqconv.BuildPayload(req, reqconv.BuildOptions{ProfileARN: profileARN, ModelID: kiroModel, ConversationID: ccSessionID, Thinking: thinking, ThinkingBudget: 0})
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

// parseAndValidateRequest decodes and validates an Anthropic request from the HTTP body.
func parseAndValidateRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) (*anthropic.Request, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req anthropic.Request
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		slog.DebugContext(ctx, "client request body", "request_body", jsontext.Value(raw))
		if err := json.UnmarshalDecode(jsontext.NewDecoder(bytes.NewReader(raw)), &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
	} else {
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	return &req, nil
}
