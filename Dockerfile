# syntax=docker/dockerfile:1
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/karimzakzouk/sentinel/internal/version.Version=${VERSION}" \
      -o /out/sentinel ./cmd/sentinel

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/sentinel /sentinel
USER 65532:65532
ENTRYPOINT ["/sentinel"]
