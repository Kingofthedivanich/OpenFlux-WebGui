package socks5

import (
	"io"
	"net"
	"testing"
	"time"
)

// pipeDialer's "target" is a real loopback TCP connection, not net.Pipe:
// net.Pipe's conn has no CloseWrite, so closeWrite() on it falls back to a
// full Close() that tears down both directions -- racing the still-pending
// reply write below against whichever goroutine reaches EOF first. A real
// TCPConn supports CloseWrite like any actual target does, so the relay's
// half-close behaves the same as in production and the test is deterministic.
type pipeDialer struct{ server net.Conn }

func (d *pipeDialer) DialTCP(string) (net.Conn, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, err
	}
	server, err := ln.Accept()
	if err != nil {
		client.Close()
		return nil, err
	}
	d.server = server
	return client, nil
}

// After the client half-closes its write side, the target's reply must still
// reach the client (NET-8: an EOF one way must not tear the other down).
func TestSOCKS5HalfClose(t *testing.T) {
	d := &pipeDialer{}
	srv := NewSOCKS5Server("127.0.0.1:0", d)
	if err := srv.Bind(); err != nil {
		t.Fatalf("bind: %v", err)
	}
	go srv.Start()
	defer srv.Close()
	time.Sleep(20 * time.Millisecond)

	c, err := net.Dial("tcp", srv.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// greeting + request CONNECT 1.2.3.4:80
	c.Write([]byte{0x05, 0x01, 0x00})
	buf := make([]byte, 2)
	io.ReadFull(c, buf)
	c.Write([]byte{0x05, 0x01, 0x00, 0x01, 1, 2, 3, 4, 0, 80})
	rep := make([]byte, 10)
	io.ReadFull(c, rep)
	if rep[1] != 0x00 {
		t.Fatalf("connect reply = %d", rep[1])
	}

	// Client sends a request, then half-closes its write side.
	c.Write([]byte("ping"))
	c.(*net.TCPConn).CloseWrite()

	got := make([]byte, 4)
	io.ReadFull(d.server, got)
	if string(got) != "ping" {
		t.Fatalf("target got %q", got)
	}

	// Target replies AFTER the client's write EOF; it must still arrive.
	d.server.Write([]byte("pong"))
	d.server.Close()
	reply := make([]byte, 4)
	if _, err := io.ReadFull(c, reply); err != nil || string(reply) != "pong" {
		t.Fatalf("client reply = %q, err=%v (half-close broke the return path)", reply, err)
	}
}
