[中文版](README.zh.md)

# qrypt

[![CI](https://github.com/yinzhenyu-su/qrypt/actions/workflows/ci.yaml/badge.svg)](https://github.com/yinzhenyu-su/qrypt/actions/workflows/ci.yaml)
[![Contract Tests](https://github.com/yinzhenyu-su/qrypt/actions/workflows/contract.yaml/badge.svg)](https://github.com/yinzhenyu-su/qrypt/actions/workflows/contract.yaml)
[![Nightly Quality Gates](https://github.com/yinzhenyu-su/qrypt/actions/workflows/nightly.yaml/badge.svg)](https://github.com/yinzhenyu-su/qrypt/actions/workflows/nightly.yaml)
[![Go](https://img.shields.io/github/go-mod/go-version/yinzhenyu-su/qrypt)](https://go.dev)
[![License](https://img.shields.io/github/license/yinzhenyu-su/qrypt)](LICENSE)
[![Release](https://img.shields.io/github/v/release/yinzhenyu-su/qrypt)](https://github.com/yinzhenyu-su/qrypt/releases)
[![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-blue)](#requirements)

qrypt turns your cloud drives into one encrypted local folder — mount it, open it, use it like any other drive.

![Encryption comparison](artifacts/compare.png)

## Introduction

qrypt maps your cloud drives into a single encrypted local folder. Once mounted, you can browse and edit files as if they were on a local disk.

- **Single mount point** — all drives appear under one directory, each in its own subdirectory
- **Encrypted storage** — filenames and content are encrypted before they reach the cloud, compatible with rclone crypt
- **Staged writes and local cache** — writes land locally first and upload in the background; repeated reads hit the cache
- **Cross-platform** — macOS, Linux, and Windows
- **Works without mounting** — `qrypt fs` reads and writes remote files directly, useful in scripts and on servers

## Install

**Linux / macOS**

```sh
curl -fsSL https://github.com/yinzhenyu-su/qrypt/releases/latest/download/install.sh | sh
```

**Windows** (run in any terminal; no need to open PowerShell first)

```powershell
powershell -c "irm https://github.com/yinzhenyu-su/qrypt/releases/latest/download/install.ps1|iex"
```

The installer picks the right build for your system and needs no administrator
rights. Open a new terminal afterwards and run `qrypt version` to confirm.

Update or remove qrypt later with `qrypt upgrade` and `qrypt uninstall` (see the
[quickstart](docs/for-user/quickstart.md)).

## Quick Start

The example below uses a local directory, so no cloud account is required. It
covers configuration, upload, read, and encryption.

### 1. Create a config file

```toml
version = "1"

[mount]
mount_point = "~/Qrypt"

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root_path = "/tmp/qrypt-data"

[mounts.encryption]
password = "my-password"
filename_encryption = "standard"
filename_encoding = "base32"
```

### 2. Upload and read a file

```bash
mkdir -p /tmp/qrypt-data
echo "hello qrypt" > /tmp/hello.txt

qrypt fs list /
qrypt fs put /tmp/hello.txt /local/hello.txt
qrypt fs cat /local/hello.txt
```

The last command prints `hello qrypt`.

### 3. Inspect the encryption

```bash
ls /tmp/qrypt-data
```

The filename is now an obfuscated string such as
`b4l6gr1s6t1q0tas6dl0q0mb0s62kbj0`, and the file content is encrypted as well.

## Mount as a Local Folder (Optional)

```bash
qrypt mount
```

Open `~/Qrypt` in your file manager and use it like any other folder: drag files
in, open them, delete them.

> Only `mount` needs FUSE: install [macFUSE](https://macfuse.io/) on macOS or
> [WinFsp](https://winfsp.dev/) on Windows. The `fs` commands do not need it.

Detailed walkthrough (including Windows notes) →
[Quickstart](docs/for-user/quickstart.md)

## Requirements

| Dependency             | macOS                          | Linux                           | Windows                       |
| ---------------------- | ------------------------------ | ------------------------------- | ----------------------------- |
| FUSE                   | [macFUSE](https://macfuse.io/) | libfuse3 / fuse3                | [WinFsp](https://winfsp.dev/) |
| Go (source build only) | 1.27+                          | 1.27+                           | 1.27+                         |

Config files are discovered automatically: `./qrypt.toml`,
`~/.qrypt/qrypt.toml`, then `$XDG_CONFIG_HOME/qrypt/qrypt.toml` on Unix or
`%AppData%\qrypt\qrypt.toml` on Windows. You can skip `--config` when the file
is at one of these paths (see [CLI Reference](docs/for-user/cli.md)).

## User Documentation

- [Quickstart](docs/for-user/quickstart.md) — install, upgrade and uninstall, minimal config, a first run in about 5 minutes, encryption demo
- [CLI Reference](docs/for-user/cli.md) — commands, arguments, config discovery paths
- [Offline Download](docs/for-user/offline-download.md) — hand magnet links or downloads to the cloud drive
- [Troubleshooting](docs/for-user/troubleshooting.md) — stale mounts, upload queue, and other common issues
- [Full Config Reference](docs/for-user/full-config.md) — all configuration options
- [Supported Drivers](docs/for-user/support-drivers.md) — driver parameters and examples

## Developer Documentation

- [Architecture](docs/for-developer/architecture.md) — layer overview and design rules
- [Package boundaries](docs/for-developer/package-boundaries.md) — ownership, forbidden dependencies, and migration criteria
- [Driver Development](docs/for-developer/driver-development.md) — how to add a new cloud-drive backend
- [Debugging](docs/for-developer/debug.md) — diagnostic tools and troubleshooting
- [Fuzz Corpus](docs/for-developer/fuzz-corpus.md) — nightly fuzzing and regression corpus lifecycle
- [Quality Gates](docs/for-developer/quality-gates.md) — CI workflows, coverage gate, contract matrix
- [Developer FAQ](docs/for-developer/faq.md) — recurring implementation issues and fixes

## Building from Source

Requires Go 1.27+ and FUSE headers (libfuse3-dev on Linux, macFUSE on macOS).
Linux builds use FUSE3 by default; mounting also requires the fuse3 runtime and
`/dev/fuse`. Legacy FUSE2 builds can be produced with `-tags fuse2`.

```
git clone https://github.com/yinzhenyu-su/qrypt.git
cd qrypt
go build ./cmd/qrypt
```

## License

MIT
