package transportstack

import (
	"errors"
	"testing"

	"openflux/transport"
)

func noEncrypt(inner transport.Transport) (transport.Transport, error) { return inner, nil }

func failEncrypt(inner transport.Transport) (transport.Transport, error) {
	return nil, errors.New("boom")
}

// markerTransport lets tests confirm Encrypt actually sits in the chain --
// unlike noEncrypt (an identity pass-through), wrapping produces a
// distinguishable type.
type markerTransport struct{ transport.Transport }

func markEncrypt(inner transport.Transport) (transport.Transport, error) {
	return markerTransport{inner}, nil
}

// Regression test for the real bug that motivated this package: encryption
// must sit directly on the raw backend, under the codec -- not the other
// way around. A panel client's Noise handshake frame landed on the exit's
// outermost BatchedTransport (which can't decode a raw handshake frame as
// a batch and silently drops it) for an entire release because exitmgr had
// its own copy of this wiring with the layers swapped.
func TestBuildWrapsEncryptionUnderTheCodec(t *testing.T) {
	p := Params{TransportType: "yandex", URL: "https://disk.yandex.com/i/x", Encrypt: markEncrypt}
	trans, err := Build(p, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	batched, ok := trans.(*transport.BatchedTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.BatchedTransport (codec must be outermost)", trans)
	}
	if _, ok := batched.Transport.(markerTransport); !ok {
		t.Fatalf("codec wraps %T directly, want the Encrypt-wrapped markerTransport underneath it", batched.Transport)
	}
}

func TestBuildWrapsLegacyCodecOverEncryption(t *testing.T) {
	p := Params{TransportType: "yandex", URL: "https://disk.yandex.com/i/x", Codec: "legacy", Encrypt: markEncrypt}
	trans, err := Build(p, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	compressed, ok := trans.(*transport.CompressedTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.CompressedTransport", trans)
	}
	if _, ok := compressed.Transport.(markerTransport); !ok {
		t.Fatalf("codec wraps %T directly, want the Encrypt-wrapped markerTransport underneath it", compressed.Transport)
	}
}

func TestBuildWithoutEncryptSkipsEncryption(t *testing.T) {
	p := Params{TransportType: "yandex", URL: "https://disk.yandex.com/i/x"}
	trans, err := Build(p, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	batched, ok := trans.(*transport.BatchedTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.BatchedTransport", trans)
	}
	if _, ok := batched.Transport.(*transport.EncryptedTransport); ok {
		t.Fatal("codec wraps an EncryptedTransport despite Encrypt being nil")
	}
}

func TestBuildPropagatesEncryptError(t *testing.T) {
	p := Params{TransportType: "yandex", URL: "https://disk.yandex.com/i/x", Encrypt: failEncrypt}
	if _, err := Build(p, transport.DefaultConfig()); err == nil {
		t.Fatal("expected Build to propagate the Encrypt error")
	}
}

func TestBuildSingleURLDoesNotWrapInMultiStream(t *testing.T) {
	p := Params{TransportType: "yandex", URL: "https://disk.yandex.com/i/one", Encrypt: noEncrypt}
	trans, err := Build(p, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := trans.(*transport.MultiStreamTransport); ok {
		t.Fatal("a single URL must not be wrapped in MultiStreamTransport (breaks the single-document wire format)")
	}
}

func TestBuildMultipleURLsWrapInMultiStreamWithFullStacksEach(t *testing.T) {
	p := Params{
		TransportType: "yandex",
		URL:           "https://disk.yandex.com/i/b, https://disk.yandex.com/i/a",
		Encrypt:       noEncrypt,
	}
	trans, err := Build(p, transport.DefaultConfig())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ms, ok := trans.(*transport.MultiStreamTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *transport.MultiStreamTransport", trans)
	}
	streams := ms.Streams()
	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(streams))
	}
	for i, s := range streams {
		if _, ok := s.(*transport.BatchedTransport); !ok {
			t.Fatalf("stream %d is %T, want a complete *transport.BatchedTransport stack", i, s)
		}
	}
}

func TestBuildRejectsMultiStreamForUnsupportedTransport(t *testing.T) {
	for _, tt := range []string{"mailru", "cupsonline", "oneme"} {
		t.Run(tt, func(t *testing.T) {
			p := Params{TransportType: tt, URL: "https://a,https://b"}
			if _, err := Build(p, transport.DefaultConfig()); err == nil {
				t.Fatalf("expected an error requesting multi-stream on %q", tt)
			}
		})
	}
}

func TestBuildOnemeInvalidMaxUidReturnsError(t *testing.T) {
	p := Params{TransportType: "oneme", MaxToken: "tok", MaxUid: "not-a-number"}
	if _, err := Build(p, transport.DefaultConfig()); err == nil {
		t.Fatal("expected an error for a non-numeric MaxUid")
	}
}

func TestBuildOnemeValidMaxUid(t *testing.T) {
	p := Params{TransportType: "oneme", MaxToken: "tok", MaxUid: "42", Encrypt: noEncrypt}
	if _, err := Build(p, transport.DefaultConfig()); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestBuildUnknownTransport(t *testing.T) {
	p := Params{TransportType: "carrier-pigeon"}
	if _, err := Build(p, transport.DefaultConfig()); err == nil {
		t.Fatal("expected an error for an unknown transport type")
	}
}

func TestSplitURLs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{""}},
		{"single", "https://a", []string{"https://a"}},
		{"trims whitespace", " https://a , https://b ", []string{"https://a", "https://b"}},
		{"dedupes", "https://a,https://a", []string{"https://a"}},
		{"sorts", "https://b,https://a", []string{"https://a", "https://b"}},
		{"drops empty segments", "https://a,,https://b", []string{"https://a", "https://b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SplitURLs(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("SplitURLs(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("SplitURLs(%q) = %v, want %v", c.in, got, c.want)
				}
			}
		})
	}
}

func TestSupportsMultiStream(t *testing.T) {
	yes := []string{"yandex", "vyandex"}
	no := []string{"oneme", "cupsonline", "mailru", "unknown"}
	for _, tt := range yes {
		if !SupportsMultiStream(tt) {
			t.Errorf("SupportsMultiStream(%q) = false, want true", tt)
		}
	}
	for _, tt := range no {
		if SupportsMultiStream(tt) {
			t.Errorf("SupportsMultiStream(%q) = true, want false", tt)
		}
	}
}
