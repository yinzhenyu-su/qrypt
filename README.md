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

## Configuration

### Top-level Options

`mount_point`, `cache_dir`, `volume_name`, `read_only`, `allow_other`,
`default_permissions`, `no_apple_double`, and `no_apple_xattr` are
top-level fields: the program creates one OS mount point, and each
`[[mounts]]` entry appears as a directory under it. Each mount stores cache
under `cache_dir/<mount-name>` unless `[mounts.cache].dir` overrides that
mount.

| Option | Description | Default |
|---|---|---|
| `mount_point` | FUSE mount point path | — |
| `cache_dir` | Root cache directory | OS temp dir |
| `volume_name` | FUSE volume name shown in Finder | `"Qrypt"` |
| `read_only` | Mount read-only, reject write callbacks | `false` |
| `allow_other` | Allow other users to access the mount | `false` |
| `default_permissions` | Kernel-enforced mode/uid/gid checks | `false` |
| `no_apple_double` | Ignore `._*` / `.DS_Store` uploads | `true` |
| `no_apple_xattr` | Ignore `com.apple.*` extended attributes | `false` |
| `attr_timeout` | FUSE attribute cache timeout | `"1s"` |
| `entry_timeout` | FUSE entry cache timeout | `"1s"` |
| `negative_timeout` | FUSE negative lookup cache timeout | `"0s"` |
| `total_space` | Reported total capacity (bytes or suffix) | — |
| `free_space` | Reported free capacity (bytes or suffix) | — |

When `no_apple_double = true`, Finder/macOS metadata writes such as
`.DS_Store`, `._*`, `.Spotlight-V100`, `.Trashes`, and `.fseventsd` are
accepted by the FUSE layer but ignored by the backend upload path. Set it to
`false` to upload those files like regular files.

`total_space` and `free_space` accept plain bytes or binary size suffixes such
as `512M`, `1G`, and `1T`.

### Mount Entries

Each `[[mounts]]` entry binds a cloud drive backend to a subdirectory of the
mount point. The first path segment is the mount name:

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
name = "mydrive"
type = "quark"

[mounts.params]
cookie = "your-quark-cookie"
```

**Driver-specific parameters** (`[mounts.params]`) vary by backend type.
Use the CLI to list them:

```sh
# List all available drivers
qrypt help

# Show parameters for a specific driver
qrypt help driver quark
qrypt help driver localfs
qrypt help driver aliyundrive
```

### Per-Mount Encryption

Each mount can optionally encrypt filenames and content using rclone-compatible
settings:

```toml
[mounts.encryption]
password = "secret"
salt = ""
filename_encryption = "standard"   # standard | off | obfuscate
filename_encoding = "base32"       # base32 | base64
```

Setting `filename_encryption = "off"` only keeps names readable on the raw
backend; file content is still encrypted when `password` is set.

With that config:

```sh
go run ./cmd/qrypt -config ./qrypt.toml list /
go run ./cmd/qrypt -config ./qrypt.toml put ./README.md /mydrive/README.md
go run ./cmd/qrypt -config ./qrypt.toml cat /mydrive/README.md
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

To debug Finder or macFUSE behavior, set debug logging in the config:

```toml
[logging]
log_level = "debug"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"
```

The debug log records callback names, paths, return codes, and durations for
operations such as `Getattr`, `Statfs`, `Readdir`, `Getxattr`, `Open`, `Read`,
`Write`, `Rename`, and `Truncate`.

For offline journal checks, live debug socket endpoints, upload progress,
cache state, filename mapping, and consistency checks, see
[`docs/debug.md`](docs/debug.md). To add a new backend, see
[`docs/driver-development.md`](docs/driver-development.md).
