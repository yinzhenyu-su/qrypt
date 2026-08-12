# qrypt build helpers
#
# Single platform:
#   make build                    # native (requires FUSE headers)
#   make linux/amd64              # Docker, Linux amd64
#   make linux/arm64              # Docker, Linux arm64
#   make darwin/amd64             # native, macOS Intel
#   make darwin/arm64             # native, macOS Apple Silicon
#   make windows/amd64            # Docker + mingw, Windows amd64
#   make windows/arm64            # pure Go cross-compile (nocgo)
#
# All platforms:
#   make dist

GO ?= go
DIST_DIR ?= dist
IMAGE ?= qrypt
DOCKER_BUILDX_CACHE_FROM ?=
DOCKER_BUILDX_CACHE_TO   ?=
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || true)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
DIRTY ?= $(shell test -n "$$(git status --porcelain 2>/dev/null)" && echo true || echo false)
LDFLAGS ?= -s -w -X github.com/yinzhenyu/qrypt/internal/cli.buildVersion=$(VERSION) -X github.com/yinzhenyu/qrypt/internal/cli.buildCommit=$(COMMIT) -X github.com/yinzhenyu/qrypt/internal/cli.buildTime=$(BUILD_TIME) -X github.com/yinzhenyu/qrypt/internal/cli.buildDirty=$(DIRTY)

.PHONY: build dist mkdist clean

build: mkdist
	$(GO) build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/qrypt ./cmd/qrypt/

dist: mkdist linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64
	@echo "--- all platforms ---"
	ls -lh $(DIST_DIR)/

mkdist:
	mkdir -p $(DIST_DIR)

# ── Linux (Docker) ──────────────────────────────────────────────────

linux/amd64: mkdist
	docker buildx build $(DOCKER_BUILDX_CACHE_FROM) $(DOCKER_BUILDX_CACHE_TO) \
		--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_TIME="$(BUILD_TIME)" --build-arg DIRTY="$(DIRTY)" \
		--platform linux/amd64 --load -t $(IMAGE):amd64 .
	docker create --name qrypt-linux-amd64 $(IMAGE):amd64
	docker cp qrypt-linux-amd64:/usr/local/bin/qrypt $(DIST_DIR)/qrypt-linux-amd64
	docker rm qrypt-linux-amd64

linux/arm64: mkdist
	docker buildx build $(DOCKER_BUILDX_CACHE_FROM) $(DOCKER_BUILDX_CACHE_TO) \
		--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_TIME="$(BUILD_TIME)" --build-arg DIRTY="$(DIRTY)" \
		--platform linux/arm64 --load -t $(IMAGE):arm64 .
	docker create --name qrypt-linux-arm64 $(IMAGE):arm64
	docker cp qrypt-linux-arm64:/usr/local/bin/qrypt $(DIST_DIR)/qrypt-linux-arm64
	docker rm qrypt-linux-arm64

# ── Windows (Docker + mingw-w64) ────────────────────────────────────

windows/amd64: mkdist
	docker build -f Dockerfile.windows --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_TIME="$(BUILD_TIME)" --build-arg DIRTY="$(DIRTY)" -t $(IMAGE):windows .
	docker create --name qrypt-win --entrypoint /qrypt-windows-amd64.exe $(IMAGE):windows
	docker cp qrypt-win:/qrypt-windows-amd64.exe $(DIST_DIR)/qrypt-windows-amd64.exe
	docker rm qrypt-win

# ── Windows arm64 (nocgo, pure Go cross-compile) ────────────────────

windows/arm64: mkdist
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -tags nocgo -ldflags="$(LDFLAGS)" \
		-o $(DIST_DIR)/qrypt-windows-arm64.exe ./cmd/qrypt/

# ── macOS (native) ──────────────────────────────────────────────────

darwin/amd64: mkdist
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/qrypt-darwin-amd64 ./cmd/qrypt/

darwin/arm64: mkdist
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/qrypt-darwin-arm64 ./cmd/qrypt/

# ── Container registry ──────────────────────────────────────────────

docker-push:
	docker buildx build $(DOCKER_BUILDX_CACHE_FROM) \
		--platform linux/amd64,linux/arm64 -t $(IMAGE):latest --push .

# ── Clean ───────────────────────────────────────────────────────────

clean:
	rm -rf $(DIST_DIR) qrypt
