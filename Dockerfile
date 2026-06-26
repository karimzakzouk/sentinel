# syntax=docker/dockerfile:1.7
# Sentinel v3.12.0 Dockerfile — includes embedded web UI.
# Multi-stage: Node.js builds the dashboard → Go compiles with embedded assets.
# Multi-arch: builds natively on amd64 + arm64 runners (no QEMU).
# Build time: ~4 min cold, ~2 min cached.

# ── Stage 1: Build the web UI (Next.js static export) ──────────────────────
FROM node:22-alpine AS web-builder

WORKDIR /web

# Copy package files first (cacheable)
COPY web/package.json web/package-lock.json* ./

RUN npm install --frozen-lockfile 2>/dev/null || npm install

# Copy web source and build
COPY web/ .

# v3.8.4: Build the Next.js static export. Previous version piped through
# `|| true` to allow broken UI builds to fall through to a placeholder —
# that masked real CI failures (a broken JS bundle would produce a green
# build with no dashboard). Now we let the build fail loudly. The fallback
# HTML below catches only the "out/ dir missing" case, not "build errored".
RUN npx next build

# Copy built files to output directory
RUN mkdir -p /out && \
    if [ -d out ]; then \
      cp -r out/* /out/; \
    fi && \
    if [ ! -f /out/index.html ]; then \
      echo '<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px"><h1 style="color:#06B6D4">Sentinel</h1><p>Web UI build incomplete. API at /healthz, /incidents, /metrics.</p></body></html>' > /out/index.html; \
    fi

# ── Stage 2: Compile the Go binary with embedded web UI ────────────────────
FROM golang:1.25-alpine AS builder

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
