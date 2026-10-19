# Exit panel

**English** | [Русский](exit-panel.ru.md)

## Description

`--role=exit-panel` runs many clients on one server behind a small,
login-gated web panel: add, edit and remove clients, see their status and
logs, and get the `openflux://` link and QR code the OpenFlux apps scan. An
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
- **vyandex** (Yandex Docs) works only from a signed-in account: Yandex no
  longer lets anyone else into the editor. A document is given as its address
  while it is open in Docs (`https://docs.yandex.ru/edit/d/…`); a share link
  (`/i/`, `/d/`) opens a preview without an editor. The sign-in is set once
  for the whole panel: the cookies of a Yandex account of its own (a card in
  the web UI, or `openflux-ctl` item 13). Should Yandex also ask for a
  captcha, the app passes it through the tunnel over a carrier that works.
- A carrier without Yandex, such as **Cups.online** or **Avito**, needs no
  sign-in and gets past DPI: the phone talks to a Russian service, not to
  the VPS. From Russian networks a direct port to the VPS is often frozen
  after the first bytes, so it is no use as the only carrier. The panel
  refuses a direct port another program on the server already listens on.
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
