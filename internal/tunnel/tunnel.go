package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"

	"openflux/internal/netguard"
	"openflux/internal/transport"
	"openflux/internal/utils"
)

// ExitMode выбирает, как выходная нода общается с интернетом.
type ExitMode int

const (
	ExitModeL3 ExitMode = iota // L3: SNAT/DNAT без gVisor (Linux)
	ExitModeL4                 // L4: gVisor TCP-терминация + net.Dial (работает везде)
)

func (m ExitMode) String() string {
	switch m {
	case ExitModeL3:
		return "l3"
	default:
		return "l4"
	}
}

// ParseExitMode разбирает строку из флага --mode.
func ParseExitMode(s string) (ExitMode, error) {
	switch s {
	case "", "l4", "proxy":
		// "proxy" is a deprecated alias kept for one release.
		return ExitModeL4, nil
	case "l3":
		return ExitModeL3, nil
	default:
		return ExitModeL4, fmt.Errorf("unknown mode %q (want l3|l4)", s)
	}
}

func parseSOCKS5(proxyStr string) (string, *proxy.Auth, error) {
	if !strings.Contains(proxyStr, "://") {
		proxyStr = "socks5://" + proxyStr
	}
	u, err := url.Parse(proxyStr)
	if err != nil {
		return "", nil, err
	}
	var auth *proxy.Auth
	if u.User != nil {
		auth = &proxy.Auth{
			User: u.User.Username(),
		}
		if pass, ok := u.User.Password(); ok {
			auth.Password = pass
		}
	}
	host := u.Host
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	return host, auth, nil
}

type TCPTunnel struct {
	gvisorStack   *stack.Stack
	tunnelEP      *TunnelLinkEndpoint
	transport     transport.Transport
	isExitNode    bool
	exitMode      ExitMode
	upstreamProxy string
	dialer        proxy.Dialer
	startTime     time.Time
	packetCount   atomic.Uint64
	closed        chan struct{}
	closeOnce     sync.Once
}

// TCP buffer size range for gvisor stacks.
var (
	TCPBufMin     = 4 * 1024 * 1024
	TCPBufDefault = 16 * 1024 * 1024
	TCPBufMax     = 64 * 1024 * 1024
)

// SetTCPBuffers applies the configured TCP send/receive buffer ranges to s.
func SetTCPBuffers(s *stack.Stack) {
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: TCPBufMin, Default: TCPBufDefault, Max: TCPBufMax}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv); err != nil {
		utils.Debugf("[TUNNEL] set recv buffer: %v", err)
	}
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: TCPBufMin, Default: TCPBufDefault, Max: TCPBufMax}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd); err != nil {
		utils.Debugf("[TUNNEL] set send buffer: %v", err)
	}
}

func NewTCPTunnel(trans transport.Transport, isExitNode bool, upstreamProxy ...string) (*TCPTunnel, error) {
	proxy := ""
	if len(upstreamProxy) > 0 {
		proxy = upstreamProxy[0]
	}
	return NewTCPTunnelModeWithProxy(trans, isExitNode, ExitModeL4, proxy)
}

func NewTCPTunnelMode(trans transport.Transport, isExitNode bool, mode ExitMode) (*TCPTunnel, error) {
	return NewTCPTunnelModeWithProxy(trans, isExitNode, mode, "")
}

func NewTCPTunnelModeWithProxy(trans transport.Transport, isExitNode bool, mode ExitMode, upstreamProxy string) (*TCPTunnel, error) {
	t := &TCPTunnel{
		transport:     trans,
		isExitNode:    isExitNode,
		exitMode:      mode,
		upstreamProxy: upstreamProxy,
		startTime:     time.Now(),
	}

	utils.Debugf("[TUNNEL] Net stack init...")
	t.gvisorStack = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})

	SetTCPBuffers(t.gvisorStack)

	// Detect loss only by duplicate ACKs and the RTO, without RACK-TLP. The
	// document relay delivers every message in order but sometimes holds them
	// for hundreds of milliseconds; RACK-TLP takes each hold for a loss and
	// halves the window, and gVisor never undoes that, which kept uploads
	// through the tunnel at ~100 KB/s.
	recovery := tcpip.TCPRecovery(0)
	if err := t.gvisorStack.SetTransportProtocolOption(tcp.ProtocolNumber, &recovery); err != nil {
		utils.Debugf("[TUNNEL] disable RACK-TLP: %v", err)
	}

	tunnelEP := NewTunnelLinkEndpoint()
	tunnelEP.onOutgoingPacket = func(data []byte) {
		if err := trans.Send(data); err != nil {
			utils.Debugf("[TUNNEL] trans.Send error: %v", err)
		}
	}
	t.tunnelEP = tunnelEP

	tunnelNIC := tcpip.NICID(1)
	if err := t.gvisorStack.CreateNIC(tunnelNIC, tunnelEP); err != nil {
		return nil, fmt.Errorf("create NIC: %v", err)
	}

	if isExitNode {
		t.setupExitNodeProxy(tunnelNIC)
	} else {
		t.setupClient(tunnelNIC)
	}

	trans.Receive(func(data []byte) {
		tunnelEP.InjectInbound(data)
	})

	t.closed = make(chan struct{})
	utils.SafeGo("tunnel.printStats", t.printStats)
	return t, nil
}

