# 命令行参考

## 通用约定

- 配置文件参数只出现在需要配置的具体命令上，不是根命令全局参数。
- 省略 `--config` 时，依次查找：
  1. `./qrypt.toml`
  2. `~/.qrypt/qrypt.toml`
  3. Unix：`$XDG_CONFIG_HOME/qrypt/qrypt.toml`，默认 `~/.config/qrypt/qrypt.toml`
  4. Windows：`%AppData%\qrypt\qrypt.toml`
- `REMOTE` 表示 qrypt 虚拟文件系统路径；`LOCAL` 表示本机路径。
- `fs get` 的 `LOCAL` 为 `-` 时写入标准输出。
- `fs put` 的 `LOCAL` 为 `-` 时读取标准输入。
- 支持 `--json` 的查询命令会向标准输出写入稳定的 JSON，状态提示和错误写入标准错误。
- 命令不再在每次执行时打印所用配置文件路径；需要确认时用 `qrypt config path`。
- 路径参数中的 `~` 会被展开为用户主目录。

### 帮助与发现

帮助是分层的，每一层都给出下一步可执行的命令：

- **出错时**：用法错误（拼错的 flag、缺少参数、拼错的子命令）会以一条可复制的下一步命令结尾（`Next:` 段）；子命令名接近时会给出 `Did you mean this?` 建议。
- **组概览**：不带子命令运行一个命令组（如 `qrypt fs`、`qrypt offline`）会打印该组的帮助与示例，而不是报错。
- **命令帮助**：每个命令都有摘要、使用场景说明和可复制示例；`--advanced` 展开进阶 flag：

  ```sh
  qrypt offline fetch --help --advanced
  ```

  进阶 flag 默认不出现在帮助里，但仍可正常使用。
- **机器可读**：`qrypt schema [COMMAND]` 以 JSON 输出子命令、参数与 flag，供脚本、编辑器、GUI 使用，无需解析渲染后的帮助文本：

  ```sh
  qrypt schema offline fetch
  ```
- **补全**：内置 shell 补全脚本。

  ```sh
  qrypt completion bash   # 也支持 zsh、fish
  ```

### 退出码

脚本可依赖以下进程退出码，语义保持稳定：

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 运行时/传输错误（网络、驱动等） |
| 2 | 用法错误（参数或 flag 拼错） |
| 3 | 操作部分成功（如目录复制有文件成功也有失败） |
| 4 | `fs check` 发现两端树存在差异 |
| 130 | 被 SIGINT 中断 |

## 配置

创建配置文件：

```sh
qrypt config init [PATH]
```

默认写入 `./qrypt.toml`。已有文件不会被覆盖；确认覆盖时使用 `--force`。

验证配置但不连接远端：

```sh
qrypt config validate [--config PATH]
```

验证包括未知配置键、配置版本、mount 名称、驱动类型、驱动必填参数、加密、缓存和带宽设置。

查看已屏蔽密码等敏感字段的配置：

```sh
qrypt config show [--config PATH]
```

查询当前解析到的配置文件路径（`--config` 优先，否则按搜索顺序取第一个存在的文件；找不到时报错）：

```sh
qrypt config path [--config PATH] [--json]
```

导出 rclone 兼容密码：

```sh
qrypt config export-rclone-password MOUNT_NAME [--config PATH]
qrypt config export-rclone-password --password-file PATH [--salt SALT]
printf '%s' "$PASSWORD" | qrypt config export-rclone-password --password-stdin
```

直接输入密码时优先使用 `--password-file` 或 `--password-stdin`。`--password` 会暴露在 shell 历史和进程参数中。

## 挂载

```sh
qrypt mount [MOUNT_NAME] [--config PATH] [--mount-point PATH]
```

