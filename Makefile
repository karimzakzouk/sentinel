GO ?= go
# v1 release tags still exist in this repository; v2 builds describe themselves from v2 tags only.
VERSION ?= $(shell git describe --tags --match 'v2.*' --always --dirty 2>/dev/null || echo dev)
IMAGE ?= sentinel:dev
E2E_CLUSTER ?= sentinel-e2e
E2E_TIMEOUT ?= 45m
LDFLAGS := -s -w -X github.com/karimzakzouk/sentinel/internal/version.Version=$(VERSION)

.PHONY: all build test lint fmt vuln generate verify image helm-lint scenarios e2e-up e2e e2e-down tidy boutique-up delivery-up score ui

all: build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	$(GO) test -race -count=1 ./...

lint:
	golangci-lint run --build-tags e2e ./...

fmt:
	golangci-lint fmt ./...

vuln:
	$(GO) tool govulncheck ./...

scenarios:
	$(GO) run ./cmd/harness validate test/scenarios/*/*.yaml

generate:
	$(GO) tool controller-gen object paths=./api/...
	$(GO) tool controller-gen crd paths=./api/... output:crd:artifacts:config=charts/sentinel-crds/files

tidy:
	$(GO) mod tidy

# Fails when generated files or go.mod are out of date.
verify: generate tidy
	git diff --exit-code

image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

helm-lint:
	helm lint charts/sentinel-crds
	helm lint charts/sentinel --set watch.namespaces={default}
	helm lint charts/sentinel-fence

e2e-up:
	E2E_CLUSTER=$(E2E_CLUSTER) IMAGE=$(IMAGE) hack/e2e-up.sh

# E2E_RUN narrows the suite to the tests a regular expression names, and
# E2E_SKIP leaves out those another names.
e2e:
	E2E_CLUSTER=$(E2E_CLUSTER) $(GO) test -v -count=1 -timeout $(E2E_TIMEOUT) -tags e2e $(if $(E2E_RUN),-run '$(E2E_RUN)') $(if $(E2E_SKIP),-skip '$(E2E_SKIP)') ./test/e2e/...

e2e-down:
	kind delete cluster --name $(E2E_CLUSTER)

boutique-up:
	E2E_CLUSTER=$(E2E_CLUSTER) hack/boutique-up.sh

# Workloads Argo CD and Flux deliver, for the GitOps acting session.
delivery-up:
	E2E_CLUSTER=$(E2E_CLUSTER) hack/delivery-up.sh

# Build the web UI into internal/ui/dist, where the binary embeds it.
ui:
	cd ui && npm ci && npm run build

# Score the scenario runs since SINCE (RFC 3339) against Sentinel's incidents.
score: build
	E2E_CLUSTER=$(E2E_CLUSTER) hack/score.sh $(SINCE)
