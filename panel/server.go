// Package panel is a small login-gated web admin UI for the exit panel
// (--role=exit-panel): it adds, edits and removes clients, each an exit
// channel run by exitmgr, and shows their status, logs and the openflux://
// link and QR code a client connects with. Bind it to loopback only.
package panel

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/p1neappleXpress/OpenFlux/exitmgr"
	"github.com/p1neappleXpress/OpenFlux/share"
)

//go:embed static
var staticFS embed.FS

const sessionCookieName = "openflux_session"

// Manager is what the panel needs from exitmgr.Manager.
type Manager interface {
	List() []exitmgr.ClientStatus
	Get(id string) (exitmgr.ClientStatus, bool)
	Logs(id string) ([]string, bool)
	Add(cfg exitmgr.ClientConfig) error
	Update(id string, cfg exitmgr.ClientConfig) error
	Remove(id string) error
	YandexCookies() (bool, time.Time)
	SetYandexCookies(data []byte) error
	ClearYandexCookies() error
}

// Server is the panel's HTTP handler. It does not own the listener.
type Server struct {
	mgr      Manager
	sessions *sessionStore
	user     string
	pass     string
	mux      *http.ServeMux
}

// NewServer builds the panel's HTTP handler.
func NewServer(mgr Manager, user, pass string) *Server {
	s := &Server{
		mgr:      mgr,
		sessions: newSessionStore(),
		user:     user,
		pass:     pass,
		mux:      http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/session", s.handleSession)

	s.mux.HandleFunc("GET /api/transport-types", s.requireAuth(s.handleTransportTypes))
	s.mux.HandleFunc("GET /api/clients", s.requireAuth(s.handleListClients))
	s.mux.HandleFunc("POST /api/clients", s.requireAuth(s.handleAddClient))
	s.mux.HandleFunc("PUT /api/clients/{id}", s.requireAuth(s.handleUpdateClient))
	s.mux.HandleFunc("DELETE /api/clients/{id}", s.requireAuth(s.handleRemoveClient))
	s.mux.HandleFunc("GET /api/clients/{id}/qr", s.requireAuth(s.handleClientQR))
	s.mux.HandleFunc("GET /api/clients/{id}/logs", s.requireAuth(s.handleClientLogs))

	s.mux.HandleFunc("GET /api/yandex-cookies", s.requireAuth(s.handleYandexCookies))
	s.mux.HandleFunc("PUT /api/yandex-cookies", s.requireAuth(s.handleSetYandexCookies))
	s.mux.HandleFunc("DELETE /api/yandex-cookies", s.requireAuth(s.handleClearYandexCookies))

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("panel: embedded static assets missing: " + err.Error())
	}
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil || !s.sessions.valid(c.Value) {
			writeJSONError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		// Client links carry encryption keys: never cache API answers.
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !checkCredentials(req.Username, req.Password, s.user, s.pass) {
		writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := s.sessions.create()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	authenticated := false
	if c, err := r.Cookie(sessionCookieName); err == nil {
		authenticated = s.sessions.valid(c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": authenticated})
}

func (s *Server) handleTransportTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, exitmgr.Types())
}

func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.List())
}

// clientRequest is what the add/edit form sends: the panel picks the id
// and keeps the context, so neither is taken from the browser.
type clientRequest struct {
	Name       string                   `json:"name"`
	Transports []exitmgr.TransportEntry `json:"transports"`
	Mode       string                   `json:"mode"`
	DirectPort int                      `json:"direct_port"`
}

func (req clientRequest) config() exitmgr.ClientConfig {
	ts := make([]exitmgr.TransportEntry, 0, len(req.Transports))
	for _, t := range req.Transports {
		t.Type = strings.TrimSpace(t.Type)
		t.URL = strings.TrimSpace(t.URL)
		ts = append(ts, t)
	}
	return exitmgr.ClientConfig{
		Name:       strings.TrimSpace(req.Name),
		Transports: ts,
		Mode:       req.Mode,
		DirectPort: req.DirectPort,
	}
}

func (s *Server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	var req clientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg := req.config()
	id, err := generateID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not generate client id")
		return
	}
	cfg.ID = id
	if err := s.mgr.Add(cfg); err != nil {
		// A client whose channel failed to start is still registered (and
		// listed with its error), so this is a 4xx, not a server fault.
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": cfg.ID})
}

func (s *Server) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req clientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := s.mgr.Get(id); !ok {
		writeJSONError(w, http.StatusNotFound, "client not found")
		return
	}
	if err := s.mgr.Update(id, req.config()); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveClient(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Remove(r.PathValue("id")); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleClientQR serves the QR code of the client's openflux:// link, which
// the OpenFlux apps scan to add the profile.
func (s *Server) handleClientQR(w http.ResponseWriter, r *http.Request) {
	st, ok := s.mgr.Get(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "client not found")
		return
	}
	if st.Link == "" {
		writeJSONError(w, http.StatusConflict, "no link yet: the channel has not printed one (still starting, or rooms not created yet)")
		return
	}
	png, err := share.PNG(st.Link, 512)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

func (s *Server) handleClientLogs(w http.ResponseWriter, r *http.Request) {
	lines, ok := s.mgr.Logs(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "client not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"lines": lines})
}

func generateID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
