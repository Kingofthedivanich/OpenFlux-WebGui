#!/bin/bash
# Interactive management console for a --role=exit-panel deployment, in the
# style of 3x-ui's menu: update, manage clients, and enable/configure/disable
# the Telegram bot -- all without SSH-tunneling into the web panel or
# hand-editing the systemd unit (see deploy/update.sh's sibling doc comment
# for the update path this reuses).
#
# Run from the directory containing this script's repo checkout, or
# symlink it somewhere on $PATH, e.g.:
#   sudo ln -sf "$(pwd)/deploy/menu.sh" /usr/local/bin/openflux-ctl
#
# State lives in $CONFIG_FILE (root-only, same trust level as the systemd
# unit that already carries the panel password in plaintext). Every write
# to the systemd override regenerates the whole ExecStart from that state,
# so there is never a partial hand-edit to get wrong.
#
# No -e: this is a long-running interactive loop, not a one-shot script
# like update.sh -- a single unexpected failure (a systemctl hiccup, a
# network blip talking to the panel) should show an error and return to
# the menu, not kill the whole session. Functions that need a failure to
# stop the rest of their own work use explicit `|| return 1`.
set -uo pipefail

# readlink -f resolves the full symlink chain: BASH_SOURCE[0] alone would
# still be the symlink path (e.g. /usr/local/bin/openflux-ctl) when run via
# the symlink this script's own header suggests creating, pointing
# SCRIPT_DIR at /usr instead of the actual repo checkout.
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
CONFIG_FILE="${OPENFLUX_CTL_CONFIG:-/root/.openflux-ctl.env}"
COOKIE_JAR="$(mktemp)"
trap 'rm -f "$COOKIE_JAR"' EXIT

# Defaults, overridden by $CONFIG_FILE once it exists.
SERVICE="openflux-exit"
BIN_PATH="$SCRIPT_DIR/openflux"
PANEL_ADDR="127.0.0.1:8088"
PANEL_USER=""
PANEL_PASS=""
# Match main.go's own flag defaults exactly (openflux-clients.json /
# openflux-panel.key) -- these are the paths a deployment gets if
# --panel-data/--panel-key-file were never explicitly passed. A mismatch
# here isn't cosmetic: detect_from_override only fills these vars in when
# the flag is actually present in ExecStart, so an implicit-default
# deployment falls through to whatever's hardcoded here. Get it wrong and
# apply_override switches to a *different* key/registry file -- for
# --panel-key-file specifically, that means a brand new Noise keypair,
# silently orphaning every client's peer-key.
PANEL_DATA="$SCRIPT_DIR/openflux-clients.json"
PANEL_KEY_FILE="$SCRIPT_DIR/openflux-panel.key"
TELEGRAM_BOT_TOKEN=""
TELEGRAM_ADMIN_IDS=""
YANDEX_TOKEN_FILE=""

# ---------- small helpers ----------

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        echo "error: run this as root (systemctl edit, journalctl -u need it)." >&2
        exit 1
    fi
}

ensure_jq() {
    if command -v jq >/dev/null 2>&1; then return; fi
    echo "jq is required to parse the panel API's responses; installing..."
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq jq
    else
        echo "error: jq not found and no apt-get to install it. Install jq manually and re-run." >&2
        exit 1
    fi
}

pause() { read -rp "Press Enter to continue..." _; }

# Strips one leading/trailing '"' if present. apply_override wraps every
# flag value in double quotes (systemd.syntax(7)); grep -oP '\S+' has no
# notion of quoting, so without this every value detect_from_override reads
# back out would carry a stray trailing '"' (and the binary path a leading
# one too).
strip_quotes() {
    local v="$1"
    v="${v%\"}"
    v="${v#\"}"
    printf '%s' "$v"
}

load_config() {
    if [ -f "$CONFIG_FILE" ]; then
        # shellcheck source=/dev/null
        source "$CONFIG_FILE"
    fi
}

