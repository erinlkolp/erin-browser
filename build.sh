#!/usr/bin/env bash
set -euo pipefail

GO_BIN="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"
mkdir -p dist

echo "==> Running tests..."
"$GO_BIN" test -v ./...

echo "==> Building OLPC XO-1 static 32-bit binary (GO386=softfloat, zero libc/OpenSSL deps)..."
CGO_ENABLED=0 GOOS=linux GOARCH=386 GO386=softfloat \
  "$GO_BIN" build -trimpath -ldflags="-s -w" -o dist/erin-xo1-linux-i386 ./cmd/erin

echo "==> Building native host binary..."
CGO_ENABLED=0 \
  "$GO_BIN" build -trimpath -ldflags="-s -w" -o dist/erin ./cmd/erin

ls -lh dist/erin-xo1-linux-i386 dist/erin
