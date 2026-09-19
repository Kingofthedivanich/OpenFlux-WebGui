// Package clientqr builds the QR-code payload for one exitmgr client: a
// JSON blob matching the Android app's Tunnel data class exactly (see
// OpenFluxAndroid data/Tunnel.kt), so scanning the PNG this package encodes
// imports and configures that client with no manual copying. Shared by the
// web panel and the Telegram bot so both surfaces stay in sync with the
// Android wire format by construction.
package clientqr

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"openflux/internal/exitmgr"
)

// Tunnel mirrors the Android app's Tunnel JSON shape exactly.
type Tunnel struct {
	ID                   int64    `json:"id"`
	Name                 string   `json:"name"`
	TransportType        string   `json:"transportType"`
	TransportConnPayload []string `json:"transportConnPayload"`
	PeerKey              *string  `json:"peerKey,omitempty"`
	EncryptionKey        *string  `json:"encryptionKey,omitempty"`
}

// Build turns one client's status into a Tunnel, ready to marshal and
// encode as a QR code. Split out from PNG so the shape and its per-transport
// edge cases can be unit tested without a PNG decoder.
func Build(status exitmgr.ClientStatus, panelPublicKey string) (Tunnel, error) {
	cfg := status.Config

	var connArgs []string
	switch cfg.Transport {
	case "oneme":
		if cfg.MaxToken == "" || cfg.MaxUid == "" {
			return Tunnel{}, fmt.Errorf("client is missing max_token/max_uid")
		}
		connArgs = []string{"--maxToken", cfg.MaxToken, "--maxUid", cfg.MaxUid}
	case "cupsonline":
		// The exit generates cupsonline's rooms itself; cfg.URL is ignored
		// for this transport (see ClientStatus.CupsonlineRooms).
		if status.CupsonlineRooms == "" {
			return Tunnel{}, fmt.Errorf("client hasn't started yet -- no rooms to share")
		}
		connArgs = []string{"--url", status.CupsonlineRooms}
	default:
		if cfg.URL == "" {
			return Tunnel{}, fmt.Errorf("client is missing a url")
		}
		connArgs = []string{"--url", cfg.URL}
	}

	payload := append([]string{"--role", "client", "--transport", cfg.Transport}, connArgs...)
	if cfg.Codec == "legacy" {
		payload = append(payload, "--codec", "legacy")
	}

	var encKey *string
	if cfg.PSKFile != "" {
		secret, err := os.ReadFile(cfg.PSKFile)
		if err != nil {
			return Tunnel{}, fmt.Errorf("read psk file: %w", err)
		}
		trimmed := strings.TrimSpace(string(secret))
		encKey = &trimmed
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	return Tunnel{
		ID:                   clientNumericID(cfg.ID),
		Name:                 name,
		TransportType:        cfg.Transport,
		TransportConnPayload: payload,
		PeerKey:              &panelPublicKey,
		EncryptionKey:        encKey,
	}, nil
}

// PNG marshals tun to JSON and encodes it as a size x size QR code PNG.
func PNG(tun Tunnel, size int) ([]byte, error) {
	data, err := json.Marshal(tun)
	if err != nil {
		return nil, fmt.Errorf("encode tunnel: %w", err)
	}
	png, err := qrcode.Encode(string(data), qrcode.Medium, size)
	if err != nil {
		return nil, fmt.Errorf("generate qr: %w", err)
	}
	return png, nil
}

// clientNumericID derives a stable positive int64 from the exit's hex
// client id, so scanning the same client's QR twice is a no-op on the
// Android side (TunnelsViewModel.addTunnel skips ids it already has)
// instead of adding a duplicate tunnel.
func clientNumericID(clientID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(clientID))
	v := int64(h.Sum64())
	if v < 0 {
		v = -v
	}
	return v
}
