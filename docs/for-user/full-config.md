# 完整配置参考

本文档为 qrypt 全部配置项的参考说明，由 `qrypt.schema.json` 自动生成。

## `[mount]`

挂载相关配置集中在 `[mount]` 下。旧的顶层写法仍兼容，但建议只用这一节。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `mount_point` | string | ~/Qrypt | FUSE mount point (e.g. ~/Qrypt). |
| `volume_name` | string | Qrypt | Volume label shown in the OS file manager. |
| `ignore_apple_metadata` | boolean | true | Do not sync Apple Double (._) metadata files to the backend on macOS. |
| `delegate_apple_xattr` | boolean | false | Delegate com.apple.* extended attributes to macFUSE on macOS. |
| `read_only` | boolean | false | Mount the filesystem read-only. |
| `allow_other` | boolean | false | Allow other local users to access the FUSE mount. |
| `default_permissions` | boolean | false | Ask the kernel to enforce mode/uid/gid permissions. |
| `attr_timeout` | string | 1s | FUSE attribute cache timeout (e.g. "1s", "500ms", "0s"). |
| `entry_timeout` | string | 1s | FUSE entry cache timeout (e.g. "1s", "500ms", "0s"). |
| `negative_timeout` | string | 0s | FUSE negative lookup cache timeout (e.g. "0s", "1s"). |
| `total_space` | string | 1T | Total capacity reported to the OS (e.g. "1T", "500G"). |
| `free_space` | string | 800G | Free space reported to the OS (e.g. "800G"). |

## 加密配置

在顶层 `[encryption]` 中设置，作为所有云盘的加密默认值。每个 mount 可以在 `[mounts.encryption]` 中单独覆盖。

格式与 rclone 兼容，可直接使用 rclone 的加密配置。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `password` | string | - | Encryption password. |
| `salt` | string | - | Encryption salt (empty = derive from password). |
| `password_obscured` | boolean | false | Set true when password is copied from rclone's obscured config value. |
| `salt_obscured` | boolean | false | Set true when salt is copied from rclone's obscured password2 config value. |
| `filename_encryption` | string | standard | File-name encryption mode. |
| `filename_encoding` | string | base32 | Encoding for encrypted file names. |
| `content_dedup` | boolean | false | When true, enables deterministic encryption so identical plaintext produces identical ciphertext, allowing the backend to deduplicate content (instant upload). May leak content equality to the storage provider. |

## 存储目录

在 `[storage]` 中设置运行数据根目录。当移动端或其他宿主传入 runtime layout 时，这些目录会被运行时布局覆盖。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `work_dir` | string | `~/.qrypt` | 所有运行数据的根目录；未单独配置的子目录会从这里派生。 |
| `read_cache_dir` | string | `<work_dir>/cache/read` | 读取缓存目录的可选覆盖。 |
| `thumbnail_cache_dir` | string | `<work_dir>/cache/thumbnail` | 缩略图缓存目录的可选覆盖。 |
| `upload_dir` | string | `<work_dir>/upload` | 上传 staging 和 pending journal 目录的可选覆盖。 |
| `state_dir` | string | `<work_dir>/state` | 驱动及运行状态目录的可选覆盖。 |
| `log_dir` | string | `<work_dir>/logs` | 运行日志目录的可选覆盖。 |
| `tmp_dir` | string | `<work_dir>/tmp` | 临时文件目录的可选覆盖。 |

环境变量 `QRYPT_HOME` 用于便携运行和测试隔离。设置后它优先于
`storage.work_dir` 及所有子目录覆盖，所有运行数据（包括 sync 会话）
都会写入该目录；未设置时才使用 `storage.work_dir`，最终回退到 `~/.qrypt`。
sync 会话固定保存在有效工作目录的 `sync/` 子目录。

## 读取缓存

在 `[read_cache]` 中设置读取缓存默认值。每个 mount 可以在 `[mounts.read_cache]` 中单独覆盖。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `max_size` | string | 2G | Maximum read-cache size (e.g. "512M", "2G", "1T"). Set to "0" to disable the read cache; when unset the default 2G applies. |

## 缩略图缓存

在 `[thumbnail_cache]` 中设置缩略图缓存默认值。生成的缩略图保存在 `thumbnail_cache_dir`（默认 `<work_dir>/cache/thumbnail`）。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `max_size` | string | 256M | Maximum generated-thumbnail cache size (e.g. "64M", "256M", "1G"). Set to "0" to disable thumbnail caching; when unset the default 256M applies. |

## 上传

在 `[upload]` 中设置上传和删除调度默认值，以及任务上传的默认目标。调度参数可以在 `[mounts.upload]` 中单独覆盖；`default_mount` 和 `default_path` 只支持顶层 `[upload]`。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `upload_delay` | string | 0s | Debounce delay before flushing a new file to the cloud (e.g. "5s", "1m"). |
| `upload_workers` | integer | 4 | Number of concurrent upload workers per mount. |
| `delete_delay` | string | 0s | Debounce delay before deleting a file from the cloud (e.g. "2s"). |
| `default_mount` | string | - | Default mount used when upload tasks receive an empty or relative destination path. Only supported in top-level [upload]. |
| `default_path` | string | / | Default directory under upload.default_mount used when upload tasks receive an empty or relative destination path. qrypt creates this directory if missing, but does not create missing parent directories. Only supported in top-level [upload]. |

## 日志

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `log_level` | string | info | Minimum log level to emit. |
| `log_file` | string | - | 主日志路径；未设置时使用 `<storage.log_dir>/qrypt.log`，或 `<storage.work_dir>/logs/qrypt.log`。 |
| `error_file` | string | - | 错误日志路径；未设置时使用 `<storage.log_dir>/qrypt-error.log`，或 `<storage.work_dir>/logs/qrypt-error.log`。 |

## 时间同步（NTP）

qrypt 依赖精确的系统时间进行文件操作。当系统时间可能不准确时（如嵌入式设备或刚开机），建议启用 NTP。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `ntp_enabled` | boolean | true | Enable background NTP clock sync for operation timestamps. |
| `ntp_servers` | array | ['ntp1.aliyun.com:123', 'ntp2.aliyun.com:123', 'ntp1.tencent.com:123', 'ntp2.tencent.com:123', 'ntp1.ntsc.ac.cn:123', 'ntp2.ntsc.ac.cn:123', 'ntp1.cstnet.cn:123', '0.cn.pool.ntp.org:123', 'time.cloudflare.com:123', 'time.google.com:123'] | NTP servers, including port. |
| `ntp_timeout` | string | 1500ms | Per-server NTP timeout. |
| `ntp_poll_interval` | string | 30m | Background NTP refresh interval. |

## 带宽控制

限制文件上传和下载的带宽。单位如 `10Mbps`、`5MBps`，留空表示不限制。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `download` | string | - | Download speed cap (e.g. "10Mbps", "5MB/s", empty = unlimited). |
| `upload` | string | - | Upload speed cap (e.g. "2Mbps", empty = unlimited). |

## 云盘挂载

每个 `[[mounts]]` 条目对应一个云盘服务。驱动类型和参数请参考：

- [支持的驱动](support-drivers.md)

通用参数：

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `name` | string | - | Mount name — used as the directory name under the mount point. |
| `test_enabled` | boolean | false | Allow debug test and benchmark commands to use this mount. Disabled by default because those commands may create, upload, rename, or delete temporary remote objects. |

每个 mount 可以在 `[mounts.encryption]`、`[mounts.read_cache]` 和 `[mounts.upload]` 中覆盖全局配置，具体参数见上文对应章节。
