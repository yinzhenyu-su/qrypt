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

## Requirements

| Dependency | macOS | Linux | Windows |
|---|---|---|---|
| FUSE | [macFUSE](https://macfuse.io/) | libfuse (usually pre-installed) | [WinFsp](https://winfsp.dev/) |
| Go (source build only) | 1.26+ | 1.26+ | 1.26+ |

The `fs` commands (list, cat, get, put) do not require FUSE — only `mount` does.
Config files are discovered automatically; you can skip `--config` when the file
is at one of the standard paths (see [CLI Reference](docs/for-user/cli.md)).

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

- [Quickstart](docs/for-user/quickstart.md) — minimal config, encryption demo, Windows notes
- [CLI Reference](docs/for-user/cli.md) — commands, arguments, config discovery paths
- [Full Config Reference](docs/for-user/full-config.md) — all configuration options
- [Supported Drivers](docs/for-user/support-drivers.md) — driver parameters and examples

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
