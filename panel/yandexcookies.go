package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// The Yandex sign-in every channel's vyandex carrier uses (see
// exitmgr.Manager.SetYandexCookies). The cookies are never sent back: the
// panel only says whether a sign-in is set and since when.

func (s *Server) handleYandexCookies(w http.ResponseWriter, r *http.Request) {
	set, at := s.mgr.YandexCookies()
	resp := map[string]any{"set": set}
	if set {
		resp["set_at"] = at.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

type setYandexCookiesRequest struct {
	Cookies string `json:"cookies"`
}

// handleSetYandexCookies takes a cookies.txt (Netscape format, from a browser
// extension) or a "name=value; ..." Cookie header, and restarts every channel
// with it.
func (s *Server) handleSetYandexCookies(w http.ResponseWriter, r *http.Request) {
	var req setYandexCookiesRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.mgr.SetYandexCookies([]byte(req.Cookies)); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleClearYandexCookies(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.ClearYandexCookies(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
