package encryptionsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openflux/internal/transport"
)

// nopTransport is the bare minimum to wrap.
type nopTransport struct{}

func (nopTransport) Start() error                    { return nil }
func (nopTransport) Stop() error                     { return nil }
func (nopTransport) Send([]byte) error               { return nil }
func (nopTransport) Receive(func([]byte))            {}
func (nopTransport) IsConnected() bool               { return false }
func (nopTransport) Stats() transport.TransportStats { return transport.TransportStats{} }

func TestEncryptionSetupRequiredByDefault(t *testing.T) {
	for _, initiator := range []bool{true, false} {
		if _, err := New(Options{}, initiator); err != ErrPlaintextNotAllowed {
			t.Fatalf("initiator=%v: err = %v, want ErrPlaintextNotAllowed", initiator, err)
		}
	}
}

func TestEncryptionSetupOffWhenPlaintextAllowed(t *testing.T) {
	for _, initiator := range []bool{true, false} {
		setup, err := New(Options{AllowPlaintext: true}, initiator)
		if err != nil {
			t.Fatal(err)
		}
		if setup != nil {
			t.Fatal("encryption configured without any flag")
		}
	}
}

func TestEncryptionSetupRejectsMisplacedFlags(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "exit.key")
	cases := []struct {
		name      string
		opts      Options
		initiator bool
		wantErr   string
	}{
		{"exit key on the client", Options{ExitKeyFile: keyFile}, true, "--exit-key-file"},
		{"peer key on the exit", Options{PeerKey: "AAAA"}, false, "--peer-key"},
		{"psk alone on the client", Options{PSK: "a sufficiently long shared secret"}, true, "--peer-key"},
		{"psk alone on the exit", Options{PSK: "a sufficiently long shared secret"}, false, "--exit-key-file"},
		{"bad peer key", Options{PeerKey: "not a key"}, true, "peer key"},
		{"short psk", Options{PeerKey: validPeerKey(t), PSK: "short"}, true, "16"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.opts, tc.initiator)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func validPeerKey(t *testing.T) string {
	t.Helper()
	key, err := transport.GenerateStaticKey()
	if err != nil {
		t.Fatal(err)
	}
	return transport.PublicKeyString(key.Public)
}

func TestEncryptionSetupExitCreatesKeyAndAnnouncesIt(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "exit.key")
	setup, err := New(Options{ExitKeyFile: keyFile}, false)
	if err != nil {
		t.Fatal(err)
	}
	key, created, err := transport.LoadOrCreateStaticKey(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("setup did not create the key file")
	}
	if !strings.Contains(setup.Banner, transport.PublicKeyString(key.Public)) {
		t.Fatalf("banner %q lacks the public key", setup.Banner)
	}
	if !strings.Contains(setup.Banner, "--peer-key") {
		t.Fatalf("banner %q does not tell the operator where the key goes", setup.Banner)
	}
	wrapped, err := setup.Wrap(nopTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wrapped.(*transport.EncryptedTransport); !ok {
		t.Fatalf("wrap returned %T", wrapped)
	}
}

func TestEncryptionSetupClientUsesPeerKey(t *testing.T) {
	setup, err := New(Options{PeerKey: validPeerKey(t)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Banner != "" {
		t.Fatalf("client has a banner: %q", setup.Banner)
	}
	wrapped, err := setup.Wrap(nopTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wrapped.(*transport.EncryptedTransport); !ok {
		t.Fatalf("wrap returned %T", wrapped)
	}
}

func TestEncryptionSetupLabelsClosedNode(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "exit.key")
	open, err := New(Options{ExitKeyFile: keyFile}, false)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := New(Options{ExitKeyFile: keyFile, PSK: "a sufficiently long shared secret"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if open.Label == closed.Label {
		t.Fatalf("open and closed node share the label %q", open.Label)
	}
	if !strings.Contains(closed.Label, "PSK") {
		t.Fatalf("closed node label %q does not mention the PSK", closed.Label)
	}
}

func TestReadSecretFileTrimsWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "psk.txt")
	if err := os.WriteFile(path, []byte("  a sufficiently long shared secret \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err := ReadSecretFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "a sufficiently long shared secret" {
		t.Fatalf("got %q", secret)
	}
	if _, err := ReadSecretFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
}
