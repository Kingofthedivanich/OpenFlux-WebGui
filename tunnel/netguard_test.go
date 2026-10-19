package tunnel

import (
	"io"
	"net"
	"os"
	"testing"

	"github.com/p1neappleXpress/OpenFlux/netguard"
)

// The tunnel tests reach echo servers on loopback and the LAN through the
// exit, which netguard refuses by default; they run with it lifted, and
// TestExitRefusesBlockedDestination turns it back on for itself.
func TestMain(m *testing.M) {
	netguard.SetAllowPrivate(true)
	os.Exit(m.Run())
}

func TestExitRefusesBlockedDestination(t *testing.T) {
	netguard.SetAllowPrivate(false)
	defer netguard.SetAllowPrivate(true)

	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()

	a, b := newTransportPair()
	exit := NewTCPTunnelMode(b, true, ExitModeL4)
	defer exit.Close()
	client := NewTCPTunnelMode(a, false, ExitModeL4)
	defer client.Close()

	if conn, err := client.DialTCP(echo.Addr().String()); err == nil {
		conn.Close()
		t.Fatalf("dial %s through the exit succeeded; want it refused", echo.Addr())
	}
	select {
	case <-accepted:
		t.Fatal("the exit connected to a loopback destination")
	default:
	}
}
