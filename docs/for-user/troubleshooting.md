# 常见问题

本文档收录使用 qrypt 时可能遇到的常见问题。

## macOS 挂载点在慢速读取后消失

macFUSE 默认对单个内核请求设置 60 秒的 `daemon_timeout`：请求超时后会自动弹出卷，日志中通常能看到 `[FUSE] Destroy`，随后访问挂载点会报 `Device not configured`。非会员网盘限速时，一次较大的读取或预取很容易超过这个时限。

qrypt 默认向 macFUSE 传入 `daemon_timeout=0`，关闭内核的自动弹出，把超时和取消交给 qrypt 自己处理。如果配置文件中手动设置了 `[mount.options].darwin`，请不要额外配置较小的 `daemon_timeout`，否则仍可能触发自动卸载。

自动卸载后，旧的 `qrypt mount` 进程可能仍在等待退出信号。新版 qrypt 会检测到 FUSE 会话结束并直接退出；旧版本可以先结束该进程，再按下文[挂载点残留](#挂载点残留)的方法清理残留挂载点。

## 挂载点残留

`Ctrl+C`、`SIGTERM` 以及关闭终端或断开 SSH（`SIGHUP`）都会触发优雅卸载。

如果挂载进程被强行结束（`kill -9`、进程崩溃、OOM、`SIGQUIT`），内核可能留下一个连接已断的挂载点。再次挂载时该目录会不可访问，常见报错包括：

- Linux：`mkdir <挂载点>: file exists`
- macOS：`mount point ... is itself on a macFUSE volume`

qrypt 会识别这种残留挂载，先断开再重建挂载点，日志中会记录 `detached stale mount`，通常不需要手工删除目录。断开尝试有超时保护，不会让 `qrypt mount` 卡住。

只有自动断开失败时才会报错。可以按提示手动恢复后重试：

```sh
# Linux
fusermount3 -u <挂载点>

# macOS
diskutil unmount force <挂载点>
```

## 上传队列

`fs journal` 用于检查离线上传队列，也可以在无法正常挂载时直接操作 journal 目录：

```sh
qrypt fs journal [--config PATH | --cache-dir PATH] [--json]
qrypt fs journal replay [--config PATH | --cache-dir PATH] [--json]
qrypt fs journal prune [--config PATH | --cache-dir PATH] [--json] [--failed]
```

- `fs journal`：检查 dirty/clean 条目、失败条目和 staging 孤儿文件；`permanent_fail` 条目会单独计数并显示失败原因。
- `fs journal replay`：重置失败条目的失败状态（清掉 permanent failure、重试计数和错误），下次挂载时自动重试。不要求挂载在线。
- `fs journal prune`：删除 staging 数据已丢失或大小不匹配的条目，并压缩 journal 文件。
- `fs journal prune --failed`：在上一项基础上，同时删除永久失败（`permanent_fail`）的上传条目及其 staging 数据。该操作会丢弃本地暂存内容，执行前请先卸载挂载点。

常见处理顺序：先 `prune` 清掉数据已丢失的条目，再 `replay` 重置剩余失败条目，最后重新挂载重试。如果确认永久失败的上传已经不需要，先卸载挂载点，再执行 `prune --failed`。

`probe test fs` 在超时、取消或上传失败时会尽力删除远端 `__qrypt_fs_test_*` 测试目录。如果挂载进程被 `kill -9` 或直接崩溃，进程内清理来不及执行，需要手动删除该前缀的残留目录。

## 上传速度慢，是网盘限速还是 qrypt 的问题

先用探针把两件事分开：

- `qrypt probe bench read --mount <挂载> --size 64m`：绕过 VFS 和加密层，直接测驱动读并发。聚合吞吐不随并发上升，说明上限在网盘侧。
- `qrypt probe bench upload --mount <挂载> --size 64m --concurrency 1,2,4`：测同时上传多个文件的聚合吞吐；`--chunk-size` 可扫描分片大小（仅部分驱动支持，报告中的 `supports_chunk_size` 会说明）。

如果裸驱动吞吐和挂载上的吞吐接近，qrypt 和加密层不是瓶颈，继续调并发或分片通常也没有收益；如果裸驱动明显更快，再把两组数字和日志一起反馈。

## 上传会从 0 重新开始（断点续传支持差异）

不是所有网盘都支持分片上传或断点续传。驱动的上传形态分两类：

- **单请求整文件**：WebDAV、Koofr。上传期间任何超时或断线都会让下一次尝试从第 0 字节开始。
- **分片上传**：sftp、123、Drime、Quark、阿里云盘、百度网盘、OneDrive、189、115、139、S3 等；其中多数还支持断点续传，失败后只重传未完成的分片。

是否支持断点续传以[支持的网盘](support-drivers.md#能力对照)中的“断点续传”一列为准。判断当前上传是否在重传：`qrypt debug raw '/v1/uploads?history=1'` 中每一轮 `bytes_uploaded` 是否回到 0。

## 大文件上传卡住或超时

qrypt 按文件大小给上传请求留预算，并在连续 2 分钟没有新字节时取消连接，避免“连接还活着但永远不前进”占住上传队列。日志里的两类错误含义不同：

- `no progress for 2m` / `upload stalled`：连接停止消费数据，qrypt 已取消并会重试；
- `exceeded ...` / `context deadline exceeded`：请求预算用完；对单请求驱动（WebDAV、Koofr）会整文件重来。

如果大文件总是失败，先确认上行带宽和时间窗口是否足够，再用 `probe bench upload` 对比线程数和分片大小。

## 上传一直重试或标记永久失败

上传目标不存在时，重试不会成功：qrypt 会把该上传标记为 `permanent_fail`，不再无限重试。常见例子：

- 通过 SFTP 挂载到另一台机器上 qrypt 的挂载根（例如 `/root/Qrypt`）：目录能列出，但往里写任何文件都返回 `file does not exist`；
- Finder 生成的 `.DS_Store` / `._*` 被写进这种只读目录。

处理方式：

```sh
qrypt fs journal --json            # 查看 permanent_fail 条目和原因
qrypt fs journal replay --json     # 目标修好后重置失败状态
qrypt fs journal prune --failed    # 确认不再需要时删除条目和本地暂存
```

不想让 Finder 继续往网络卷写元数据：

```sh
defaults write com.apple.desktopservices DSDontWriteNetworkStores -bool true
killall Finder
```

非永久失败连续重试达到上限（约一天、100 次）后也会转为永久失败；重新保存或修改文件即可重新排队。

## 读取速度慢

qrypt 默认使用驱动作者按网盘特性选择的读策略，通常是单流。单流限速的线路可以先
测量多流是否更快，再只为该 mount 开启覆盖：

```toml
[[mounts]]
name = "koofr"
type = "koofr"

[mounts.read_profile]
streams = 4
window_chunks = 16
```

`window_chunks` 的单位是 512 KiB，`16` 表示 8 MiB 窗口；配合 `streams = 4`
时每个子 Range 约 2 MiB。该覆盖只影响 `bulk` 读，`interactive` / `probe` 仍
保持单流。不同网络下收益可能相反；不确定时先用
`qrypt probe test read` 对比单流与多流，不要直接照搬。没有 `read_profile`
的 mount 继续使用驱动默认值。

## Finder 复制报错 -36 / 输入输出错误

`-36` 是 macOS 把 FUSE 返回的 EIO 显示成 ioErr。多数情况下是某次远端读取失败：连接被断开、分片被截断，或 provider 停住不动。先看 `~/.qrypt/qrypt.log` 中同一路径的 `[FUSE] Read` 与驱动错误；qrypt 会对截断的远端分片做有界重试，但 provider 持续失败时仍会返回 EIO。

## 探针会写入测试数据

`probe test` / `probe bench` 需要 `test_enabled = true`，并会在挂载里创建 `__qrypt_*` 前缀的临时目录或文件，结束后自动删除。不要对不希望出现任何写入的目录运行探针；进程被 `kill -9` 时可能残留 `__qrypt_*` 目录，需要手动删除。

## 更多诊断

需要更详细的调试命令、测试项和日志说明时，参见[调试文档](../for-developer/debug.md)。
