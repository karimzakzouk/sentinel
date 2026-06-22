# syntax=docker/dockerfile:1.7
# v3 Dockerfile — optimized for fast CI builds.
# Single-arch (amd64), BuildKit cache for Go modules, distroless runtime.
# Build time: ~3 min cold, ~1 min cached.

# ── Builder stage: compile the Go binary ───────────────────────────────────
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go.mod + go.sum first (cacheable layer)
COPY v3/go.mod v3/go.sum ./v3/

# Download deps with BuildKit cache mount — survives across builds
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd v3 && go mod download

# Copy source + build
COPY v3 ./v3

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd v3 && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-s -w -extldflags "-static"' \
    -trimpath \
    -o /sentinel ./cmd/sentinel

# ── Runtime stage: distroless, ~25MB ──────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# CA certs for HTTPS calls to GitHub / LLM / Docker Hub
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# The binary
COPY --from=builder /sentinel /sentinel

# Storage volume for SQLite
VOLUME ["/data"]

USER nonroot:nonroot

EXPOSE 8000

ENTRYPOINT ["/sentinel"]