省略 `MOUNT_NAME` 时挂载配置中的全部云盘；指定 `MOUNT_NAME` 时只挂载该云盘，根目录就是该云盘内容。省略 `--mount-point` 时读取配置中的 `[mount].mount_point`。运行时 socket 使用配置目录下的内置路径，无需手工指定。

`Ctrl+C`、`SIGTERM` 以及关闭终端或断开 SSH（`SIGHUP`）都会触发优雅卸载。若上一次挂载的进程是被强行结束的（`kill -9`、进程崩溃、OOM、`SIGQUIT`），内核会留下一个连接已断的挂载点，再次挂载时该目录不可访问（Linux 报 `mkdir <挂载点>: file exists`，macOS 报 `mount point ... is itself on a macFUSE volume`）：qrypt 会识别这种残留挂载、先断开再重建挂载点（日志记录 `detached stale mount`），无需手工删除目录；断开尝试有超时保护，不会让 `qrypt mount` 卡住。只有在自动断开失败时才会报错，并按提示恢复（Linux：`fusermount3 -u <挂载点>`；macOS：`diskutil unmount force <挂载点>`）后重试。

## 文件系统操作

```sh
qrypt fs list [REMOTE] [--json | --jsonl] [--human]
qrypt fs find [REMOTE] NAME [--json]
qrypt fs df [MOUNT] [--json] [--bytes]
qrypt fs du [REMOTE] [--json] [--bytes]
qrypt fs stat REMOTE [--json]
qrypt fs cat REMOTE
qrypt fs get REMOTE LOCAL [--force]
qrypt fs put LOCAL REMOTE [--wait-timeout DURATION]
qrypt fs copy SOURCE DESTINATION [--recursive] [--force] [--dry-run] [--json]
qrypt fs crypt-encode [MOUNT] NAME [--json]
qrypt fs crypt-decode [MOUNT] NAME [--json]
qrypt fs mkdir REMOTE
qrypt fs mv SOURCE DESTINATION [--json]
qrypt fs rm REMOTE [--wait-timeout DURATION] [--json]
qrypt fs pending [--verbose | --json]
qrypt fs journal [--config PATH | --cache-dir PATH] [--json]
qrypt fs journal replay [--config PATH | --cache-dir PATH] [--json]
qrypt fs journal prune [--config PATH | --cache-dir PATH] [--json]
```

`fs` 命令组支持 `--config PATH`，既可写在子命令前，也可写在子命令后：

```sh
qrypt fs --config PATH list /
qrypt fs list / --config PATH
```

`get` 和单文件 `copy` 默认拒绝覆盖文件；明确覆盖时使用 `--force`。`copy` 直接通过驱动在远端路径之间复制文件。复制目录时使用 `--recursive`，目标路径会作为父目录并追加源目录名，例如 `/src/parent -> /dst` 会写入 `/dst/parent/...`；目录会自动创建，已有文件默认跳过，`--force` 会覆盖已有文件。目录复制遇到读取、创建目录或复制文件错误时会停止并返回失败；`--json` 会输出逐项 `entries`，标记 `ready`、`copied`、`skipped` 或 `failed`。`copy` 支持 `--dry-run`：只枚举并输出将传输的文件计划（`--json` 时输出结构化计划），不执行任何复制。目录复制部分成功（有文件复制成功也有失败）时退出码为 3，全部失败时为 1。`put` 和 `rm` 会等待异步远端操作完成，可通过 `--wait-timeout` 调整最长等待时间。

`list` 支持 `--jsonl`（JSON Lines）：每行输出一个条目的 JSON，适合大目录流式消费（`jq -s` 可聚合）；`--json` 与 `--jsonl` 互斥。`list --human` 以 K/M/G/T 显示大小。`find` 递归查找名字包含 `NAME`（大小写不敏感）的条目，`--json` 输出匹配条目列表。

