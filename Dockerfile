# syntax=docker/dockerfile:1.7
# Sentinel — multi-stage build: Node.js web UI -> Go binary -> distroless runtime.
# Final image: 43MB, nonroot, read-only rootfs, no shell.

# ── Stage 1: Build the web UI ──────────────────────────────────────────────
FROM node:22-alpine AS web-builder

WORKDIR /web
COPY web/package.json web/package-lock.json* ./

RUN for i in 1 2 3; do \
        npm ci --legacy-peer-deps --fetch-retries=5 --fetch-retry-mintimeout=20000 && break; \
        echo "npm ci attempt $i failed, retrying in 10s..." >&2; \
        sleep 10; \
    done && test -d node_modules

COPY web/ .
RUN npx next build
RUN test -f out/index.html || (echo "ERROR: next build did not produce out/index.html" && exit 1)

# Pre-gzip static assets for smaller embed + faster serving.
RUN find out -type f \( -name '*.js' -o -name '*.css' -o -name '*.html' -o -name '*.txt' -o -name '*.svg' -o -name '*.woff2' \) -exec gzip -9 -k {} \;

RUN mkdir -p /out && cp -r out/* /out/

# ── Stage 2: Compile the Go binary ─────────────────────────────────────────
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .
COPY --from=web-builder /out/ internal/web/out/

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -extldflags '-static' -X main.Version=${VERSION} -X github.com/karimzakzouk/sentinel/internal/version.Version=${VERSION}" \
    -trimpath \
    -o /sentinel ./cmd/sentinel

# ── Stage 3: Runtime ──────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /sentinel /sentinel

VOLUME ["/data"]

USER nonroot:nonroot

EXPOSE 8000

ENTRYPOINT ["/sentinel"]