save_config() {
    umask 077
    cat >"$CONFIG_FILE" <<EOF
SERVICE=$(printf '%q' "$SERVICE")
BIN_PATH=$(printf '%q' "$BIN_PATH")
PANEL_ADDR=$(printf '%q' "$PANEL_ADDR")
PANEL_USER=$(printf '%q' "$PANEL_USER")
PANEL_PASS=$(printf '%q' "$PANEL_PASS")
PANEL_DATA=$(printf '%q' "$PANEL_DATA")
PANEL_KEY_FILE=$(printf '%q' "$PANEL_KEY_FILE")
TELEGRAM_BOT_TOKEN=$(printf '%q' "$TELEGRAM_BOT_TOKEN")
TELEGRAM_ADMIN_IDS=$(printf '%q' "$TELEGRAM_ADMIN_IDS")
YANDEX_TOKEN_FILE=$(printf '%q' "$YANDEX_TOKEN_FILE")
EOF
    chmod 600 "$CONFIG_FILE"
}

# Pulls current --flag=value settings out of the systemd override's
# ExecStart, if one already exists, so first-time setup can offer them as
# defaults instead of asking the operator to retype a working config.
detect_from_override() {
    local override="/etc/systemd/system/${SERVICE}.service.d/override.conf"
    [ -f "$override" ] || return 1
    local line
    line="$(grep -m1 '^ExecStart=/' "$override" 2>/dev/null || true)"
    [ -n "$line" ] || return 1

    local val
    val="$(grep -oP '(?<=^ExecStart=)\S+' <<<"$line" || true)"; [ -n "$val" ] && BIN_PATH="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-addr=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_ADDR="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-user=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_USER="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-pass=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_PASS="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-data=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_DATA="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-key-file=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_KEY_FILE="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--telegram-bot-token=)\S+' <<<"$line" || true)"; [ -n "$val" ] && TELEGRAM_BOT_TOKEN="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--telegram-admin-ids=)\S+' <<<"$line" || true)"; [ -n "$val" ] && TELEGRAM_ADMIN_IDS="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--yandex-token-file=)\S+' <<<"$line" || true)"; [ -n "$val" ] && YANDEX_TOKEN_FILE="$(strip_quotes "$val")"
    return 0
}

prompt_default() {
    # prompt_default "Question" "$CURRENT_VALUE" -> echoes the answer
    local question="$1" current="$2" answer
    read -rp "$question [$current]: " answer
    echo "${answer:-$current}"
}

