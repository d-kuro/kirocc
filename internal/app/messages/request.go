package messages

import (
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/d-kuro/kirocc/internal/anthropic"
	"github.com/d-kuro/kirocc/internal/models"
	"github.com/d-kuro/kirocc/internal/reqconv"
	"github.com/d-kuro/kirocc/internal/tokencount"
)

// HandleCountTokens serves POST /v1/messages/count_tokens.
func (s *Service) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	req, err := parseAndValidateRequest(w, r)
	if err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
		return
	}

	profileARN := ""
	if creds, err := s.auth.GetToken(r.Context()); err == nil {
		profileARN = creds.ProfileARN
	}

	kiroModel, thinking, _ := models.Resolve(req.Model)
	if req.IsThinkingEnabled() {
		thinking = true
	}

	payload, err := reqconv.BuildPayload(req, reqconv.BuildOptions{ProfileARN: profileARN, ModelID: kiroModel, Thinking: thinking, ThinkingBudget: 0, EnvState: s.envState})
	if err != nil {
		WriteErrorJSON(w, http.StatusBadRequest, errTypeInvalidRequest, err.Error())
		return
	}

	data, err := json.Marshal(payload)
	if err != nil {
		WriteErrorJSON(w, http.StatusInternalServerError, errTypeAPI, "failed to serialize payload")
		return
	}

	n, err := tokencount.CountBytes(data)
	if err != nil {
		WriteErrorJSON(w, http.StatusInternalServerError, errTypeAPI, "token counting unavailable")
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
func parseAndValidateRequest(w http.ResponseWriter, r *http.Request) (*anthropic.Request, error) {
	var req anthropic.Request
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages must not be empty")
	}
	return &req, nil
}
