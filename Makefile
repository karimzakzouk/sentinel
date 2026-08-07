# Sentinel — Makefile
#
# Targets:
#   build          Build the sentinel binary (includes web UI)
#   build-cli      Build the sentinelctl CLI
#   build-all      Build both binaries
#   build-no-web   Build the sentinel binary without rebuilding the web UI
#   web            Build the web UI (Next.js static export) into internal/web/out
#   web-dev        Start the Next.js dev server (hot reload)
#   test           Run all Go tests with -race
#   test-coverage  Run tests + emit coverage.html
#   lint           Run golangci-lint
#   vet            Run go vet
#   security       Run govulncheck
#   run            Build (no web) and run locally
#   dev            Build (no web) and run with DEBUG logging
#   docker         Build the Docker image
#   helm-lint      Validate the Helm chart
#   helm-template  Render the Helm chart and sanity-check kind count
#   release        Bump VERSION, sync all files, commit, tag, push (VERSION=x.y.z)
#   bump-version   Sync VERSION across all files (VERSION=x.y.z)
#   check-version  CI mode: fail if any file is out of sync with VERSION
#   install-cli    Install sentinelctl to /usr/local/bin
#   clean          Remove build artifacts
#   ci             Run all CI checks (lint + test + vet + helm-lint + check-version)
#   help           Show this help

SHELL := /bin/bash

VERSION ?= $(shell cat VERSION 2>/dev/null | tr -d '[:space:]')
ifeq ($(VERSION),)
VERSION := dev
endif

GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
GO     ?= go

LDFLAGS := -s -w \
	-X main.Version=$(VERSION) \
	-X github.com/karimzakzouk/sentinel/internal/version.Version=$(VERSION)

BINARY     := sentinel
CLI_BINARY := sentinelctl
BUILD_DIR  := bin
WEB_DIR    := web
WEB_OUT    := internal/web/out

DOCKER_IMAGE ?= karimzakzouk/sentinel

.PHONY: all
all: build

# ─── Build ──────────────────────────────────────────────────────────────

.PHONY: build
build: web
	@echo "==> Building sentinel ($(VERSION))..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags='$(LDFLAGS)' -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/sentinel

.PHONY: build-cli
build-cli:
	@echo "==> Building sentinelctl ($(VERSION))..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags='$(LDFLAGS)' -trimpath -o $(BUILD_DIR)/$(CLI_BINARY) ./cmd/sentinelctl

.PHONY: build-all
build-all: build build-cli

.PHONY: build-no-web
build-no-web:
	@echo "==> Building sentinel (no web rebuild)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags='$(LDFLAGS)' -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/sentinel

# ─── Web UI ─────────────────────────────────────────────────────────────

