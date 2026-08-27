# syntax=docker/dockerfile:1
# Multi-stage build for qrypt.
#
# Build for current platform:
#   docker build -t qrypt .
#
# Cross-compile for a specific Linux arch (requires buildx + QEMU):
#   docker buildx build --platform linux/amd64 -t qrypt .
#   docker buildx build --platform linux/arm64 -t qrypt .

# ---- Build stage ----
# Debian-based build: its libfuse-dev ships libfuse.a, so the Linux binaries
# link statically (glibc) and run on any distribution, musl or glibc hosts
# alike. (Alpine's fuse-dev only provides the shared library.)
FROM golang:1.27 AS build

RUN apt-get update && apt-get install -y --no-install-recommends gcc libfuse-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/cgofuse/go.mod third_party/cgofuse/go.mod
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=
ARG BUILD_TIME=
ARG DIRTY=false
# netgo: keep DNS in Go's own resolver; glibc's NSS/dlopen path SIGFPEs from a
# statically linked executable (seen at mount startup). staticfuse: link the
# vendored cgofuse against libfuse2 directly instead of dlopening libfuse.so.2
# at runtime — glibc's dlopen itself SIGFPEs (div by zero in dl_open_worker)
# when called from this static build, so both uses of the dynamic loader must
# go. FUSE stays cgo; only loading changes.
RUN CGO_ENABLED=1 go build -tags netgo,staticfuse \
    -ldflags="-s -w -extldflags=-static -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildVersion=${VERSION} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildCommit=${COMMIT} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildTime=${BUILD_TIME} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildDirty=${DIRTY}" \
    -o /usr/local/bin/qrypt ./cmd/qrypt/

# ---- Runtime stage ----
# The binary carries its own glibc and libfuse (fully static), so a minimal
# runtime suffices.
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=build /usr/local/bin/qrypt /usr/local/bin/qrypt

ENTRYPOINT ["/usr/local/bin/qrypt"]
