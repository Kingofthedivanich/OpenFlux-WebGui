package exitmgr

import (
	"os"
	"path/filepath"
	"testing"

	"openflux/transport"
)

// BuildTransport's own layering (encryption under the codec, multi-stream
// wrap order, etc.) is transportstack.Build's responsibility and tested
// exhaustively there. These tests cover what's actually exitmgr's: does
// BuildTransport translate a ClientConfig into transportstack.Params
// correctly, and wire in the panel's static key / this client's PSK.

// Integration smoke test: BuildTransport must still produce a working,
// correctly-layered stack after being routed through transportstack.Build.
// The full matrix of layering behavior (encryption-under-codec, legacy
// codec, multi-stream) lives in transportstack's own tests.
func TestBuildTransportProducesAWorkingEncryptedStack(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	cfg := ClientConfig{ID: "c1", Transport: "yandex", URL: "https://disk.yandex.com/i/x"}
	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	batched, ok := trans.(*transport.BatchedTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.BatchedTransport", trans)
	}
	if _, ok := batched.Transport.(*transport.EncryptedTransport); !ok {
		t.Fatalf("codec wraps %T, want *transport.EncryptedTransport underneath", batched.Transport)
	}
}

// unwrapCupsonline (used to surface the room list a cupsonline client needs
// as --url, see manager.go) depends on BuildTransport passing IsExit=true
// through to transportstack.Build so cupsonline gets isClient=false. A
// wiring mistake there wouldn't show up in a yandex-backed test.
func TestBuildTransportCupsonlineIsReachableByUnwrapCupsonline(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}

	cfg := ClientConfig{ID: "c1", Transport: "cupsonline"}
	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}

	cups, ok := unwrapCupsonline(trans)
	if !ok {
		t.Fatalf("unwrapCupsonline could not reach a *cupsonline.CupsonlineTransport through %T", trans)
	}
	// RoomsPacked is empty before Start creates the rooms; just confirm the
	// unwrapped value is live (a nil pointer would panic here).
	_ = cups.RoomsPacked()
}

// Multi-stream smoke test: confirms cfg.URL's comma-separated value reaches
// transportstack.Build as-is. The multi-stream layering itself (each
// document getting a complete stack, combined via MultiStreamTransport) is
// covered exhaustively in transportstack's tests.
func TestBuildTransportMultiStreamURL(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	cfg := ClientConfig{
		ID:        "c1",
		Transport: "yandex",
		URL:       "https://disk.yandex.com/i/b,https://disk.yandex.com/i/a",
	}
	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	ms, ok := trans.(*transport.MultiStreamTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.MultiStreamTransport", trans)
	}
	if got := len(ms.Streams()); got != 2 {
		t.Fatalf("got %d streams, want 2", got)
	}
}

func TestBuildTransportRejectsMultiStreamForUnsupportedTransport(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	cfg := ClientConfig{ID: "c1", Transport: "mailru", URL: "https://cloud.mail.ru/public/a,https://cloud.mail.ru/public/b"}
	if _, err := BuildTransport(cfg, transport.DefaultConfig(), key); err == nil {
		t.Fatal("expected an error requesting multi-stream on a transport that doesn't support it")
	}
}

// PSKFile is exitmgr's own responsibility (transportstack.Build never reads
// files itself -- it just calls whatever Encrypt closure it's given).
func TestBuildTransportReadsPSKFile(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	pskPath := filepath.Join(t.TempDir(), "psk.txt")
	if err := os.WriteFile(pskPath, []byte("a-shared-secret-16+chars\n"), 0o600); err != nil {
		t.Fatalf("write psk file: %v", err)
	}

	cfg := ClientConfig{ID: "c1", Transport: "yandex", URL: "https://disk.yandex.com/i/x", PSKFile: pskPath}
	if _, err := BuildTransport(cfg, transport.DefaultConfig(), key); err != nil {
		t.Fatalf("BuildTransport with a valid PSK file: %v", err)
	}
}

func TestBuildTransportRejectsMissingPSKFile(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	cfg := ClientConfig{ID: "c1", Transport: "yandex", URL: "https://disk.yandex.com/i/x", PSKFile: "/does/not/exist"}
	if _, err := BuildTransport(cfg, transport.DefaultConfig(), key); err == nil {
		t.Fatal("expected an error for a missing PSK file")
	}
}
