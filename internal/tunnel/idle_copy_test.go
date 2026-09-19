package tunnel

import (
	"io"
	"net"
	"testing"
	"time"
)

// Before this fix, handleExitTCP's two io.CopyBuffer directions had no
// read/write deadlines: a stalled peer (mobile client loses network without
// sending FIN/RST) left the copy goroutine, both sockets, and both 256KB
// buffers blocked on Read forever -- a leak per stalled connection over a
// long-running exit node's life. copyWithIdleTimeout must give up (and let
// the caller close both ends) after idleTimeout of no data in either
// direction.
func TestCopyWithIdleTimeoutForwardsData(t *testing.T) {
	src, srcRemote := net.Pipe()
	dst, dstRemote := net.Pipe()
	defer src.Close()
	defer srcRemote.Close()
	defer dst.Close()
	defer dstRemote.Close()

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		copyWithIdleTimeout(dst, src, buf, time.Second)
		close(done)
	}()

	go func() {
		srcRemote.Write([]byte("hello"))
		srcRemote.Close() // triggers EOF on src, ending the copy loop
	}()

	got := make([]byte, 5)
	dstRemote.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(dstRemote, got); err != nil {
		t.Fatalf("did not receive forwarded data: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("copyWithIdleTimeout did not return after src EOF")
	}
}

func TestCopyWithIdleTimeoutGivesUpOnStalledPeer(t *testing.T) {
	src, srcRemote := net.Pipe()
	dst, dstRemote := net.Pipe()
	defer src.Close()
	defer srcRemote.Close()
	defer dst.Close()
	defer dstRemote.Close()

	// Drain dst so a Write inside the copy loop (if any happened) wouldn't
	// itself block; here nothing is ever written by srcRemote, simulating a
	// peer that went silent without closing the connection.
	go io.Copy(io.Discard, dstRemote)

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		copyWithIdleTimeout(dst, src, buf, 100*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		// good: gave up instead of blocking forever
	case <-time.After(2 * time.Second):
		t.Fatal("copyWithIdleTimeout blocked forever on a stalled peer instead of timing out")
	}
}
