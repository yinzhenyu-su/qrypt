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
RUN CGO_ENABLED=1 go build \
    -ldflags="-s -w -extldflags=-static -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildVersion=${VERSION} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildCommit=${COMMIT} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildTime=${BUILD_TIME} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildDirty=${DIRTY}" \
    -o /usr/local/bin/qrypt ./cmd/qrypt/

# ---- Runtime stage ----
# The binary is statically linked, so a minimal runtime suffices.
FROM alpine:3.21

RUN apk add --no-cache fuse ca-certificates tzdata

COPY --from=build /usr/local/bin/qrypt /usr/local/bin/qrypt

ENTRYPOINT ["/usr/local/bin/qrypt"]
