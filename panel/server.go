// Package panel is a small local HTTP admin UI for exitmgr.Manager: a
// login-gated API plus an embedded single-page frontend, meant to be bound
// to 127.0.0.1 only.
package panel

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"openflux/exitmgr"
)

//go:embed static
var staticFS embed.FS

const sessionCookieName = "openflux_session"

// Server is the panel's HTTP handler. Construct with NewServer and pass to
// http.ListenAndServe (or similar) yourself -- Server does not own the
// listener, so callers control bind address/TLS/etc.
type Server struct {
	mgr       *exitmgr.Manager
	sessions  *sessionStore
	user      string
	pass      string
	publicKey string
	mux       *http.ServeMux
}

// NewServer builds the panel's HTTP handler. publicKey is the panel's own
// Noise static public key (base64, printed at startup): clients need it as
// their --peer-key, and the panel is the only place an operator should have
// to look for it day to day, so it's also served over /api/panel-key.
func NewServer(mgr *exitmgr.Manager, user, pass, publicKey string) *Server {
	s := &Server{
		mgr:       mgr,
		sessions:  newSessionStore(),
		user:      user,
		pass:      pass,
		publicKey: publicKey,
		mux:       http.NewServeMux(),
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

	s.mux.HandleFunc("GET /api/panel-key", s.requireAuth(s.handlePanelKey))

	s.mux.HandleFunc("GET /api/clients", s.requireAuth(s.handleListClients))
	s.mux.HandleFunc("POST /api/clients", s.requireAuth(s.handleAddClient))
	s.mux.HandleFunc("PUT /api/clients/{id}", s.requireAuth(s.handleUpdateClient))
	s.mux.HandleFunc("DELETE /api/clients/{id}", s.requireAuth(s.handleRemoveClient))
	s.mux.HandleFunc("GET /api/clients/{id}/qr", s.requireAuth(s.handleClientQR))

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

func (s *Server) handlePanelKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"public_key": s.publicKey})
}

func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.List())
}

func (s *Server) handleAddClient(w http.ResponseWriter, r *http.Request) {
	var cfg exitmgr.ClientConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if cfg.ID == "" {
		id, err := generateID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not generate client id")
			return
		}
		cfg.ID = id
	}
	if err := s.mgr.AddClient(cfg); err != nil {
		// Still 4xx (not 5xx): the client is registered (visible via
		// GET /api/clients in an "error" state) even when its transport
		// failed to start, so this is a request-level rejection/warning,
		// not a server fault.
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": cfg.ID})
}

func (s *Server) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var cfg exitmgr.ClientConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.mgr.UpdateClient(id, cfg); err != nil {
		// UpdateClient's only pre-start failure mode is "not found"; a start
		// failure (bad config, transport wouldn't come up) is a 400 like
		// AddClient's, since the client is still registered (in an "error"
		// state) rather than rejected outright.
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.mgr.RemoveClient(id); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
