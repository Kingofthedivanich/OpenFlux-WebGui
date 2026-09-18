package exitmgr

import (
	"testing"

	"openflux/transport"
)

// Regression test for a real bug: BuildTransport used to wrap encryption
// OUTSIDE the codec (backend -> codec -> encryption), the reverse of
// main.go's wiring (backend -> encryption -> codec). A panel client's Noise
// handshake frame arrived at the exit's outermost BatchedTransport, which
// can't decode it (it's not a batch frame) and silently drops it -- the
// handshake never reaches EncryptedTransport underneath, so it never
// completes. This asserts the layering directly instead of relying on a
// live handshake, so it doesn't need network.
func TestBuildTransportWrapsEncryptionUnderTheCodec(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}

	cfg := ClientConfig{
		ID:        "c1",
		Transport: "yandex",
		URL:       "https://disk.yandex.com/i/does-not-matter",
	}

	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}

	batched, ok := trans.(*transport.BatchedTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.BatchedTransport (codec must be outermost)", trans)
	}
	if _, ok := batched.Transport.(*transport.EncryptedTransport); !ok {
		t.Fatalf("codec wraps %T, want *transport.EncryptedTransport directly underneath "+
			"(encryption must sit on the raw transport, under the codec, like main.go wires it)", batched.Transport)
	}
}

func TestBuildTransportWrapsLegacyCodecOverEncryption(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}

	cfg := ClientConfig{
		ID:        "c1",
		Transport: "yandex",
		URL:       "https://disk.yandex.com/i/does-not-matter",
		Codec:     "legacy",
	}

	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}

	compressed, ok := trans.(*transport.CompressedTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.CompressedTransport", trans)
	}
	if _, ok := compressed.Transport.(*transport.EncryptedTransport); !ok {
		t.Fatalf("codec wraps %T, want *transport.EncryptedTransport directly underneath", compressed.Transport)
	}
}

// unwrapCupsonline (used to surface the room list a cupsonline client needs
// as --url, see manager.go) depends on the exact wrap chain BuildTransport
// produces. This locks that chain in for the cupsonline backend
// specifically, since it's the one BuildTransport builds with isClient=false
// unconditionally -- a wiring mistake there wouldn't show up in the
// generic yandex-backed tests above.
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

func TestBuildTransportSingleURLDoesNotWrapInMultiStream(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	cfg := ClientConfig{ID: "c1", Transport: "yandex", URL: "https://disk.yandex.com/i/one"}
	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	if _, ok := trans.(*transport.MultiStreamTransport); ok {
		t.Fatal("a single URL must not be wrapped in MultiStreamTransport (breaks the single-document wire format)")
	}
}

func TestBuildTransportMultipleURLsWrapInMultiStream(t *testing.T) {
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatalf("generate static key: %v", err)
	}
	cfg := ClientConfig{
		ID:        "c1",
		Transport: "yandex",
		URL:       "https://disk.yandex.com/i/b, https://disk.yandex.com/i/a",
	}
	trans, err := BuildTransport(cfg, transport.DefaultConfig(), key)
	if err != nil {
		t.Fatalf("BuildTransport: %v", err)
	}
	ms, ok := trans.(*transport.MultiStreamTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.MultiStreamTransport", trans)
	}
	streams := ms.Streams()
	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(streams))
	}
	// Each stream must itself be a complete codec(encryption(raw)) stack,
	// the same as the single-document case -- not the raw backend directly.
	for i, s := range streams {
		batched, ok := s.(*transport.BatchedTransport)
		if !ok {
			t.Fatalf("stream %d is %T, want *transport.BatchedTransport", i, s)
		}
		if _, ok := batched.Transport.(*transport.EncryptedTransport); !ok {
			t.Fatalf("stream %d: codec wraps %T, want *transport.EncryptedTransport", i, batched.Transport)
		}
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
