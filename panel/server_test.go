package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openflux/exitmgr"
	"openflux/transport"
)

type stubTransport struct{}

func (stubTransport) Start() error                 { return nil }
func (stubTransport) Stop() error                  { return nil }
func (stubTransport) Send(data []byte) error        { return nil }
func (stubTransport) Receive(cb func([]byte))       {}
func (stubTransport) IsConnected() bool             { return true }
func (stubTransport) Stats() transport.TransportStats { return transport.TransportStats{} }

func stubBuilder(exitmgr.ClientConfig, transport.TransportConfig) (transport.Transport, error) {
	return stubTransport{}, nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	mgr := exitmgr.NewManagerWithBuilder(nil, stubBuilder)
	return NewServer(mgr, "admin", "s3cret")
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
	w := do(t, srv, "GET", "/api/clients", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
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

func TestLoginThenAuthenticatedRequestsSucceed(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")
	if cookie == nil {
		t.Fatal("login did not set a session cookie")
	}

	w := do(t, srv, "GET", "/api/clients", nil, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	do(t, srv, "POST", "/api/logout", nil, cookie)

	w := do(t, srv, "GET", "/api/clients", nil, cookie)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want 401", w.Code)
	}
}

func TestAddListRemoveClientOverHTTP(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	addBody := exitmgr.ClientConfig{Name: "test client", Transport: "yandex", URL: "https://disk.yandex.com/i/x"}
	w := do(t, srv, "POST", "/api/clients", addBody, cookie)
	if w.Code != http.StatusCreated {
		t.Fatalf("add status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	var added map[string]string
	json.Unmarshal(w.Body.Bytes(), &added)
	id := added["id"]
	if id == "" {
		t.Fatal("response did not include a generated client id")
	}

	w = do(t, srv, "GET", "/api/clients", nil, cookie)
	var list []exitmgr.ClientStatus
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Config.ID != id {
		t.Fatalf("list = %+v, want one client with id %q", list, id)
	}

	w = do(t, srv, "DELETE", "/api/clients/"+id, nil, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200, body=%s", w.Code, w.Body.String())
	}

	w = do(t, srv, "GET", "/api/clients", nil, cookie)
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("list after delete = %+v, want empty", list)
	}
}

func TestAddClientRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	addBody := exitmgr.ClientConfig{Name: "x", Transport: "yandex", URL: "https://x"}
	w := do(t, srv, "POST", "/api/clients", addBody, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAddClientRejectsInvalidConfig(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	w := do(t, srv, "POST", "/api/clients", exitmgr.ClientConfig{Name: "no transport"}, cookie)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}