first_time_setup() {
    echo "No config at $CONFIG_FILE yet -- let's set it up."
    detect_from_override && echo "(found an existing systemd override; using it as defaults)"
    echo
    SERVICE="$(prompt_default "systemd service name" "$SERVICE")"
    BIN_PATH="$(prompt_default "path to the openflux binary" "$BIN_PATH")"
    PANEL_ADDR="$(prompt_default "panel bind address" "$PANEL_ADDR")"
    PANEL_USER="$(prompt_default "panel admin username" "$PANEL_USER")"
    while [ -z "$PANEL_USER" ]; do PANEL_USER="$(prompt_default "panel admin username (required)" "")"; done
    PANEL_PASS="$(prompt_default "panel admin password" "$PANEL_PASS")"
    while [ -z "$PANEL_PASS" ]; do PANEL_PASS="$(prompt_default "panel admin password (required)" "")"; done
    PANEL_DATA="$(prompt_default "clients registry path" "$PANEL_DATA")"
    PANEL_KEY_FILE="$(prompt_default "panel Noise key file path" "$PANEL_KEY_FILE")"
    YANDEX_TOKEN_FILE="$(prompt_default "yandex OAuth token file path (optional -- enables the panel's document-generation card; empty disables it; the file need not exist yet)" "$YANDEX_TOKEN_FILE")"
    save_config
    echo "Saved to $CONFIG_FILE. Applying..."
    apply_override
    pause
}

# ---------- systemd override ----------

# systemd's ExecStart= splits like a simplified shell command line -- a
# password or token containing a space would otherwise silently turn into
# two arguments. Wraps flag=value in double quotes, escaping \ and "
# (systemd.syntax(7)'s supported quoting), so any value is safe verbatim.
sdarg() {
    local combined="${1}=${2}"
    combined="${combined//\\/\\\\}"
    combined="${combined//\"/\\\"}"
    printf '"%s"' "$combined"
}

# Same escaping, no "flag=" prefix -- for the executable path itself.
sdpath() {
    local v="$1"
    v="${v//\\/\\\\}"
    v="${v//\"/\\\"}"
    printf '"%s"' "$v"
}

# Regenerates the whole override from current state and restarts the
# service, so toggling the bot on/off (or any other setting) can never
# leave a stale half-edited ExecStart -- the failure mode that motivated
# this script in the first place.
apply_override() {
    local dir="/etc/systemd/system/${SERVICE}.service.d"
    mkdir -p "$dir"

    local exec_start="$(sdpath "$BIN_PATH") --role=exit-panel"
    exec_start="$exec_start $(sdarg --panel-addr "$PANEL_ADDR")"
    exec_start="$exec_start $(sdarg --panel-user "$PANEL_USER")"
    exec_start="$exec_start $(sdarg --panel-pass "$PANEL_PASS")"
    exec_start="$exec_start $(sdarg --panel-data "$PANEL_DATA")"
    exec_start="$exec_start $(sdarg --panel-key-file "$PANEL_KEY_FILE")"
    if [ -n "$TELEGRAM_BOT_TOKEN" ]; then
        exec_start="$exec_start $(sdarg --telegram-bot-token "$TELEGRAM_BOT_TOKEN")"
        exec_start="$exec_start $(sdarg --telegram-admin-ids "$TELEGRAM_ADMIN_IDS")"
    fi
    if [ -n "$YANDEX_TOKEN_FILE" ]; then
        exec_start="$exec_start $(sdarg --yandex-token-file "$YANDEX_TOKEN_FILE")"
    fi

    # A missing key file isn't wrong on a genuinely first-ever start (the
    # binary creates one), but on a service that's already been running
    # it almost always means PANEL_KEY_FILE just got pointed at the wrong
    # path -- which silently mints a brand new Noise keypair and orphans
    # every existing client's --peer-key. Confirm before restarting into
    # that, rather than discovering it from "the app doesn't get internet".
    if [ ! -f "$PANEL_KEY_FILE" ] && systemctl is-active --quiet "$SERVICE" 2>/dev/null; then
        echo "warning: $SERVICE is already running, but $PANEL_KEY_FILE doesn't exist." >&2
        echo "Restarting with this path will generate a NEW key and disconnect every existing client." >&2
        read -rp "Continue anyway? Type 'yes' to confirm: " confirm_key
        if [ "$confirm_key" != "yes" ]; then
            echo "cancelled -- not restarting."
            return 1
        fi
    fi

    cat >"$dir/override.conf" <<EOF
[Service]
ExecStart=
ExecStart=$exec_start
EOF

    systemctl daemon-reload
    systemctl restart "$SERVICE"
    sleep 1
    if systemctl is-active --quiet "$SERVICE"; then
        echo "OK: $SERVICE is running."
        # Belt-and-suspenders: catch a new key even if the pre-check above
        # missed it for some other reason (e.g. a typo'd path that
        # happens to exist but isn't the real key).
        if journalctl -u "$SERVICE" --no-pager -n 5 | grep -qF 'PANEL PUBLIC KEY (generated and saved to'; then
            echo "WARNING: a NEW Noise key was just generated -- every existing client's" >&2
            echo "--peer-key is now stale. Re-check option 6 and re-add/re-scan clients." >&2
        fi
    else
        echo "error: $SERVICE did not come up healthy. Recent logs:" >&2
        journalctl -u "$SERVICE" -n 20 --no-pager >&2
        return 1
    fi
}

# ---------- panel API ----------

panel_login() {
    local code
    # Password goes through jq's $ENV (not --arg) and curl's --data-binary
    # from stdin (not -d) so it never appears in this process's argv, which
    # `ps aux` can show to any local user even though we're root.
    code="$(PANEL_LOGIN_PW="$PANEL_PASS" jq -n --arg u "$PANEL_USER" '{username:$u, password:$ENV.PANEL_LOGIN_PW}' \
        | curl -s -o /dev/null -w '%{http_code}' -c "$COOKIE_JAR" \
              -H 'Content-Type: application/json' --data-binary @- \
              "http://$PANEL_ADDR/api/login")"
    if [ "$code" != "200" ]; then
        echo "error: panel login failed (HTTP $code) -- check panel-user/panel-pass (option 12)." >&2
        return 1
    fi
}

panel_get_clients() {
    local body
    body="$(curl -s -b "$COOKIE_JAR" "http://$PANEL_ADDR/api/clients")"
    if ! jq -e . >/dev/null 2>&1 <<<"$body"; then
        echo "error: unexpected response from the panel at http://$PANEL_ADDR -- is it running?" >&2
        return 1
    fi
    printf '%s' "$body"
}

list_clients() {
    panel_login || return 1
    local body; body="$(panel_get_clients)" || return 1
    local count; count="$(jq 'length' <<<"$body")"
    if [ "$count" -eq 0 ]; then
        echo "No clients registered."
        return 0
    fi
    jq -r '.[] | "\(.config.id)\t\(.config.name)\t\(.config.transport)\t\(.status)\(.error // "" | if . != "" then " (" + . + ")" else "" end)"' <<<"$body" \
        | column -t -s $'\t' -N ID,NAME,TRANSPORT,STATUS 2>/dev/null \
        || jq -r '.[] | "\(.config.id)  \(.config.name)  \(.config.transport)  \(.status)"' <<<"$body"
}

show_client() {
    panel_login || return 1
    local id="$1"
    local body; body="$(panel_get_clients)" || return 1
    local entry; entry="$(jq --arg id "$id" '.[] | select(.config.id == $id)' <<<"$body")"
    if [ -z "$entry" ]; then
        echo "No client with id '$id'."
        return 1
    fi
    echo "$entry" | jq .
}

add_client() {
    panel_login || return 1
    echo "Transport: 1=yandex 2=vyandex 3=oneme (MAX) 4=cupsonline 5=mailru"
    read -rp "Choice [1-5]: " tchoice
    case "$tchoice" in
        1) transport=yandex ;;
        2) transport=vyandex ;;
        3) transport=oneme ;;
        4) transport=cupsonline ;;
        5) transport=mailru ;;
        *) echo "invalid choice"; return 1 ;;
    esac

    read -rp "Name: " name
    local url="" maxtoken="" maxuid=""
    if [ "$transport" = "oneme" ]; then
        read -rp "MAX Access Token: " maxtoken
        read -rp "MAX User ID: " maxuid
    elif [ "$transport" != "cupsonline" ]; then
        read -rp "Document URL (comma-separate for multi-stream): " url
    else
        echo "(cupsonline generates its own rooms; no URL needed)"
    fi
    read -rp "PSK file path (optional, Enter to skip): " pskfile

    # MAX tokens are credentials too; keep them out of every subprocess's
    # argv the same way panel_login keeps the panel password out of it.
    local payload
    payload="$(NAME="$name" TRANSPORT="$transport" URL="$url" MAXTOKEN="$maxtoken" MAXUID="$maxuid" PSKFILE="$pskfile" \
        jq -n '{name:$ENV.NAME, transport:$ENV.TRANSPORT, url:$ENV.URL, max_token:$ENV.MAXTOKEN, max_uid:$ENV.MAXUID, psk_file:$ENV.PSKFILE}')"

    local resp code
    resp="$(mktemp)"
    code="$(printf '%s' "$payload" | curl -s -o "$resp" -w '%{http_code}' -b "$COOKIE_JAR" -X POST \
        -H 'Content-Type: application/json' --data-binary @- "http://$PANEL_ADDR/api/clients")"
    if [ "$code" = "201" ]; then
        echo "Added: $(jq -r .id <"$resp")"
    else
        echo "error ($code): $(jq -r '.error // .' <"$resp" 2>/dev/null || cat "$resp")" >&2
    fi
    rm -f "$resp"
}

