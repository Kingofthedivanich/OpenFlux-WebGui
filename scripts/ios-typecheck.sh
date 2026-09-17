#!/usr/bin/env bash
# Type-checks the iOS cgo bridges (export_ios*.go) on any host, without Xcode:
# the files are rebuilt for GOOS=darwin with the "C" pseudo-package replaced
# by a Go stub, then the package is compiled through an -overlay. It catches Go-side
# breakage in the bridges; the real iOS build still needs Xcode (see CI).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
go="${GO:-go}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/fakec.go" <<'EOF'
package zzfakec

import "unsafe"

type Char = byte
type Int = int32

func GoString(*Char) string              { return "" }
func CString(string) *Char               { return nil }
func GoBytes(unsafe.Pointer, Int) []byte { return nil }
func Free(unsafe.Pointer)                {}
EOF

overlay="{\"Replace\":{\"$root/zzfakec/fakec.go\":\"$tmp/fakec.go\""
for f in "$root"/export_ios*.go; do
	name="$(basename "$f" .go)"
	sed -e 's#^//go:build ios$#//go:build darwin#' \
		-e 's#^import "C"$#import C "openflux/zzfakec"#' \
		-e 's#C\.char#C.Char#g; s#C\.int\b#C.Int#g; s#C\.free#C.Free#g' \
		"$f" >"$tmp/$name.go"
	overlay+=",\"$root/zz_${name}_darwin.go\":\"$tmp/$name.go\""
done
overlay+="}}"
echo "$overlay" >"$tmp/overlay.json"

cd "$root"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 "$go" build -overlay="$tmp/overlay.json" -o /dev/null .
echo "ios bridges: type-check OK"