`check A B` 比较两个目录树（`A`/`B` 可为 `/MOUNT/...` 虚拟路径或本机路径，至少一端是虚拟路径）：逐文件比对大小与修改时间（`--hash` 或 `--compare=hash` 时再比内容哈希），输出缺失/多余/不一致清单，`--json` 输出结构化结果。退出码 0 一致、4 有差异。文件与目录类型不一致会报告 `type` 差异。加密挂载透明处理：本地明文目录可直接与加密挂载比对；`--hash` 需要网盘提供内容哈希（已支持：阿里云盘 sha1、115 sha1、百度网盘 md5、OneDrive sha1；加密挂载下本地会使用远端 nonce 重算密文哈希，全程不下载文件正文），加密挂载下本地会使用远端 nonce 重算密文哈希，全程不下载文件正文。`--compare=mtime-only` 只按修改时间判定差异（忽略大小差异，内容变了但大小与 mtime 都没变会被当作一致）。`du` 统计目录树文件数与总字节数；`df` 显示所配置驱动的空间（默认逐 mount 列出；`MOUNT` 参数或 `--mount` 只看单个）。两者默认人类可读大小，`--bytes` 输出原始字节，`--json` 输出数字。`rm`/`mv` 支持 `--json` 输出结构化结果。

`sync SOURCE DESTINATION` 单向同步：让 `DESTINATION` 匹配 `SOURCE`（`SOURCE` 权威，至少一端是虚拟路径）。默认只新增缺失文件、更新变化文件（大小，`--hash` 时含内容哈希），**不删除** `DESTINATION` 多余文件；`--delete` 才删除。`--compare` 选择比较策略：`size-mtime`（默认）、`mtime-only`（只看修改时间，忽略大小差异）、`hash`（等价 `--hash`）。`sync` 默认对支持后端内容哈希的网盘（阿里云盘 sha1、115 sha1、百度网盘 md5、OneDrive sha1）自动启用哈希比对，能检测同大小内容变化；无后端哈希的网盘（如夸克）自动降级为大小比对。注意：`mtime-only` 在无法保留 mtime 的网盘（如夸克，只记录上传时间）上会每次全量重传——只有明确知道目标端 mtime 可靠时才使用。上传会把源文件 mtime 传播给目标端；若目标网盘无法保留 mtime（如夸克，只记录上传时间），则 mtime 差异不触发内容重传（按 size/hash 判定），避免每次同步全量重传。文件/目录类型冲突默认不覆盖（报告 `conflict`，退出码 3），可用 `--conflict=skip` 跳过或 `--conflict=source` 让源端胜出。`--dry-run` 只输出计划不产生任何写入（退出码 0）；`--json` 输出结构化计划/结果（含 add/update/delete/skip/conflict/failed 汇总与按路径排序的条目）。同步执行顺序保证先建目录再传文件、先删子项再删目录；单个文件失败不中断其余文件，存在失败时退出码 3。同步会等待所有上传完成后才返回（不设超时，慢网盘不会误报失败）；Ctrl-C 可中断，中断后会话保留，用 `--resume` 继续。`--resume` 继续上次中断/部分失败的会话：只重试未完成和失败的文件，不重新扫描两棵树。会话计划保存在工作目录的 `sync/` 子目录（默认 `~/.qrypt/sync/`，同一对源/目标共享一个会话目录），全部完成后自动清除，中断遗留的会话 7 天未动会在下次同步时自动清理；`--dry-run` 不写任何会话。

`fs` 命令组还支持 `--bwlimit` 系列 flag，覆盖配置中的 `[bandwidth]` 段：

```sh
qrypt fs --bwlimit 10M list /
qrypt fs --bwlimit-download 5M get REMOTE LOCAL
qrypt fs --bwlimit-upload 2M put LOCAL REMOTE
qrypt fs --bwlimit 10M --bwlimit-upload 2M copy SRC DST
```

`--bwlimit SIZE` 同时限制下载与上传；`--bwlimit-download` / `--bwlimit-upload` 单独限制一个方向，可与 `--bwlimit` 组合（细粒度 flag 优先）。

