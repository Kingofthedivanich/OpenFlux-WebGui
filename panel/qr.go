package panel

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"openflux/exitmgr"
)

// qrTunnel mirrors the Android app's Tunnel JSON shape exactly (see
// OpenFluxAndroid data/Tunnel.kt) -- scanning the PNG this handler returns
// decodes straight into one, no separate wire format to keep in sync beyond
// this struct.
type qrTunnel struct {
	ID                   int64    `json:"id"`
	Name                 string   `json:"name"`
	TransportType        string   `json:"transportType"`
	TransportConnPayload []string `json:"transportConnPayload"`
	PeerKey              *string  `json:"peerKey,omitempty"`
	EncryptionKey        *string  `json:"encryptionKey,omitempty"`
}

func (s *Server) handleClientQR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, ok := s.mgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "client not found")
		return
	}

	tun, err := buildQRTunnel(status, s.publicKey)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := json.Marshal(tun)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "encode tunnel: "+err.Error())
		return
	}

	png, err := qrcode.Encode(string(data), qrcode.Medium, 512)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "generate qr: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

// buildQRTunnel turns one client's status into the exact JSON shape the
// Android app's Tunnel data class expects, so the QR handler just needs to
// marshal and encode it. Split out from the HTTP handler so the shape and
// its per-transport edge cases can be unit tested without a PNG decoder.
func buildQRTunnel(status exitmgr.ClientStatus, panelPublicKey string) (qrTunnel, error) {
	cfg := status.Config

	var connArgs []string
	switch cfg.Transport {
	case "oneme":
		if cfg.MaxToken == "" || cfg.MaxUid == "" {
			return qrTunnel{}, fmt.Errorf("client is missing max_token/max_uid")
		}
		connArgs = []string{"--maxToken", cfg.MaxToken, "--maxUid", cfg.MaxUid}
	case "cupsonline":
		// The exit generates cupsonline's rooms itself; cfg.URL is ignored
		// for this transport (see ClientStatus.CupsonlineRooms).
		if status.CupsonlineRooms == "" {
			return qrTunnel{}, fmt.Errorf("client hasn't started yet -- no rooms to share")
		}
		connArgs = []string{"--url", status.CupsonlineRooms}
	default:
		if cfg.URL == "" {
			return qrTunnel{}, fmt.Errorf("client is missing a url")
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
			return qrTunnel{}, fmt.Errorf("read psk file: %w", err)
		}
		trimmed := strings.TrimSpace(string(secret))
		encKey = &trimmed
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	return qrTunnel{
		ID:                   qrClientID(cfg.ID),
		Name:                 name,
		TransportType:        cfg.Transport,
		TransportConnPayload: payload,
		PeerKey:              &panelPublicKey,
		EncryptionKey:        encKey,
	}, nil
}

// qrClientID derives a stable positive int64 from the exit's hex client id,
// so scanning the same client's QR twice is a no-op on the Android side
// (TunnelsViewModel.addTunnel skips ids it already has) instead of adding a
// duplicate tunnel.
func qrClientID(clientID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(clientID))
	v := int64(h.Sum64())
	if v < 0 {
		v = -v
	}
	return v
}
