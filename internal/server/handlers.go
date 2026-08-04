package server

import (
	"net/http"
	"time"

	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/models"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	modelList := models.ListModels()
	data := make([]any, 0, len(modelList))
	now := time.Now().Unix()
	for _, m := range modelList {
		entry := map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": "kiro",
		}
		if m.DisplayName != "" {
			entry["display_name"] = m.DisplayName
		}
		data = append(data, entry)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}