remove_client() {
    panel_login || return 1
    read -rp "Client id to remove: " id
    [ -n "$id" ] || return 0
    read -rp "Delete '$id'? Type 'yes' to confirm: " confirm
    [ "$confirm" = "yes" ] || { echo "cancelled"; return 0; }
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X DELETE "http://$PANEL_ADDR/api/clients/$id")"
    if [ "$code" = "200" ]; then
        echo "Removed."
    else
        echo "error: delete failed (HTTP $code)" >&2
    fi
}

panel_key() {
    panel_login || return 1
    curl -s -b "$COOKIE_JAR" "http://$PANEL_ADDR/api/panel-key" | jq -r .public_key
}

# ---------- Telegram bot ----------

bot_configure() {
    echo "Get a token from @BotFather (https://t.me/BotFather) and your"
    echo "numeric id from @userinfobot (https://t.me/userinfobot)."
    read -rp "Bot token: " token
    read -rp "Admin Telegram id(s), comma-separated: " ids
    if [ -z "$token" ] || [ -z "$ids" ]; then
        echo "both a token and at least one admin id are required."
        return 1
    fi
    TELEGRAM_BOT_TOKEN="$token"
    TELEGRAM_ADMIN_IDS="$ids"
    save_config
    apply_override
}

bot_disable() {
    TELEGRAM_BOT_TOKEN=""
    TELEGRAM_ADMIN_IDS=""
    save_config
    apply_override
}

