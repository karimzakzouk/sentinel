# hack/ci.sh builds the binary first, so the build pulls no toolchain image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY shop /shop
ENTRYPOINT ["/shop"]
