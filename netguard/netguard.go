// Package netguard decides which destinations an exit node may reach. By
// default it refuses the exit host's own loopback and the private, link-local
// and cloud-metadata ranges, so a client (anyone who can write the shared
// document) cannot turn the exit node into an SSRF proxy into its private
// network or 169.254.169.254. --allow-private lifts the restriction for
// operators who deliberately expose an internal network.
package netguard

import (
	"net"
	"sync/atomic"
)

var allowPrivate atomic.Bool

// SetAllowPrivate lets the exit reach otherwise-blocked ranges. Off by default.
func SetAllowPrivate(v bool) { allowPrivate.Store(v) }

// AllowPrivate reports the current setting.
func AllowPrivate() bool { return allowPrivate.Load() }

// blocked4 lists the IPv4 ranges refused unless --allow-private is set.
var blocked4 = mustCIDRs(
	"0.0.0.0/8",       // "this host"
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // CGNAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local incl. 169.254.169.254 cloud metadata
	"172.16.0.0/12",   // RFC 1918
	"192.168.0.0/16",  // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments
	"198.18.0.0/15",   // benchmarking
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved / 255.255.255.255 broadcast
)

var blocked6 = mustCIDRs(
	"::1/128",   // loopback
	"::/128",    // unspecified
	"fc00::/7",  // unique local
	"fe80::/10", // link-local
	"ff00::/8",  // multicast
)

// Blocked reports whether ip must be refused. AllowPrivate turns it off.
func Blocked(ip net.IP) bool {
	if ip == nil || allowPrivate.Load() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		for _, n := range blocked4 {
			if n.Contains(v4) {
				return true
			}
		}
		return false
	}
	for _, n := range blocked6 {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("netguard: bad CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}
