package exitmgr

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"openflux/transport"
	"openflux/transport/cupsonline"
	"openflux/transport/mailru"
	"openflux/transport/oneme"
	"openflux/transport/yandex"
)

// BuildTransport constructs the full transport stack for one client
// (backend -> codec -> optional encryption), mirroring main.go's
// single-client wiring in main() so a client added through the panel
// behaves identically to one started via CLI flags.
func BuildTransport(cfg ClientConfig, base transport.TransportConfig) (transport.Transport, error) {
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

	if cfg.Codec == "legacy" {
		inner = transport.NewCompressedTransport(inner)
	} else {
		inner = transport.NewBatchedTransport(inner)
	}

	if cfg.EncryptionKeyFile != "" {
		secretBytes, err := os.ReadFile(cfg.EncryptionKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read encryption key: %w", err)
		}
		ctx := cfg.Transport
		if cfg.URL != "" {
			ctx = cfg.URL
		}
		enc, err := transport.NewEncryptedTransport(inner, strings.TrimSpace(string(secretBytes)), ctx, true)
		if err != nil {
			return nil, fmt.Errorf("configure encryption: %w", err)
		}
		inner = enc
	}

	return inner, nil
}
