# Sentinel — The Autonomous AI SRE
# Makefile for building, testing, and releasing Sentinel.
#
# Versioning:
#   VERSION file is the single source of truth.
#   `make bump-version VERSION=X.Y.Z` rewrites every other file to match.
#   `make check-version` fails if any file is out of sync (used by CI).
#   `make release VERSION=X.Y.Z` bumps, commits, tags, and pushes — CI does the rest.
#
# Usage:
#   make build          # Build the sentinel binary
#   make build-cli      # Build the sentinelctl CLI
#   make build-all      # Build both binaries
#   make web            # Build the web UI (Next.js static export)
#   make test           # Run all tests with race detector
#   make lint           # Run golangci-lint
#   make security       # Run govulncheck (CVE scan)
#   make run            # Run Sentinel locally (needs kubeconfig)
#   make dev            # Run Sentinel in dev mode (mock K8s)
#   make docker         # Build the Docker image
#   make release        # Bump, commit, tag, push (usage: make release VERSION=4.5.0)
#   make bump-version   # Sync VERSION across all files (usage: make bump-version VERSION=4.5.0)
#   make check-version  # CI mode: fail if drift detected
#   make helm-lint      # Validate the Helm chart
#   make clean          # Remove built artifacts
#   make install-cli    # Install sentinelctl to /usr/local/bin

SHELL := /bin/bash

# --- Variables ---
# VERSION is sourced from the VERSION file at the repo root.
# This is the single source of truth — scripts/sync-version.py keeps
# Chart.yaml, values.yaml, README.md, web/package.json, and version.go
# in sync. Override with `make build VERSION=4.5.0` for one-off builds.
VERSION ?= $(shell cat VERSION 2>/dev/null | tr -d '[:space:]')
ifeq ($(VERSION),)
VERSION := dev
endif
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X github.com/karimzakzouk/sentinel/internal/version.Version=$(VERSION)
BINARY := sentinel
CLI_BINARY := sentinelctl
BUILD_DIR := bin
WEB_DIR := web
WEB_OUT := web/out
DOCKER_IMAGE ?= karimzakzouk/sentinel
GO ?= go

# --- Default target ---
.PHONY: all
all: build

# --- Build ---
.PHONY: build
build: web
        @echo "==> Building sentinel binary ($(VERSION))..."
        @mkdir -p $(BUILD_DIR)
        $(GO) build -ldflags='$(LDFLAGS)' -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/sentinel

.PHONY: build-cli
build-cli:
        @echo "==> Building sentinelctl binary..."
        @mkdir -p $(BUILD_DIR)
        $(GO) build -ldflags='$(LDFLAGS)' -trimpath -o $(BUILD_DIR)/$(CLI_BINARY) ./cmd/sentinelctl

.PHONY: build-all
build-all: build build-cli

.PHONY: build-no-web
build-no-web:
        @echo "==> Building sentinel binary (no web UI)..."
        @mkdir -p $(BUILD_DIR)
        $(GO) build -ldflags='$(LDFLAGS)' -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/sentinel