bot_status() {
    if [ -z "$TELEGRAM_BOT_TOKEN" ]; then
        echo "Telegram bot: not configured."
        return 0
    fi
    echo "Telegram bot: configured, admin id(s): $TELEGRAM_ADMIN_IDS"
    journalctl -u "$SERVICE" --no-pager | grep -F '[TGBOT]' | tail -5 || echo "(no [TGBOT] log lines yet)"
}

# ---------- update / service ----------

do_update() {
    ( cd "$SCRIPT_DIR" && OPENFLUX_SERVICE="$SERVICE" OPENFLUX_BIN="$BIN_PATH" ./deploy/update.sh )
}

svc_status() {
    systemctl status "$SERVICE" --no-pager || true
}

svc_logs() {
    journalctl -u "$SERVICE" -n 40 --no-pager
}

# ---------- menu ----------

menu() {
    cat <<EOF

========================================
  OpenFlux exit-panel management
  service: $SERVICE   panel: $PANEL_ADDR
========================================
 1) Update to latest release
 2) List clients
 3) Add client
 4) Remove client
 5) Show one client's status
 6) Show panel public key (--peer-key)
----------------------------------------
 7) Telegram bot: enable / reconfigure
 8) Telegram bot: disable
 9) Telegram bot: status
----------------------------------------
10) Service status
11) Recent logs
12) Reconfigure this menu (panel address/user/pass/paths)
 0) Exit
========================================
EOF
    read -rp "Choice: " choice
    echo
    case "$choice" in
        1) do_update ;;
        2) list_clients ;;
        3) add_client ;;
        4) remove_client ;;
        5) read -rp "Client id: " cid; show_client "$cid" ;;
        6) panel_key ;;
        7) bot_configure ;;
        8) bot_disable ;;
        9) bot_status ;;
        10) svc_status ;;
        11) svc_logs ;;
        12) first_time_setup ;;
        0) exit 0 ;;
        *) echo "invalid choice" ;;
    esac
    pause
}

# ---------- entry point ----------

require_root
ensure_jq
load_config
if [ -z "$PANEL_USER" ] || [ -z "$PANEL_PASS" ]; then
    first_time_setup
fi
while true; do menu; done
