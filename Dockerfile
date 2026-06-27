# syntax=docker/dockerfile:1.7
# Sentinel v3.12.0 Dockerfile — includes embedded web UI.
# Multi-stage: Node.js builds the dashboard → Go compiles with embedded assets.
# Multi-arch: builds natively on amd64 + arm64 runners (no QEMU).
# Build time: ~4 min cold, ~2 min cached.

# ── Stage 1: Build the web UI (Next.js static export) ──────────────────────
FROM node:26-alpine AS web-builder

WORKDIR /web

# Copy package files first (cacheable)
COPY web/package.json web/package-lock.json* ./

# v3.18.0: M41 — use `npm ci` (strict lockfile mode). Was:
#   npm install --frozen-lockfile 2>/dev/null || npm install
# which silently fell back to `npm install` on lockfile drift, regenerating the
# lockfile and hiding dependency mismatches. `npm ci` fails loudly on drift.
RUN npm ci

# Copy web source and build
COPY web/ .

# v3.18.0: H23 + M42 — let the build fail loudly. Was: piped through `|| true`
# + a placeholder HTML fallback that masked real CI failures (broken JS bundle
# produced a green build with no dashboard). Now if `next build` fails OR
# produces no out/index.html, the Docker build fails.
RUN npx next build

# v3.18.0: M42 — fail loudly if the build didn't produce output.
RUN test -f out/index.html || (echo "ERROR: next build did not produce out/index.html" && exit 1)

# Copy built files to output directory
RUN mkdir -p /out && cp -r out/* /out/

# ── Stage 2: Compile the Go binary with embedded web UI ────────────────────
FROM golang:1.26.4-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go.mod + go.sum first (cacheable layer)
COPY go.mod go.sum ./

# Download deps with BuildKit cache mount
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy source
COPY . .

# Copy the built web UI into the embed directory (overwrites placeholder)
COPY --from=web-builder /out/ internal/web/out/

# v3.8.4: VERSION comes from the VERSION file (single source of truth).
# The build arg still exists for backward compatibility with CI that
# passes --build-arg VERSION=X.Y.Z, but defaults to reading VERSION.
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -extldflags '-static' -X main.Version=${VERSION} -X github.com/karimzakzouk/sentinel/v3/internal/version.Version=${VERSION}" \
    -trimpath \
    -o /sentinel ./cmd/sentinel

# Also build the CLI
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X github.com/karimzakzouk/sentinel/v3/internal/version.Version=${VERSION}" \
    -trimpath \
    -o /sentinelctl ./cmd/sentinelctl

# ── Stage 3: Runtime (distroless, 43MB) ──────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# CA certs for HTTPS calls to GitHub / LLM / Docker Hub
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# The binary (includes embedded web UI)
COPY --from=builder /sentinel /sentinel

# The CLI binary
COPY --from=builder /sentinelctl /sentinelctl

# Storage volume for SQLite
VOLUME ["/data"]

USER nonroot:nonroot

EXPOSE 8000

ENTRYPOINT ["/sentinel"]
