package netguard

import (
	"net"
	"testing"
)

func TestBlockedDefault(t *testing.T) {
	SetAllowPrivate(false)
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.16.5.5",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1",
		"::1", "fe80::1", "fc00::1",
	}
	for _, ip := range blocked {
		if !Blocked(net.ParseIP(ip)) {
			t.Errorf("%s should be blocked by default", ip)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111"}
	for _, ip := range allowed {
		if Blocked(net.ParseIP(ip)) {
			t.Errorf("%s should be allowed", ip)
		}
	}
}

func TestAllowPrivateLiftsBlock(t *testing.T) {
	SetAllowPrivate(true)
	defer SetAllowPrivate(false)
	if Blocked(net.ParseIP("169.254.169.254")) {
		t.Error("--allow-private should permit metadata IP")
	}
}