// ---- exit node: proxy ----

func (t *TCPTunnel) setupExitNodeProxy(tunnelNIC tcpip.NICID) {
	baseDialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	if t.upstreamProxy == "" || strings.ToLower(t.upstreamProxy) == "direct" {
		t.dialer = baseDialer
		utils.Debugf("[TUNNEL] EXIT NODE - proxy mode (direct connection, no upstream SOCKS5)")
	} else {
		proxyHost, auth, err := parseSOCKS5(t.upstreamProxy)
		if err != nil {
			utils.Debugf("[TUNNEL] Invalid upstream proxy %q: %v", t.upstreamProxy, err)
			t.dialer = baseDialer
		} else {
			d, err := proxy.SOCKS5("tcp", proxyHost, auth, baseDialer)
			if err != nil {
				utils.Debugf("[TUNNEL] Failed to create SOCKS5 dialer for %s: %v", proxyHost, err)
				t.dialer = baseDialer
			} else {
				t.dialer = d
				utils.Debugf("[TUNNEL] EXIT NODE - Routing via SOCKS5 proxy %s", proxyHost)
			}
		}
	}

	t.gvisorStack.SetPromiscuousMode(tunnelNIC, true)
	t.gvisorStack.SetSpoofing(tunnelNIC, true)
	t.gvisorStack.AddRoute(tcpip.Route{
		Destination: header.IPv4EmptySubnet,
		NIC:         tunnelNIC,
	})

	fwd := tcp.NewForwarder(t.gvisorStack, 0, 8192, t.handleExitTCP)
	t.gvisorStack.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}

// l4IdleTimeout bounds how long an L4 relay direction waits for data before
// giving up. Without this, a stalled peer (e.g. a mobile client that loses
// network without sending FIN/RST) leaves the relay goroutine and both
// sockets blocked on Read forever.
const l4IdleTimeout = 5 * time.Minute

// copyWithIdleTimeout is io.CopyBuffer with a deadline reset before every
// Read on src: as long as some data arrives within idleTimeout the copy
// continues indefinitely, but a src that goes silent for idleTimeout makes
// Read return a timeout error, ending the copy instead of blocking forever.
func copyWithIdleTimeout(dst, src net.Conn, buf []byte, idleTimeout time.Duration) {
	for {
		src.SetReadDeadline(time.Now().Add(idleTimeout))
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

func (t *TCPTunnel) handleExitTCP(r *tcp.ForwarderRequest) {
	reqID := r.ID()
	dstIP := net.IP(reqID.LocalAddress.AsSlice())
	if netguard.Blocked(dstIP) {
		utils.Debugf("[EXIT] refused blocked destination %s (use --allow-private to permit)", dstIP)
		r.Complete(true)
		return
	}
	dest := net.JoinHostPort(dstIP.String(), strconv.Itoa(int(reqID.LocalPort)))

	utils.SafeGo("exit.flow", func() {
		dialer := t.dialer
		if dialer == nil {
			dialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var remote net.Conn
		var err error
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			remote, err = cd.DialContext(ctx, "tcp", dest)
		} else {
			remote, err = dialer.Dial("tcp", dest)
		}

		if err != nil {
			utils.Debugf("[EXIT] dial %s failed: %v", dest, err)
			r.Complete(true)
			return
		}
		defer remote.Close()

		var wq waiter.Queue
		ep, tErr := r.CreateEndpoint(&wq)
		if tErr != nil {
			utils.Debugf("[EXIT] CreateEndpoint %s: %v", dest, tErr)
			r.Complete(true)
			return
		}
		r.Complete(false)

		local := gonet.NewTCPConn(&wq, ep)
		defer local.Close()

		if tc, ok := remote.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetReadBuffer(16 * 1024 * 1024)
			_ = tc.SetWriteBuffer(16 * 1024 * 1024)
		}
		utils.Debugf("[EXIT] %s connected", dest)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			buf := make([]byte, 256*1024)
			copyWithIdleTimeout(remote, local, buf, l4IdleTimeout)
			halfClose(remote)
			remote.SetReadDeadline(time.Now().Add(halfCloseLinger))
		}()

		go func() {
			defer wg.Done()
			buf := make([]byte, 256*1024)
			copyWithIdleTimeout(local, remote, buf, l4IdleTimeout)
			// Half-close: let the local->remote direction keep flowing after
			// the server stops sending, instead of tearing the flow down.
			halfClose(local)
			local.SetReadDeadline(time.Now().Add(halfCloseLinger))
		}()

		wg.Wait()
	})
}

