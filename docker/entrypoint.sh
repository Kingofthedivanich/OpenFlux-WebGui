#!/bin/sh
# Maps environment variables to openflux flags and, for the exit node,
# installs the kernel-RST-drop rule *inside this container's netns*.
#
# Why the rule: the exit node's TCP connections live in a userspace (gVisor)
# stack, so the kernel has no socket for them and answers every inbound
# SYN-ACK with an RST, tearing the tunnel down. Confining the DROP to the
# container netns is the scoped variant of upstream's host-wide rule — it
# cannot affect the host or other containers.
set -eu

role="${ROLE:-client}"
transport="${TRANSPORT:-yandex}"
listen="${SOCKS5_LISTEN:-:1080}"
mode="${MODE:-l3}"
codec="${CODEC:-batched}"

case "$role" in
  client | exit) ;;
  exit-node) role=exit ;; # accept the old spelling
  *)
    echo "ROLE must be 'client' or 'exit' (got '$role')" >&2
    exit 2
    ;;
esac

case "$transport" in
  yandex | vyandex | oneme | cupsonline | mailru) ;;
  *)
    echo "TRANSPORT must be yandex|vyandex|oneme|cupsonline|mailru (got '$transport')" >&2
    exit 2
    ;;
esac

set -- "--role=$role" "--transport=$transport" "--codec=$codec"

if [ "$role" = client ]; then
  set -- "$@" --socks5 "$listen"
else
  set -- "$@" "--mode=$mode"
fi

[ -n "${URL:-}" ] && set -- "$@" --url "$URL"
[ -n "${MAX_TOKEN:-}" ] && set -- "$@" --maxToken "$MAX_TOKEN"
[ -n "${MAX_UID:-}" ] && set -- "$@" --maxUid "$MAX_UID"
[ -n "${LOCAL_IP:-}" ] && set -- "$@" --local-ip "$LOCAL_IP"
[ -n "${UPSTREAM_PROXY:-}" ] && set -- "$@" --upstream-proxy "$UPSTREAM_PROXY"

# Encryption (see README). The exit prints its public key at startup; put it
# in the client's PEER_KEY. A missing key is a hard error unless ALLOW_PLAINTEXT.
[ -n "${EXIT_KEY_FILE:-}" ] && set -- "$@" --exit-key-file "$EXIT_KEY_FILE"
[ -n "${PEER_KEY:-}" ] && set -- "$@" --peer-key "$PEER_KEY"
[ -n "${PSK_FILE:-}" ] && set -- "$@" --psk-file "$PSK_FILE"
case "${ALLOW_PLAINTEXT:-0}" in 1 | true | yes) set -- "$@" --allow-plaintext ;; esac
case "${ALLOW_PRIVATE:-0}" in 1 | true | yes) set -- "$@" --allow-private ;; esac
case "${DEBUG:-0}" in 1 | true | yes) set -- "$@" --debug ;; esac

if [ "$role" = exit ] && [ "$mode" = l3 ]; then
  echo "[entrypoint] dropping outbound TCP RSTs inside the container netns"
  if ! iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP; then
    echo "[entrypoint] WARNING: iptables failed (missing NET_ADMIN?); kernel RSTs will kill tunnel connections" >&2
  fi
fi

# Do not echo argv: it carries the document URL and MAX token.
echo "[entrypoint] exec: openflux --role=$role --transport=$transport --mode=$mode --codec=$codec (secrets hidden)"
exec openflux "$@"
