# 命令行参考

## 通用约定

- 配置文件参数只出现在需要配置的具体命令上，不是根命令全局参数。
- 省略 `--config` 时，依次查找：
  1. `./qrypt.toml`
  2. `~/.qrypt/qrypt.toml`
  3. Unix：`$XDG_CONFIG_HOME/qrypt/qrypt.toml`，默认 `~/.config/qrypt/qrypt.toml`
  4. Windows：`%AppData%\qrypt\qrypt.toml`
- 界面语言按 `--lang` → `QRYPT_LANG` → 系统 locale → 英文的顺序选择，支持 `en` 和 `zh-CN`。帮助、内置命令以及 `fs`/`offline` 的人类可读输出会随语言切换；`--json` 输出、诊断报告、表格头、状态/类型等稳定代码、错误信息和日志保持英文。需要稳定英文输出时设置 `QRYPT_LANG=en` 或传入 `--lang en`；脚本解析结构化结果时应使用 `--json`。
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

### 共享凭据

同一账号挂载多个目录时，凭据只维护一份：

```sh
qrypt credential add main --type quark --param cookie="..."
qrypt credential list
qrypt credential show main
qrypt credential set main --param cookie="..."
qrypt credential remove main
```

凭据保存账号级参数（cookie、refresh token、用户名密码等），轮换与刷新由 qrypt 统一维护。`credential remove` 要求先移除引用它的挂载；加 `--purge` 会同时删除已持久化的会话目录。`qrypt credential test NAME [--param k=v]` 可以只认证一次而不挂载，用于确认凭据和网络是否可用。

已有内联凭据的旧挂载可以一次性转换：

```sh
qrypt credential adopt docs --name main
qrypt credential adopt docs --name main --delete-legacy
```

它会把挂载里的凭据级参数移到新凭据、让挂载引用该凭据，并在账号匹配时采纳旧的轮换状态；`--delete-legacy` 才会删除旧的挂载状态文件。

参数值支持 `key=@文件路径` 和 `key=-`（从标准输入读取），避免密钥进入 shell 历史：

```sh
qrypt credential add main --type quark --param cookie=@cookie.txt
printf '%s' "$COOKIE" | qrypt credential add main --type quark --param cookie=-
```

### 管理挂载

```sh
qrypt mount add docs --credential main --param root_path=/Docs
qrypt mount add photos --credential main --param root_path=/Photos --password "..."
qrypt mount add vault --credential main --param root_path=/Vault --password "..." \
  --salt "..." --filename-encryption obfuscate --filename-encoding base64 --content-dedup
qrypt mount list
qrypt mount set docs --param root_path=/Documents
qrypt mount set docs --credential other-main
qrypt mount set docs --password "new-secret" --force
qrypt mount remove docs [--purge]
```

`mount add` 的驱动类型来自所选凭据，只需配置 `root_path` 等挂载级属性。加密参数与 `[mounts.encryption]` 一一对应：`--password` 启用加密，`--salt`、`--filename-encryption`（`standard`/`obfuscate`/`off`）、`--filename-encoding`（`base32`/`base64`）、`--content-dedup` 调整细节；除密码外的加密参数都必须与密码一起使用，密码可以通过 `--password`、`--password-file` 或 `--password-stdin` 提供。`mount set` 同样支持这些加密参数，但一旦启用加密就不能再退回明文模式；修改已有加密的密码、盐或文件名模式必须加 `--force`，因为远端已有数据可能无法再读取。`mount remove --purge` 会同时删除该挂载的本地状态目录；挂载名 `add`、`set`、`remove`、`list` 保留给子命令，名称冲突时用 `qrypt mount --name NAME` 单独挂载。

配置文件的修改不会热更新到正在运行的挂载进程；执行 `qrypt config validate` 确认配置后，重启 `qrypt mount` 才会生效。

## 挂载

```sh
qrypt mount [MOUNT_NAME] [--config PATH] [--mount-point PATH]
```

省略 `MOUNT_NAME` 时挂载配置中的全部云盘；指定 `MOUNT_NAME` 时只挂载该云盘，根目录就是该云盘内容。省略 `--mount-point` 时读取配置中的 `[mount].mount_point`。运行时 socket 使用配置目录下的内置路径，无需手工指定。

