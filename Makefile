# Sentinel — The Autonomous AI SRE
# Makefile for building, testing, and releasing Sentinel.
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
#   make release        # Tag and push a release (usage: make release VERSION=3.6.0)
#   make helm-lint      # Validate the Helm chart
#   make clean          # Remove built artifacts
#   make install-cli    # Install sentinelctl to /usr/local/bin

SHELL := /bin/bash

# --- Variables ---
# v3.8.4: VERSION is sourced from the VERSION file at the repo root.
# This is the single source of truth — Chart.yaml, values.yaml, and the
# README are kept in sync by `make release-bump`. Override with
# `make build VERSION=3.9.0` for one-off builds.
VERSION ?= $(shell cat VERSION 2>/dev/null | tr -d '[:space:]')
ifeq ($(VERSION),)
VERSION := dev
endif
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X github.com/karimzakzouk/sentinel/v3/internal/version.Version=$(VERSION)
BINARY := sentinel
CLI_BINARY := sentinelctl
BUILD_DIR := bin
WEB_DIR := web
WEB_OUT := web/out
DOCKER_IMAGE ?= karimzakzouk/sentinel-v3
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
.PHONY: release
release: release-bump
        @if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=3.6.0"; exit 1; fi
        @echo "==> Tagging v$(VERSION)..."
        git add VERSION chart/Chart.yaml chart/values.yaml README.md
        git commit -m "release: v$(VERSION)" || echo "(nothing to commit — already at $(VERSION))"
        git tag v$(VERSION)
        git push origin main v$(VERSION)
        @echo "==> Release triggered. CI will build and publish."

# v3.8.4: release-bump syncs the VERSION file's value into Chart.yaml,
# values.yaml, and README.md. The VERSION file is the single source of
# truth; everything else is derived. Run manually after editing VERSION,
# or automatically as part of `make release`.
.PHONY: release-bump
release-bump:
        @v=$$(cat VERSION | tr -d '[:space:]'); \
        if [ -z "$$v" ]; then echo "VERSION file is empty"; exit 1; fi; \
        echo "==> Bumping chart/values/README/web to $$v..."; \
        # Chart.yaml: version + appVersion \
        sed -i.bak -E "s/^version: .*/version: $$v/" chart/Chart.yaml; \
        sed -i.bak -E 's/^appVersion: ".*"/appVersion: "'$$v'"/' chart/Chart.yaml; \
        # values.yaml: image.tag \
        sed -i.bak -E 's|^  tag: ".*"|  tag: "'$$v'"|' chart/values.yaml; \
        # README.md: --version X.Y.Z in helm install examples \
        sed -i.bak -E 's/--version [0-9.]+/--version '$$v'/g' README.md; \
        # v3.18.0: M47 — web/package.json + my-values.yaml header + sidebar version string \
        sed -i.bak -E 's/"version": "[0-9.]+.*"/"version": "'$$v'"/' web/package.json; \
        sed -i.bak -E 's/^# Sentinel v[0-9.]+/# Sentinel v'$$v'/' my-values.yaml; \
        sed -i.bak -E 's/Sentinel v[0-9.]+\.[0-9]+ starting/Sentinel '$$v' starting/' web/src/app/page.tsx; \
        sed -i.bak -E 's/>v[0-9.]+\.[0-9]+\.[0-9]+</>v'$$v'</' web/src/app/page.tsx; \
        rm -f chart/Chart.yaml.bak chart/values.yaml.bak README.md.bak web/package.json.bak my-values.yaml.bak web/src/app/page.tsx.bak; \
        echo "==> Synced. Review with 'git diff'."

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
        @echo "  release        Tag and push a release (VERSION=3.6.0)"
        @echo "  install-cli    Install sentinelctl to /usr/local/bin"
        @echo "  clean          Remove build artifacts"
        @echo "  ci             Run all CI checks"
        @echo "  help           Show this help"
