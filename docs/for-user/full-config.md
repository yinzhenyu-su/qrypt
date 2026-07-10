# 完整配置参考

本文档为 qrypt 全部配置项的参考说明，由 `qrypt.schema.json` 自动生成。

## 全局设置

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `mount_point` | string | ~/Qrypt | FUSE mount point (e.g. ~/Qrypt). |
| `cache_dir` | string | ~/.qrypt/qrypt-cache | Directory for read cache and pending upload staging. |
| `volume_name` | string | Qrypt | Volume label shown in the OS file manager. |
| `no_apple_double` | boolean | true | Skip writing Apple Double (._) metadata files on macOS. |
| `no_apple_xattr` | boolean | false | Ignore com.apple.* extended attributes on macOS. |
| `read_only` | boolean | false | Mount the filesystem read-only. |
| `allow_other` | boolean | false | Allow other local users to access the FUSE mount. |
| `default_permissions` | boolean | false | Ask the kernel to enforce mode/uid/gid permissions. |

## FUSE 参数

控制 FUSE 内核驱动的缓存行为和文件系统容量显示。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
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

## 缓存配置

在 `[defaults.cache]` 中设置，作为所有云盘的缓存默认值。每个 mount 可以在 `[mounts.cache]` 中单独覆盖。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `dir` | string | - | Cache directory for this mount. Falls back to cache_dir/<mount_name>. |
| `max_size` | string | 512M | Maximum read-cache size (e.g. "512M", "2G", "1T"). |
| `upload_delay` | string | 0s | Debounce delay before flushing a new file to the cloud (e.g. "5s", "1m"). |
| `upload_workers` | integer | 4 | Number of concurrent upload workers per mount. |
| `delete_delay` | string | 0s | Debounce delay before deleting a file from the cloud (e.g. "2s"). |

## 日志

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `log_level` | string | info | Minimum log level to emit. |
| `log_file` | string | - | Path to the main log file. |
| `error_file` | string | - | Path to the error log file (falls back to <log_file>-err.log). |

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

每个 mount 可以在 `[mounts.encryption]` 和 `[mounts.cache]` 中覆盖全局加密和缓存配置，具体参数见上文对应章节。
