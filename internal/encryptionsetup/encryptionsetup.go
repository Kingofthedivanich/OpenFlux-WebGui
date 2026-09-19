package encryptionsetup

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"openflux/internal/transport"
)

// Options are the encryption settings as given on the command
// line or by the mobile bridge. All empty means plaintext.
type Options struct {
	ExitKeyFile string // exit node: its static key file, created on first use
	PeerKey     string // client: the exit node's public key (base64)
	PSK         string // both, optional: the shared secret that closes the node to strangers
	// AllowPlaintext permits running without encryption. Without it a missing
	// key is an error: a plaintext tunnel lets anyone who can read or write
	// the document see the traffic and use the exit node as an open proxy.
	AllowPlaintext bool
}

// Setup is a configured encryption layer ready to wrap raw
// transports (one per document in a multi-stream tunnel).
type Setup struct {
	Wrap   func(transport.Transport) (transport.Transport, error)
	Label  string // one line for the startup log
	Banner string // exit node only: the public key to hand to clients
}

// ErrPlaintextNotAllowed is returned when no encryption option was given and
// plaintext was not explicitly allowed.
var ErrPlaintextNotAllowed = errors.New("encryption is required: use --exit-key-file on the exit node and " +
	"--peer-key on the client (or --allow-plaintext to run an unprotected tunnel)")

// New validates the options for this side and prepares the
// layer. It returns nil, nil only when no encryption option was given and
// plaintext is allowed. A secret alone is an error rather than silently
// plaintext: the pre-v2 flag used to enable encryption by itself.
func New(opts Options, initiator bool) (*Setup, error) {
	if opts.ExitKeyFile == "" && opts.PeerKey == "" && opts.PSK == "" {
		if !opts.AllowPlaintext {
			return nil, ErrPlaintextNotAllowed
		}
		return nil, nil
	}
	cfg := transport.EncryptedConfig{Initiator: initiator}
	var banner string
	if initiator {
		if opts.ExitKeyFile != "" {
			return nil, errors.New("--exit-key-file belongs on the exit node; the client takes --peer-key")
		}
		if opts.PeerKey == "" {
			return nil, errors.New("encryption on the client needs --peer-key=<exit public key>; --psk-file alone only adds client authorization")
		}
		pub, err := transport.ParsePublicKey(opts.PeerKey)
		if err != nil {
			return nil, fmt.Errorf("invalid peer key: %w", err)
		}
		cfg.PeerStatic = pub
	} else {
		if opts.PeerKey != "" {
			return nil, errors.New("--peer-key belongs on the client; the exit node takes --exit-key-file")
		}
		if opts.ExitKeyFile == "" {
			return nil, errors.New("encryption on the exit node needs --exit-key-file=<path>; --psk-file alone only adds client authorization")
		}
		key, created, err := transport.LoadOrCreateStaticKey(opts.ExitKeyFile)
		if err != nil {
			return nil, err
		}
		cfg.StaticKey = key
		pub := transport.PublicKeyString(key.Public)
		state := "loaded from"
		if created {
			state = "generated and saved to"
		}
		banner = fmt.Sprintf("\n=== EXIT PUBLIC KEY (%s %s) ===\n%s\nStart clients with --peer-key=%s\n\n",
			state, opts.ExitKeyFile, pub, pub)
	}

	label := "Noise NKpsk0 (X25519 + AES-256-GCM), open node: any client with the public key may connect"
	if opts.PSK != "" {
		psk, err := transport.DerivePSK(opts.PSK)
		if err != nil {
			return nil, fmt.Errorf("--psk-file: %w", err)
		}
		cfg.PSK = psk
		label = "Noise NKpsk0 (X25519 + AES-256-GCM), closed node: PSK required"
	}

	return &Setup{
		Wrap: func(inner transport.Transport) (transport.Transport, error) {
			return transport.NewEncryptedTransport(inner, cfg)
		},
		Label:  label,
		Banner: banner,
	}, nil
}

// ReadSecretFile reads a secret from path, ignoring surrounding whitespace
// (editors love trailing newlines).
func ReadSecretFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}
