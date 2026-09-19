package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"openflux/exitmgr"
)

func TestBuildQRTunnelYandex(t *testing.T) {
	status := exitmgr.ClientStatus{
		Config: exitmgr.ClientConfig{ID: "abc123", Name: "phone", Transport: "yandex", URL: "https://docs.yandex.ru/x"},
	}
	tun, err := buildQRTunnel(status, "panel-pub-key")
	if err != nil {
		t.Fatalf("buildQRTunnel: %v", err)
	}
	if tun.Name != "phone" || tun.TransportType != "yandex" {
		t.Fatalf("tun = %+v", tun)
	}
	want := []string{"--role", "client", "--transport", "yandex", "--url", "https://docs.yandex.ru/x"}
	if !equalStrings(tun.TransportConnPayload, want) {
		t.Fatalf("payload = %v, want %v", tun.TransportConnPayload, want)
	}
	if tun.PeerKey == nil || *tun.PeerKey != "panel-pub-key" {
		t.Fatalf("peerKey = %v, want panel-pub-key", tun.PeerKey)
	}
	if tun.EncryptionKey != nil {
		t.Fatalf("encryptionKey = %v, want nil (no psk configured)", *tun.EncryptionKey)
	}
	if tun.ID == 0 {
		t.Fatal("id should be a nonzero derived value")
	}
}

func TestBuildQRTunnelIDIsStableAndPositive(t *testing.T) {
	status := exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "same-id", Transport: "yandex", URL: "https://x"}}
	a, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("id not stable: %d != %d", a.ID, b.ID)
	}
	if a.ID < 0 {
		t.Fatalf("id must be positive (Android's Tunnel.id is a plain Long used as a list key): %d", a.ID)
	}
}

func TestBuildQRTunnelOneme(t *testing.T) {
	status := exitmgr.ClientStatus{
		Config: exitmgr.ClientConfig{ID: "id", Transport: "oneme", MaxToken: "tok", MaxUid: "42"},
	}
	tun, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatalf("buildQRTunnel: %v", err)
	}
	want := []string{"--role", "client", "--transport", "oneme", "--maxToken", "tok", "--maxUid", "42"}
	if !equalStrings(tun.TransportConnPayload, want) {
		t.Fatalf("payload = %v, want %v", tun.TransportConnPayload, want)
	}
}

func TestBuildQRTunnelOnemeRejectsIncompleteConfig(t *testing.T) {
	status := exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "id", Transport: "oneme", MaxToken: "tok"}}
	if _, err := buildQRTunnel(status, "k"); err == nil {
		t.Fatal("expected an error for a missing max_uid")
	}
}

func TestBuildQRTunnelCupsonlineUsesGeneratedRooms(t *testing.T) {
	status := exitmgr.ClientStatus{
		Config:          exitmgr.ClientConfig{ID: "id", Transport: "cupsonline", URL: "ignored-by-cupsonline"},
		CupsonlineRooms: "room-list-b64",
	}
	tun, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatalf("buildQRTunnel: %v", err)
	}
	want := []string{"--role", "client", "--transport", "cupsonline", "--url", "room-list-b64"}
	if !equalStrings(tun.TransportConnPayload, want) {
		t.Fatalf("payload = %v, want %v (cfg.URL must be ignored for cupsonline)", tun.TransportConnPayload, want)
	}
}

func TestBuildQRTunnelCupsonlineRejectsBeforeStart(t *testing.T) {
	status := exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "id", Transport: "cupsonline"}}
	if _, err := buildQRTunnel(status, "k"); err == nil {
		t.Fatal("expected an error when the client hasn't started (no rooms yet)")
	}
}

func TestBuildQRTunnelRejectsMissingURL(t *testing.T) {
	status := exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "id", Transport: "yandex"}}
	if _, err := buildQRTunnel(status, "k"); err == nil {
		t.Fatal("expected an error for a missing url")
	}
}

func TestBuildQRTunnelIncludesLegacyCodecFlag(t *testing.T) {
	status := exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "id", Transport: "yandex", URL: "https://x", Codec: "legacy"}}
	tun, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatalf("buildQRTunnel: %v", err)
	}
	want := []string{"--role", "client", "--transport", "yandex", "--url", "https://x", "--codec", "legacy"}
	if !equalStrings(tun.TransportConnPayload, want) {
		t.Fatalf("payload = %v, want %v", tun.TransportConnPayload, want)
	}
}

func TestBuildQRTunnelReadsPSKFileAsEncryptionKey(t *testing.T) {
	dir := t.TempDir()
	pskPath := filepath.Join(dir, "psk.txt")
	if err := os.WriteFile(pskPath, []byte("  top-secret  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := exitmgr.ClientStatus{
		Config: exitmgr.ClientConfig{ID: "id", Transport: "yandex", URL: "https://x", PSKFile: pskPath},
	}
	tun, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatalf("buildQRTunnel: %v", err)
	}
	if tun.EncryptionKey == nil || *tun.EncryptionKey != "top-secret" {
		t.Fatalf("encryptionKey = %v, want trimmed file contents", tun.EncryptionKey)
	}
}

func TestBuildQRTunnelFallsBackToIDWhenNameEmpty(t *testing.T) {
	status := exitmgr.ClientStatus{Config: exitmgr.ClientConfig{ID: "id123", Transport: "yandex", URL: "https://x"}}
	tun, err := buildQRTunnel(status, "k")
	if err != nil {
		t.Fatalf("buildQRTunnel: %v", err)
	}
	if tun.Name != "id123" {
		t.Fatalf("name = %q, want fallback to client id", tun.Name)
	}
}

// TestQRTunnelJSONShapeMatchesAndroid locks down the exact field names/casing
// the Android app's kotlinx.serialization Tunnel decoder expects -- a rename
// here without a matching Android change would silently break every
// existing QR onboarding flow.
func TestQRTunnelJSONShapeMatchesAndroid(t *testing.T) {
	peerKey := "pk"
	encKey := "ek"
	data, err := json.Marshal(qrTunnel{
		ID:                   1,
		Name:                 "n",
		TransportType:        "yandex",
		TransportConnPayload: []string{"--role", "client"},
		PeerKey:              &peerKey,
		EncryptionKey:        &encKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "name", "transportType", "transportConnPayload", "peerKey", "encryptionKey"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("json is missing %q: %s", key, data)
		}
	}
}

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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
