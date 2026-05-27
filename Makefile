# Makefile for mycomputer (Linux X11 computer-use CLI+MCP).
#
# Targets:
#   build              build ./bin/mycomputer with version ldflags
#   release            same as build, plus -s -w to strip symbols/DWARF
#   test               go test ./...
#   lint               go vet + gofmt + staticcheck + deadcode (+golangci-lint
#                      when present). staticcheck and deadcode are HARD-FAIL
#                      gates — any finding fails the target. gofmt drift also
#                      fails the target. deadcode runs with -test so symbols
#                      kept reachable via TestExportedAPIKeepalive references
#                      in _test.go files are honored. Install with:
#                        go install honnef.co/go/tools/cmd/staticcheck@latest
#                        go install golang.org/x/tools/cmd/deadcode@latest
#   conventions-check  validate cmd surface vs conventions.yaml (anvil)
#   clean              remove ./bin
#
# Version injection:
#   VERSION  defaults to `git describe --tags --always --dirty` or 'dev'
#   COMMIT   defaults to `git rev-parse HEAD` or 'unknown'
#   BUILT    ISO-8601 UTC timestamp; respects SOURCE_DATE_EPOCH for
#            reproducible builds.
#
# Override any of these on the command line, e.g.:
#   make build VERSION=v0.3.0 COMMIT=$(git rev-parse HEAD)
#
# Plain `go build ./...` (no ldflags) still produces a working binary
# that reports dev/unknown/unknown — that path is preserved for daily
# developer workflow.

SHELL := /bin/bash

# --- Version metadata ------------------------------------------------------

# Resolve VERSION: git describe → fallback "dev".
# Note: git writes "HEAD" to stdout when no commits exist, so we capture
# stdout only on success and emit the fallback otherwise.
VERSION ?= $(shell v=$$(git describe --tags --always --dirty 2>/dev/null); if [ -n "$$v" ] && git rev-parse --verify HEAD >/dev/null 2>&1; then echo $$v; else echo dev; fi)

# Resolve COMMIT: full SHA → fallback "unknown".
COMMIT  ?= $(shell c=$$(git rev-parse HEAD 2>/dev/null); if [ -n "$$c" ] && git rev-parse --verify HEAD >/dev/null 2>&1; then echo $$c; else echo unknown; fi)

# Resolve BUILT: SOURCE_DATE_EPOCH wins (reproducible builds), else now.
ifeq ($(origin SOURCE_DATE_EPOCH), undefined)
BUILT ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
else
BUILT ?= $(shell date -u -d @$(SOURCE_DATE_EPOCH) +%Y-%m-%dT%H:%M:%SZ)
endif

# --- Build flags -----------------------------------------------------------

PKG        := github.com/1broseidon/mc
CMD        := ./cmd/mycomputer
BIN_DIR    := bin
BIN        := $(BIN_DIR)/mycomputer

LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.built=$(BUILT)
RELEASE_LDFLAGS := -s -w $(LDFLAGS)

# --- Targets ---------------------------------------------------------------

.PHONY: build release install test lint conventions-check clean

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(CMD)

release:
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(BIN) $(CMD)

# install builds the dev binary and overwrites the user's mycomputer at
# $(INSTALL_DIR) — defaults to ~/.local/bin so MCP hosts resolve the
# locally-built binary instead of whatever release tarball is installed.
# Use this before /release to smoke-test the candidate commit; the
# release pipeline still ships a clean -trimpath build from the tag.
INSTALL_DIR ?= $(HOME)/.local/bin
install: build
	@mkdir -p $(INSTALL_DIR)
	install -m 755 $(BIN) $(INSTALL_DIR)/mycomputer
	@echo "installed dev build to $(INSTALL_DIR)/mycomputer"
	@$(INSTALL_DIR)/mycomputer version

test:
	go test ./...

lint:
	go vet ./...
	@out=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$out" ]; then \
		echo "gofmt: files need formatting:"; echo "$$out"; \
		exit 1; \
	fi
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not found; install with:"; \
		echo "  go install honnef.co/go/tools/cmd/staticcheck@latest"; \
		exit 1; \
	fi
	@if command -v deadcode >/dev/null 2>&1; then \
		out=$$(deadcode -test ./...); \
		if [ -n "$$out" ]; then echo "$$out"; exit 1; fi; \
	else \
		echo "deadcode not found; install with:"; \
		echo "  go install golang.org/x/tools/cmd/deadcode@latest"; \
		exit 1; \
	fi
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	fi

conventions-check:
	@if command -v anvil >/dev/null 2>&1; then \
		anvil audit --conventions conventions.yaml; \
	else \
		echo "anvil not found on PATH; skipping conventions-check"; \
	fi

clean:
	rm -rf $(BIN_DIR)
