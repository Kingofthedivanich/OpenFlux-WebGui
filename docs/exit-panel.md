# Exit panel

**English** | [Русский](exit-panel.ru.md)

## Description

`--role=exit-panel` runs many clients on one server behind a small,
login-gated web panel: add, edit and remove clients, see their status and
logs, and get the `openflux://` link and QR code the OpenFlux apps scan. Given
a Yandex Disk OAuth token, it creates Yandex Volga documents itself. An
optional Telegram bot does the same, and on a VPS the `openflux-ctl` console
([`deploy/menu.sh`](../deploy/menu.sh)) manages it all.

## How it works

- Every client is a channel of its own: `<panel-data>/clients/<id>/` holds a
  `node.conf` in `node-install.sh`'s format, a generated AES-256-GCM key and
  a cookie jar, served by its own `openflux --config … --share` process — the
  core's ordinary exit node, so the stock OpenFlux apps connect to it.
- A client can have several transports (Volga, Yandex Docs, Mail.ru,
  Telemost, Boards, Avito, Cups.online) plus a backup direct TCP port; the
  client holds them in one session and fails over by priority when one
  service is blocked.
- When Yandex asks the server for a captcha (it does for datacenter
  addresses), the app passes it through the tunnel — over a carrier that
  already works. Give a client a direct port and open it in the firewall.
  Volga documents are given as editor links (`disk.yandex.ru/edit/d/…`): the
  `…/d/…` preview page is useless to vyandex.
- The key and encryption context survive edits; rooms the exit made for
  Cups.online and Avito are written back into the config, so a link handed
  out keeps working across restarts and edits.
- A channel whose process dies is restarted with a backoff. Status comes from
  the core's IPC: client connected, traffic, active carrier, a pending
  captcha. On Linux the channels stop with the panel.
- The exit refuses its own loopback, private networks and cloud metadata
  (169.254.169.254); `--allow-private` lifts it.

## Install guide

```bash
go build -o openflux .
./openflux --role=exit-panel --panel-user=admin --panel-pass=CHANGE_ME
```

The panel listens on `127.0.0.1:8088`; reach it over an SSH tunnel
(`ssh -L 8088:127.0.0.1:8088 root@host`). Links carry client keys: do not
expose the panel.

| Flag | Default | |
|---|---|---|
| `--panel-addr` | `127.0.0.1:8088` | panel address |
| `--panel-user`, `--panel-pass` | — | login, required |
| `--panel-data` | `openflux-panel` | clients, keys, channels |
| `--share-host` | first public IPv4 | address in links for the direct port |
| `--yandex-token-file` | — | Disk OAuth token file (`disk.write`) to create Volga documents |
| `--allow-private` | off | let clients reach the host's private networks |
| `--telegram-bot-token-file` | — | file with the bot token from @BotFather |
| `--telegram-admin-ids` | — | comma-separated Telegram user ids the bot answers |
| `--debug` | `0` | channels' log level |

On a VPS, make an `openflux-exit` systemd service whose `WorkingDirectory`
holds the binary, then:

```bash
sudo ln -sf "$(pwd)/deploy/menu.sh" /usr/local/bin/openflux-ctl
sudo openflux-ctl
```

It writes the ExecStart itself, updates (`deploy/update.sh` checks
`SHA256SUMS.txt` and rolls back on failure), adds and removes clients, shows
a client's link and QR code (with `qrencode`) and logs, and turns the
Telegram bot on and off (items 10–12). The bot: `/list`, `/status <id>`
(status, link and QR code), `/logs <id>`, `/add`, `/edit <id>`, `/remove <id>`.
