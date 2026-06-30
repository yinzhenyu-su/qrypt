# qrypt build helpers
#
# Single platform:
#   make build                    # native (requires FUSE headers)
#   make linux/amd64              # Docker, Linux amd64
#   make linux/arm64              # Docker, Linux arm64
#   make darwin/amd64             # native, macOS Intel
#   make darwin/arm64             # native, macOS Apple Silicon
#   make windows/amd64            # Docker + mingw, Windows amd64
#
# All platforms:
#   make dist

GO ?= go
DIST_DIR ?= dist
IMAGE ?= qrypt
DOCKER_BUILDX_CACHE_FROM ?=
DOCKER_BUILDX_CACHE_TO   ?=

.PHONY: build dist mkdist clean

build:
	$(GO) build -ldflags="-s -w" -o qrypt ./cmd/qrypt/

dist: mkdist linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64
	@echo "--- all platforms ---"
	ls -lh $(DIST_DIR)/

mkdist:
	mkdir -p $(DIST_DIR)

# ── Linux (Docker) ──────────────────────────────────────────────────

linux/amd64: mkdist
	docker buildx build $(DOCKER_BUILDX_CACHE_FROM) $(DOCKER_BUILDX_CACHE_TO) \
		--platform linux/amd64 --load -t $(IMAGE):amd64 .
	docker create --name qrypt-linux-amd64 $(IMAGE):amd64
	docker cp qrypt-linux-amd64:/usr/local/bin/qrypt $(DIST_DIR)/qrypt-linux-amd64
	docker rm qrypt-linux-amd64

linux/arm64: mkdist
	docker buildx build $(DOCKER_BUILDX_CACHE_FROM) $(DOCKER_BUILDX_CACHE_TO) \
		--platform linux/arm64 --load -t $(IMAGE):arm64 .
	docker create --name qrypt-linux-arm64 $(IMAGE):arm64
	docker cp qrypt-linux-arm64:/usr/local/bin/qrypt $(DIST_DIR)/qrypt-linux-arm64
	docker rm qrypt-linux-arm64

# ── Windows (Docker + mingw-w64) ────────────────────────────────────

windows/amd64: mkdist
	docker build -f Dockerfile.windows -t $(IMAGE):windows .
	docker create --name qrypt-win --entrypoint /qrypt-windows-amd64.exe $(IMAGE):windows
	docker cp qrypt-win:/qrypt-windows-amd64.exe $(DIST_DIR)/qrypt-windows-amd64.exe
	docker rm qrypt-win

# ── macOS (native) ──────────────────────────────────────────────────

darwin/amd64: mkdist
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -ldflags="-s -w" -o $(DIST_DIR)/qrypt-darwin-amd64 ./cmd/qrypt/

darwin/arm64: mkdist
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -ldflags="-s -w" -o $(DIST_DIR)/qrypt-darwin-arm64 ./cmd/qrypt/

# ── Container registry ──────────────────────────────────────────────

docker-push:
	docker buildx build $(DOCKER_BUILDX_CACHE_FROM) \
		--platform linux/amd64,linux/arm64 -t $(IMAGE):latest --push .

# ── Clean ───────────────────────────────────────────────────────────

clean:
	rm -rf $(DIST_DIR) qrypt
