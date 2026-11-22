GO ?= $(shell command -v go || echo $$HOME/.local/go/bin/go)
BIN_DIR ?= dist

.PHONY: all host xo1 test clean

all: xo1 host

# Static 32-bit x86 binary with NO SSE2 instructions (AMD Geode LX-700 / OLPC XO-1)
# and ZERO libc/OpenSSL/ncurses dependencies (works out of the box on Fedora 18).
xo1:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=386 GO386=softfloat \
		$(GO) build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/erin-xo1-linux-i386 ./cmd/erin
	@ls -lh $(BIN_DIR)/erin-xo1-linux-i386

# Native binary for the current development host
host:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/erin ./cmd/erin
	@ls -lh $(BIN_DIR)/erin

test:
	$(GO) test -v ./...

clean:
	rm -rf $(BIN_DIR)