`fs` 命令组支持 `--config PATH`，既可写在子命令前，也可写在子命令后：

`crypt-encode` / `crypt-decode` 用指定 mount 的 `[mounts.encryption]` 配置对文件名逐段加解密，用于对照网盘后端存储的密文文件名和本地明文路径：

```sh
qrypt fs crypt-encode [MOUNT] "hello.txt"    # 明文 → 密文（验证网盘里应显示的名字）
qrypt fs crypt-decode [MOUNT] "b4l6g..."     # 密文 → 明文（反查本地对应文件）
```

`MOUNT` 省略时使用第一个配置了加密的 mount。加密方向与挂载时 VFS 对后端文件名使用的算法一致（同一 password/salt/编码，可逐段处理多级路径）。未加密的 mount 会报错。

## 离线下载

`offline` 命令组把远程资源交给网盘侧的离线下载服务，并在同一个任务模型里观察、控制、拉取结果。所有命令只通过本机控制 socket 与运行中的 `qrypt mount` 通信：没有运行中的 Core 时会报运行时错误，且不创建任何任务。命令不要求提供 provider 名、驱动名、socket 路径或网盘任务 id；provider 由解析落地挂载（`--parse-to` 的 mount）选择。

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

**来源规范化**：接受磁力链、HTTP(S)/FTP 链接和本地 `.torrent` 文件。本地 `.torrent` 只在本地解析出 info hash，并连同 `dn`/`tr` 规范化为磁力链后提交，种子文件本身不会上传。分享链接和不支持的 scheme 会被拒绝。等价的 `.torrent` 与磁力链具有相同的来源标识，用于去重。

**两类目的地**：
- `--parse-to /MOUNT/DIR` 是网盘侧暂存目录，可选，省略时用 provider 的默认（磁力/系统）目录；省略 `--parse-to` 时可用 `--mount NAME` 显式选择 provider。暂存目录必须位于一个挂载上，且不允许是加密挂载（网盘侧字节不经过 qrypt 加密）；目录不存在时默认报错，`--mkdir` 才会创建，挂载根目录始终可用。
- `--download-to PATH` 是 `offline fetch` 的最终可写目的地，可为本机路径、qrypt 命名空间路径或 Core 自身 FUSE 挂载树内的路径，允许是加密挂载（这些字节会经过 qrypt）。当 `PATH` 是一个目录（已存在，或以路径分隔符结尾）时，选中的每个文件会**按其在网盘侧的相对路径**写入该目录，重建 provider 的文件夹结构（含资源自身的文件夹）；provider 报告不出路径时退化为按文件名写入。`--flat` 强制只按文件名写入（不建子目录）。当 `PATH` 不是目录时，只有“恰好选中一个文件且该文件没有目录层级”才会写到 `PATH` 本身，否则任务以 `offline_path_invalid` 失败。`PATH` 会做路径清洗（拒绝绝对路径、`..`、非法字符等），并拒绝穿过已存在符号链接逃出目的地的路径；两个文件落到同一目标也会失败而不是互相覆盖。目录不存在会自动创建。`fetch` 会拒绝解析目录与下载目录指向同一挂载同一目录的请求；下载目录位于同一 provider 的另一个目录时会给出告警（结果会被下载后又上传回同一网盘）。

**选择语义**：`--select all`（默认）导入所有解析文件，`--select largest[:N]` 导入最大的 N 个，`--include`/`--exclude` 用文件名 glob 选择；`--select` 不能与 `--include`/`--exclude` 同时使用。provider 不支持选择时，除 `all` 外的选择会被拒绝。

