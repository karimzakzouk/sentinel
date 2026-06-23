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
VERSION ?= dev
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
LDFLAGS := -s -w -X main.Version=$(VERSION)
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
	@cd $(WEB_DIR) && npm run build:static
	@echo "==> Web UI built to $(WEB_OUT)/"

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
release:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=3.6.0"; exit 1; fi
	@echo "==> Tagging v$(VERSION)..."
	git tag v$(VERSION)
	git push origin v$(VERSION)
	@echo "==> Release triggered. CI will build and publish."

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
