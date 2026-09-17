#!/bin/bash
set -e

ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$HOME/Android/Sdk/ndk/27.0.12077973}"
OUTPUT_DIR="output/android/arm64-v8a"
BINARY_NAME="openflux"

# Pick the NDK prebuilt toolchain for the build host (was hardcoded to macOS).
case "$(uname -s)" in
  Darwin) NDK_HOST="darwin-x86_64" ;;
  Linux)  NDK_HOST="linux-x86_64" ;;
  *)      NDK_HOST="${NDK_HOST:-linux-x86_64}" ;;
esac
NDK_BIN="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$NDK_HOST/bin"

mkdir -p "$OUTPUT_DIR"

export GOARCH=arm64
export GOOS=android
export CGO_ENABLED=1
export CC="$NDK_BIN/aarch64-linux-android35-clang"
export CXX="$NDK_BIN/aarch64-linux-android35-clang++"
export CGO_CFLAGS="-march=armv8-a -O2"
export CGO_CXXFLAGS="-march=armv8-a -O2"
export CGO_LDFLAGS="-Wl,-rpath,/system/lib64 -Wl,-rpath,/vendor/lib64"

go build \
    -v \
    -ldflags="-s -w -linkmode external -extldflags '-Wl,-rpath,/system/lib64 -Wl,-rpath,/vendor/lib64' -checklinkname=0" \
    -o "$OUTPUT_DIR/$BINARY_NAME" \
    .

if [ -f "$OUTPUT_DIR/$BINARY_NAME" ]; then
    echo "Build successful: $OUTPUT_DIR/$BINARY_NAME"
    file "$OUTPUT_DIR/$BINARY_NAME"
fi
