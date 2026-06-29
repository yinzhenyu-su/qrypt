# qrypt

qrypt is an encrypted virtual filesystem for cloud drives. It mounts one local
namespace with FUSE, exposes each configured drive as a directory, and can wrap
each drive with rclone-compatible crypt encryption.

## Features

- rclone-compatible filename and content encryption
- one FUSE mount point containing multiple named drives
- read cache, staged writes, upload retry state, and pending upload recovery
- macOS-friendly FUSE behavior for Finder metadata and extended attributes
- driver boundary for local filesystems and cloud-drive backends

## Quick Start

Create `qrypt.toml`:

```toml
mount_point = "~/Qrypt"
cache_dir = "~/.cache/qrypt"
volume_name = "Qrypt"
no_apple_double = true
no_apple_xattr = true

[logging]
log_level = "info"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"

[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root = "/tmp/qrypt-remote"
```

Verify the config before mounting:

```sh
go run ./cmd/qrypt -config ./qrypt.toml list /
go run ./cmd/qrypt -config ./qrypt.toml put ./README.md /local/README.md
go run ./cmd/qrypt -config ./qrypt.toml cat /local/README.md
```

Mount it:

```sh
go run ./cmd/qrypt -config ./qrypt.toml mount
```

The mount point contains one directory per `[[mounts]]` entry:

```text
~/Qrypt/local
~/Qrypt/aliyun
~/Qrypt/quark
```

## Configuration

qrypt creates one OS mount point. Each `[[mounts]]` entry becomes a directory
under that mount point.

```toml
mount_point = "~/Qrypt"
cache_dir = "~/.cache/qrypt"
volume_name = "Qrypt"
read_only = false
no_apple_double = true
no_apple_xattr = true
attr_timeout = "1s"
entry_timeout = "1s"
negative_timeout = "0s"

[[mounts]]
name = "aliyun"
type = "aliyundrive"

[mounts.params]
refresh_token = "your-refresh-token"
drive_id = "your-drive-id"
root_id = "your-root-folder-id"

[mounts.encryption]
password = "secret"
salt = ""
filename_encryption = "standard"
filename_encoding = "base32"
```

`cache_dir` is shared by the namespace. Each mount stores its own cache under
`cache_dir/<mount-name>` unless that mount overrides `[mounts.cache].dir`.

## Drivers

Use `qrypt help driver <name>` for the exact parameter schema.

| Driver | Required params | Notes |
|---|---|---|
| `localfs` | `root` | Local directory backend, useful for testing |
| `aliyundrive` | `refresh_token`, `drive_id`, `root_id` | Aliyun Drive backend |
| `baidu_netdisk` | `refresh_token` | Baidu Netdisk backend; list, read, upload, metadata write, and space support |
| `quark` | `cookie` | Quark cloud drive backend |
| `139yun` | `authorization` | 139 cloud drive backend |
| `115` | `cookie` | 115 backend; read support is limited by provider behavior |
| `webdav` | `url`, `username`, `password` | Standard WebDAV backend; optional `root_path` |

```sh
go run ./cmd/qrypt help
go run ./cmd/qrypt help driver aliyundrive
go run ./cmd/qrypt help driver baidu_netdisk
go run ./cmd/qrypt help driver quark
go run ./cmd/qrypt help driver webdav
```

For `baidu_netdisk`, `use_online_api` defaults to `true` and uses the
OpenList-compatible online refresh API. If your token is a normal Baidu OAuth
refresh token, set `use_online_api = false` and provide `client_id` and
`client_secret`. `client_id` is the Baidu app API Key, and `client_secret` is
the app Secret Key; no sign key is required.

## Encryption

Encryption is configured per mount. The format is compatible with rclone crypt.

```toml
[mounts.encryption]
password = "secret"
salt = ""
filename_encryption = "standard" # standard | off | obfuscate
filename_encoding = "base32"     # base32 | base64
```

When `password` is set, file content is encrypted. Setting
`filename_encryption = "off"` only keeps remote filenames readable.

## Mount Options

Common top-level options:

| Option | Default | Description |
|---|---:|---|
| `mount_point` | required | FUSE mount point |
| `cache_dir` | OS temp dir | root cache directory |
| `volume_name` | `Qrypt` | Finder volume name |
| `read_only` | `false` | reject write callbacks |
| `no_apple_double` | `true` | ignore `.DS_Store`, `._*`, `.Spotlight-V100`, `.Trashes`, `.fseventsd` |
| `no_apple_xattr` | `false` | ignore `com.apple.*` extended attributes |
| `attr_timeout` | `1s` | FUSE attribute cache timeout |
| `entry_timeout` | `1s` | FUSE entry cache timeout |
| `negative_timeout` | `0s` | FUSE negative lookup cache timeout |

Advanced options include `allow_other`, `default_permissions`, `total_space`,
and `free_space`.

## Debugging

Enable file logging in `qrypt.toml`:

```toml
[logging]
log_level = "debug"
log_file = "~/.qrypt/qrypt.log"
error_file = "~/.qrypt/qrypt-error.log"
```

For live runtime state, start qrypt with a debug socket:

```sh
go run ./cmd/qrypt \
  -config ./qrypt.toml \
  -debug-socket /tmp/qrypt.sock \
  mount
```

Then query it from another shell:

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live health
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live state
go run ./cmd/qrypt -config ./qrypt.toml pending --verbose
```

See [`docs/debug.md`](docs/debug.md) for live endpoints, pending upload
inspection, cache checks, and consistency tools.

## Development

```text
cmd/qrypt                  CLI adapter
internal/mount             FUSE adapter
internal/driver/*          concrete backend drivers
pkg/vfs                    platform-independent virtual filesystem
pkg/crypt                  rclone-compatible crypt wrapper
pkg/drive                  backend driver contracts and registry
```

Run tests:

```sh
go test ./...
```

To add a backend, see [`docs/driver-development.md`](docs/driver-development.md).
