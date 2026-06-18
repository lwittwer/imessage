#!/bin/bash
# build.sh — Build extract-key on macOS (including High Sierra 10.13+).
# Downloads Go 1.20 if needed. No root access required.
#
# Usage:
#   cd tools/extract-key
#   ./build.sh
#   ./extract-key

set -euo pipefail
cd "$(dirname "$0")"

GO_VERSION="1.20.14"

# Download a Go toolchain matching the Mac's native architecture. Apple Silicon
# Macs can run x86_64 binaries under Rosetta; if we build extract-key that way,
# runtime.GOARCH reports amd64 and the extractor mis-detects the machine as
# Intel, which also disables the arm64 ROM fallback. Build native instead.
case "$(uname -m)" in
    arm64) GO_ARCH="arm64" ;;
    x86_64) GO_ARCH="amd64" ;;
    *)
        echo "Unsupported macOS architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

GO_TARBALL="go${GO_VERSION}.darwin-${GO_ARCH}.tar.gz"
GO_URL="https://go.dev/dl/${GO_TARBALL}"
LOCAL_GO="./.go-${GO_VERSION}-${GO_ARCH}"

# Try system Go first, but only if it matches the native architecture.
# On Apple Silicon, an x86_64 Go installed/running under Rosetta would produce
# an x86_64 extractor and mis-detect the hardware.
if command -v go >/dev/null 2>&1 && go version >/dev/null 2>&1 && go version | grep -q "darwin/${GO_ARCH}"; then
    GO_CMD="go"
    echo "Using system Go: $(go version)"
else
    # Download Go locally
    if [ ! -x "${LOCAL_GO}/bin/go" ]; then
        echo "Go not found — downloading Go ${GO_VERSION}..."
        curl -fSL -o "${GO_TARBALL}" "${GO_URL}"
        mkdir -p "${LOCAL_GO}"
        tar -xzf "${GO_TARBALL}" --strip-components=1 -C "${LOCAL_GO}"
        rm -f "${GO_TARBALL}"
    fi
    GO_CMD="${LOCAL_GO}/bin/go"
    echo "Using local Go: $("${GO_CMD}" version)"
fi

echo "Building extract-key..."
CGO_ENABLED=1 "${GO_CMD}" build -trimpath -o extract-key .

echo ""
echo "✓ Built: $(pwd)/extract-key"
echo "  Run:   ./extract-key"
