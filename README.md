# OpenFlux

**English** | [Русский](README.ru.md)

Network stack research tool. TCP tunnel with pluggable transports,
batched+zstd codec, and two exit-node backends (L3 raw forward / L4 gVisor proxy).

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

## This fork

Fork of [p1neappleXpress/OpenFlux](https://github.com/p1neappleXpress/OpenFlux)
(based on `0.0.3`), combining this fork's own work with hardening adopted
from the community fork [danusha2345/OpenFlux](https://github.com/danusha2345/OpenFlux)
and a handful of individual upstream PRs. Paired with an
[Android client fork](https://github.com/Kingofthedivanich/OpenFluxAndroid)
that tracks this repo's encryption changes.

**Encryption & identity**
- **Noise NKpsk0 encryption** (X25519 + AES-256-GCM, rotating session keys)
  replaces the old static-AES-GCM transport encryption; required by default
  unless `--allow-plaintext` is passed. See [Encryption](#encryption-optional).
- **`netguard`** — an SSRF denylist blocking private/loopback/link-local and
  cloud-metadata destinations on the exit node by default (`--allow-private`
  to disable).

**Multi-client exit node**
- **Local web admin panel** (`--role=exit-panel`) — run many clients in one
  process, each on its own Yandex.Docs / Volga / MAX / Cups.online / Mail.ru
  document or room, managed live (add/edit/remove, status, traffic, the
  panel's public key to copy) with no restart needed. Every client shares
  the panel's own Noise identity; a per-client PSK file optionally closes
  one client to strangers. A QR button on each client card imports it into
  the Android app in one scan, and an optional Yandex OAuth token lets the
  panel generate a fresh `--url` document itself instead of one made by
  hand. See
  [Exit node - multi-client admin panel](#exit-node---multi-client-admin-panel).
- **Telegram admin bot** (optional, same role) — full parity with the web
  panel's client management, operable from a phone with no SSH tunnel. See
  [Telegram admin bot](#telegram-admin-bot-optional).
- **`deploy/menu.sh`** — a 3x-ui-style interactive console on the VPS
  itself: update, manage clients, and enable/configure/disable the
  Telegram bot without hand-editing the systemd unit. See
  [Management console](#management-console-like-3x-uis-menu).

**Transport**
- **Multi-stream** — `--url` takes a comma-separated document list
  (yandex/vyandex, CLI and panel clients both), spreading connections across
  them with failover to the others if one dies.
- **`--upstream-proxy`** — the exit node can itself dial out through an
  upstream SOCKS5 proxy instead of the internet directly.
- **Jittered Yandex.Docs keep-alives** — randomized interval and payload
  length instead of a fixed-size/fixed-interval packet, which is a
  traffic-analysis fingerprint that survives TLS.
- **`--traffic-stats=<duration>`** — periodic machine-readable
  `[TRAFFIC] connected=%t tx=%d rx=%d` log line from the transport's real
  byte counters, for anything that wants to read it off stdout.

**Correctness & robustness** — ~25 fixes across the exit-node and transport
paths since forking, each with a regression test: two remote-triggerable
panics, a decompression-bomb DoS, `--local-ip` being silently ignored, a
client RST never reaching the real destination, an unbounded memory/goroutine
leak on stalled L4 relays, a conntrack lock that blocked all packet
forwarding during its sweep, no graceful shutdown on SIGTERM, a conntrack
entry-count cap, half-close on every data path instead of a hard close,
SOCKS5 RFC 1928 compliance, an unguarded data race in transport
re-authorization, `EncryptedTransport.IsConnected()` reporting true before
the Noise handshake actually completed (a real packet-blackhole window),
and reconnect/keepalive fixes across the Yandex/Volga/MAX/Mail.ru
transports.

**Not maintained here**
- **iOS bridge** — `export_ios.go` builds under `//go:build ios` (inherited
  from upstream/the community fork) but this fork ships no iOS app or CI
  job for it.
- `scripts/build_android.sh` is host-OS aware (macOS/Linux/Windows) and builds
  all three ABIs the [Android app fork](https://github.com/Kingofthedivanich/OpenFluxAndroid)
  ships, instead of just `arm64-v8a` from a macOS host — but the Android app
  itself lives in that separate repo.

## Clients

| Platform | Download | Notes |
|----------|----------|-------|
| **macOS**   | build from source | CLI + utun L3 client (`--inbound=tun`, default on macOS) |
| **Linux**   | build from source | CLI client (SOCKS5) / exit node (L3 or L4) |
| **Windows** | build from source | CLI client (SOCKS5) / exit node (`l4`, or `l3` via QEMU - see TODO) |
| **Android** | [OpenFluxAndroid fork releases](https://github.com/Kingofthedivanich/OpenFluxAndroid/releases) | APK, built against this repo's encryption |
| **iOS**     | [TestFlight beta](https://testflight.apple.com/join/BwnAcdus) | Upstream's client; not paired with this fork's encryption changes |

> **iOS app** built by [@saharev1](https://github.com/saharev1) for upstream -
> full iOS client, TestFlight pipeline, system VPN support, DNS-over-TLS, and
> many stability fixes. Not tracked by this fork; expect it to need
> `--allow-plaintext` against an exit node running this repo's code, since
> its own Noise/peer-key support (if any) targets upstream instead.
>
> **Android app** - [Kingofthedivanich/OpenFluxAndroid](https://github.com/Kingofthedivanich/OpenFluxAndroid),
> this fork's paired client (peer-key field, PSK, all built from this repo).

## Architecture

> **Encryption is required by default** (Noise `NKpsk0`): start the exit with
> `--exit-key-file` and the client with `--peer-key`, or pass `--allow-plaintext`
> to run an unprotected tunnel. All clients (desktop, iOS, Android) use the
> **batched** codec and the same wire format.

Any client works with either exit backend. `--mode` is chosen on the **exit
node**, not on the client.

```
Client (any):  macOS (utun) / Linux / Windows / iOS (packet tunnel) / Android
                    |
                    v
               Transport (Yandex.Docs / Volga / MAX / Cups / Mail.ru)
                    |
                    v
               Exit node  -->  Internet
                 --mode l3   (raw SNAT/DNAT, Linux + root)
                 --mode l4   (gVisor proxy, any platform)
```

| Client (any)                            | Exit backend | Requires              |
|-----------------------------------------|--------------|-----------------------|
| macOS / Linux / Windows / iOS / Android | `--mode l3`  | exit on Linux + root  |
| macOS / Linux / Windows / iOS / Android | `--mode l4`  | nothing               |

In `l3`, the exit node terminates nothing: it forwards raw IP packets with
SNAT/DNAT (conntrack + egress-IP filter). One TCP connection end-to-end
between the client and the real server.

In `l4`, the exit node terminates TCP in a userspace gVisor stack, then
re-dials the real server with `net.Dial`. Works on any OS, no root.

The client terminates TCP locally (gVisor, utun, or NEPacketTunnelProvider),
then sends raw IP packets into the transport.

## Exit-node backends

The exit node has exactly **two** backends, selected with `--mode` on the
**exit node**. The client does not choose a backend - the same client works
against either.

| `--mode` | Backend | Forwarding | Requires | Platforms |
|----------|---------|-----------|----------|-----------|
| `l3` | Raw L3 | SNAT/DNAT on raw IPv4 via SOCK_RAW + conntrack. No userspace TCP stack. | root / CAP_NET_RAW | Linux only |
| `l4` (alias `proxy`) | gVisor proxy | Terminates TCP in a userspace gVisor stack, then `net.Dial` to the real server. | nothing | Linux, macOS, Windows |

- `proxy` is a deprecated alias for `l4`; both select the same backend.
  `l4` is the canonical name going forward.
- **l3 is faster** (single end-to-end TCP connection, no double termination)
  but Linux-only and needs root.
- **l4 works everywhere** without root, at the cost of terminating TCP twice
  (client -> gVisor on exit -> real server).
- On Linux with root, prefer `l3`. On Windows, the intended path is `l3`
  inside a lightweight QEMU VM (see TODO) - the WinDivert backend is not wired
  yet, and `l4` is the working fallback until QEMU is shipped. On non-root
  hosts, use `l4`.

### l3 and kernel RSTs

In `l3` mode the kernel sees return packets for connections it never opened
and emits RSTs, tearing the tunnel connections down. Drop them:

```
# Scoped (recommended): assign a dedicated egress IP, run with --local-ip, then:
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s <egress-ip> -j DROP

# Host-wide fallback (drops ALL outbound RST; makes closed ports look filtered):
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP
```

The L3 code additionally drops client-originated RSTs before `sendto()`, so
the kernel rule above is only needed for kernel-generated RSTs.

## Highlights

- **Pluggable transports** - Yandex.Docs (WS), Yandex Volga (HTTP relay + WS),
  MAX/OneMe (WebRTC DataChannel), Cups.online (Centrifugo rooms),
  Mail.ru Docs (WS).
- **Batched + zstd codec** - coalesces many tunnel packets into a single
  transport message. Fewer channel messages, higher throughput. See
  `transport/batched.go` and `transport/framing.go`.
- **Two exit backends** - `l3` (raw SNAT/DNAT) and `l4` (gVisor proxy).
  See [Exit-node backends](#exit-node-backends).
- **macOS utun client** - `--inbound=tun` (default on macOS). Creates a utun
  interface, watches its own sockets to install bypass routes, then takes
  the default route. No SOCKS5, no gVisor on the client.
- **iOS packet tunnel** - NEPacketTunnelProvider, pure L3 forwarding.
- **Legacy codec** - `--codec=legacy` reverts to the old per-packet LZ4 codec
  (compatible with older clients).
- **Optional encryption** - `--exit-key-file` on the exit node and `--peer-key`
  on the client run a Noise `NKpsk0` handshake (X25519 + AES-256-GCM) over the
  channel; session keys rotate every 2 minutes. `--psk-file` on both sides
  closes the node to clients without the secret.
- **Benchmark modes** - `--role=bench-send --bench-bytes=N` / `--role=bench-sink`
  measure raw goodput through the transport without touching the host network.
- **Traffic telemetry** - `--traffic-stats=<duration>` logs
  `[TRAFFIC] connected=%t tx=%d rx=%d` from the transport's real byte
  counters on an interval, for anything reading the process's stdout.

## Requirements

1. **Go** - to build the desktop client / exit-node binary. See `go.mod` for
   the exact version.
2. **Android NDK r27+** - to build the [Android client](https://github.com/Kingofthedivanich/OpenFluxAndroid)'s
   native library from this repo.
3. **A Linux VPS / VDS** for the exit node. The `l3` backend requires root;
   `l4` works without.

Xcode is only needed for `export_ios.go`'s `//go:build ios` target, which
this fork doesn't build or ship — skip it unless you're doing that work
yourself.

## Structure

Everything under `internal/` is private to this module by Go's own
compiler-enforced rule (nothing outside `openflux/...` can import it) --
appropriate here since this is an application, not a library. `cmd/openflux`
is the only buildable entry point.

```
OpenFlux/
  cmd/openflux/
    main.go                        # CLI entry (client / exit / benches)
    export_ios.go, export_ios_packet.go  # cgo bridge for the iOS static
                                    # library -- must stay next to main.go:
                                    # -buildmode=c-archive builds one dir
  internal/
    bench/                         # --role=bench-send/bench-sink helpers
    encryptionsetup/                # CLI flags -> Noise encryption config
    multistream/                   # Multi-stream status log
    signals/                       # OS signal handling (unix/windows)
    tun/                           # macOS utun L3 client, socket watcher, host learner
    transportstack/                # Backend+encryption+codec+multi-stream wiring, shared by main.go and exitmgr
    transport/
      transport.go                 # Transport interface
      batched.go                   # BatchedTransport (coalescing + zstd)
      framing.go                   # Wire framing for batched frames
      compressor.go                # Legacy per-packet LZ4 codec
      encrypted.go                 # Optional encrypted session layer (Noise NKpsk0)
      noise_keys.go                # Exit static key file, peer key parsing, PSK derivation
      replay.go                    # Anti-replay window for the encrypted layer
      multistream.go               # One tunnel over several documents
      flowhash.go                  # Per-connection hash for multi-stream
      yandex/                      # Yandex.Docs + Volga backends
      oneme/                       # MAX Messenger backend
      cupsonline/                  # Cups.online backend
      mailru/                      # Mail.ru Docs backend
    tunnel/
      tunnel.go                    # Client tunnel (gVisor + TunnelLinkEndpoint)
      endpoint.go                  # Virtual NIC (client)
      exit.go                      # NewExitNode dispatcher (l3 / l4)
      proxy_exit.go                # L4 exit (gVisor + net.Dial)
      l3/
        l3.go                      # L3Exit: SNAT/DNAT, conntrack, egress filter
        backend.go                 # L3Backend interface
        backend_linux.go           # SOCK_RAW backend (Linux)
        backend_windows.go         # Stub (WinDivert not wired yet)
        backend_other.go           # Unsupported-platform stub
        conntrack.go               # Conntrack table
        flow.go                    # Flow keys, SNAT/DNAT, checksums
      rawsocket_linux.go           # Legacy raw exit (kept for reference)
      rawsocket_{darwin,windows}.go  # Stubs
      windivert/                   # WinDivert backend (present, not wired to L3 yet)
    socks5/                        # SOCKS5 server (client fallback)
    network/                       # Checksums, packet parsing
    netguard/                      # SSRF denylist for the exit node
    exitmgr/                       # --role=exit-panel: multi-client registry (config, store, transport wiring)
    panel/                         # --role=exit-panel: web UI + HTTP API over exitmgr.Manager
    telegrambot/                   # --role=exit-panel: optional Telegram admin bot over exitmgr.Manager
    clientqr/                      # Client -> Android Tunnel QR payload, shared by panel and telegrambot
    yandexdisk/                    # Official Yandex Disk REST API client: creates+publishes --url documents
    utils/                         # Logging
  ios-app/                         # SwiftUI iOS client (XcodeGen)
  scripts/
    build_ios.sh                   # Build iOS static library (liboflux.a)
    build_ios_app.sh               # Build + archive + export iOS app IPA
    build_android.sh               # Build Android client binary
    cleanup-utun.sh                # Remove leftover utun routes (macOS)
  deploy/
    update.sh                      # Self-service VPS updater, automatic rollback
    menu.sh                        # Interactive ops console (openflux-ctl)
  docker/
    entrypoint.sh
  docs/                            # Design notes, review write-ups
```

## Build

```
go mod tidy
go build -o openflux ./cmd/openflux
```

Cross-build for the exit node (Linux amd64), stripped:

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -trimpath -o openflux-linux ./cmd/openflux
```

## Usage

### Exit node - L3 (Linux, root)

```
sudo ./openflux --role=exit --mode=l3 \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL"
```

Requires root / CAP_NET_RAW. Install the iptables rule (see
[l3 and kernel RSTs](#l3-and-kernel-rsts)).

### Exit node - L4 (any OS, no root)

```
./openflux --role=exit --mode=l4 \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL"
```

Fallback for platforms where `l3` is unavailable (Windows without WinDivert,
macOS, non-root Linux). Slower than `l3` (double TCP termination).

### Exit node - multi-client admin panel

```
./openflux --role=exit-panel \
    --panel-addr=127.0.0.1:8088 \
    --panel-user=admin --panel-pass=CHANGE_ME \
    --panel-data=./clients.json --panel-key-file=./panel.key
```

Runs many clients in one process, each with its own transport (its own
document/room) and its own independent `l4` tunnel — one client's traffic
never crosses into another's. Clients are managed live through a local,
login-gated web UI at `--panel-addr` (add, edit, remove, see status/traffic,
copy the panel's public key) with no restart needed; registrations persist
to `--panel-data` and reload on the next start. `--panel-addr` should stay
bound to `127.0.0.1`; reach it remotely over an SSH tunnel
(`ssh -L 8088:127.0.0.1:8088 user@host`) rather than exposing it directly.
`l3` isn't offered here — see the plain `--role=exit` above if you need raw
SNAT/DNAT.

A cupsonline client is a special case: the exit generates its own rooms, so
the UI has no URL field for it — the generated room list (the value a client
needs as its own `--url`) shows up on that client's card once its transport
starts.

`--panel-key-file` is the panel's own Noise static key, created on first run
and printed at startup; every client shares this same identity and connects
with `--peer-key=<that public key>`. Adding a client in the UI accepts an
optional PSK file to close just that one client to anyone without the secret.

Every client card has a **QR** button: scan it with the Android app's "+"
button and it imports and configures that client automatically (transport,
url/token, the panel's public key, and its PSK if any) -- no manual copying.

#### Generating Yandex.Docs documents (optional)

```
./openflux --role=exit-panel \
    --panel-addr=127.0.0.1:8088 --panel-user=admin --panel-pass=CHANGE_ME \
    --yandex-token-file=./yandex.token
```

`--yandex-token-file` just says where the token lives -- the file doesn't
need to exist yet. Passing it adds a card below the panel's public key to
set, replace, or clear the token from the UI itself, with no restart needed
either way. Once a token is set, the url field for **`vyandex`** clients in
"Add client" gets a **Сгенерировать** button: it uploads a blank `.docx` to
that Yandex account's Disk, publishes it with edit access for anyone with
the link, and fills in its public link -- no more making one by hand and
copying the share URL. Click it again to add another document to the same
client (multi-stream).

This only works for `vyandex`, not plain `yandex`: a file uploaded through
the Disk API always opens through Yandex's WOPI/"Volga" online-office
viewer, which is exactly the protocol `--transport=vyandex` speaks --
`--transport=yandex` needs a document made *natively* in the Yandex Docs
UI (a different, non-WOPI editor session with no equivalent public API), so
the button doesn't appear for it.

Get a token from [oauth.yandex.ru](https://oauth.yandex.ru/client/new):
create an app with the "Yandex.Disk REST API" permission (write access),
then open `https://oauth.yandex.ru/authorize?response_type=token&client_id=<your client id>`,
approve it, and paste the `access_token` from the resulting page into the
panel's card. This is the same official Disk API real Yandex apps use --
unrelated to how `transport/yandex`/`vyandex` themselves talk to a document
(an unauthenticated scrape of the public editor page), so the token is only
ever used to create+publish documents, never to move traffic.

#### Telegram admin bot (optional)

```
./openflux --role=exit-panel \
    --panel-addr=127.0.0.1:8088 --panel-user=admin --panel-pass=CHANGE_ME \
    --telegram-bot-token=<token from @BotFather> \
    --telegram-admin-ids=<your Telegram user id>[,<more ids>]
```

Runs a Telegram bot in the same process, wired directly to the same client
manager as the web UI — full parity (`/list`, `/status <id>`, `/key`,
`/add`, `/edit <id>`, `/remove <id>`), so the panel can be operated from a
phone without an SSH tunnel. `/status <id>` also sends that client's QR
code as a photo, same as the panel's QR button, for one-scan import into
the Android app. Only the whitelisted `--telegram-admin-ids`
get a response; everyone else is silently ignored. Get a token from
[@BotFather](https://t.me/BotFather) and your user id from
[@userinfobot](https://t.me/userinfobot).

### Updating a deployed exit node

Every tagged release publishes a prebuilt `openflux-linux-amd64` (with
`--version` baked in) as a GitHub Release asset — see
[`.github/workflows/release.yml`](.github/workflows/release.yml).
[`deploy/update.sh`](deploy/update.sh) downloads the latest one, swaps it
into place, and restarts the systemd service, backing up the current binary
first and rolling back automatically if the new one doesn't come up
healthy:

```
cd /root/OpenFlux && ./deploy/update.sh
```

Run it manually whenever you want to update — nothing on the box checks
for updates on its own.

### Management console (like 3x-ui's menu)

[`deploy/menu.sh`](deploy/menu.sh) is an interactive, root-only console for
a `--role=exit-panel` box — update, list/add/remove clients, and
enable/reconfigure/disable the Telegram bot, all from one command instead
of curl+jq against the panel API or hand-editing the systemd unit:

```
cd /root/OpenFlux && sudo ./deploy/menu.sh

# or, symlinked once for a short command from anywhere:
sudo ln -sf "$(pwd)/deploy/menu.sh" /usr/local/bin/openflux-ctl
sudo openflux-ctl
```

First run walks through setup (panel address/user/pass, key/data paths),
auto-detecting them from an existing systemd override if there is one.
State lives in `/root/.openflux-ctl.env` (root-only, `0600`) — same trust
level as the systemd unit, which already carries the panel password in
plaintext. Every change to the Telegram bot's settings (or anything else
in the menu that touches the unit) regenerates the *entire* `ExecStart`
from that state and restarts the service, so there's never a half-applied
hand-edit to get wrong — the exact mistake that's easy to make editing the
override in `nano` by hand.

### Client - macOS utun (default on macOS)

```
sudo ./openflux --role=client --inbound=tun \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL"
```

Creates a utun interface, installs bypass routes for the transport, waits for
the transport to connect, then takes the default route. No SOCKS5.
Requires sudo. All traffic except the transport goes through the tunnel.

### Client - SOCKS5 (all platforms, fallback)

```
./openflux --role=client --inbound=socks5 \
    --transport=yandex \
    --url="YOUR_YANDEX_DOC_URL" \
    --socks5=:1080
```

Point your browser / app at `127.0.0.1:1080` as a SOCKS5 proxy. This is the
default inbound on non-macOS platforms.

### Codec selection

By default the transport uses the batched + zstd codec
(`transport/batched.go` + `transport/framing.go`). For the old per-packet
LZ4 codec, pass `--codec=legacy`:

```
./openflux --role=client --codec=legacy ...
```

**Important:** the batched wire format is NOT compatible with the legacy LZ4
format. Client and exit node must both use the same codec (both new, or both
`--codec=legacy`).

### Encryption (optional)

The encrypted transport is a Noise `NKpsk0` handshake (X25519, AES-256-GCM,
SHA-256) between the client and the exit node, carried over the same channel
as the data. The exit node owns a static key pair; the client only needs its
public key. Every handshake makes fresh session keys and the client
re-handshakes every 2 minutes on WireGuard's schedule while the old session
keeps flowing, so a key that leaks later does not expose recorded traffic.
Data frames carry a 64-bit counter that is the AEAD nonce and feeds a sliding
anti-replay window.

**Exit node:** pick a file for the static key. It is created on the first run
and the public key is printed at startup:

```
./openflux --role=exit ... --exit-key-file=/etc/openflux/exit.key
```
```
=== EXIT PUBLIC KEY (generated and saved to /etc/openflux/exit.key) ===
<base64 key>
Start clients with --peer-key=<base64 key>
```

**Client:** pass that public key:

```
./openflux --role=client ... --peer-key=<base64 key>
```

**Closed node (optional):** a shared secret of 16+ characters in a file on
both sides is mixed into the handshake as a pre-shared key. The exit node then
silently ignores clients that do not have it:

```
./openflux --role=exit   ... --exit-key-file=exit.key --psk-file=secret.txt
./openflux --role=client ... --peer-key=<base64 key> --psk-file=secret.txt
```

Notes:
- The encrypted layer sits under the codec: one AEAD covers a whole batch and
  compression keeps working. Overhead is 33 bytes per batch.
- One client per document. A second client handshaking on the same document
  takes it over (without encryption it would corrupt the traffic instead).
  Give every client its own document.
- Unset means unencrypted, unchanged behavior. The old `--encryption-key-file`
  is accepted as an alias of `--psk-file` but no longer turns encryption on by
  itself, and the v1 wire format is not accepted.

### Multi-stream (several documents)

Pass a comma-separated list to `--url` (`yandex`, `vyandex`) to run one tunnel
over several documents at once. If one document dies, or the relay stops
delivering on it, the tunnel keeps working over the others (issue #50). Panel
clients (`--role=exit-panel`) support this too — same comma-separated value
in the client's URL field, in the web UI or the Telegram bot.

```
# exit node
./openflux --role=exit --mode=l3 \
    --url="https://disk.yandex.ru/i/AAA,https://disk.yandex.ru/i/BBB" \
    --multistream-status=10s

# client: the same documents, in any order
./openflux --role=client \
    --url="https://disk.yandex.ru/i/BBB,https://disk.yandex.ru/i/AAA" \
    --multistream-status=10s
```

How it works:

- Every document is a complete stream of its own (transport, codec,
  encryption), so each one carries the single-document wire format.
- Each TCP connection is pinned to one document; different connections spread
  over the documents. Spreading the packets of one connection over documents
  would reorder them and collapse its throughput.
- A document is preferred while the peer's keepalives arrive on it. A
  disconnected document stops getting traffic immediately; so does one whose
  participant list shows nobody but us (the peer left). One that is connected
  but silent for another reason (e.g. the peer landed on another document
  backend) is dropped after 25s.
- A connection's packets are sent over the document its packets last arrived
  on. When one side moves a connection to another document, the other side
  follows at once instead of sending its ACKs into the lost document.
- A single URL keeps the exact single-document behavior.

`--multistream-status` logs one line per interval:

```
[MULTI] connected=2/2 peer=1/2 s0[AAA]=UP(peer=3s,rx=812,tx=790,rc=0) s1[BBB]=NOPEER(peer=41s,rx=15,tx=9,rc=2)
```

`UP`: connected and the peer was heard from recently; `NOPEER`: connected but
the peer is silent; `DOWN`: not connected. On iOS, enter the comma-separated
list in the URL field.

### Benchmarks

Measure raw goodput through the transport, without touching the host network:

```
# Sender: push 100 MB
./openflux --role=bench-send --bench-bytes=100 --transport=yandex --url="..."

# Receiver: measure goodput
./openflux --role=bench-sink --transport=yandex --url="..."
```

### Traffic telemetry

Machine-readable traffic on a fixed interval, straight from the transport's
own byte counters (not the OS's per-app network stats, which count every
app's VPN traffic together):

```
./openflux --role=client --traffic-stats=1s ...
```

```
[TRAFFIC] connected=true tx=182933 rx=1044021
```

`connected` reflects the real state — for an encrypted transport, that means
a confirmed Noise session, not just a live carrier connection underneath it.

### Other transports

```
# Yandex Volga (HTTP relay + WS)
./openflux --role=exit --mode=l3 --transport=vyandex --url="..." --debug

# MAX / OneMe (WebRTC DataChannel)
./openflux --role=exit --mode=l3 --transport=oneme \
    --maxToken="..." --maxUid="..." --debug

# Cups.online (Centrifugo rooms)
./openflux --role=exit --mode=l3 --transport=cupsonline --debug
# prints a base64 room list; pass it to the client via --url

# Mail.ru Docs (WS)
./openflux --role=exit --mode=l3 --transport=mailru \
    --url="YOUR_MAILRU_PUBLIC_LINK" --debug
# accepts either a bare weblink (AbCdEfGh1/IjKlMnOp2) or a full URL
# (https://cloud.mail.ru/public/AbCdEfGh1/IjKlMnOp2)
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--role` | `-r` | `client` | `client` \| `exit` \| `exit-panel` \| `bench-send` \| `bench-sink` |
| `--inbound` | `-i` | (platform) | `tun` (macOS) \| `socks5` |
| `--transport` | `-t` | `yandex` | `yandex` \| `vyandex` \| `oneme` \| `cupsonline` \| `mailru` |
| `--mode` | `-m` | `l3` | Exit-node mode: `l3` \| `l4` |
| `--codec` | `-c` | `batched` | `batched` \| `legacy` |
| `--url` | `-u` | `http://#` | Document URL; comma-separated list for multi-stream |
| `--multistream-status` | | `0` | Log per-document state on this interval (e.g. `10s`) |
| `--traffic-stats` | | `0` | Log machine-readable transport traffic on this interval (e.g. `1s`) |
| `--socks5` | `-s` | `127.0.0.1:1080` | SOCKS5 listen address (loopback) |
| `--upstream-proxy` | | | Upstream SOCKS5 proxy for exit node (forces l4, auto-detected from -s/--socks5) |
| `--local-ip` | `-l` | (auto) | Egress IP for l3 SNAT / RST filter |
| `--debug` | `-d` | `false` | Verbose per-packet logging |
| `--exit-key-file` | | | Exit: static key file for the encrypted transport (created on first run) |
| `--peer-key` | | | Client: the exit node's public key; turns encryption on |
| `--psk-file` | | | Both, optional: shared secret file (16+ chars) that closes the node |
| `--allow-plaintext` | | `false` | Run without encryption (UNSAFE; encryption is required otherwise) |
| `--allow-private` | | `false` | Exit: allow private/loopback/link-local and cloud-metadata destinations |
| `--maxToken` | | | MAX auth token (`--transport=oneme`) |
| `--maxUid` | | | MAX user id (`--transport=oneme`) |
| `--panel-addr` | | `127.0.0.1:8088` | `--role=exit-panel` bind address |
| `--panel-user` / `--panel-pass` | | | `--role=exit-panel` admin login (required) |
| `--panel-data` | | `openflux-clients.json` | `--role=exit-panel` persisted client registry |
| `--panel-key-file` | | `openflux-panel.key` | `--role=exit-panel` Noise static key, shared by every client |
| `--telegram-bot-token` | | | `--role=exit-panel` optional: run a Telegram admin bot (token from @BotFather) |
| `--telegram-admin-ids` | | | `--role=exit-panel` comma-separated Telegram user ids allowed to use the bot |
| `--bench-bytes` | | `0` | MB to push (`--role=bench-send`) |
| `--bench-compressible` | | `false` | Use compressible payload (bench) |

Deprecated (kept for one release, mapped automatically to the new flags):
`--client`, `--exit-node`, `--tun`, `--socks5-mode`, `--legacy`,
`--bench-send`, `--bench-sink`.

## Implementing custom transports

Implement the `Transport` interface from `transport/transport.go` and register
your transport in the `main.go` switch block (see `transport/mailru/` for a
complete example). The batched codec (`BatchedTransport`) wraps any transport,
so a new backend gets batching for free.

## TODO

- **L3 exit on Windows and macOS.** The L3 exit currently works on Linux
  (SOCK_RAW) only; Windows and macOS use `--mode=l4`. The `tunnel/windivert/`
  package (Windows) exists but is not wired to the L3 forwarder yet. A native
  macOS L3 exit is not implemented.
- **Run the exit node (QEMU).**

## License

GNU General Public License v3.0 or later. See LICENSE for the full text.

Third-party licenses are listed in [NOTICE](NOTICE).