`Ctrl+C`、`SIGTERM`、关闭终端或断开 SSH（`SIGHUP`）都会触发优雅卸载。如果上一次挂载进程被强行结束，qrypt 会在下次挂载时自动识别并清理残留挂载点；仍失败时参见[挂载点残留](troubleshooting.md#挂载点残留)。

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

`check A B` 比较两个目录树（`A`/`B` 可为 `/MOUNT/...` 虚拟路径或本机路径，至少一端是虚拟路径）：逐文件比对大小与修改时间（`--hash` 或 `--compare=hash` 时再比内容哈希），输出缺失/多余/不一致清单，`--json` 输出结构化结果。退出码 0 一致、4 有差异。文件与目录类型不一致会报告 `type` 差异。加密挂载透明处理：本地明文目录可直接与加密挂载比对；`--hash` 需要网盘提供内容哈希（已支持：阿里云盘 sha1、115 sha1、百度网盘 md5、OneDrive sha1；加密挂载下本地会使用远端 nonce 重算密文哈希，全程不下载文件正文）。`--compare=mtime-only` 只按修改时间判定差异（忽略大小差异，内容变了但大小与 mtime 都没变会被当作一致）。`du` 统计目录树文件数与总字节数；`df` 显示所配置驱动的空间（默认逐 mount 列出；`MOUNT` 参数或 `--mount` 只看单个）。两者默认人类可读大小，`--bytes` 输出原始字节，`--json` 输出数字。`rm`/`mv` 支持 `--json` 输出结构化结果。

`sync SOURCE DESTINATION` 单向同步：让 `DESTINATION` 匹配 `SOURCE`（`SOURCE` 权威，至少一端是虚拟路径）。默认只新增缺失文件、更新变化文件（大小，`--hash` 时含内容哈希），**不删除** `DESTINATION` 多余文件；`--delete` 才删除。`--compare` 选择比较策略：`size-mtime`（默认）、`mtime-only`（只看修改时间，忽略大小差异）、`hash`（等价 `--hash`）。`sync` 默认对支持后端内容哈希的网盘（阿里云盘 sha1、115 sha1、百度网盘 md5、OneDrive sha1）自动启用哈希比对，能检测同大小内容变化；无后端哈希的网盘（如夸克）自动降级为大小比对。注意：`mtime-only` 在无法保留 mtime 的网盘（如夸克，只记录上传时间）上会每次全量重传——只有明确知道目标端 mtime 可靠时才使用。上传会把源文件 mtime 传播给目标端；若目标网盘无法保留 mtime（如夸克，只记录上传时间），则 mtime 差异不触发内容重传（按 size/hash 判定），避免每次同步全量重传。文件/目录类型冲突默认不覆盖（报告 `conflict`，退出码 3），可用 `--conflict=skip` 跳过或 `--conflict=source` 让源端胜出。`--dry-run` 只输出计划不产生任何写入（退出码 0）；`--json` 输出结构化计划/结果（含 add/update/delete/skip/conflict/failed 汇总与按路径排序的条目）。同步执行顺序保证先建目录再传文件、先删子项再删目录；单个文件失败不中断其余文件，存在失败时退出码 3。同步会等待所有上传完成后才返回（不设超时，慢网盘不会误报失败）；Ctrl-C 可中断，中断后会话保留，用 `--resume` 继续。`--resume` 继续上次中断/部分失败的会话：只重试未完成和失败的文件，不重新扫描两棵树。会话计划保存在工作目录的 `sync/` 子目录（默认 `~/.qrypt/sync/`，同一对源/目标共享一个会话目录），全部完成后自动清除，中断遗留的会话 7 天未动会在下次同步时自动清理；`--dry-run` 不写任何会话。

`fs` 命令组还支持 `--bwlimit` 系列 flag，覆盖配置中的 `[bandwidth]` 段：

```sh
qrypt fs --bwlimit 10M list /
qrypt fs --bwlimit-download 5M get REMOTE LOCAL
qrypt fs --bwlimit-upload 2M put LOCAL REMOTE
qrypt fs --bwlimit 10M --bwlimit-upload 2M copy SRC DST
```

`--bwlimit SIZE` 同时限制下载与上传；`--bwlimit-download` / `--bwlimit-upload` 单独限制一个方向，可与 `--bwlimit` 组合（细粒度 flag 优先）。

`crypt-encode` / `crypt-decode` 用指定 mount 的 `[mounts.encryption]` 配置对文件名逐段加解密，用于对照网盘后端存储的密文文件名和本地明文路径：

```sh
qrypt fs crypt-encode [MOUNT] "hello.txt"    # 明文 → 密文（验证网盘里应显示的名字）
qrypt fs crypt-decode [MOUNT] "b4l6g..."     # 密文 → 明文（反查本地对应文件）
```

`MOUNT` 省略时使用第一个配置了加密的 mount。加密方向与挂载时 VFS 对后端文件名使用的算法一致（同一 password/salt/编码，可逐段处理多级路径）。未加密的 mount 会报错。

## 离线下载

`offline` 命令组通过本机控制 socket 把远程资源交给网盘侧的离线下载服务，并在同一个任务模型里观察、控制和拉取结果。需要运行中的 `qrypt mount`，当前支持该能力的驱动为 `quark`、`115` 和 `115_open`。

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

常用流程：

```sh
qrypt offline add magnet:?xt=urn:btih:... --parse-to /quark/downloads
qrypt offline wait <task-id>
qrypt fs get /quark/downloads/<file> ./<file>
```

来源格式、目的地规则、两阶段 fetch、并发参数和错误码见[离线下载](offline-download.md)。

## 分享与接收

`share` 命令组管理自己创建的分享链接（同步调用，不产生任务）；`receive` 命令组浏览、转存和下载别人的分享（转存与下载是持久化任务）。两者都通过本机控制 socket 与运行中的 Core 通信；provider 由挂载或链接识别，不需要 provider 名。当前支持该能力的驱动为 `quark`。

```sh
qrypt share create PATH... [--mount NAME] [--name TITLE]
                          [--expires 1d|7d|30d|forever] [--passcode[=CODE]]
                          [--limit-visits N] [--json]
qrypt share list | show | update | remove ...
qrypt receive inspect LINK [--passcode[=CODE]] [--mount NAME] [--dir ID] [--json]
qrypt receive save  LINK (--to /MOUNT/DIR | --mount NAME)
                          [--select ...] [--include GLOB] [--exclude GLOB]
                          [--mkdir] [--new] [--idempotency-key KEY] [--no-wait]
qrypt receive fetch LINK [--to /MOUNT/DIR] --download-to PATH
                          [--keep-cloud] [--workers N] [--chunk-size SIZE]
                          [--select ...] [--new] [--no-wait]
qrypt receive list | show | cancel | retry | wait ...
```

`share` 的链接操作是同步的；`receive save/fetch` 默认阻塞到终态，`--no-wait` 在任务受理后返回。`--passcode=CODE` 指定提取码，只写 `--passcode` 时 `share` 生成新提取码、`receive` 从 stdin 读一行。提取码默认掩码，`--show-secrets` 才显示明文。部分条目失败时退出码为 `3`。

常用流程：

```sh
qrypt share create /quark-download/report.pdf --expires 7d --passcode
qrypt receive save https://pan.quark.cn/s/xxxx --to /quark-download/inbox --mkdir
qrypt receive fetch https://pan.quark.cn/s/xxxx --to /quark-download/inbox --download-to ~/Downloads
```

链接与落点规则、提取码、同 provider 限制和错误码见[分享与接收](share.md)。

## 驱动信息

```sh
qrypt driver list [--json]
qrypt driver schema NAME [--json]
```

`schema` 展示驱动参数类型、必填项、默认值、示例和敏感字段标记。

## 调试

调试命令通过运行目录中的内置 debug socket 连接运行中的 debug server。Unix-like 系统使用 socket 文件，Windows 使用 named pipe。

```sh
qrypt debug collect [REMOTE] [--dest DESTINATION] --mount NAME
qrypt debug watch [REMOTE] [--jsonl] --mount NAME
qrypt debug raw ENDPOINT
qrypt debug bundle [REMOTE] [--dest DESTINATION] --mount NAME --out FILE [--force]

qrypt probe test TEST [probe flags]
qrypt probe bench TEST [benchmark flags]
```

`collect`、`watch` 和 `bundle` 需要用 `--mount NAME` 指定挂载；`--mount` 可重复传入，确需检查全部挂载时使用 `--all-mounts`。`debug watch --jsonl` 适合管道式消费。

`probe test` / `probe bench` 只使用配置中设置了 `test_enabled = true` 的挂载。测试项、输出含义和完整调试流程见[调试文档](../for-developer/debug.md)。

任务控制：

```sh
qrypt control task cancel TASK_ID
qrypt control task retry TASK_ID
```

上传队列异常时使用 `fs journal`，说明见[上传队列](troubleshooting.md#上传队列)。

## 版本与补全

```sh
qrypt version [--json]
qrypt --version
qrypt completion bash
qrypt completion zsh
qrypt completion fish
qrypt completion powershell
```

## 升级

```sh
qrypt upgrade
qrypt upgrade --check
qrypt upgrade --to <版本>   # 进阶：安装指定版本（可降级）
qrypt upgrade --force       # 进阶：当前不是发行版时仍继续
```

`qrypt upgrade` 拉取当前平台最新的正式 release，校验 `qrypt_<版本>_checksums.txt` 中的 sha256，校验通过后原子替换正在运行的可执行文件。文件先下载到临时目录，校验成功前不会改动现有二进制，因此下载失败或校验不匹配都会保留当前版本。

- `--check` 只报告当前版本与最新版本，不下载也不写入。
- `--to <版本>` 安装指定 release（例如 `v0.5.5`），可与当前版本相同或更旧。
- `--force` 在无法比较的构建（如 `v0.5.5-10-g9d16c54` 这类 `git describe` 版本）下仍继续；默认会拒绝。
- `--json` 输出单个 JSON 结果（含当前版本、目标版本与结果状态），进度信息写入标准错误。

替换的是当前运行的可执行文件本身，指向它的符号链接保持有效。可执行文件不可写时，命令不会自行提权，而是打印需要以相应权限重跑的命令（Unix 上形如 `sudo <路径> upgrade`）。

### 卸载

```sh
qrypt uninstall
qrypt uninstall --yes
qrypt uninstall --purge --yes   # 进阶：连同 ~/.qrypt 下的数据一起删除
```

`qrypt uninstall` 卸载安装脚本创建的 qrypt，并移除安装脚本写入的 `PATH` 配置。默认**保留**你的 qrypt 数据（配置、缓存、日志、会话）；加 `--purge` 才会一并删除。默认会先确认，`--yes` 跳过确认；没有可删除内容时命令成功返回并提示 `nothing to remove`。

