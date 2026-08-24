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
FROM golang:1.27-alpine AS build

RUN apk add --no-cache fuse-dev gcc musl-dev

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
    -ldflags="-s -w -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildVersion=${VERSION} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildCommit=${COMMIT} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildTime=${BUILD_TIME} -X github.com/yinzhenyu/qrypt/pkg/buildinfo.buildDirty=${DIRTY}" \
    -o /usr/local/bin/qrypt ./cmd/qrypt/

# ---- Runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache fuse ca-certificates tzdata

COPY --from=build /usr/local/bin/qrypt /usr/local/bin/qrypt

ENTRYPOINT ["/usr/local/bin/qrypt"]
