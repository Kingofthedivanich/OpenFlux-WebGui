package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/exitmgr"
	"github.com/p1neappleXpress/OpenFlux/share"
)

// stubManager keeps clients in memory; exitmgr's own tests run real channels.
type stubManager struct {
	mu      sync.Mutex
	clients map[string]exitmgr.ClientStatus
	failAdd error
	cookies []byte
}

func (m *stubManager) YandexCookies() (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cookies != nil, time.Unix(1700000000, 0)
}

func (m *stubManager) SetYandexCookies(data []byte) error {
	if !strings.Contains(string(data), "Session_id") {
		return fmt.Errorf("no Session_id cookie for yandex.ru")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cookies = data
	return nil
}

func (m *stubManager) ClearYandexCookies() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cookies = nil
	return nil
}

func newStubManager() *stubManager {
	return &stubManager{clients: map[string]exitmgr.ClientStatus{}}
}

func (m *stubManager) List() []exitmgr.ClientStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []exitmgr.ClientStatus
	for _, c := range m.clients {
		out = append(out, c)
	}
	return out
}

func (m *stubManager) Get(id string) (exitmgr.ClientStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	return c, ok
}

func (m *stubManager) Logs(id string) ([]string, bool) {
	if _, ok := m.Get(id); !ok {
		return nil, false
	}
	return []string{"line 1", "line 2"}, true
}

func (m *stubManager) Add(cfg exitmgr.ClientConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if m.failAdd != nil {
		return m.failAdd
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[cfg.ID] = exitmgr.ClientStatus{Config: cfg, Status: "running"}
	return nil
}

func (m *stubManager) Update(id string, cfg exitmgr.ClientConfig) error {
	cfg.ID = id
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[id]; !ok {
		return fmt.Errorf("client %q not found", id)
	}
	m.clients[id] = exitmgr.ClientStatus{Config: cfg, Status: "running"}
	return nil
}

func (m *stubManager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.clients[id]; !ok {
		return fmt.Errorf("client %q not found", id)
	}
	delete(m.clients, id)
	return nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return NewServer(newStubManager(), "admin", "s3cret")
}

func do(t *testing.T, srv *Server, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, srv *Server, user, pass string) *http.Cookie {
	t.Helper()
	w := do(t, srv, "POST", "/api/login", loginRequest{Username: user, Password: pass}, nil)
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/api/clients", "/api/clients/x/qr", "/api/clients/x/logs", "/api/transport-types"} {
		if w := do(t, srv, "GET", path, nil, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, w.Code)
		}
	}
}

