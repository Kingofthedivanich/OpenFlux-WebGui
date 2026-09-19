package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) handleYandexDocAvailable(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"available": s.yandexDisk != nil})
}

type generateYandexDocRequest struct {
	Name string `json:"name"`
}

// handleGenerateYandexDoc creates a fresh, publicly-shared blank document on
// the configured Yandex account and returns its share link -- the button
// next to the panel's url field, for when an operator would rather not make
// one by hand and paste it in.
func (s *Server) handleGenerateYandexDoc(w http.ResponseWriter, r *http.Request) {
	if s.yandexDisk == nil {
		writeJSONError(w, http.StatusBadRequest, "not configured: start the panel with --yandex-token-file")
		return
	}
	var req generateYandexDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	url, err := s.yandexDisk.CreateDoc(ctx, req.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}