.PHONY: web
web:
	@if [ ! -d $(WEB_DIR)/node_modules ]; then \
		echo "==> Installing web dependencies..."; \
		cd $(WEB_DIR) && npm install; \
	fi
	@echo "==> Building web UI (static export)..."
	@cd $(WEB_DIR) && npx next build
	@echo "==> Copying built files to embed directory..."
	@rm -rf $(WEB_OUT)
	@mkdir -p $(WEB_OUT)
	@if [ -d $(WEB_DIR)/out ]; then \
		cp -r $(WEB_DIR)/out/* $(WEB_OUT)/; \
	elif [ -d $(WEB_DIR)/.next ]; then \
		echo "==> Next.js output:standalone detected, copying .next..."; \
		cp -r $(WEB_DIR)/.next $(WEB_OUT)/.next; \
		cp -r $(WEB_DIR)/public/* $(WEB_OUT)/ 2>/dev/null || true; \
	fi
	@if [ ! -f $(WEB_OUT)/index.html ]; then \
		echo '<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px"><h1 style="color:#06B6D4">Sentinel</h1><p>Web UI build incomplete. API at /healthz, /incidents, /metrics.</p></body></html>' > $(WEB_OUT)/index.html; \
	fi
	@echo "==> Web UI built and embedded."

.PHONY: web-dev
web-dev:
	@cd $(WEB_DIR) && npm run dev

.PHONY: web-clean
web-clean:
	@rm -rf $(WEB_DIR)/node_modules $(WEB_DIR)/.next $(WEB_OUT)

# ─── Test ───────────────────────────────────────────────────────────────

.PHONY: test
test:
	@echo "==> Running tests with race detector..."
	$(GO) test ./... -race -count=1

.PHONY: test-verbose
test-verbose:
	$(GO) test ./... -race -v -count=1

.PHONY: test-coverage
test-coverage:
	$(GO) test ./... -race -coverprofile=coverage.out -count=1
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "==> Coverage report: coverage.html"

# ─── Lint / vet / security ──────────────────────────────────────────────

.PHONY: lint
lint:
	@golangci-lint run --timeout=5m

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: security
security:
	@$(GO) install golang.org/x/vuln/cmd/govulncheck@latest 2>/dev/null || true
	@govulncheck ./...

# ─── Run ────────────────────────────────────────────────────────────────

.PHONY: run
run: build-no-web
	./$(BUILD_DIR)/$(BINARY) --addr :8000 --db /tmp/sentinel.db

.PHONY: dev
dev: build-no-web
	SENTINEL_LOG_LEVEL=DEBUG ./$(BUILD_DIR)/$(BINARY) --addr :8000 --db /tmp/sentinel-dev.db

# ─── Docker ─────────────────────────────────────────────────────────────

.PHONY: docker
docker:
	docker build -t $(DOCKER_IMAGE):$(VERSION) --build-arg VERSION=$(VERSION) .

# ─── Helm ───────────────────────────────────────────────────────────────

.PHONY: helm-lint
helm-lint:
	helm lint chart/

.PHONY: helm-template
helm-template:
	helm template test chart/ > /tmp/sentinel-rendered.yaml
	@test $$(grep -c "kind:" /tmp/sentinel-rendered.yaml) -ge 6 && echo "Chart OK" || (echo "Chart FAILED" && exit 1)

# ─── Versioning ─────────────────────────────────────────────────────────

.PHONY: bump-version
bump-version:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make bump-version VERSION=x.y.z"; exit 1; fi
	@python3 scripts/sync-version.py --bump $(VERSION)
	@echo "==> Synced all files to $(VERSION). Review with 'git diff'."

.PHONY: check-version
check-version:
	@python3 scripts/sync-version.py --check

.PHONY: release
release:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=x.y.z"; exit 1; fi
	@echo "==> Syncing all files to $(VERSION)..."
	@python3 scripts/sync-version.py --bump $(VERSION)
	@git add -A
	@git commit -m "release: v$(VERSION)" || echo "(nothing to commit)"
	@git tag v$(VERSION)
	@git push origin main v$(VERSION)
	@echo "==> Release triggered. CI will build + publish image + Helm chart + GitHub Release."

# ─── Install ────────────────────────────────────────────────────────────

.PHONY: install-cli
install-cli: build-cli
	@sudo cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/
	@echo "==> Installed. Run 'sentinelctl --help' to verify."

# ─── Cleanup ────────────────────────────────────────────────────────────

.PHONY: clean
clean:
	@rm -rf $(BUILD_DIR) $(WEB_OUT) $(WEB_DIR)/.next coverage.out coverage.html

# ─── CI ─────────────────────────────────────────────────────────────────

.PHONY: ci
ci: lint test vet helm-lint check-version
	@echo "==> CI checks passed."

.PHONY: help
help:
	@echo "Sentinel — autonomous AI SRE"
	@echo ""
	@echo "Targets:"
	@echo "  build          Build the sentinel binary (includes web UI)"
	@echo "  build-cli      Build the sentinelctl CLI"
	@echo "  build-all      Build both binaries"
	@echo "  build-no-web   Build without rebuilding the web UI"
	@echo "  web            Build the web UI (Next.js static export)"
	@echo "  web-dev        Start the Next.js dev server"
	@echo "  test           Run tests with race detector"
	@echo "  test-coverage  Run tests + emit coverage.html"
	@echo "  lint           Run golangci-lint"
	@echo "  vet            Run go vet"
	@echo "  security       Run govulncheck"
	@echo "  run            Build (no web) and run locally"
	@echo "  dev            Build (no web) and run with DEBUG logging"
	@echo "  docker         Build the Docker image"
	@echo "  helm-lint      Validate the Helm chart"
	@echo "  release        Bump + commit + tag + push (VERSION=x.y.z)"
	@echo "  bump-version   Sync VERSION across all files (VERSION=x.y.z)"
	@echo "  check-version  CI mode: fail if version drift detected"
	@echo "  install-cli    Install sentinelctl to /usr/local/bin"
	@echo "  clean          Remove build artifacts"
	@echo "  ci             Run all CI checks"
