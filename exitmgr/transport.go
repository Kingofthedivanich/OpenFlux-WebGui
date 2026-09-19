package exitmgr

import (
	"fmt"
	"os"
	"strings"

	"github.com/flynn/noise"

	"openflux/transport"
	"openflux/transportstack"
)

// BuildTransport constructs the full transport stack for one client,
// delegating the actual backend/encryption/codec/multi-stream wiring to
// transportstack.Build -- the same code path main.go's CLI dispatch uses,
// so a client added through the panel is wired identically to one started
// via CLI flags and the two can't silently diverge on layering order again
// (see transportstack's package doc for what happened when they did).
// Every client shares the panel's own static key (staticKey, loaded once
// by runExitPanel); cfg.PSKFile optionally closes this one client to
// strangers who don't have the shared secret.
func BuildTransport(cfg ClientConfig, base transport.TransportConfig, staticKey noise.DHKey) (transport.Transport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var psk []byte
	if cfg.PSKFile != "" {
		secretBytes, err := os.ReadFile(cfg.PSKFile)
		if err != nil {
			return nil, fmt.Errorf("read psk file: %w", err)
		}
		psk, err = transport.DerivePSK(strings.TrimSpace(string(secretBytes)))
		if err != nil {
			return nil, fmt.Errorf("psk file: %w", err)
		}
	}

	return transportstack.Build(transportstack.Params{
		TransportType: cfg.Transport,
		URL:           cfg.URL,
		IsExit:        true, // panel clients are always the exit side
		MaxToken:      cfg.MaxToken,
		MaxUid:        cfg.MaxUid,
		Codec:         cfg.Codec,
		Encrypt: func(inner transport.Transport) (transport.Transport, error) {
			return transport.NewEncryptedTransport(inner, transport.EncryptedConfig{
				Initiator: false,
				StaticKey: staticKey,
				PSK:       psk,
			})
		},
	}, base)
}