# --- Web UI ---
.PHONY: web
web:
        @if [ ! -d $(WEB_DIR)/node_modules ]; then \
                echo "==> Installing web dependencies..."; \
                cd $(WEB_DIR) && npm install; \
        fi
        @echo "==> Building web UI (static export)..."
        @cd $(WEB_DIR) && npx next build
        @echo "==> Copying built files to embed directory..."
        @rm -rf internal/web/out
        @mkdir -p internal/web/out
        @if [ -d $(WEB_DIR)/out ]; then \
                cp -r $(WEB_DIR)/out/* internal/web/out/; \
        elif [ -d $(WEB_DIR)/.next ]; then \
                echo "==> Next.js output:standalone detected, copying .next..."; \
                cp -r $(WEB_DIR)/.next internal/web/out/.next; \
                cp -r $(WEB_DIR)/public/* internal/web/out/ 2>/dev/null || true; \
        fi
        @if [ ! -f internal/web/out/index.html ]; then \
                echo '<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px"><h1 style="color:#06B6D4">Sentinel</h1><p>Web UI build incomplete. API at /healthz, /incidents, /metrics.</p></body></html>' > internal/web/out/index.html; \
        fi
        @echo "==> Web UI built and embedded. Run 'make build' to compile."

.PHONY: web-dev
web-dev:
        @echo "==> Starting web UI dev server..."
        @cd $(WEB_DIR) && npm run dev

.PHONY: web-clean
web-clean:
        @rm -rf $(WEB_DIR)/node_modules $(WEB_DIR)/.next $(WEB_OUT)

# --- Testing ---
.PHONY: test
test:
        @echo "==> Running tests with race detector..."
        $(GO) test ./... -race -count=1

.PHONY: test-verbose
test-verbose:
        @echo "==> Running tests (verbose)..."
        $(GO) test ./... -race -v -count=1

.PHONY: test-coverage
test-coverage:
        @echo "==> Running tests with coverage..."
        $(GO) test ./... -race -coverprofile=coverage.out -count=1
        $(GO) tool cover -html=coverage.out -o coverage.html
        @echo "==> Coverage report: coverage.html"

# --- Linting ---
.PHONY: lint
lint:
        @echo "==> Running golangci-lint..."
        @golangci-lint run --timeout=5m

.PHONY: security
security:
        @echo "==> Running govulncheck..."
        @$(GO) install golang.org/x/vuln/cmd/govulncheck@latest 2>/dev/null || true
        @govulncheck ./...

.PHONY: vet
vet:
        @echo "==> Running go vet..."
        $(GO) vet ./...

# --- Run ---
.PHONY: run
run: build-no-web
        @echo "==> Running Sentinel locally..."
        ./$(BUILD_DIR)/$(BINARY) --addr :8000 --db /tmp/sentinel.db

.PHONY: dev
dev: build-no-web
        @echo "==> Running Sentinel in dev mode..."
        SENTINEL_LOG_LEVEL=DEBUG ./$(BUILD_DIR)/$(BINARY) --addr :8000 --db /tmp/sentinel-dev.db

# --- Docker ---
.PHONY: docker
docker:
        @echo "==> Building Docker image..."
        docker build -t $(DOCKER_IMAGE):$(VERSION) \
                --build-arg VERSION=$(VERSION) \
                .

.PHONY: docker-push
docker-push:
        @echo "==> Pushing Docker image..."
        docker push $(DOCKER_IMAGE):$(VERSION)
        docker tag $(DOCKER_IMAGE):$(VERSION) $(DOCKER_IMAGE):latest
        docker push $(DOCKER_IMAGE):latest

# --- Helm ---
.PHONY: helm-lint
helm-lint:
        @echo "==> Linting Helm chart..."
        helm lint chart/

.PHONY: helm-template
helm-template:
        @echo "==> Rendering Helm chart..."
        helm template test chart/ > /tmp/sentinel-rendered.yaml
        @test $$(grep -c "kind:" /tmp/sentinel-rendered.yaml) -ge 6 && echo "Chart OK" || (echo "Chart FAILED" && exit 1)

# --- Release ---
# Bump the VERSION file and sync every other file to match.
# Usage: make bump-version VERSION=4.5.0
.PHONY: bump-version
bump-version:
        @if [ -z "$(VERSION)" ]; then echo "Usage: make bump-version VERSION=4.5.0"; exit 1; fi
        @python3 scripts/sync-version.py --bump $(VERSION)
        @echo "==> Synced all files to $(VERSION). Review with 'git diff'."

# CI mode: fail if any file is out of sync with VERSION file.
.PHONY: check-version
check-version:
        @python3 scripts/sync-version.py --check

# Full release flow: bump, commit, tag, push. CI does the rest.
# Usage: make release VERSION=4.5.0
.PHONY: release
release:
        @if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=4.5.0"; exit 1; fi
        @echo "==> Syncing all files to $(VERSION)..."
        @python3 scripts/sync-version.py --bump $(VERSION)
        @git add -A
        @git commit -m "release: v$(VERSION)" || echo "(nothing to commit)"
        @git tag v$(VERSION)
        @git push origin main v$(VERSION)
        @echo "==> Release triggered. CI will build + publish Docker image + Helm chart + GitHub Release."

# --- Install ---
.PHONY: install-cli
install-cli: build-cli
        @echo "==> Installing sentinelctl to /usr/local/bin..."
        @sudo cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/
        @echo "==> Done! Run 'sentinelctl --help' to verify."

.PHONY: install-completion
install-completion:
        @echo "==> Installing shell completion..."
        @echo "Add this to your ~/.bashrc or ~/.zshrc:"
        @echo '  source <(sentinelctl completion bash)  # bash'
        @echo '  source <(sentinelctl completion zsh)   # zsh'

# --- Cleanup ---
.PHONY: clean
clean:
        @echo "==> Cleaning build artifacts..."
        @rm -rf $(BUILD_DIR) $(WEB_OUT) $(WEB_DIR)/.next coverage.out coverage.html

# --- CI (simulates what GitHub Actions does) ---
.PHONY: ci
ci: lint test vet helm-lint
        @echo "==> CI checks passed!"

# --- Help ---
.PHONY: help
help:
        @echo "Sentinel — The Autonomous AI SRE"
        @echo ""
        @echo "Targets:"
        @echo "  build          Build the sentinel binary (includes web UI)"
        @echo "  build-cli      Build the sentinelctl CLI"
        @echo "  build-all      Build both binaries"
        @echo "  web            Build the web UI (Next.js static export)"
        @echo "  web-dev        Start web UI dev server"
        @echo "  test           Run tests with race detector"
        @echo "  test-coverage  Run tests with coverage report"
        @echo "  lint           Run golangci-lint"
        @echo "  security       Run govulncheck (CVE scan)"
        @echo "  run            Run Sentinel locally"
        @echo "  dev            Run Sentinel in dev mode (DEBUG logging)"
        @echo "  docker         Build Docker image"
        @echo "  helm-lint      Validate Helm chart"
        @echo "  release        Bump + commit + tag + push (VERSION=4.5.0)"
        @echo "  bump-version   Sync VERSION across all files (VERSION=4.5.0)"
        @echo "  check-version  CI mode: fail if version drift detected"
        @echo "  install-cli    Install sentinelctl to /usr/local/bin"
        @echo "  clean          Remove build artifacts"
        @echo "  ci             Run all CI checks"
        @echo "  help           Show this help"
