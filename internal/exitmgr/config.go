// Package exitmgr manages multiple concurrent exit-node clients within a
// single process: each ClientConfig gets its own transport (its own
// Yandex.Docs/Volga/MAX/Cups.online/Mail.ru document or room) and its own
// independent L4 (gVisor) tunnel, so one client's traffic never crosses
// into another's.
package exitmgr

import "fmt"

// ClientConfig is one client's exit-node registration: which transport
// backend it uses and that backend's connection details. Fields are tagged
// for JSON so a Manager can persist a set of these across restarts.
type ClientConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"` // yandex | vyandex | oneme | cupsonline | mailru

	URL      string `json:"url,omitempty"`       // yandex, vyandex, mailru (ignored for cupsonline: the exit makes its own rooms)
	MaxToken string `json:"max_token,omitempty"` // oneme
	MaxUid   string `json:"max_uid,omitempty"`   // oneme

	Codec   string `json:"codec,omitempty"`             // "" (batched, default) | "legacy"
	PSKFile string `json:"psk_file,omitempty"`          // optional: file with a shared secret that closes this client to strangers
}

var validTransports = map[string]bool{
	"yandex":     true,
	"vyandex":    true,
	"oneme":      true,
	"cupsonline": true,
	"mailru":     true,
}

// Validate checks a config is complete enough to attempt starting a
// transport from, without actually starting anything.
func (c ClientConfig) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("id is required")
	}
	if !validTransports[c.Transport] {
		return fmt.Errorf("unknown transport %q (want yandex|vyandex|oneme|cupsonline|mailru)", c.Transport)
	}
	switch c.Transport {
	case "yandex", "vyandex", "mailru":
		if c.URL == "" {
			return fmt.Errorf("transport %q requires url", c.Transport)
		}
	case "oneme":
		if c.MaxToken == "" || c.MaxUid == "" {
			return fmt.Errorf("transport %q requires max_token and max_uid", c.Transport)
		}
	}
	if c.Codec != "" && c.Codec != "legacy" && c.Codec != "batched" {
		return fmt.Errorf("unknown codec %q (want batched|legacy)", c.Codec)
	}
	return nil
}