// ---- client ----

func (t *TCPTunnel) setupClient(tunnelNIC tcpip.NICID) {
	clientAddr := tcpip.AddrFrom4([4]byte{10, 10, 10, 2})
	t.gvisorStack.AddProtocolAddress(tunnelNIC, tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   clientAddr,
			PrefixLen: 24,
		},
	}, stack.AddressProperties{})

	t.gvisorStack.AddRoute(tcpip.Route{
		Destination: header.IPv4EmptySubnet,
		NIC:         tunnelNIC,
	})
}

func (t *TCPTunnel) DialTCP(address string) (net.Conn, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}

	ip := tcpAddr.IP.To4()
	if ip == nil {
		return nil, fmt.Errorf("IPv6 not supported")
	}
	utils.Debugf("[TUNNEL] DialTCP %s -> %s:%d", address, ip.String(), tcpAddr.Port)

	nic := tcpip.NICID(1)
	if t.isExitNode && false {
		nic = tcpip.NICID(2)
	}

	conn, err := gonet.DialTCP(t.gvisorStack, tcpip.FullAddress{
		NIC:  nic,
		Addr: tcpip.AddrFrom4([4]byte{ip[0], ip[1], ip[2], ip[3]}),
		Port: uint16(tcpAddr.Port),
	}, ipv4.ProtocolNumber)

	return conn, err
}

func (t *TCPTunnel) ListenTCP(port uint16) (net.Listener, error) {
	return gonet.ListenTCP(t.gvisorStack, tcpip.FullAddress{
		NIC:  1,
		Port: port,
	}, ipv4.ProtocolNumber)
}

func (t *TCPTunnel) printStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-t.closed:
			return
		case <-ticker.C:
		}
		stats := t.gvisorStack.Stats()
		utils.Debugf("[STATS] uptime=%v mode=%s packets=%d connected=%d established=%d retrans=%d",
			time.Since(t.startTime).Round(time.Second),
			t.exitMode.String(),
			t.packetCount.Load(),
			stats.TCP.CurrentConnected.Value(),
			stats.TCP.CurrentEstablished.Value(),
			stats.TCP.Retransmits.Value(),
		)
	}
}

// Close stops the stats loop and tears down the gVisor stack. It does not
// touch the underlying transport; callers that own the transport (e.g.
// exitmgr.Manager) are responsible for stopping that separately. Safe to
// call more than once.
func (t *TCPTunnel) Close() error {
	t.closeOnce.Do(func() {
		if t.closed != nil {
			close(t.closed)
		}
		if t.gvisorStack != nil {
			t.gvisorStack.Close()
		}
	})
	return nil
}

// Stats returns a snapshot of this tunnel's current counters, for exitmgr's
// per-client status reporting.
type Stats struct {
	UptimeSeconds int64
	Connected     uint64
	Established   uint64
	Retransmits   uint64
}

func (t *TCPTunnel) StatsSnapshot() Stats {
	s := t.gvisorStack.Stats()
	return Stats{
		UptimeSeconds: int64(time.Since(t.startTime).Seconds()),
		Connected:     s.TCP.CurrentConnected.Value(),
		Established:   s.TCP.CurrentEstablished.Value(),
		Retransmits:   s.TCP.Retransmits.Value(),
	}
}

