package exitmgr

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/flynn/noise"

	"openflux/transport"
	"openflux/transport/cupsonline"
	"openflux/transport/mailru"
	"openflux/transport/oneme"
	"openflux/transport/yandex"
)

// BuildTransport constructs the full transport stack for one client
// (backend -> encryption -> codec), mirroring main.go's single-client
// wiring so a client added through the panel behaves identically to one
// started via CLI flags. Every client shares the panel's own static key
// (staticKey, loaded once by runExitPanel); cfg.PSKFile optionally closes
// this one client to strangers who don't have the shared secret.
func BuildTransport(cfg ClientConfig, base transport.TransportConfig, staticKey noise.DHKey) (transport.Transport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var inner transport.Transport
	switch cfg.Transport {
	case "yandex":
		inner = yandex.NewYandexDocsTransport(cfg.URL, base)
	case "vyandex":
		inner = yandex.NewYandexVolgaTransport(cfg.URL, base)
	case "oneme":
		uid, err := strconv.ParseInt(cfg.MaxUid, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("max_uid: %w", err)
		}
		inner = oneme.NewOneMeTransport(true, cfg.MaxToken, uid, base)
	case "cupsonline":
		inner = cupsonline.NewCupsonlineTransport(cfg.URL, base, false)
	case "mailru":
		inner = mailru.NewMailruDocsTransport(cfg.URL, base)
	default:
		return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
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

	// Encryption sits directly on the raw transport, under the codec, the
	// same way main.go wires it: one AEAD covers a whole compressed batch,
	// and the codec's batching means the peer sees one Noise frame per
	// flushed batch instead of one per IP packet.
	enc, err := transport.NewEncryptedTransport(inner, transport.EncryptedConfig{
		Initiator: false,
		StaticKey: staticKey,
		PSK:       psk,
	})
	if err != nil {
		return nil, fmt.Errorf("configure encryption: %w", err)
	}
	inner = enc

	if cfg.Codec == "legacy" {
		inner = transport.NewCompressedTransport(inner)
	} else {
		inner = transport.NewBatchedTransport(inner)
	}

	return inner, nil
}
