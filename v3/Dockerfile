# ── Builder stage: compile the Go binary ───────────────────────────────────
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Cache deps first
COPY v3/go.mod v3/go.sum ./v3/
RUN cd v3 && go mod download

# Build (multi-arch: respects TARGETARCH from buildx)
COPY v3 ./v3
ARG TARGETARCH
RUN cd v3 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags='-s -w -extldflags "-static"' \
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
