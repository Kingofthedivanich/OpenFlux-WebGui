#!/bin/bash
# Interactive management console for an --role=exit-panel deployment, in the
# style of 3x-ui's menu: update, add/remove clients, show a client's link and
# QR code, logs -- without SSH-tunnelling into the web panel or hand-editing
# the systemd unit.
#
# Run from the repo checkout, or symlink it onto $PATH:
#   sudo ln -sf "$(pwd)/deploy/menu.sh" /usr/local/bin/openflux-ctl
#
# State lives in $CONFIG_FILE (root-only, the same trust level as the systemd
# unit that carries the panel password). Every change regenerates the whole
# ExecStart from that state, so there is never a half-edited unit.
#
# No -e: this is an interactive loop; one failed call shows an error and
# returns to the menu. Functions that must stop use explicit `|| return 1`.
set -uo pipefail

# readlink -f: run through the openflux-ctl symlink, BASH_SOURCE is the link.
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
CONFIG_FILE="${OPENFLUX_CTL_CONFIG:-/root/.openflux-ctl.env}"
COOKIE_JAR="$(mktemp)"
trap 'rm -f "$COOKIE_JAR"' EXIT

# Defaults, overridden by $CONFIG_FILE once it exists. PANEL_DATA matches the
# core's --panel-data default ("openflux-panel" in the unit's working
# directory): pointing it elsewhere starts the panel with no clients, and
# their keys -- the links handed out -- stay behind in the old directory.
SERVICE="openflux-exit"
BIN_PATH="$SCRIPT_DIR/openflux"
PANEL_ADDR="127.0.0.1:8088"
PANEL_USER=""
PANEL_PASS=""
PANEL_DATA="$SCRIPT_DIR/openflux-panel"
SHARE_HOST=""
YANDEX_TOKEN_FILE=""

TYPES="vyandex yandex mailru telemost boards avito cupsonline"

# ---------- small helpers ----------

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        echo "error: run this as root (systemctl and journalctl need it)." >&2
        exit 1
    fi
}

ensure_jq() {
    command -v jq >/dev/null 2>&1 && return
    echo "jq is needed to read the panel's answers; installing..."
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && apt-get install -y -qq jq
    else
        echo "error: jq not found and no apt-get to install it. Install jq and re-run." >&2
        exit 1
    fi
}

pause() { read -rp "Press Enter to continue..." _; }

# apply_override wraps every value in double quotes; grep -oP '\S+' does not
# know quoting, so strip them when reading values back.
strip_quotes() {
    local v="$1"
    v="${v%\"}"
    v="${v#\"}"
    printf '%s' "$v"
}

load_config() {
    # shellcheck source=/dev/null
    [ -f "$CONFIG_FILE" ] && source "$CONFIG_FILE"
    fix_legacy_panel_data
}

# The old panel kept its clients in a file (openflux-clients.json); this one
# needs a directory. Point at one beside the old file; the old clients are
# not carried over (their Noise keys do not apply to this core).
fix_legacy_panel_data() {
    case "$PANEL_DATA" in
        *.json)
            PANEL_DATA="$(dirname "$PANEL_DATA")/openflux-panel"
            echo "note: the clients path pointed at the old panel's JSON file; using $PANEL_DATA" >&2
            ;;
    esac
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
SHARE_HOST=$(printf '%q' "$SHARE_HOST")
YANDEX_TOKEN_FILE=$(printf '%q' "$YANDEX_TOKEN_FILE")
EOF
    chmod 600 "$CONFIG_FILE"
}

