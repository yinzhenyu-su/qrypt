# 离线下载

`offline` 命令组把远程资源交给网盘侧的离线下载服务，并在同一个任务模型里观察、控制和拉取结果。所有命令只通过本机控制 socket 与运行中的 `qrypt mount` 通信；没有运行中的 Core 时会报运行时错误，且不会创建任务。

当前已支持离线下载的驱动为 `quark`。其他驱动会在提交任务时返回不支持。

## 命令

```sh
qrypt offline add SOURCE [--parse-to /MOUNT/DIR | --mount NAME] [--mkdir]
                       [--select all|largest[:N]] [--include GLOB] [--exclude GLOB]
                       [--name TITLE] [--new] [--idempotency-key KEY] [--wait] [--json]
qrypt offline fetch SOURCE [--parse-to /MOUNT/DIR | --mount NAME] --download-to PATH
                         [--wait] [--flat] [--keep-cloud] [--mkdir]
                         [--workers N] [--chunk-size SIZE]
                         [--select ...] [--include GLOB] [--exclude GLOB]
                         [--name TITLE] [--new] [--idempotency-key KEY] [--json]
qrypt offline inspect SOURCE [--parse-to /MOUNT/DIR | --mount NAME] [--select ...] [--json]
qrypt offline list [--all] [--state STATE] [--mount NAME] [--limit N] [--json]
qrypt offline show TASK_ID [--json]
qrypt offline cancel TASK_ID
qrypt offline retry TASK_ID
qrypt offline wait TASK_ID [--timeout DURATION] [--json]
```

命令不要求提供 provider 名、驱动名、socket 路径或网盘任务 id；provider 由解析落地挂载（`--parse-to` 的 mount）选择。

## 来源

接受磁力链、HTTP(S)/FTP 链接和本地 `.torrent` 文件。本地 `.torrent` 只在本地解析出 info hash，并连同 `dn`/`tr` 规范化为磁力链后提交，种子文件本身不会上传。分享链接和不支持的 scheme 会被拒绝。等价的 `.torrent` 与磁力链具有相同的来源标识，用于去重。

## 目的地

- `--parse-to /MOUNT/DIR` 是网盘侧暂存目录，可选；省略时使用 provider 的默认（磁力/系统）目录。省略 `--parse-to` 时，可用 `--mount NAME` 显式选择 provider。暂存目录必须位于一个挂载上，且不能是加密挂载（网盘侧字节不经过 qrypt 加密）；目录不存在时默认报错，`--mkdir` 才会创建，挂载根目录始终可用。
- `--download-to PATH` 是 `offline fetch` 的最终可写目的地，可为本机路径、qrypt 命名空间路径或 Core 自身 FUSE 挂载树内的路径，允许是加密挂载（这些字节会经过 qrypt）。当 `PATH` 是一个目录（已存在，或以路径分隔符结尾）时，选中的每个文件会按其在网盘侧的相对路径写入该目录，重建 provider 的文件夹结构（含资源自身的文件夹）；provider 报告不出路径时退化为按文件名写入。`--flat` 强制只按文件名写入（不建子目录）。当 `PATH` 不是目录时，只有“恰好选中一个文件且该文件没有目录层级”才会写到 `PATH` 本身，否则任务以 `offline_path_invalid` 失败。`PATH` 会做路径清洗（拒绝绝对路径、`..`、非法字符等），并拒绝穿过已存在符号链接逃出目的地的路径；两个文件落到同一目标也会失败而不是互相覆盖。目录不存在会自动创建。`fetch` 会拒绝解析目录与下载目录指向同一挂载同一目录的请求；下载目录位于同一 provider 的另一个目录时会给出告警（结果会被下载后又上传回同一网盘）。

## 选择文件

`--select all`（默认）导入所有解析文件，`--select largest[:N]` 导入最大的 N 个，`--include`/`--exclude` 用文件名 glob 选择；`--select` 不能与 `--include`/`--exclude` 同时使用。provider 不支持选择时，除 `all` 外的选择会被拒绝。

## 任务

提交会返回一个持久化的 qrypt 任务（无 `--download-to` 时为 `offline_download`，有时为 `offline_fetch`）。相同来源、暂存目录与选择的活动任务默认被复用（`--new` 强制新建，`--idempotency-key` 提供显式幂等键）；`--download-to` 不参与去重，因此同一来源拉到不同本地目录会共享同一个暂存任务。`add` 会等到 provider 任务出现第一个可观察状态；若该状态已是失败或取消，命令以非零码退出。

## 两阶段 fetch

`fetch` 先在网盘侧暂存，再把结果传输到 `--download-to`。默认不阻塞：提交后等到 provider 任务出现第一个可观察状态，打印任务详情与后续观察方式（`qrypt offline show <id>` / `qrypt offline wait <id>`）即返回，Core 继续在后台传输；`--wait` 才阻塞到写入完成，并在阻塞期间输出进度（进度走 stderr，`--json` 仍只有一份快照）。Ctrl-C 只会停止等待，Core 任务继续运行（Core 重启后按任务 id 重新连接）。传输完成后默认删除本次创建的云副本：本任务创建的文件，以及仅剩这些文件时由磁链自动创建的资源文件夹（不会删 `--parse-to` 本身；文件夹里还有不属于本任务的条目时原样保留）；`--keep-cloud` 全部保留。下载 URL 过期时会重新获取并从中断偏移继续。

## 传输并发

`--workers N` 选择传输时同时在飞的下载连接数（1–16，默认 4），`--chunk-size SIZE` 选择每个请求覆盖的字节数（1MiB–64MiB，支持 `4M`/`8MiB` 这类后缀，默认 8MiB）。两个值会写入任务记录，Core 重启后恢复的传输沿用同一套参数；只在带 `--download-to` 时可用（`add` 没有传输阶段），超出范围会在建任务前以用法错误拒绝。

这两个参数只作用于下载 URL 的传输路径；通过 VFS 暂存读取普通云文件时，仍使用驱动自身的并发参数。

## 与 `fs get` 的分工

`offline add` 只把资源下载到网盘；要把已完成的远端条目取到本地，仍使用 `fs get`：

```sh
qrypt offline add magnet:?xt=urn:btih:... --parse-to /quark/downloads
qrypt offline wait <task-id>
qrypt fs get /quark/downloads/<file> ./<file>
```

## 错误与退出码

离线失败使用稳定的 provider 中立错误码（如 `offline_auth_required`、`offline_quota_exceeded`、`offline_duplicate`、`offline_source_unsupported`、`offline_same_destination`、`offline_wait_timeout`），并带显式的可重试标记；配额、策略、不支持的来源等不会被标记为可重试。

退出码沿用通用约定：0 成功，1 运行时/任务失败（取消与失败同码，脚本可用 `--json` 区分），2 用法错误，130 中断。
