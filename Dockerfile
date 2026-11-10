# syntax=docker/dockerfile:1
FROM node:22.23.4-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui ./
RUN npm run build

FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui /src/internal/ui/dist ./internal/ui/dist
COPY hack/grammar-tags ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -tags "$(cat grammar-tags)" \
      -ldflags "-s -w -X github.com/karimzakzouk/sentinel/internal/version.Version=${VERSION}" \
      -o /out/sentinel ./cmd/sentinel

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/sentinel /sentinel
USER 65532:65532
ENTRYPOINT ["/sentinel"]