# Reads the current settings out of the systemd override's ExecStart, so a
# first-time setup offers a working config as defaults.
detect_from_override() {
    local override="/etc/systemd/system/${SERVICE}.service.d/override.conf"
    [ -f "$override" ] || return 1
    local line val
    line="$(grep -m1 '^ExecStart=.' "$override" 2>/dev/null || true)"
    [ -n "$line" ] || return 1
    val="$(grep -oP '(?<=^ExecStart=)\S+' <<<"$line" || true)"; [ -n "$val" ] && BIN_PATH="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-addr=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_ADDR="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-user=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_USER="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-pass=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_PASS="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--panel-data=)\S+' <<<"$line" || true)"; [ -n "$val" ] && PANEL_DATA="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--share-host=)\S+' <<<"$line" || true)"; [ -n "$val" ] && SHARE_HOST="$(strip_quotes "$val")"
    val="$(grep -oP '(?<=--yandex-token-file=)\S+' <<<"$line" || true)"; [ -n "$val" ] && YANDEX_TOKEN_FILE="$(strip_quotes "$val")"
    fix_legacy_panel_data
    return 0
}

prompt_default() {
    # prompt_default "Question" "$CURRENT" -> echoes the answer
    local answer
    read -rp "$1 [$2]: " answer
    echo "${answer:-$2}"
}

