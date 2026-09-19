package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"openflux/exitmgr"
)

// The client-shape edge cases (per-transport payloads, PSK, missing fields)
// are covered in clientqr's own tests; these just check the HTTP wiring.

func TestClientQREndpointRequiresAuth(t *testing.T) {
	srv := newTestServer(t)
	w := do(t, srv, "GET", "/api/clients/whatever/qr", nil, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestClientQREndpointReturnsPNGForKnownClient(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	addBody := exitmgr.ClientConfig{Name: "test client", Transport: "yandex", URL: "https://disk.yandex.com/i/x"}
	w := do(t, srv, "POST", "/api/clients", addBody, cookie)
	var added map[string]string
	json.Unmarshal(w.Body.Bytes(), &added)
	id := added["id"]

	w = do(t, srv, "GET", "/api/clients/"+id+"/qr", nil, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	pngMagic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if !bytes.HasPrefix(w.Body.Bytes(), pngMagic) {
		t.Fatal("response body does not start with the PNG magic bytes")
	}
}

func TestClientQREndpointReturns404ForUnknownClient(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	w := do(t, srv, "GET", "/api/clients/does-not-exist/qr", nil, cookie)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestClientQREndpointReturns400WhenNotBuildable(t *testing.T) {
	srv := newTestServer(t)
	cookie := login(t, srv, "admin", "s3cret")

	// cupsonline is valid to add without a url (the exit generates its own
	// rooms once started); the stub transport never populates
	// CupsonlineRooms, so this client can never produce a QR.
	addBody := exitmgr.ClientConfig{Name: "cups", Transport: "cupsonline"}
	w := do(t, srv, "POST", "/api/clients", addBody, cookie)
	var added map[string]string
	json.Unmarshal(w.Body.Bytes(), &added)
	id := added["id"]

	w = do(t, srv, "GET", "/api/clients/"+id+"/qr", nil, cookie)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}
