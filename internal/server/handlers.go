package server

import (
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"time"

	"github.com/d-kuro/kirocc/internal/models"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, map[string]string{"status": "ok"}); err != nil {
		slog.ErrorContext(r.Context(), "write health response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	modelList := models.ListModels()
	data := make([]any, 0, len(modelList))
	now := time.Now().Unix()
	for _, m := range modelList {
		data = append(data, map[string]any{
			"id":       m,
			"object":   "model",
			"created":  now,
			"owned_by": "kiro",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, map[string]any{
		"object": "list",
		"data":   data,
	}); err != nil {
		slog.ErrorContext(r.Context(), "write models response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}

func (s *Server) handleEventLogging(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, map[string]string{"status": "ok"}); err != nil {
		slog.ErrorContext(r.Context(), "write event logging response failed", "err", err)
		return
	}
	_, _ = w.Write([]byte("\n"))
}