first_time_setup() {
    echo "No config at $CONFIG_FILE yet -- let's set it up."
    detect_from_override && echo "(found an existing systemd override; using it as defaults)"
    echo
    SERVICE="$(prompt_default "systemd service name" "$SERVICE")"
    BIN_PATH="$(prompt_default "path to the openflux binary" "$BIN_PATH")"
    PANEL_ADDR="$(prompt_default "panel address (keep it on 127.0.0.1)" "$PANEL_ADDR")"
    PANEL_USER="$(prompt_default "panel login" "$PANEL_USER")"
    while [ -z "$PANEL_USER" ]; do PANEL_USER="$(prompt_default "panel login (required)" "")"; done
    PANEL_PASS="$(prompt_default "panel password" "$PANEL_PASS")"
    while [ -z "$PANEL_PASS" ]; do PANEL_PASS="$(prompt_default "panel password (required)" "")"; done
    PANEL_DATA="$(prompt_default "clients directory (keys, channels)" "$PANEL_DATA")"
    SHARE_HOST="$(prompt_default "address clients dial for a direct port (empty: this host's public IPv4)" "$SHARE_HOST")"
    YANDEX_TOKEN_FILE="$(prompt_default "Yandex OAuth token file (optional: lets the panel create vyandex documents; need not exist yet)" "$YANDEX_TOKEN_FILE")"
    save_config
    echo "Saved to $CONFIG_FILE. Applying..."
    apply_override
}

# ---------- systemd override ----------

# ExecStart= splits like a simplified shell line: quote every flag=value,
# escaping \ and " (systemd.syntax(7)), so any value is passed verbatim.
sdarg() {
    local combined="${1}=${2}"
    combined="${combined//\\/\\\\}"
    combined="${combined//\"/\\\"}"
    printf '"%s"' "$combined"
}

sdpath() {
    local v="$1"
    v="${v//\\/\\\\}"
    v="${v//\"/\\\"}"
    printf '"%s"' "$v"
}

# Regenerates the whole override from the saved state and restarts the
# service, so no setting can leave a stale, half-edited ExecStart behind.
apply_override() {
    local dir="/etc/systemd/system/${SERVICE}.service.d"
    mkdir -p "$dir"

    local exec_start
    exec_start="$(sdpath "$BIN_PATH") --role=exit-panel"
    exec_start="$exec_start $(sdarg --panel-addr "$PANEL_ADDR")"
    exec_start="$exec_start $(sdarg --panel-user "$PANEL_USER")"
    exec_start="$exec_start $(sdarg --panel-pass "$PANEL_PASS")"
    exec_start="$exec_start $(sdarg --panel-data "$PANEL_DATA")"
    [ -n "$SHARE_HOST" ] && exec_start="$exec_start $(sdarg --share-host "$SHARE_HOST")"
    [ -n "$YANDEX_TOKEN_FILE" ] && exec_start="$exec_start $(sdarg --yandex-token-file "$YANDEX_TOKEN_FILE")"

    # A clients directory without clients.json on a service that already
    # runs almost always means PANEL_DATA points at the wrong place: the
    # panel would come up empty and every link handed out would die.
    if [ ! -f "$PANEL_DATA/clients.json" ] && systemctl is-active --quiet "$SERVICE" 2>/dev/null; then
        echo "warning: $SERVICE is running, but $PANEL_DATA/clients.json does not exist." >&2
        echo "Restarting with this path starts the panel with no clients." >&2
        read -rp "Continue anyway? Type 'yes' to confirm: " confirm
        if [ "$confirm" != "yes" ]; then
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
    sleep 2
    if systemctl is-active --quiet "$SERVICE"; then
        echo "OK: $SERVICE is running."
    else
        echo "error: $SERVICE did not come up. Recent logs:" >&2
        journalctl -u "$SERVICE" -n 20 --no-pager >&2
        return 1
    fi
}

# ---------- panel API ----------

panel_login() {
    local code
    # The password goes through jq's $ENV and curl's stdin, never argv,
    # which any local user can read in `ps`.
    code="$(PANEL_LOGIN_PW="$PANEL_PASS" jq -n --arg u "$PANEL_USER" '{username:$u, password:$ENV.PANEL_LOGIN_PW}' \
        | curl -s -o /dev/null -w '%{http_code}' -c "$COOKIE_JAR" \
              -H 'Content-Type: application/json' --data-binary @- \
              "http://$PANEL_ADDR/api/login")"
    if [ "$code" != "200" ]; then
        echo "error: panel login failed (HTTP $code) -- is it running? check login/password (option 9)." >&2
        return 1
    fi
}

panel_get() {
    local body
    body="$(curl -s -b "$COOKIE_JAR" "http://$PANEL_ADDR$1")"
    if ! jq -e . >/dev/null 2>&1 <<<"$body"; then
        echo "error: unexpected answer from the panel at http://$PANEL_ADDR$1" >&2
        return 1
    fi
    printf '%s' "$body"
}

list_clients() {
    panel_login || return 1
    local body
    body="$(panel_get /api/clients)" || return 1
    if [ "$(jq 'length' <<<"$body")" -eq 0 ]; then
        echo "No clients."
        return 0
    fi
    jq -r '.[] | [
        .config.id,
        .config.name,
        ([.config.transports[]?.type] + (if (.config.direct_port // 0) > 0 then ["direct:\(.config.direct_port)"] else [] end) | join(",")),
        (.status + (if .connected then ", connected" else "" end) + (if .error then " (" + .error + ")" else "" end))
      ] | @tsv' <<<"$body" \
        | column -t -s $'\t' -N ID,NAME,TRANSPORTS,STATUS 2>/dev/null \
        || jq -r '.[] | "\(.config.id)  \(.config.name)  \(.status)"' <<<"$body"
}

# pick_type prints the chosen type; returns 1 when the operator is done.
pick_type() {
    local -a types
    read -ra types <<<"$TYPES"
    local i n
    for i in "${!types[@]}"; do
        printf '  %d) %s\n' "$((i + 1))" "${types[$i]}" >&2
    done
    read -rp "Transport [1-${#types[@]}, Enter to finish]: " n
    [ -z "$n" ] && return 1
    if ! [[ "$n" =~ ^[0-9]+$ ]] || [ "$n" -lt 1 ] || [ "$n" -gt "${#types[@]}" ]; then
        echo "invalid choice" >&2
        return 2
    fi
    printf '%s' "${types[$((n - 1))]}"
}

add_client() {
    panel_login || return 1
    local name type url port transports='[]' rc
    read -rp "Name: " name
    echo "Add one or more transports: the client fails over between them by priority."
    while true; do
        type="$(pick_type)"
        rc=$?
        [ $rc -eq 1 ] && break
        [ $rc -ne 0 ] && continue
        url=""
        case "$type" in
            cupsonline | avito) echo "($type: the exit makes its own rooms; no link needed)" ;;
            *) read -rp "$type link: " url ;;
        esac
        transports="$(T="$type" U="$url" jq -c '. + [if $ENV.U == "" then {type:$ENV.T} else {type:$ENV.T, url:$ENV.U} end]' <<<"$transports")"
        echo "  added $type"
    done
    read -rp "Direct TCP port (optional backup straight to this host, Enter to skip): " port
    if [ "$transports" = "[]" ] && [ -z "$port" ]; then
        echo "nothing to add: no transports and no direct port."
        return 1
    fi

    local payload resp code
    payload="$(N="$name" P="${port:-0}" jq -n --argjson ts "$transports" '{name:$ENV.N, transports:$ts, direct_port:($ENV.P|tonumber)}')" || return 1
    resp="$(mktemp)"
    code="$(printf '%s' "$payload" | curl -s -o "$resp" -w '%{http_code}' -b "$COOKIE_JAR" -X POST \
        -H 'Content-Type: application/json' --data-binary @- "http://$PANEL_ADDR/api/clients")"
    if [ "$code" = "201" ]; then
        local id
        id="$(jq -r .id <"$resp")"
        echo "Added: $id. Its link appears once the channel is up (option 4)."
    else
        echo "error ($code): $(jq -r '.error // .' <"$resp" 2>/dev/null || cat "$resp")" >&2
    fi
    rm -f "$resp"
}

remove_client() {
    panel_login || return 1
    local id confirm code
    read -rp "Client id to remove: " id
    [ -n "$id" ] || return 0
    read -rp "Delete '$id' and its key (its link stops working)? Type 'yes': " confirm
    [ "$confirm" = "yes" ] || { echo "cancelled"; return 0; }
    code="$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X DELETE "http://$PANEL_ADDR/api/clients/$id")"
    [ "$code" = "200" ] && echo "Removed." || echo "error: delete failed (HTTP $code)" >&2
}

# show_link prints a client's openflux:// link and, with qrencode, its QR code.
show_link() {
    panel_login || return 1
    local id body link
    read -rp "Client id: " id
    body="$(panel_get /api/clients)" || return 1
    link="$(jq -r --arg id "$id" '.[] | select(.config.id == $id) | .link // ""' <<<"$body")"
    if [ -z "$link" ]; then
        echo "No link for '$id' yet (unknown id, or the channel has not started). See its logs (option 5)."
        return 1
    fi
    echo "The link carries the client's encryption key: send it to that client only."
    echo
    echo "$link"
    echo
    if command -v qrencode >/dev/null 2>&1; then
        qrencode -t ansiutf8 "$link"
    else
        echo "(apt-get install qrencode to see the QR code here; the web panel shows it too)"
    fi
}

client_logs() {
    panel_login || return 1
    local id body
    read -rp "Client id: " id
    body="$(panel_get "/api/clients/$id/logs")" || return 1
    jq -r '.lines[]?' <<<"$body" | tail -n 40
}

# ---------- update / service ----------

do_update() {
    (cd "$(dirname "$BIN_PATH")" && OPENFLUX_SERVICE="$SERVICE" OPENFLUX_BIN="$BIN_PATH" "$SCRIPT_DIR/deploy/update.sh")
}

svc_status() { systemctl status "$SERVICE" --no-pager || true; }
svc_logs() { journalctl -u "$SERVICE" -n 40 --no-pager; }

# ---------- menu ----------

menu() {
    cat <<EOF

========================================
  OpenFlux exit-panel management
  service: $SERVICE   panel: $PANEL_ADDR
========================================
 1) Update to the latest release
 2) List clients
 3) Add client
 4) Show a client's link / QR code
 5) Show a client's logs
 6) Remove client
----------------------------------------
 7) Service status
 8) Service logs
 9) Reconfigure (panel address/login/paths)
 0) Exit
========================================
EOF
    local choice
    read -rp "Choice: " choice
    echo
    case "$choice" in
        1) do_update ;;
        2) list_clients ;;
        3) add_client ;;
        4) show_link ;;
        5) client_logs ;;
        6) remove_client ;;
        7) svc_status ;;
        8) svc_logs ;;
        9) first_time_setup ;;
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
    pause # from the menu, menu() pauses after it
fi
while true; do menu; done
