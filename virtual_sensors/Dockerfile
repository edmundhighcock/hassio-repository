# Supervisor 2026.04+ no longer passes BUILD_FROM: name both stages
# explicitly (see influxdb2/Dockerfile for the same convention).
#
# Stage 1 (builder): pinned golang:1.27.3-alpine3.24, CGO disabled, static
# binary, -trimpath -ldflags="-s -w" to drop debug info/symbols (smaller
# binary, nothing to profile on a production Pi anyway). `-p 1` caps Go's
# own build parallelism at one package at a time - a wide-parallel `go
# build` briefly spiking several hundred MB of compiler memory has been
# observed to cause trouble on a 4GB Pi already running everything else;
# one package at a time is slower but bounded.
#
# No --platform/TARGETARCH cross-compilation: the Supervisor builds this
# image ON the Pi, natively, and its builder is not guaranteed to set
# BuildKit's BUILDPLATFORM (an empty one breaks the FROM line). GOARCH is
# left to default to the build host's own architecture.
FROM golang:1.27.3-alpine3.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -p 1 -trimpath -ldflags="-s -w" \
    -o /out/virtual_sensors ./cmd/virtual_sensors

# Stage 2 (runtime): ghcr.io/home-assistant/base:3.23-2026.10.3 - verified
# on 2026-11-04 to publish both linux/amd64 and linux/arm64 manifests
# under this tag (ghcr.io/v2/home-assistant/base/manifests/3.23-2026.10.3
# OCI image index). This is the generic multi-arch base (not the
# per-arch aarch64-base/amd64-base images static_sites/build.yaml uses),
# which is exactly why no build.yaml or `ARG BUILD_FROM` is needed here -
# one FROM line covers every arch in this add-on's config.yaml `arch:`
# list. Ships s6-overlay v3; this add-on's own CMD below bypasses it
# entirely (no s6 services directory), same as influxdb2 and static_sites
# both do with their own run.sh.
FROM ghcr.io/home-assistant/base:3.23-2026.10.3
COPY --from=builder /out/virtual_sensors /usr/bin/virtual_sensors
COPY rootfs/ /
RUN chmod a+x /run.sh

ENTRYPOINT []
CMD ["/run.sh"]
