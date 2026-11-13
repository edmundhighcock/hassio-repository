# Same conventions as virtual_sensors/Dockerfile: Supervisor 2026.04+ no
# longer passes BUILD_FROM, so both stages are named explicitly.
#
# Stage 1 (builder): pinned golang:1.27.3-alpine3.24, CGO disabled, static
# binary, -trimpath -ldflags="-s -w". `-p 1` caps Go's build parallelism at
# one package at a time: the Supervisor builds this image ON the Pi, and a
# wide-parallel build can spike several hundred MB of compiler memory.
# No cross-compilation: GOARCH defaults to the build host's own.
FROM golang:1.27.3-alpine3.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -p 1 -trimpath -ldflags="-s -w" \
    -o /out/netconsole_receiver ./cmd/netconsole_receiver

# Stage 2 (runtime): the generic multi-arch Home Assistant base image, as
# in virtual_sensors (publishes linux/amd64 and linux/arm64 under this tag).
# The binary is exec'd directly by run.sh; the base's s6 services directory
# is not used.
FROM ghcr.io/home-assistant/base:3.23-2026.10.3
COPY --from=builder /out/netconsole_receiver /usr/bin/netconsole_receiver
COPY rootfs/ /
RUN chmod a+x /run.sh

ENTRYPOINT []
CMD ["/run.sh"]
