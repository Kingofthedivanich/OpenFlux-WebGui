#!/bin/bash
# Updates a --role=exit(-panel) deployment to the latest GitHub release.
# Run from the directory containing the current `openflux` binary (the
# systemd unit's WorkingDirectory), e.g.:
#
#   cd /root/OpenFlux && ./deploy/update.sh
#
# Safe by design: verifies the download before touching anything, keeps a
# timestamped backup of the binary it replaces, and rolls back automatically
# if the service doesn't come back up healthy.
set -euo pipefail

REPO="${OPENFLUX_REPO:-Kingofthedivanich/OpenFlux-WebGui}"
SERVICE="${OPENFLUX_SERVICE:-openflux-exit}"
BINARY="${OPENFLUX_BIN:-./openflux}"
ASSET="openflux-linux-amd64"

if [ ! -f "$BINARY" ]; then
    echo "error: $BINARY not found. Run this from the directory with the current binary" >&2
    echo "       (or set OPENFLUX_BIN=/path/to/openflux)." >&2
    exit 1
fi

current_version="$("$BINARY" --version 2>/dev/null || echo unknown)"
echo "Current version: $current_version"

echo "Checking latest release of $REPO..."
# Buffer the whole response before parsing it: piping curl straight into
# `grep -m1` let grep close the pipe the instant it saw a match, so on a
# large enough response curl was still writing when that happened, got
# SIGPIPE, and (with pipefail) killed this line under `set -e` -- silently,
# before the error message below ever ran.
release_json="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")" || {
    echo "error: could not reach the GitHub API" >&2
    exit 1
}
latest_tag="$(printf '%s' "$release_json" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

if [ -z "$latest_tag" ]; then
    echo "error: could not determine the latest release tag" >&2
    exit 1
fi
echo "Latest release: $latest_tag"

if [ "$current_version" = "$latest_tag" ]; then
    echo "Already up to date."
    exit 0
fi

asset_url="https://github.com/$REPO/releases/download/$latest_tag/$ASSET"
tmp="$(mktemp)"
echo "Downloading $asset_url ..."
if ! curl -fsSL -o "$tmp" "$asset_url"; then
    echo "error: download failed" >&2
    rm -f "$tmp"
    exit 1
fi

if ! file "$tmp" | grep -q "ELF 64-bit"; then
    echo "error: downloaded file is not a valid Linux binary, aborting" >&2
    rm -f "$tmp"
    exit 1
fi
chmod +x "$tmp"

backup="${BINARY}.bak-$(date +%Y%m%d%H%M%S)"
echo "Backing up current binary to $backup"
cp "$BINARY" "$backup"

echo "Stopping $SERVICE..."
sudo systemctl stop "$SERVICE"

mv "$tmp" "$BINARY"
chmod +x "$BINARY"

echo "Starting $SERVICE..."
sudo systemctl start "$SERVICE"

sleep 2
if sudo systemctl is-active --quiet "$SERVICE"; then
    new_version="$("$BINARY" --version 2>/dev/null || echo unknown)"
    echo "Update OK: now running $new_version"
    # Keep only the 3 most recent backups.
    ls -t "${BINARY}".bak-* 2>/dev/null | tail -n +4 | xargs -r rm -- || true
else
    echo "error: $SERVICE did not come up healthy after the update; rolling back" >&2
    sudo systemctl stop "$SERVICE" || true
    mv "$backup" "$BINARY"
    sudo systemctl start "$SERVICE"
    echo "Rolled back to $current_version." >&2
    exit 1
fi
