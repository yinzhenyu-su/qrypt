# 用法问答

本页回答"参数该怎么写"这类用法问题：先给能直接复制的命令，再说明原因和代价。完整参数见[命令行参考](cli.md)，单项功能的规则见对应功能文档（例如[离线下载](offline-download.md)）。

## 怎么把离线下载的结果放进加密目录？

`offline add` 不能直接写进加密目录。加密挂载是 qrypt 的加解密视图，而网盘侧离线下载的字节不经过 qrypt，所以把加密挂载交给 `--parse-to` 会在建任务前被拒绝：

```
Error: ... encrypted parse destination is not supported: mount "quark-encrypt"
# 退出码 2（用法错误），不会创建任务
```

要落到加密目录，用 `offline fetch` 两段式：先在**非加密**挂载上暂存，再把结果加密上传到加密挂载。

```sh
# 1. 先确认来源能解析；这条命令不创建任务
qrypt offline inspect "<magnet 或 URL>" --parse-to /quark/downloads

# 2. 暂存到非加密挂载，最终写入加密挂载
qrypt offline fetch "<magnet 或 URL>" \
  --parse-to /quark/downloads --mkdir \
  --download-to /quark-encrypt/影片
```

- `--parse-to` 必须指向非加密挂载的目录（示例里的 `quark` 是普通挂载；`quark-encrypt` 是带 `[mounts.encryption]` 的加密挂载）。换成你配置里对应的挂载名即可。
- `--download-to` 写的是**明文路径**，qrypt 负责把文件名和内容加密后写到网盘；目录不存在会自动创建。写成挂载命名空间路径（`/quark-encrypt/影片`）或 Core 的 FUSE 树路径都可以，两者等价。
- 结果会在 qrypt 侧占用一次完整下载加一次完整加密上传的网络流量：云侧明文不能直接变成加密字节，必须经过 qrypt 解密再加密上传。
- 传输成功后默认删除本次任务创建的云侧暂存副本，`--keep-cloud` 保留明文副本；`--parse-to` 指定的目录本身不会被删除。
- `--parse-to` 与 `--download-to` 不能是同一挂载的同一目录（`offline_same_destination`）。

常用变体：

| 需求 | 参数 |
|---|---|
| 只取最大的一个文件 | `--select largest:1` |
| 按文件名平铺，不重建资源文件夹 | `--flat` |
| 阻塞到写入完成，期间显示进度 | `--wait` |
| 保留云侧明文暂存副本 | `--keep-cloud` |

## 怎么删除已结束的离线任务？

`qrypt offline list` 默认显示活动任务和失败任务，所以旧的失败任务会一直留在列表里。已经结束（成功、失败或已取消）的任务没有可取消的工作，`qrypt offline cancel` 会提示没有可取消的操作；用 `rm` 移除任务记录：

```sh
qrypt offline rm <task-id>
qrypt offline rm --finished            # 一次移除全部已结束任务
qrypt offline rm --finished --mount quark
```

`rm` 只删除本地任务记录和进度，不会删除网盘或本地已经下载的文件；对仍在执行的任务会先停止再移除。
