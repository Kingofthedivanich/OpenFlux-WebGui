#!/bin/bash
# Updates an --role=exit-panel deployment to the latest GitHub release.
# Run from the directory with the current `openflux` binary (the systemd
# unit's WorkingDirectory), e.g.:
#
#   cd /root/OpenFlux && ./deploy/update.sh
#
# Verifies the download against the release's SHA256SUMS.txt before touching
# anything, keeps a timestamped backup of the binary it replaces, and rolls
# back if the service does not come back up. The installed release's tag is
# kept in <binary>.version (the core has no --version flag).
set -euo pipefail

REPO="${OPENFLUX_REPO:-Kingofthedivanich/OpenFlux-WebGui}"
SERVICE="${OPENFLUX_SERVICE:-openflux-exit}"
BINARY="${OPENFLUX_BIN:-./openflux}"
VERSION_FILE="${BINARY}.version"
SUDO=""
[ "$(id -u)" -eq 0 ] || SUDO=sudo

case "$(uname -m)" in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    armv7l | armv7*) ARCH=arm ;;
    *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
ASSET="openflux-linux-$ARCH"

if [ ! -f "$BINARY" ]; then
    echo "error: $BINARY not found. Run this from the directory with the current binary" >&2
    echo "       (or set OPENFLUX_BIN=/path/to/openflux)." >&2
    exit 1
fi

current_version="$(cat "$VERSION_FILE" 2>/dev/null || echo unknown)"
echo "Current version: $current_version"

echo "Checking latest release of $REPO..."
# Buffer the whole response before parsing it: piping curl into `grep -m1`
# lets grep close the pipe early, curl dies of SIGPIPE and, with pipefail,
# takes this line down under set -e before any message is printed.
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

base="https://github.com/$REPO/releases/download/$latest_tag"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
echo "Downloading $base/$ASSET ..."
if ! curl -fsSL -o "$tmpdir/$ASSET" "$base/$ASSET"; then
    echo "error: download failed" >&2
    exit 1
fi
if curl -fsSL -o "$tmpdir/SHA256SUMS.txt" "$base/SHA256SUMS.txt"; then
    if ! (cd "$tmpdir" && grep -E " \*?$ASSET\$" SHA256SUMS.txt | sha256sum -c --status); then
        echo "error: $ASSET does not match the release's SHA256SUMS.txt, aborting" >&2
        exit 1
    fi
    echo "Checksum OK."
else
    echo "warning: the release has no SHA256SUMS.txt; checking the file type only" >&2
fi
if ! head -c 4 "$tmpdir/$ASSET" | grep -q 'ELF'; then
    echo "error: the download is not a Linux binary, aborting" >&2
    exit 1
fi
chmod +x "$tmpdir/$ASSET"

backup="${BINARY}.bak-$(date +%Y%m%d%H%M%S)"
echo "Backing up the current binary to $backup"
cp "$BINARY" "$backup"

echo "Stopping $SERVICE..."
$SUDO systemctl stop "$SERVICE"
mv "$tmpdir/$ASSET" "$BINARY"
echo "Starting $SERVICE..."
$SUDO systemctl start "$SERVICE"

sleep 3
if $SUDO systemctl is-active --quiet "$SERVICE"; then
    printf '%s\n' "$latest_tag" > "$VERSION_FILE"
    echo "Update OK: now running $latest_tag"
    # Keep only the 3 most recent backups.
    ls -t "${BINARY}".bak-* 2>/dev/null | tail -n +4 | xargs -r rm -- || true
else
    echo "error: $SERVICE did not come up after the update; rolling back" >&2
    $SUDO systemctl stop "$SERVICE" || true
    mv "$backup" "$BINARY"
    $SUDO systemctl start "$SERVICE"
    echo "Rolled back to $current_version." >&2
    exit 1
fi
