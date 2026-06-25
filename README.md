# qrypt

qrypt is a Go implementation of an encrypted virtual filesystem for cloud
drives. The new architecture keeps the core file API independent from FUSE,
CLI, and concrete cloud-drive SDKs.

## Goals

- Compatible with rclone crypt naming and content encryption.
- Mount-capable architecture for cloud drives such as Baidu Netdisk, Aliyun
  Drive, 115, and other Alist-style providers.
- Reusable core with read caching, write staging, pending-operation recovery,
  and a desktop FUSE adapter boundary.

- 每一轮迭代都问这 4 个问题：

1. 这次是否让挂载更稳定？

2. 是否降低数据丢失风险？

3. 是否有可复现测试？

4. 是否保持模块边界清晰？

不满足这几个，就先别加新功能。

## Architecture

```text
cmd/qrypt                  CLI adapter
internal/mount             FUSE adapter boundary
internal/driver/*          concrete backend drivers
pkg/vfs                    platform-independent file API
pkg/crypt                  rclone-compatible crypt and encrypted driver wrapper
pkg/drive                  backend driver contracts and registry
```

Cloud-drive drivers should adapt provider APIs to `pkg/drive.Driver`,
`pkg/drive.Writer`, and `pkg/drive.Uploader`. The VFS layer only depends on
those interfaces.

## Current Backend

`localfs` is included as the first backend so the architecture can be tested
without real cloud credentials:

```sh
go run ./cmd/qrypt -root /tmp/qrypt-remote put ./README.md /README.md
go run ./cmd/qrypt -root /tmp/qrypt-remote list /
go run ./cmd/qrypt -root /tmp/qrypt-remote cat /README.md
```

Enable rclone-compatible encryption with:

```sh
go run ./cmd/qrypt -root /tmp/qrypt-remote -password secret put ./README.md /README.md
```

Encryption can also be read from TOML using the same field names as the old
project:

```toml
[encryption]
password = "secret"
salt = ""
filename_encryption = "standard" # standard | off | obfuscate
filename_encoding = "base32"     # base32 | base64
```

The CLI can override those values:

```sh
go run ./cmd/qrypt \
  -config ./qrypt.toml \
  -root /tmp/qrypt-remote \
  -salt "custom-salt" \
  -filename-encryption obfuscate \
  -filename-encoding base64 \
  list /
```

For old-style config files, `[defaults.encryption]` and
`[[mounts]].encryption` are also understood. Use `-mount-name NAME` to select
a specific mount's encryption section.

## Multiple Drives Under One Mount Point

When the config contains multiple `[[mounts]]`, qrypt exposes them under one
namespace. The first path segment is the mount name:

```text
~/Qrypt/quark
~/Qrypt/quark2
~/Qrypt/localfs
```

Example:

```toml
mount_point = "~/Qrypt"
cache_dir = "/tmp/qrypt-cache"
volume_name = "Qrypt"
no_apple_double = true
total_space = "1T"
free_space = "800G"

[[mounts]]
name = "quark"
type = "quark"

[mounts.params]
cookie = "your-quark-cookie"
root_path = "/"

[mounts.encryption]
password = "quark-password"
salt = "quark-salt"
filename_encryption = "standard"
filename_encoding = "base32"

[[mounts]]
name = "quark2"
type = "localfs"

[mounts.params]
root = "/tmp/qrypt-quark2"

[mounts.encryption]
password = "quark2-password"
salt = "quark2-salt"
filename_encryption = "obfuscate"
filename_encoding = "base64"

[[mounts]]
name = "localfs"
type = "localfs"

[mounts.params]
root = "/tmp/qrypt-localfs"

[mounts.encryption]
password = "localfs-password"
salt = ""
filename_encryption = "off"
filename_encoding = "base32"
```

Each mount has its own encryption settings. Setting
`filename_encryption = "off"` only keeps names readable on the raw backend;
file content is still encrypted when `password` is set.

`mount_point`, `cache_dir`, `volume_name`, and `no_apple_double` are
intentionally top-level: the program creates one OS mount point, and each
`[[mounts]]` entry appears as a directory under it. Each mount stores cache
under `cache_dir/<mount-name>` unless `[mounts.cache].dir` overrides that
mount.

`total_space` and `free_space` control the capacity reported by FUSE `Statfs`.
They accept plain bytes or binary size suffixes such as `512M`, `1G`, and `1T`.

With that config:

```sh
go run ./cmd/qrypt -config ./qrypt.toml list /
go run ./cmd/qrypt -config ./qrypt.toml put ./README.md /quark/README.md
go run ./cmd/qrypt -config ./qrypt.toml cat /localfs/README.md
```

To mount the namespace with FUSE:

```sh
go run ./cmd/qrypt -config ./qrypt.toml mount
```

If no mount point is passed, qrypt uses the top-level `mount_point` in the config.
You can also pass one explicitly:

```sh
go run ./cmd/qrypt -config ./qrypt.toml mount ~/Qrypt
```

The CLI `-cache` flag is still available as an override:

```sh
go run ./cmd/qrypt -config ./qrypt.toml -cache /tmp/other-qrypt-cache mount
```

To debug Finder or macFUSE behavior, enable FUSE tracing in the config:

```toml
[logging]
log_level = "info"
log_file = "~/.qrypt/qrypt.log"
```

The environment variables are still available as temporary overrides:

```sh
QRYPT_FUSE_TRACE=1 \
QRYPT_FUSE_TRACE_FILE=/tmp/qrypt-fuse.log \
go run ./cmd/qrypt -config ./qrypt.toml mount
```

The trace records callback names, paths, return codes, and durations for
operations such as `Getattr`, `Statfs`, `Readdir`, `Getxattr`, `Open`, `Read`,
`Write`, `Rename`, and `Truncate`.
