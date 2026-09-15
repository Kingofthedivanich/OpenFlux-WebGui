# OpenFlux

**English** | [Русский](README.ru.md)

Network stack research tool. TCP tunnel with pluggable transports,
batched+zstd codec, and L3 exit-node mode.

# Disclaimer

The author of OpenFlux **does not encourage** the use of this project to bypass
restrictions or violate the rules of any platform, and **is not responsible**
for the final scenarios of how users apply this tool in real life or on the
Internet. Any specific technical features of the application are nothing more
than an **architectural coincidence**, created **without any intent**.

The project is **entirely non-commercial**, contains **no paid features, hidden
subscriptions, or commercial benefit**.

The author **is not responsible** for forks, modifications, or derivative
versions of OpenFlux created by third parties. Any changes added to a fork are
the responsibility of its author.

The author **is not responsible** for:

- Any use of OpenFlux by third parties
- Consequences caused by the use of forks and modifications
- Damage resulting from derivative versions
- Violations committed using forks

The original code is provided **as is**, **without any warranties**.

## Clients

| Platform | Download | Notes |
|----------|----------|-------|
| **macOS**  | build from source | CLI + utun L3 client (--tun) |
| **Linux**  | build from source | CLI client / exit node |
| **Windows**| build from source | CLI client / exit node (proxy mode) |
| **Android**| [OpenFluxAndroid releases](https://github.com/p1neappleXpress/OpenFluxAndroid) | Standalone APK |
| **iOS**    | [TestFlight beta](https://testflight.apple.com/join/BwnAcdus) | System-wide VPN via Network Extension |

> **iOS app** built by [@saharev1](https://github.com/saharev1) - full iOS client,
> TestFlight pipeline, system VPN support, DNS-over-TLS, and many stability fixes.
> HUGE thanks!
>
> **Android app** - [p1neappleXpress/OpenFluxAndroid](https://github.com/p1neappleXpress/OpenFluxAndroid).

## Architecture

macOS client (utun)    --> Transport --> Exit node (L3) --> Internet
Linux/Windows client   --> Transport --> Exit node (L3) --> Internet
iOS packet tunnel      --> Transport --> Exit node (L3) --> Internet
Android client         --> Transport --> Exit node (L3) --> Internet

Exit node terminates nothing: it forwards raw IP packets with SNAT/DNAT
(conntrack + egress-IP filter). One TCP connection end-to-end between
the client and the real server.

The client terminates TCP locally (gVisor, utun, or NEPacketTunnelProvider),
sends raw IP packets into the transport. The exit node rewrites source/dest
addresses and forwards - it never sees TCP state.

## Highlights

- **Pluggable transports** - Yandex.Docs (WS), Yandex Volga (HTTP relay),
  MAX/OneMe (WebRTC DataChannel), Cups.online (Centrifugo rooms).
- **Batched + zstd codec** - coalesces many tunnel packets into a single
  transport message. Fewer channel messages, higher throughput. See
  transport/batched.go and transport/framing.go.
- **L3 exit** - exit node runs in --mode l3 and forwards raw IPv4 packets
  via SOCK_RAW (Linux) or WinDivert (Windows). No userspace TCP stack,
  no double termination.
- **macOS utun client** - --client --tun (macOS only). Creates a utun
  interface, watches its own sockets to install bypass routes, then takes
  the default route. No SOCKS5, no gVisor.
- **Legacy fallback** - --legacy reverts the transport to the old
  per-packet LZ4 codec (compatible with older clients).
- **Benchmark modes** - --bench-send N / --bench-sink measure raw
  goodput through the transport without touching the host network.

## Requirements

1. **Go 1.26.3+** - to build the desktop client / exit-node binary.
2. **Android NDK r27+** - to build the Android client binary.
3. **Xcode 26.6+** - to build the iOS client binary.
4. A Linux VPS / VDS for the exit node (or run the exit locally via QEMU,
   see below).

## Structure

OpenFlux/
  main.go                          # CLI entry (client / exit-node / benches)
  bench.go                         # Benchmark helpers (--bench-send/--bench-sink)
  tun_darwin.go                    # macOS utun L3 client
  tun_watch.go                     # Socket watcher for bypass routes
  tun_other.go                     # Stubs for non-darwin platforms
  export_ios.go                    # cgo bridge for the iOS static library
  transport/
    transport.go                   # Transport interface
    batched.go                     # BatchedTransport (coalescing + zstd)
    framing.go                     # Wire framing for batched frames
    compressor.go                  # Legacy per-packet LZ4 codec
    encrypted.go                   # Optional AES-256-GCM wrapper
    yandex/                        # Yandex.Docs + Volga backends
    oneme/                         # MAX Messenger backend
    cupsonline/                    # Cups.online backend
  tunnel/
    tunnel.go                      # Client tunnel (gVisor + TunnelLinkEndpoint)
    endpoint.go                    # Virtual NIC (client)
    exit.go                        # NewExitNode dispatcher (l3 / proxy)
    proxy_exit.go                  # Legacy proxy exit (gVisor + net.Dial)
    l3/                            # L3 exit node
      l3.go                        # L3Exit: SNAT/DNAT, conntrack, egress filter
      backend.go                   # L3Backend interface
      backend_linux.go             # SOCK_RAW backend (Linux)
      backend_windows.go           # WinDivert backend (stub)
      backend_other.go             # Unsupported-platform stub
      conntrack.go                 # Conntrack table
      flow.go                      # Flow keys, SNAT/DNAT, checksums
    rawsocket_linux.go             # Legacy raw exit (kept for reference)
    rawsocket_{darwin,windows}.go
  socks5/                          # SOCKS5 server (client fallback)
  network/                         # Checksums, packet parsing
  utils/                           # Logging
  ios-app/                         # SwiftUI iOS client (XcodeGen)
  build_ios.sh                     # Build iOS static library (liboflux.a)
  build_ios_app.sh                 # Build + archive + export iOS app IPA
  build_android.sh                 # Build Android client binary
  scripts/
    cleanup-utun.sh                # Remove leftover utun routes (macOS)
    build-flx-linux-img.sh         # Build minimal Alpine rootfs for QEMU

## Build

go mod tidy
go build -o openflux .

Cross-build for the exit node (Linux amd64), stripped:

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags=\"-s -w\" -trimpath -o openflux-linux .

## Usage

### Exit node (Linux, L3 mode)

L3 mode forwards raw IPv4 packets between the transport and the OS network
stack. Requires root (CAP_NET_RAW).

sudo ./openflux --exit-node --mode l3 \
    --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\"

Add --debug for verbose logging. On Linux, no iptables rule is required -
the L3 code drops outbound RSTs before sendto().

### Exit node (legacy proxy mode, no root needed)

./openflux --exit-node --mode proxy \
    --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\"

Proxy mode is a fallback for platforms where L3 is not available
(Windows without WinDivert, macOS, or non-root Linux).

### Client - macOS L3 (utun)

sudo ./openflux --client --tun \
    --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\"

Creates a utun interface, installs bypass routes for the transport, waits
for the transport to connect, then takes the default route. No SOCKS5.

Requires sudo. All traffic except the transport goes through the tunnel.

### Client - SOCKS5 (all platforms, fallback)

./openflux --client --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\" \
    --socks5 :1080

Point your browser at 127.0.0.1:1080 as a SOCKS5 proxy.

### Codec selection

By default the transport uses the batched + zstd codec
(transport/batched.go + transport/framing.go). To use the old
per-packet LZ4 codec instead, pass --legacy:

./openflux --client --legacy ...   # on both client and exit node

Important: the batched wire format is NOT compatible with the legacy
LZ4 format. Client and exit node must both use the same codec (both new,
or both --legacy).

### Benchmarks

Measure raw goodput over the transport, without touching the host network:

# Sender: push 100 MB
./openflux --client --transport yandex --url \"...\" --bench-send 100

# Receiver: measure goodput
./openflux --client --transport yandex --url \"...\" --bench-sink

### Other transports

# Yandex Volga (HTTP relay)
./openflux --exit-node --mode l3 --transport vyandex --url \"...\" --debug

# MAX / OneMe (WebRTC DataChannel)
./openflux --exit-node --mode l3 --transport oneme \
    --maxToken \"...\" --maxUid \"...\" --debug

# Cups.online (Centrifugo rooms)
./openflux --exit-node --mode l3 --transport cupsonline --debug
# prints a base64 room list; pass it to the client via --url

## TODO

- **Run the exit node without a VPS (QEMU).** A minimal Alpine Linux image
  (~13 MB) can host the exit node on any desktop (macOS / Windows / Linux)
  with QEMU installed. Base files (vmlinuz-virt + base-initramfs.gz) are built
  once; per-user images are repacked in ~3 seconds with the oflx binary and
  the transport URL. Not shipped yet — tracked as a future addition.

- **Windows L3 client.** The L3 exit works on Linux (SOCK_RAW) and is
  stubbed for Windows (WinDivert). Wiring the WinDivert backend to the L3
  forwarder is planned.

- **Additional transports.** New backends can be implemented against the
  Transport interface; the batched codec wraps any of them.

- **Public App Store distribution.** Current iOS build is TestFlight-internal
  only (App Store Guideline 5.4 requires a NetworkExtension target and an
  organization account for public VPN apps).


## Flags

| Flag | Default | Description |
|------|---------|-------------|
| --client | | Run as client |
| --exit-node | | Run as exit node |
| --tun | false | macOS client: use utun L3 mode (needs sudo) |
| --socks5 | :1080 | SOCKS5 listen address |
| --url | https://localhost | Document URL (Yandex Docs, Cups base64 list) |
| --transport | yandex | yandex, vyandex, oneme, cupsonline |
| --mode | l3 | Exit-node mode: l3 (raw forward) or proxy (gVisor + net.Dial) |
| --legacy | false | Use legacy per-packet LZ4 codec instead of batching |
| --encryption-key-file | | Optional AES-256-GCM wrapper (shared secret) |
| --maxToken | | Auth token (MAX) |
| --maxUid | | User ID (MAX) |
| --bench-send | 0 | Benchmark: push N MB and exit |
| --bench-sink | false | Benchmark: receive and measure goodput |
| --bench-compressible | false | Benchmark: use compressible payload |
| --debug | false | Enable verbose logging |

## Implementing custom transports

Implement the Transport interface from transport/transport.go and register
your transport in the main.go switch block. The batched codec
(BatchedTransport) wraps any transport, so a new backend gets batching for
free.

## License

This project is licensed under the GNU General Public License v3.0 or later.
See LICENSE for the full text.

Third-party licenses are listed in NOTICE.

## Disclaimer

Educational use only. Test on your own machines and networks.

