# 常见问题

本文档收录使用 qrypt 时可能遇到的常见问题。

## macOS 挂载点在慢速读取后消失

macFUSE 默认对单个内核请求设置 60 秒的 `daemon_timeout`：请求超时后会自动弹出卷，日志中通常能看到 `[FUSE] Destroy`，随后访问挂载点会报 `Device not configured`。非会员网盘限速时，一次较大的读取或预取很容易超过这个时限。

qrypt 默认向 macFUSE 传入 `daemon_timeout=0`，关闭内核的自动弹出，把超时和取消交给 qrypt 自己处理。如果配置文件中手动设置了 `[mount.options].darwin`，请不要额外配置较小的 `daemon_timeout`，否则仍可能触发自动卸载。

自动卸载后，旧的 `qrypt mount` 进程可能仍在等待退出信号。新版 qrypt 会检测到 FUSE 会话结束并直接退出；旧版本可以先结束该进程，再按上一节的方法清理残留挂载点。

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
qrypt fs journal prune [--config PATH | --cache-dir PATH] [--json]
```

- `fs journal`：检查 dirty/clean 条目、失败条目和 staging 孤儿文件。
- `fs journal replay`：重置失败条目的失败状态（清掉 permanent failure、重试计数和错误），下次挂载时自动重试。不要求挂载在线。
- `fs journal prune`：删除 staging 数据已丢失或大小不匹配的条目，并压缩 journal 文件。

常见处理顺序：先 `prune` 清掉数据已丢失的条目，再 `replay` 重置剩余失败条目，最后重新挂载重试。

## 更多诊断

需要更详细的调试命令、测试项和日志说明时，参见[调试文档](../for-developer/debug.md)。
