# 完整配置参考

本文档列出 qrypt 的全部配置项。大多数用户只需要 `[mount]`、`[[mounts]]` 和 `[mounts.encryption]`；其余章节按需查阅。

## `[mount]`

挂载相关配置集中在 `[mount]` 下。旧的顶层写法仍兼容，但建议只用这一节。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `mount_point` | string | ~/Qrypt | 挂载点路径，所有云盘会出现在这个目录下 |
| `volume_name` | string | Qrypt | 文件管理器中显示的卷标 |
| `options` | object | - | 按目标平台填写挂载参数；每个数组元素会作为一个 `-o` 参数传递。 |
| `read_only` | boolean | false | 以只读方式挂载 |
| `allow_other` | boolean | false | 允许其他本地用户访问挂载点 |
| `default_permissions` | boolean | false | 由内核检查 mode/uid/gid 权限 |
| `attr_timeout` | string | 1s | FUSE 属性缓存时间 |
| `entry_timeout` | string | 1s | FUSE 目录项缓存时间 |
| `negative_timeout` | string | 0s | FUSE 负查找缓存时间 |
| `total_space` | string | 1T | 向操作系统报告的总容量 |
| `free_space` | string | 800G | 向操作系统报告的可用空间 |

### `mount.options`

按目标平台填写挂载参数；每个数组元素会作为一个 `-o` 参数传递。

| 平台 | 示例值 |
|---|---|
| `darwin` | `["defer_permissions", "auto_xattr", "iosize=4194304"]` |
| `windows` | `["FileInfoTimeout=1000"]` |

## 云盘挂载

每个 `[[mounts]]` 条目对应一个云盘服务。驱动类型和参数请参考：

- [支持的驱动](support-drivers.md)

通用参数：

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `name` | string | - | mount 名称，也是挂载点下的目录名 |
| `test_enabled` | boolean | false | 允许 probe 测试和基准命令使用此 mount；默认关闭，因为这些命令可能创建、上传、重命名或删除临时远端对象 |

每个 mount 可以在 `[mounts.encryption]`、`[mounts.read_cache]` 和 `[mounts.upload]` 中覆盖全局配置，具体参数见对应章节。

## 加密配置

在顶层 `[encryption]` 中设置，作为所有云盘的加密默认值。每个 mount 可以在 `[mounts.encryption]` 中单独覆盖。

格式与 rclone 兼容，可直接使用 rclone 的加密配置。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `password` | string | - | 加密密码 |
| `salt` | string | - | 加密 salt；留空时由密码派生 |
| `password_obscured` | boolean | false | 密码来自 rclone obscured 配置时设为 true |
| `salt_obscured` | boolean | false | salt 来自 rclone obscured password2 配置时设为 true |
| `filename_encryption` | string | standard | 文件名加密模式 |
| `filename_encoding` | string | base32 | 加密文件名的编码方式 |
| `content_dedup` | boolean | false | 启用确定性加密，使相同明文产生相同密文，便于网盘去重和秒传；会向存储提供方泄露内容相同关系 |

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
| `max_size` | string | 2G | 读取缓存上限；设为 `0` 可关闭，未设置时默认 2G |

## 缩略图缓存

在 `[thumbnail_cache]` 中设置缩略图缓存默认值。生成的缩略图保存在 `thumbnail_cache_dir`（默认 `<work_dir>/cache/thumbnail`）。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `max_size` | string | 256M | 缩略图缓存上限；设为 `0` 可关闭，未设置时默认 256M |

## 上传

在 `[upload]` 中设置上传和删除调度默认值，以及任务上传的默认目标。调度参数可以在 `[mounts.upload]` 中单独覆盖；`default_mount` 和 `default_path` 只支持顶层 `[upload]`。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `upload_delay` | string | 0s | 新文件上传前的防抖延迟 |
| `upload_workers` | integer | 4 | 每个 mount 的并发上传 worker 数 |
| `delete_delay` | string | 0s | 删除云端文件前的防抖延迟 |
| `default_mount` | string | - | 上传任务收到空路径或相对路径时使用的默认 mount，仅支持顶层 `[upload]` |
| `default_path` | string | / | 默认目录，位于 `upload.default_mount` 下；目录不存在时会创建，但不会创建缺失的父目录。仅支持顶层 `[upload]` |

## 日志

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `log_level` | string | info | 最低日志级别 |
| `log_file` | string | - | 主日志路径，包含所有级别的完整记录；未设置时使用 `<storage.log_dir>/qrypt.log`，或 `<storage.work_dir>/logs/qrypt.log`。 |
| `error_file` | string | - | 错误日志路径，只包含主日志中 warn 及以上级别的子集；未设置时使用 `<storage.log_dir>/qrypt-error.log`，或 `<storage.work_dir>/logs/qrypt-error.log`。 |

## 调试服务

在 `[debug]` 中启用本地调试入口。调试命令通过对应配置的 Unix socket 或 Windows named pipe 连接。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `enabled` | boolean | false | 启用本地调试服务（Unix socket 或 Windows named pipe） |

## 时间同步（NTP）

qrypt 依赖精确的系统时间进行文件操作。当系统时间可能不准确时（如嵌入式设备或刚开机），建议启用 NTP。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `ntp_enabled` | boolean | true | 为操作时间戳启用后台 NTP 校时 |
| `ntp_servers` | array | ['ntp1.aliyun.com:123', 'ntp2.aliyun.com:123', 'ntp1.tencent.com:123', 'ntp2.tencent.com:123', 'ntp1.ntsc.ac.cn:123', 'ntp2.ntsc.ac.cn:123', 'ntp1.cstnet.cn:123', '0.cn.pool.ntp.org:123', 'time.cloudflare.com:123', 'time.google.com:123'] | NTP 服务器列表，包含端口 |
| `ntp_timeout` | string | 1500ms | 单个 NTP 服务器的超时时间 |
| `ntp_poll_interval` | string | 30m | 后台 NTP 刷新间隔 |

## 带宽控制

限制文件上传和下载的带宽。单位如 `10Mbps`、`5MBps`，留空表示不限制。

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `download` | string | - | 下载限速，例如 `10Mbps`、`5MB/s`；留空表示不限制 |
| `upload` | string | - | 上传限速，例如 `2Mbps`；留空表示不限制 |
