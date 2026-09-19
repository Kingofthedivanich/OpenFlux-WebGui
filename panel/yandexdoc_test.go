package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"openflux/yandexdisk"
)

// newFakeYandexDisk stands in for the real Yandex Disk API, just enough for
// yandexdisk.Client.CreateDoc to succeed end to end (folder PUT, upload
// href, file PUT, publish, and the final public_url lookup).
func newFakeYandexDisk(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/resources", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"public_url": "https://disk.yandex.ru/i/fake123"})
		}
	})
	mux.HandleFunc("/resources/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		hrefBase := "http://" + r.Host
		json.NewEncoder(w).Encode(map[string]string{"href": hrefBase + "/upload-target"})
	})
	mux.HandleFunc("/upload-target", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/resources/publish", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestYandexDocAvailableFalseByDefault(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	w := do(t, srv, "GET", "/api/yandex-doc-available", nil, cookie)
	var got map[string]bool
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["available"] {
		t.Fatal("available = true, want false when no token is configured")
	}
}

func TestYandexDocAvailableRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	w := do(t, srv, "GET", "/api/yandex-doc-available", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestGenerateYandexDocNotConfigured(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	w := do(t, srv, "POST", "/api/yandex-doc", generateYandexDocRequest{Name: "x"}, cookie)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestGenerateYandexDocRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	w := do(t, srv, "POST", "/api/yandex-doc", generateYandexDocRequest{Name: "x"}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestGenerateYandexDocHappyPath(t *testing.T) {
	srv := newTestServer(t)
	fake := newFakeYandexDisk(t)
	srv.yandexDisk = yandexdisk.NewWithBase("test-token", fake.URL)
	cookie := login(t, srv, "admin", "s3cret")

	w := do(t, srv, "GET", "/api/yandex-doc-available", nil, cookie)
	var avail map[string]bool
	json.Unmarshal(w.Body.Bytes(), &avail)
	if !avail["available"] {
		t.Fatal("available = false, want true once a token is configured")
	}

	w = do(t, srv, "POST", "/api/yandex-doc", generateYandexDocRequest{Name: "Alice's phone"}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var got map[string]string
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["url"] != "https://disk.yandex.ru/i/fake123" {
		t.Fatalf("url = %q", got["url"])
	}
}