**任务语义**：提交会返回一个持久化的 qrypt 任务（无 `--download-to` 时为 `offline_download`，有时为 `offline_fetch`）。相同来源、暂存目录与选择的活动任务默认被复用（`--new` 强制新建，`--idempotency-key` 提供显式幂等键）；`--download-to` 不参与去重，因此同一来源拉到不同本地目录会共享同一个暂存任务。`add` 会等到 provider 任务出现第一个可观察状态；若该状态已是失败或取消，命令以非零码退出。

**两阶段 fetch**：`fetch` 先在网盘侧暂存，再把结果传输到 `--download-to`。**默认不阻塞**：提交后等到 provider 任务出现第一个可观察状态，打印任务详情与后续观察方式（`qrypt offline show <id>` / `qrypt offline wait <id>`）即返回，Core 继续在后台传输；`--wait` 才阻塞到写入完成，并在阻塞期间输出进度（进度走 stderr，`--json` 仍只有一份快照）。Ctrl-C 只会停止等待，Core 任务继续运行（Core 重启后按任务 id 重新连接）。传输完成后默认删除本次创建的云副本：本任务创建的文件，以及仅剩这些文件时由磁链自动创建的资源文件夹（不会删 `--parse-to` 本身；文件夹里还有不属于本任务的条目时原样保留）；`--keep-cloud` 全部保留。下载 URL 过期时会重新获取并从中断偏移继续。`--no-wait` 已废弃，是默认行为的 no-op。

**传输并发**：`--workers N` 选择传输时同时在飞的下载连接数（1–16，默认 4），`--chunk-size SIZE` 选择每个请求覆盖的字节数（1MiB–64MiB，支持 `4M`/`8MiB` 这类后缀，默认 8MiB）。两个值会写入任务记录，Core 重启后恢复的传输沿用同一套参数；只在带 `--download-to` 时可用（`add` 没有传输阶段），超出范围会在建任务前以用法错误拒绝。默认 4×8MiB 已经能跑到实测最快的一档；把 chunk 调到 1MiB 以下会让每个请求的固定开销占主导（实测 8 并发 × 256KiB 只有约 4MB/s，而 8 并发 × 8MiB 约 24MB/s），把 chunk 调到 64MiB 以上只会让单个请求失败时重传的代价更大。注意它只作用于下载 URL 的传输路径：走 VFS 暂存读的那条路（已材料化的普通云文件）仍使用 driver 自身的并发参数。

**与 `fs get` 的分工**：`offline add` 只把资源下载到网盘；要把已完成的远端条目取到本地，仍使用 `fs get`：

```sh
qrypt offline add magnet:?xt=urn:btih:... --parse-to /quark/downloads
qrypt offline wait <task-id>
qrypt fs get /quark/downloads/<file> ./<file>
```

**错误与退出码**：离线失败使用稳定的 provider 中立错误码（如 `offline_auth_required`、`offline_quota_exceeded`、`offline_duplicate`、`offline_source_unsupported`、`offline_same_destination`、`offline_wait_timeout`），并带显式的可重试标记；配额、策略、不支持的来源等不会被标记为可重试。退出码沿用通用约定：0 成功，1 运行时/任务失败（取消与失败同码，脚本可用 `--json` 区分），2 用法错误，130 中断。

## 驱动信息

```sh
qrypt driver list [--json]
qrypt driver schema NAME [--json]
```

`schema` 展示驱动参数类型、必填项、默认值、示例和敏感字段标记。

## 调试

运行时调试命令使用应用运行目录（默认为 `~/.qrypt/run/<config-hash>/`，
设置了 `storage.work_dir` 时为其下的 `run/`）中的内置 debug socket
连接运行中的 debug server。`collect`、`watch` 和 `bundle` 还需要说明要检查哪个挂载：平时请用
`--mount NAME`；确实需要检查全部挂载时，再用 `--all-mounts`。

```sh
qrypt debug collect [REMOTE] [--dest DESTINATION] --mount NAME
qrypt debug watch [REMOTE] [--jsonl] --mount NAME
qrypt debug raw ENDPOINT
qrypt debug bundle [REMOTE] [--dest DESTINATION] --mount NAME --out FILE [--force]

qrypt probe test TEST [probe flags]
qrypt probe bench TEST [benchmark flags]
```