func TestLoginRejectsWrongCredentials(t *testing.T) {
	srv := newTestServer(t)
	w := do(t, srv, "POST", "/api/login", loginRequest{Username: "admin", Password: "wrong"}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("a session cookie was set despite a failed login")
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	do(t, srv, "POST", "/api/logout", nil, cookie)
	if w := do(t, srv, "GET", "/api/clients", nil, cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want 401", w.Code)
	}
}

func TestAddEditRemoveClient(t *testing.T) {
	mgr := newStubManager()
	srv := NewServer(mgr, "admin", "s3cret")
	cookie := login(t, srv, "admin", "s3cret")

	w := do(t, srv, "POST", "/api/clients", map[string]any{
		"id":   "../evil", // ignored: the panel picks the id
		"name": "  Bob  ",
		"transports": []map[string]string{
			{"type": "vyandex", "url": " https://docs.yandex.ru/edit/d/abcdefghijklmnopqr "},
			{"type": "cupsonline"},
		},
		"direct_port": 41000,
	}, cookie)
	if w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	var created map[string]string
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created["id"]
	st, ok := mgr.Get(id)
	if !ok || id == "../evil" {
		t.Fatalf("client %q not added with a panel id", id)
	}
	if st.Config.Name != "Bob" || st.Config.Transports[0].URL != "https://docs.yandex.ru/edit/d/abcdefghijklmnopqr" || st.Config.DirectPort != 41000 {
		t.Fatalf("config not trimmed/taken over: %+v", st.Config)
	}

	w = do(t, srv, "PUT", "/api/clients/"+id, map[string]any{
		"name":       "Bob",
		"transports": []map[string]string{{"type": "mailru", "url": "https://cloud.mail.ru/public/x"}},
		"mode":       "l3",
	}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if st, _ := mgr.Get(id); st.Config.Mode != "l3" || st.Config.Transports[0].Type != "mailru" {
		t.Fatalf("update not applied: %+v", st.Config)
	}

	if w := do(t, srv, "PUT", "/api/clients/nope", map[string]any{"name": "x"}, cookie); w.Code != http.StatusNotFound {
		t.Fatalf("update of a missing client: %d", w.Code)
	}
	if w := do(t, srv, "DELETE", "/api/clients/"+id, nil, cookie); w.Code != http.StatusOK {
		t.Fatalf("remove: %d", w.Code)
	}
	if _, ok := mgr.Get(id); ok {
		t.Fatal("client still there after remove")
	}
}

func TestAddRejectsInvalidConfig(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")
	w := do(t, srv, "POST", "/api/clients", map[string]any{
		"name": "x", "transports": []map[string]string{{"type": "yandex"}},
	}, cookie)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "url is required") {
		t.Fatalf("add without url: %d %s", w.Code, w.Body)
	}
}

func TestQRAndLogs(t *testing.T) {
	mgr := newStubManager()
	srv := NewServer(mgr, "admin", "s3cret")
	cookie := login(t, srv, "admin", "s3cret")

	link, err := share.MakeLink(share.Config{Transports: []share.Transport{{Type: "yandex", URL: "https://disk.yandex.ru/i/abc"}}})
	if err != nil {
		t.Fatal(err)
	}
	mgr.clients["a1"] = exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "a1"}, Link: link}
	mgr.clients["b2"] = exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "b2"}}

	w := do(t, srv, "GET", "/api/clients/a1/qr", nil, cookie)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("qr: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("qr of a link with a key is cacheable")
	}
	if w := do(t, srv, "GET", "/api/clients/b2/qr", nil, cookie); w.Code != http.StatusConflict {
		t.Fatalf("qr without a link: %d", w.Code)
	}
	if w := do(t, srv, "GET", "/api/clients/zz/qr", nil, cookie); w.Code != http.StatusNotFound {
		t.Fatalf("qr of a missing client: %d", w.Code)
	}

	w = do(t, srv, "GET", "/api/clients/a1/logs", nil, cookie)
	var logs map[string][]string
	json.Unmarshal(w.Body.Bytes(), &logs)
	if w.Code != http.StatusOK || len(logs["lines"]) != 2 {
		t.Fatalf("logs: %d %s", w.Code, w.Body)
	}
}

func TestTransportTypes(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")
	w := do(t, srv, "GET", "/api/transport-types", nil, cookie)
	var types []string
	json.Unmarshal(w.Body.Bytes(), &types)
	if len(types) == 0 || types[0] != "vyandex" {
		t.Fatalf("types = %v, want vyandex first", types)
	}
}

func TestYandexCookies(t *testing.T) {
	mgr := newStubManager()
	srv := NewServer(mgr, "admin", "s3cret")
	cookie := login(t, srv, "admin", "s3cret")

	if w := do(t, srv, "PUT", "/api/yandex-cookies", map[string]string{"cookies": "x"}, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated set: %d", w.Code)
	}
	if w := do(t, srv, "PUT", "/api/yandex-cookies", map[string]string{"cookies": "yandexuid=1"}, cookie); w.Code != http.StatusBadRequest {
		t.Fatalf("cookies without a session: %d", w.Code)
	}
	if w := do(t, srv, "PUT", "/api/yandex-cookies", map[string]string{"cookies": "Session_id=abc"}, cookie); w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	w := do(t, srv, "GET", "/api/yandex-cookies", nil, cookie)
	if !strings.Contains(w.Body.String(), `"set":true`) || strings.Contains(w.Body.String(), "abc") {
		t.Fatalf("status = %s: want set, and never the cookies themselves", w.Body)
	}
	do(t, srv, "DELETE", "/api/yandex-cookies", nil, cookie)
	if w := do(t, srv, "GET", "/api/yandex-cookies", nil, cookie); !strings.Contains(w.Body.String(), `"set":false`) {
		t.Fatalf("after clear: %s", w.Body)
	}
}
