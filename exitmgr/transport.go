package exitmgr

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/flynn/noise"

	"openflux/transport"
	"openflux/transport/cupsonline"
	"openflux/transport/mailru"
	"openflux/transport/oneme"
	"openflux/transport/yandex"
)

// splitURLs splits a comma-separated cfg.URL into document URLs, trimmed,
// de-duplicated and sorted -- mirrors main.go's splitURLs so a panel client
// and a CLI client route a connection over the same document regardless of
// the order the URLs were typed in. An empty value yields [""].
func splitURLs(raw string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	sort.Strings(out)
	return out
}

// supportsMultiStream mirrors main.go's: cupsonline carries its rooms inside
// one URL, oneme has no URL, and mailru has not been tested with several
// documents.
func supportsMultiStream(transportType string) bool {
	return transportType == "yandex" || transportType == "vyandex"
}

// BuildTransport constructs the full transport stack for one client
// (backend -> encryption -> codec, one complete stream per document for a
// multi-stream client), mirroring main.go's wiring so a client added
// through the panel behaves identically to one started via CLI flags.
// Every client shares the panel's own static key (staticKey, loaded once by
// runExitPanel); cfg.PSKFile optionally closes this one client to strangers
// who don't have the shared secret.
func BuildTransport(cfg ClientConfig, base transport.TransportConfig, staticKey noise.DHKey) (transport.Transport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	urls := splitURLs(cfg.URL)
	if len(urls) > 1 && !supportsMultiStream(cfg.Transport) {
		return nil, fmt.Errorf("url: several documents are supported with transport=yandex or vyandex, not %s", cfg.Transport)
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

	buildStream := func(url string) (transport.Transport, error) {
		inner, err := buildRawTransport(cfg, url, base)
		if err != nil {
			return nil, err
		}

		// Encryption sits directly on the raw transport, under the codec,
		// the same way main.go wires it: one AEAD covers a whole compressed
		// batch, and the codec's batching means the peer sees one Noise
		// frame per flushed batch instead of one per IP packet.
		enc, err := transport.NewEncryptedTransport(inner, transport.EncryptedConfig{
			Initiator: false,
			StaticKey: staticKey,
			PSK:       psk,
		})
		if err != nil {
			return nil, fmt.Errorf("configure encryption: %w", err)
		}

		if cfg.Codec == "legacy" {
			return transport.NewCompressedTransport(enc), nil
		}
		return transport.NewBatchedTransport(enc), nil
	}

	if len(urls) <= 1 {
		return buildStream(urls[0])
	}

	streams := make([]transport.Transport, 0, len(urls))
	for _, u := range urls {
		s, err := buildStream(u)
		if err != nil {
			return nil, err
		}
		streams = append(streams, s)
	}
	return transport.NewMultiStreamTransport(streams), nil
}

// buildRawTransport constructs the backend for one document/room. url is
// empty for oneme, which has no URL.
func buildRawTransport(cfg ClientConfig, url string, base transport.TransportConfig) (transport.Transport, error) {
	switch cfg.Transport {
	case "yandex":
		return yandex.NewYandexDocsTransport(url, base), nil
	case "vyandex":
		return yandex.NewYandexVolgaTransport(url, base), nil
	case "oneme":
		uid, err := strconv.ParseInt(cfg.MaxUid, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("max_uid: %w", err)
		}
		return oneme.NewOneMeTransport(true, cfg.MaxToken, uid, base), nil
	case "cupsonline":
		return cupsonline.NewCupsonlineTransport(url, base, false), nil
	case "mailru":
		return mailru.NewMailruDocsTransport(url, base), nil
	default:
		return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
	}
}
