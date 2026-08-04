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

### 退出码

脚本可依赖以下进程退出码，语义保持稳定：

| 码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 运行时/传输错误（网络、驱动等） |
| 2 | 用法错误（参数或 flag 拼错） |
| 3 | 操作部分成功（如目录复制有文件成功也有失败） |
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
qrypt mount [MOUNT_NAME] [--config PATH] [--mount-point PATH] [--socket PATH]
```

省略 `MOUNT_NAME` 时挂载配置中的全部云盘；指定 `MOUNT_NAME` 时只挂载该云盘，根目录就是该云盘内容。省略 `--mount-point` 时读取配置中的 `mount_point`。`--socket` 启动本机调试接口。

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

`list` 支持 `--jsonl`（JSON Lines）：每行输出一个条目的 JSON，适合大目录流式消费（`jq -s` 可聚合）；`--json` 与 `--jsonl` 互斥。`list --human` 以 K/M/G/T 显示大小。`find` 递归查找名字包含 `NAME`（大小写不敏感）的条目，`--json` 输出匹配条目列表。`du` 统计目录树文件数与总字节数；`df` 显示所配置驱动的空间（默认逐 mount 列出；`MOUNT` 参数或 `--mount` 只看单个）。两者默认人类可读大小，`--bytes` 输出原始字节，`--json` 输出数字。`rm`/`mv` 支持 `--json` 输出结构化结果。

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

## 驱动信息

```sh
qrypt driver list [--json]
qrypt driver schema NAME [--json]
```

`schema` 展示驱动参数类型、必填项、默认值、示例和敏感字段标记。

## 调试

运行时调试命令使用 `--socket PATH`、`--url URL` 或配置中的
`[debug].listen` 连接运行中的 debug server。`collect`、`watch` 和
`bundle` 还需要说明要检查哪个挂载：平时请用 `--mount NAME`；确实需要
检查全部挂载时，再用 `--all-mounts`。

```sh
qrypt debug collect [REMOTE] [--dest DESTINATION] --socket PATH --mount NAME
qrypt debug collect [REMOTE] [--dest DESTINATION] --url http://127.0.0.1:19090 --mount NAME
qrypt debug watch [REMOTE] [--jsonl] --socket PATH --mount NAME
qrypt debug test TEST --socket PATH
qrypt debug raw ENDPOINT --socket PATH
qrypt debug bundle [REMOTE] [--dest DESTINATION] --socket PATH --mount NAME --out FILE [--force]
```

也可以在配置中启用 HTTP 调试入口：

```toml
[debug]
enabled = true
listen = "127.0.0.1:19090"
```

启用后，debug 命令可以省略 `--url`：

```sh
qrypt debug collect --mount NAME
```

`--mount` 可以重复传入多个挂载名。`--all-mounts` 会收集全部挂载，输出可能更大，请只在需要整体排查时使用。

`debug watch --jsonl` 会逐行输出每个采样样本的 JSON（而非单个汇总报告），适合管道式消费：`qrypt debug watch --jsonl ... | jq -c '.uploads[]'`。

跨挂载传输问题应同时提供源路径和 `--dest`；报告会分别收集两端路径状态、读取/上传历史和挂载能力。

driver 和 VFS 测试通过 `debug test` 显式执行，`TEST` 可为 `auth`、`crud`、`instantupload`、`fs`、`resume` 或 `xfer`。这些测试只会使用在 `[[mounts]]` 中设置了 `test_enabled = true` 的挂载；`xfer` 要求 source 和 dest 都启用。`auth` 是只读认证探测，其他测试可能创建临时远端对象。`fs` 会检查 VFS 写入、上传、读取和清理流程；`resume` 会故意取消一次上传，用来确认断点续传或重试恢复是否正常。

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