mount 会自动启动按配置隔离的本地 endpoint；Unix-like 系统使用 socket 文件，Windows 使用 named pipe：

```sh
qrypt debug collect --mount NAME
```

`--mount` 可以重复传入多个挂载名。`--all-mounts` 会收集全部挂载，输出可能更大，请只在需要整体排查时使用。

`debug watch --jsonl` 会逐行输出每个采样样本的 JSON（而非单个汇总报告），适合管道式消费：`qrypt debug watch --jsonl ... | jq -c '.uploads[]'`。

跨挂载传输问题应同时提供源路径和 `--dest`；报告会分别收集两端路径状态、读取/上传历史和挂载能力。

driver 和 VFS 测试通过 `probe test` 显式执行，基准测试通过 `probe bench` 执行。`TEST` 可为 `auth`、`batchupload`、`batchmove`、`crud`、`instantupload`、`multipart`、`fs`、`read`、`resume` 或 `xfer`。这些测试只会使用在 `[[mounts]]` 中设置了 `test_enabled = true` 的挂载；`xfer` 要求 source 和 dest 都启用。`auth` 是只读认证探测，其他测试可能创建临时远端对象。`fs` 会检查 VFS 写入、上传、读取和清理流程；`read` 从实际 FUSE 挂载目录读取自动生成的远端文件，报告冷/热缓存的首包延迟、持续与峰值速度、读取调用延迟和缓存命中情况；使用 `mount-debug-test.sh` 时临时挂载目录会自动传入。`batchupload` 会在同一父目录批量写入小文件，并检查目录解析缓存是否复用；`batchmove` 会先等待这些文件上传完成，再通过 VFS `Rename` 将它们从源目录逐个移动到目标目录。两个批量测试都支持 `--count`（默认 50，最大 100）和 `--size`（默认 4 KiB，最大 1 MiB）。`resume` 会故意取消一次上传，用来确认断点续传或重试恢复是否正常。`multipart` 会上传一个 34 MiB 的伪随机文件（可用 `--size` 调整）并逐字节读回校验，覆盖分片/块上传路径——普通 CRUD 测试的小文件不会触达这部分。注意读回校验受网盘下载速度影响：在限速的网盘上（如 115 非会员，约 100 KB/s）34 MiB 全量读回可能耗时数分钟，建议先跑小 `--size` 验证或按需调整。所有测试都会使用带随机后缀的临时目录并在结束时清理。

运行中的任务可以通过 Control socket 操作：

```sh
qrypt control task cancel TASK_ID
qrypt control task retry TASK_ID
```

离线 journal 检查使用 `fs journal`，可传配置文件或明确的缓存目录。

```sh
qrypt fs journal [--config PATH | --cache-dir PATH] [--json]
qrypt fs journal replay [--config PATH | --cache-dir PATH] [--json]
qrypt fs journal prune [--config PATH | --cache-dir PATH] [--json]
```

- `fs journal` 检查离线上传队列：dirty/clean 条目、失败条目、staging 孤儿文件等。
- `fs journal replay` 重置失败条目的失败状态（清掉 permanent failure、重试计数和错误），下次挂载时自动重试；不要求挂载在线，可直接操作 journal 目录。
- `fs journal prune` 删除 staging 数据已丢失或大小不匹配的条目（这些条目已无法上传），并压缩 journal 文件。

常用组合：先 `prune` 清掉数据已丢失的条目，再 `replay` 重置剩余失败条目，最后挂载重试。

详细说明见[调试文档](../for-developer/debug.md)。

## 版本与补全

```sh
qrypt version [--json]
qrypt --version
qrypt completion bash
qrypt completion zsh
qrypt completion fish
qrypt completion powershell
```
