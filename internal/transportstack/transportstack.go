// Package transportstack is the single place that wires a backend
// transport, optional encryption, the codec, and multi-stream into a
// complete Transport. Before this package existed, main.go's CLI dispatch
// (newStream/newDocStreams) and exitmgr.BuildTransport each implemented
// this wiring independently -- and they silently diverged on layering
// order (encryption ended up outside the codec in exitmgr instead of under
// it), which meant every panel client's Noise handshake landed on the
// wrong layer and never completed. Build is now the only implementation;
// every caller shares it and can't diverge on it again.
package transportstack

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"openflux/internal/transport"
	"openflux/internal/transport/cupsonline"
	"openflux/internal/transport/mailru"
	"openflux/internal/transport/oneme"
	"openflux/internal/transport/yandex"
)

// Params fully describes one client's or exit's transport stack.
type Params struct {
	// TransportType selects the backend: yandex | vyandex | oneme | cupsonline | mailru.
	TransportType string
	// URL is the document/room URL. A comma-separated list runs multi-stream
	// (yandex/vyandex only). Ignored for oneme, which has no URL; ignored by
	// cupsonline when IsExit is true, since the exit generates its own rooms.
	URL string
	// IsExit selects which side of the handshake this stack is: for oneme,
	// the exit answers instead of dialing out (passed straight through as
	// oneme's own isExit parameter); for cupsonline, the exit generates its
	// own rooms instead of joining ones from URL (cupsonline's own
	// constructor parameter is named isClient, the inverse of IsExit).
	IsExit bool
	// MaxToken and MaxUid configure the oneme (MAX) backend.
	MaxToken string
	MaxUid   string
	// Codec selects the app-layer codec: "legacy" for the old per-packet
	// LZ4 path, anything else (including "") for the default batched+zstd
	// one.
	Codec string
	// Encrypt, if non-nil, wraps the raw backend in an encrypted transport
	// before the codec is applied. nil means plaintext.
	Encrypt func(transport.Transport) (transport.Transport, error)
}

// SplitURLs splits a comma-separated URL list into document URLs: trimmed,
// de-duplicated, and sorted so two peers route a connection over the same
// document regardless of the order the URLs were typed in on each side. An
// empty value yields [""] so callers can always use the result's [0].
func SplitURLs(raw string) []string {
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

// SupportsMultiStream reports whether transportType takes one document per
// stream. cupsonline carries its rooms inside one URL, oneme has no URL,
// and mailru has not been tested with several documents.
func SupportsMultiStream(transportType string) bool {
	return transportType == "yandex" || transportType == "vyandex"
}

// Build constructs the full stack for p: the raw backend, wrapped in
// encryption (if p.Encrypt is set) directly underneath the codec, so one
// AEAD covers a whole compressed batch and the codec's batching means the
// peer sees one Noise frame per flushed batch instead of one per IP
// packet. p.URL naming more than one document (yandex/vyandex only)
// produces one complete stream per document, combined with
// transport.NewMultiStreamTransport; a single document keeps the exact
// single-document wire format.
func Build(p Params, base transport.TransportConfig) (transport.Transport, error) {
	urls := SplitURLs(p.URL)
	if len(urls) > 1 && !SupportsMultiStream(p.TransportType) {
		return nil, fmt.Errorf("url: several documents are supported with transport=yandex or vyandex, not %s", p.TransportType)
	}

	buildOne := func(url string) (transport.Transport, error) {
		inner, err := buildBackend(p, url, base)
		if err != nil {
			return nil, err
		}
		if p.Encrypt != nil {
			encrypted, err := p.Encrypt(inner)
			if err != nil {
				return nil, fmt.Errorf("configure encrypted transport: %w", err)
			}
			inner = encrypted
		}
		if p.Codec == "legacy" {
			return transport.NewCompressedTransport(inner), nil
		}
		return transport.NewBatchedTransport(inner), nil
	}

	if len(urls) <= 1 {
		return buildOne(urls[0])
	}

	streams := make([]transport.Transport, 0, len(urls))
	for _, u := range urls {
		s, err := buildOne(u)
		if err != nil {
			return nil, err
		}
		streams = append(streams, s)
	}
	return transport.NewMultiStreamTransport(streams), nil
}

// buildBackend constructs the raw (unencrypted, uncoded) backend for one
// document/room.
func buildBackend(p Params, url string, base transport.TransportConfig) (transport.Transport, error) {
	switch p.TransportType {
	case "yandex":
		return yandex.NewYandexDocsTransport(url, base), nil
	case "vyandex":
		return yandex.NewYandexVolgaTransport(url, base), nil
	case "oneme":
		uid, err := strconv.ParseInt(p.MaxUid, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("max_uid: %w", err)
		}
		return oneme.NewOneMeTransport(p.IsExit, p.MaxToken, uid, base), nil
	case "cupsonline":
		return cupsonline.NewCupsonlineTransport(url, base, !p.IsExit), nil
	case "mailru":
		return mailru.NewMailruDocsTransport(url, base), nil
	default:
		return nil, fmt.Errorf("unknown transport %q", p.TransportType)
	}
}
