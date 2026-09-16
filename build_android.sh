#!/bin/bash
# Cross-compiles the openflux CLI binary for Android via cgo + the NDK's
# clang wrappers. Host-OS aware (darwin/linux/windows) and builds every ABI
# the OpenFluxAndroid app ships jniLibs for.
set -e

ANDROID_NDK_HOME="${ANDROID_NDK_HOME:?set ANDROID_NDK_HOME to your NDK r27+ install}"
MIN_SDK="${MIN_SDK:-26}" # must match OpenFluxAndroid's app/build.gradle.kts minSdk
BINARY_NAME="openflux"
OUTPUT_ROOT="output/android"

case "$(uname -s)" in
    Darwin*)  NDK_HOST="darwin-x86_64" ;;
    Linux*)   NDK_HOST="linux-x86_64" ;;
    MINGW*|MSYS*|CYGWIN*) NDK_HOST="windows-x86_64" ;;
    *) echo "unsupported build host: $(uname -s)" >&2; exit 1 ;;
esac

TOOLCHAIN_BIN="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$NDK_HOST/bin"
if [ ! -d "$TOOLCHAIN_BIN" ]; then
    echo "NDK toolchain not found at $TOOLCHAIN_BIN (check ANDROID_NDK_HOME / host)" >&2
    exit 1
fi
# NDK clang wrappers are extensionless bash scripts on every host (including
# Windows, where they shell out to clang.exe); the .cmd siblings are for
# cmd.exe only and don't apply when driven from bash/git-bash.

# On git-bash/MSYS, any /system/lib64-looking argument passed to a native
# Windows child process (go build) gets silently rewritten into a Windows
# path (e.g. "C:/Program Files/Git/system/lib64"), breaking these rpaths.
# A leading "//" is MSYS's own documented escape to skip that rewrite for a
# single path, and round-trips to a single "/" by the time clang sees it.
RPATH_SYS="/system/lib64"
RPATH_VENDOR="/vendor/lib64"
if [ "$NDK_HOST" = "windows-x86_64" ]; then
    RPATH_SYS="/$RPATH_SYS"
    RPATH_VENDOR="/$RPATH_VENDOR"
fi

# abi -> "GOARCH:clang-triple:march"
ABIS=(
    "arm64-v8a:aarch64-linux-android:"
    "armeabi-v7a:armv7a-linux-androideabi:"
    "x86_64:x86_64-linux-android:"
)

for entry in "${ABIS[@]}"; do
    abi="${entry%%:*}"
    rest="${entry#*:}"
    triple="${rest%%:*}"
    out_dir="$OUTPUT_ROOT/$abi"
    mkdir -p "$out_dir"

    case "$abi" in
        arm64-v8a)    goarch=arm64;   march="-march=armv8-a" ;;
        armeabi-v7a)  goarch=arm;     march="-march=armv7-a -mfpu=neon" ;;
        x86_64)       goarch=amd64;   march="" ;;
    esac

    cc="$TOOLCHAIN_BIN/${triple}${MIN_SDK}-clang"
    cxx="$TOOLCHAIN_BIN/${triple}${MIN_SDK}-clang++"
    if [ ! -f "$cc" ]; then
        echo "skip $abi: $cc not found" >&2
        continue
    fi

    echo "=== building $abi (GOARCH=$goarch, API $MIN_SDK) ==="
    GOARCH="$goarch" \
    GOOS=android \
    CGO_ENABLED=1 \
    CC="$cc" \
    CXX="$cxx" \
    CGO_CFLAGS="$march -O2" \
    CGO_CXXFLAGS="$march -O2" \
    CGO_LDFLAGS="-Wl,-z,max-page-size=16384 -Wl,-rpath,$RPATH_SYS -Wl,-rpath,$RPATH_VENDOR" \
    GOARM=7 \
    go build \
        -v \
        -ldflags="-s -w -linkmode external -extldflags '-Wl,-z,max-page-size=16384 -Wl,-rpath,$RPATH_SYS -Wl,-rpath,$RPATH_VENDOR' -checklinkname=0" \
        -o "$out_dir/$BINARY_NAME" \
        .

    if [ -f "$out_dir/$BINARY_NAME" ]; then
        echo "built: $out_dir/$BINARY_NAME"
    fi
done
