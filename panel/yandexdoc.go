package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"openflux/yandexdisk"
)

func (s *Server) getYandexDisk() *yandexdisk.Client {
	s.yandexMu.RLock()
	defer s.yandexMu.RUnlock()
	return s.yandexDisk
}

// handleYandexDocAvailable reports two independent things the frontend needs:
// "configured" (was --yandex-token-file passed at all, i.e. should the token
// card show up), and "available" (is there actually a usable token right
// now, i.e. should the generate button be enabled).
func (s *Server) handleYandexDocAvailable(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"configured": s.yandexTokenFile != "",
		"available":  s.getYandexDisk() != nil,
	})
}

type generateYandexDocRequest struct {
	Name string `json:"name"`
}

// handleGenerateYandexDoc creates a fresh, publicly-shared blank document on
// the configured Yandex account and returns its share link -- the button
// next to the panel's url field, for when an operator would rather not make
// one by hand and paste it in.
func (s *Server) handleGenerateYandexDoc(w http.ResponseWriter, r *http.Request) {
	disk := s.getYandexDisk()
	if disk == nil {
		writeJSONError(w, http.StatusBadRequest, "not configured: set a Yandex token below the panel key first")
		return
	}
	var req generateYandexDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	url, err := disk.CreateDoc(ctx, req.Name)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

type setYandexTokenRequest struct {
	Token string `json:"token"`
}

// handleSetYandexToken saves a new Yandex OAuth token to --yandex-token-file
// and swaps it in immediately, so pasting a token into the panel's card
// works with no restart and survives one.
func (s *Server) handleSetYandexToken(w http.ResponseWriter, r *http.Request) {
	if s.yandexTokenFile == "" {
		writeJSONError(w, http.StatusBadRequest, "panel was started without --yandex-token-file")
		return
	}
	var req setYandexTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "token is empty (use DELETE to clear it instead)")
		return
	}

	if err := os.WriteFile(s.yandexTokenFile, []byte(token), 0o600); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save token: "+err.Error())
		return
	}
	s.yandexMu.Lock()
	s.yandexDisk = yandexdisk.New(token)
	s.yandexMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleClearYandexToken removes the saved token and disables document
// generation until a new one is set.
func (s *Server) handleClearYandexToken(w http.ResponseWriter, r *http.Request) {
	if s.yandexTokenFile == "" {
		writeJSONError(w, http.StatusBadRequest, "panel was started without --yandex-token-file")
		return
	}
	if err := os.Remove(s.yandexTokenFile); err != nil && !os.IsNotExist(err) {
		writeJSONError(w, http.StatusInternalServerError, "remove token file: "+err.Error())
		return
	}
	s.yandexMu.Lock()
	s.yandexDisk = nil
	s.yandexMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
