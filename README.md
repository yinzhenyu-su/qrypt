[中文版](README.zh.md)

# qrypt

qrypt turns your cloud drives into one encrypted local folder — mount it, open it, use it like any other drive.

## Features

- **FUSE mount** — mounts all configured drives as subdirectories under one local directory
- **rclone-compatible encryption** — filename obfuscation and content encryption compatible with rclone crypt
- **Pluggable cloud drive backends** — see [supported drivers](docs/for-user/support-drivers.md) for the full list
- **Local read cache** — caches remote file data locally for fast repeated access
- **Staged writes** — new files and modifications are written locally first, then uploaded asynchronously with configurable debounce, concurrency, and retry
- **Platform-native FUSE** — macOS (macFUSE), Linux (libfuse), Windows (WinFsp)
- **macOS friendly** — suppresses Apple Double metadata files and extended attributes clutter in Finder
- **Diagnostics** — debug socket with structured JSON reports, health tracking per mount, consistency checks, and staging inspection
- **Bandwidth control** — per-direction download and upload rate limiting
- **Config auto-discovery** — searches `./qrypt.toml`, `~/.qrypt/qrypt.toml`, then `$XDG_CONFIG_HOME/qrypt/qrypt.toml` on Unix or `%AppData%\qrypt\qrypt.toml` on Windows

## Quick Start

1. Download the [latest release](https://github.com/yinzhenyu-su/qrypt/releases) for your platform.
2. Create `qrypt.toml`:

```toml
mount_point = "~/Qrypt"

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root = "/tmp/qrypt-data"
```

3. Run:

```
./qrypt fs list /
```

Extended walkthrough (encryption demo, Windows notes) →
[docs/for-user/quickstart.md](docs/for-user/quickstart.md).

## User Documentation

- [快速上手](docs/for-user/quickstart.md) — 最小配置，5 分钟跑通，含加密效果演示
- [命令行参考](docs/for-user/cli.md) — 命令、参数、输入输出和配置查找规则
- [完整配置参考](docs/for-user/full-config.md) — 全部配置项说明
- [支持的驱动](docs/for-user/support-drivers.md) — 各云盘驱动的参数列表

## Developer Documentation

- [Architecture](docs/for-developer/architecture.md) — layer overview and design rules
- [Driver Development](docs/for-developer/driver-development.md) — how to add a new cloud-drive backend
- [Debugging](docs/for-developer/debug.md) — diagnostic tools and troubleshooting

## Building from Source

Requires Go 1.26+ and FUSE headers (libfuse-dev on Linux, macFUSE on macOS).

```
git clone https://github.com/yinzhenyu-su/qrypt.git
cd qrypt
go build ./cmd/qrypt
```

## License

MIT
