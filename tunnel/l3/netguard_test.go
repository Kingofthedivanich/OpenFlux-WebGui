package l3

import (
	"os"
	"testing"

	"github.com/p1neappleXpress/OpenFlux/netguard"
)

// The raw-socket integration tests (TestLinuxRaw*) talk to echo servers on
// loopback inside a disposable namespace, which netguard refuses by default;
// the package's tests run with it lifted, and
// TestL3DropsBlockedDestination turns it back on for itself.
func TestMain(m *testing.M) {
	netguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}

func TestL3DropsBlockedDestination(t *testing.T) {
	netguard.SetAllowPrivate(false)
	defer netguard.SetAllowPrivate(true)

	n, _ := testNAT(t)
	b := &recordingBackend{}
	r := &recordingTransport{packets: make(chan []byte, 1)}
	exit := &L3Exit{backend: b, trans: r, ct: newConntrack(), udp: n}
	defer exit.Stop()

	for _, dst := range [][4]byte{{169, 254, 169, 254}, {10, 0, 0, 5}, {127, 0, 0, 1}} {
		exit.handleFromTransport(udpPacket(clientIPBytes, dst, 12345, 80, []byte("x")))
	}
	if b.sent != nil {
		t.Fatal("forwarded a packet to a blocked destination")
	}
	if got := exit.dropBlocked.Load(); got != 3 {
		t.Fatalf("dropBlocked = %d, want 3", got)
	}
	exit.handleFromTransport(udpPacket(clientIPBytes, [4]byte{203, 0, 113, 7}, 12345, 443, []byte("ok")))
	if b.sent == nil {
		t.Fatal("a public destination was not forwarded")
	}
}
