// Package exitmgr runs many exit-node channels on one host for the exit
// panel: each client gets its own channel directory (node.conf, encryption
// key, cookie jar) and its own `openflux --config` child process, so one
// client's carriers, keys and traffic never touch another's, and every
// channel is exactly the exit node the core itself runs.
package exitmgr

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/p1neappleXpress/OpenFlux/yandexdisk"
)

// TransportEntry is one carrier of a client's channel.
type TransportEntry struct {
	Type string `json:"type"`
	// URL is the document, board, conference or room link. Empty for
	// cupsonline and avito, whose exit creates its own rooms.
	URL string `json:"url,omitempty"`
}

// ClientConfig is one client's channel. The encryption key lives in the
// channel directory, not here, so it is never part of the client list.
type ClientConfig struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Transports []TransportEntry `json:"transports"`
	// Mode is "l4" (default) or "l3".
	Mode string `json:"mode,omitempty"`
	// DirectPort, when set, also runs the direct transport on this TCP port:
	// a backup clients dial straight to this host.
	DirectPort int `json:"direct_port,omitempty"`
	// Context is the encryption context, fixed when the client is made so
	// links handed out keep working when its transports change.
	Context string `json:"context,omitempty"`
}

// priorities follow deploy/node-install.sh, so a panel channel picks its
// carriers in the same order as a channel the apps' node wizard made.
var priorities = map[string]int{
	"vyandex":    100,
	"yandex":     95,
	"mailru":     90,
	"telemost":   85,
	"boards":     80,
	"avito":      75,
	"cupsonline": 70,
}

const directPriority = 50

// makesOwnRooms are the types whose exit creates its rooms and so take no URL.
var makesOwnRooms = map[string]bool{"cupsonline": true, "avito": true}

// MakesOwnRooms reports whether a channel's exit creates the rooms of a
// transport of this type itself, so it takes no URL from the operator.
func MakesOwnRooms(typ string) bool { return makesOwnRooms[typ] }

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Types lists the transport types a panel channel can carry, in priority order.
func Types() []string {
	out := make([]string, 0, len(priorities))
	for t := range priorities {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return priorities[out[i]] > priorities[out[j]] })
	return out
}

// Validate checks a config is complete enough to write a channel from.
func (c ClientConfig) Validate() error {
	if !idPattern.MatchString(c.ID) {
		return fmt.Errorf("id %q: want 1-32 lowercase letters, digits or dashes", c.ID)
	}
	if len(c.Transports) == 0 && c.DirectPort == 0 {
		return fmt.Errorf("at least one transport is required")
	}
	for i, t := range c.Transports {
		if _, ok := priorities[t.Type]; !ok {
			return fmt.Errorf("transport %d: unknown type %q (want one of %v)", i+1, t.Type, Types())
		}
		if t.URL == "" && !makesOwnRooms[t.Type] {
			return fmt.Errorf("transport %d (%s): url is required", i+1, t.Type)
		}
		if !confSafe(t.URL) {
			return fmt.Errorf("transport %d (%s): url must be one line without '#' or ';' (a .conf value ends there)", i+1, t.Type)
		}
	}
	switch c.Mode {
	case "", "l4", "l3":
	default:
		return fmt.Errorf("unknown mode %q (want l4|l3)", c.Mode)
	}
	if c.DirectPort < 0 || c.DirectPort > 65535 {
		return fmt.Errorf("direct_port %d out of range", c.DirectPort)
	}
	if !confSafe(c.Context) {
		return fmt.Errorf("context must be one line without '#' or ';'")
	}
	if strings.ContainsAny(c.Name, "\r\n") {
		return fmt.Errorf("name must be one line")
	}
	return nil
}

func (c ClientConfig) mode() string {
	if c.Mode == "" {
		return "l4"
	}
	return c.Mode
}

// defaultContext is the context a new channel gets: its first document by
// priority, as node-install.sh chooses it. A channel without a document
// (rooms the exit makes, direct) gets a fixed one of its own: what the core
// would derive instead changes once the rooms it made are written back, and
// with it the keys a link handed out was made for.
func defaultContext(id string, ts []TransportEntry) string {
	best, bestPrio := "", -1
	for _, t := range ts {
		if t.URL != "" && !makesOwnRooms[t.Type] && priorities[t.Type] > bestPrio {
			best, bestPrio = t.URL, priorities[t.Type]
		}
	}
	if best == "" {
		best = "openflux-panel-" + id
	}
	return best
}

// carrierNames are the [Transport] section names of ts: carrier names are
// unique within a Session, so a type that repeats goes yandex, yandex-2, ...
func carrierNames(ts []TransportEntry) []string {
	seen := map[string]int{}
	names := make([]string, len(ts))
	for i, t := range ts {
		seen[t.Type]++
		names[i] = t.Type
		if n := seen[t.Type]; n > 1 {
			names[i] = fmt.Sprintf("%s-%d", t.Type, n)
		}
	}
	return names
}

// normalize fixes links a channel can be given but its core cannot use, and
// reports whether it changed any: a vyandex document given as its Disk
// public link (disk.yandex.ru/d/..., the preview page) becomes the editor
// link the transport needs (disk.yandex.ru/edit/d/...).
func (c *ClientConfig) normalize() bool {
	changed := false
	for i, t := range c.Transports {
		if t.Type != "vyandex" {
			continue
		}
		if u := yandexdisk.EditURL(t.URL); u != t.URL {
			c.Transports[i].URL = u
			changed = true
		}
	}
	return changed
}

// confSafe reports whether s survives as a node.conf value: the core's
// .conf reader ends a value at '#' or ';' and a line at a line break.
func confSafe(s string) bool {
	return !strings.ContainsAny(s, "#;\r\n")
}
