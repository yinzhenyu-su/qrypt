# 分享与接收

qrypt 把「把网盘里的内容分享出去」和「接收别人的分享」拆成两个命令组：

- `share`：管**自己创建的**链接（创建、列表、详情、改策略、删除）。
- `receive`：管**接收别人的**链接（浏览、转存到自己的网盘、下载到本地），接收动作是持久化任务。

两个组都只通过本机控制 socket 与运行中的 `qrypt mount` 通信；没有运行中的 Core 时会报运行时错误，且不会创建任务或链接。命令不要求提供 provider 名、驱动名、socket 路径或网盘侧的分享 id。

当前已支持分享/接收的驱动为 `quark`；其他驱动会在创建任务或链接时返回 `share_unsupported`。

各 provider 的分享能力并不一致，qrypt 由驱动声明策略、命令在调用前校验：例如有的 provider 强制要求提取码（`--passcode` 可省略但会生成）、有的完全不支持提取码（`--passcode` 会被拒绝）、访问人数上限和"我的分享"列表也并非每家都有。命令的帮助与报错会按当前 provider 的实际能力给出，而不是假装所有选项都可用。

## 命令

```sh
qrypt share create PATH... [--mount NAME] [--name TITLE]
                          [--expires 1d|7d|30d|forever] [--passcode[=CODE]]
                          [--limit-visits N] [--json]
qrypt share list   [--mount NAME] [--all] [--limit N] [--json]
qrypt share show   <LINK|SHARE_ID> [--show-secrets] [--json]
qrypt share update <SHARE_ID> [--name TITLE] [--expires DURATION]
                              [--passcode[=CODE]] [--public] [--json]
qrypt share remove <SHARE_ID>...

qrypt receive inspect LINK [--passcode[=CODE]] [--mount NAME] [--dir ID] [--json]
qrypt receive save    LINK (--to /MOUNT/DIR | --mount NAME)
                           [--passcode[=CODE]] [--select ...] [--include GLOB] [--exclude GLOB]
                           [--mkdir] [--new] [--idempotency-key KEY] [--no-wait] [--json]
qrypt receive fetch   LINK [--to /MOUNT/DIR | --mount NAME] --download-to PATH
                           [--passcode[=CODE]] [--keep-cloud] [--mkdir]
                           [--workers N] [--chunk-size SIZE]
                           [--select ...] [--include GLOB] [--exclude GLOB]
                           [--new] [--idempotency-key KEY] [--no-wait] [--json]
qrypt receive list  [--all] [--state STATE] [--mount NAME] [--limit N] [--json]
qrypt receive show  TASK_ID [--show-secrets] [--json]
qrypt receive cancel TASK_ID
qrypt receive retry  TASK_ID
qrypt receive wait   TASK_ID [--timeout DURATION] [--show-secrets] [--json]
```

退出码沿用通用约定：`0` 成功，`1` 运行时/任务失败，`2` 用法错误，`3` 部分条目失败，`130` 中断。

`--passcode` 是可选值参数：`--passcode=abcd` 指定提取码；只写 `--passcode` 时，`share create/update` 生成新提取码，`receive` 从 stdin 读一行。

## 分享自己的内容

链接是同步结果，`share create` 直接打印链接与有效期，不产生任务。

```sh
qrypt share create /quark-download/report.pdf --expires 7d --passcode
qrypt share create /quark-download/release.zip --expires forever
qrypt share list --mount quark-download
qrypt share show https://pan.quark.cn/s/19a64ef19d03 --show-secrets
```

- 路径必须是**挂载限定**的（`/MOUNT/DIR/...`）且已存在；一次调用里的路径必须属于同一个挂载（链接属于某个 provider）。显式给出 `--mount` 时它会与路径上的挂载核对，不一致直接拒绝，而不是静默忽略。
- `--expires` 只接受 `1d`、`7d`、`30d`、`forever`。provider 支持更长有效期时，以 provider 为准；不支持或取值为空时按用法错误拒绝。
- `--passcode` 不带值时生成一个提取码；`--passcode=CODE` 用指定提取码（provider 对长度/字符有要求，非法时直接报错）。
- `--limit-visits N` 限制访问人数，provider 不支持时会被拒绝而不是静默忽略。
- `share list` 在配置了多个挂载时需要 `--mount`；只有一个挂载时自动选择。
- `share show` 默认把提取码显示为 `****`；加 `--show-secrets` 才输出明文（转发链接时需要）。
- `share create` 默认也掩码显示提取码；需要转发时用 `qrypt share show <LINK_ID> --show-secrets` 取回。
- `share update` 至少给出 `--name`、`--expires`、`--passcode`、`--public` 之一；`--public` 会去掉提取码。
- `share remove` 接受链接 id 或分享 id，内部会解析成 provider 的管理 id；可一次传多个。

> 分享标题由 provider 决定：Quark 会忽略创建请求里的 `title`，改用被分享的第一项的名字。

## 接收别人的分享

### 账号与 provider

- 链接的 provider 由驱动识别，不需要手工指定。
- 真正执行的账号按以下顺序确定：显式 `--mount` → `--to` 的目标挂载 → 该 provider 唯一配置的挂载。
- 同一 provider 配置了多个挂载且无法唯一确定时，交互式终端会列出候选并提示选择；非交互场景（脚本、管道、`--json`）会列出候选并要求显式 `--mount`，不会替你猜账号。

### 浏览（只读）

```sh
qrypt receive inspect https://pan.quark.cn/s/b8967776e456
qrypt receive inspect https://pan.quark.cn/s/73ac68e19b29 --passcode=abcd
qrypt receive inspect https://pan.quark.cn/s/73ac68e19b29 --dir 1234
```

`inspect` 只打开链接并列出目录，不创建任务、不写网盘、不保存会话凭据；`--dir` 进入子目录，`--cursor` 翻页。

### 转存到自己的网盘

```sh
qrypt receive save https://pan.quark.cn/s/b8967776e456 --to /quark-download/inbox --mkdir
qrypt receive save https://pan.quark.cn/s/xxxx --mount quark-download --select largest:2
```

- 转存是**provider 侧复制**，字节不经过本机。
- `--to /MOUNT/DIR` 指定落点，省略时用 `--mount NAME` 的根目录；目录不存在默认报错，`--mkdir` 才创建。
- 转存任务完成后会**校验落点**：provider 列举存在最终一致，qrypt 会在有限窗口内重试列举，确认保存的条目真的出现才报成功。
- 转存按**条目**记录结果：部分条目成功、部分被 provider 拒绝时，任务是"部分失败"，成功的条目保留为任务结果，失败条目各自带中立错误码，不会把整批算成失败；命令以退出码 `3` 结束。
- 默认阻塞到任务进入终态（成功 / 部分失败 / 失败 / 取消）；`--no-wait` 在任务受理后立即返回。

### 下载到本地

```sh
# 先转存到网盘，再下载，成功后删除本次转存的云端副本
qrypt receive fetch https://pan.quark.cn/s/xxxx --to /quark-download/inbox --download-to ~/Downloads

# 保留云端副本
qrypt receive fetch https://pan.quark.cn/s/xxxx --to /quark-download/inbox \
  --download-to ~/Downloads --keep-cloud

# 不转存，直接从分享下载
qrypt receive fetch https://pan.quark.cn/s/xxxx --download-to ~/Downloads
```

- `--download-to` 可为本机路径、qrypt 命名空间路径或 Core 自身 FUSE 挂载树内的路径；允许加密挂载。
- 带 `--to` 时会先转存、再从转存后的条目下载；下载成功后只删除**本次转存**产生的条目，`--keep-cloud` 全部保留。不带 `--to` 时直接下载分享内容，不会删除任何东西。
- 多文件部分失败时不会清理已转存的副本，失败的任务可 `receive retry`。
- 目录编排、路径清洗与符号链接防护与 `offline fetch` 一致；`--workers`（1–16）和 `--chunk-size`（1–64 MiB）控制分块下载。

### 加密挂载

- **从加密挂载分享是允许的**：链接指向该挂载上的内容，qrypt 不会因此拒绝。
- **转存的云端落点不能是加密挂载**（`receive save --to /vault/...` 会被拒绝）：转存是 provider 侧复制，字节不经过 qrypt，无法满足加密挂载的密文约定。
- **下载到加密挂载是允许的**（`receive fetch --download-to /vault/...`）：这些字节会正常经过 qrypt 加密。

### 提取码

以下三种方式等价，任一即可：

```sh
qrypt receive inspect https://pan.quark.cn/s/73ac68e19b29?pwd=abcd
qrypt receive save https://pan.quark.cn/s/73ac68e19b29 --passcode=abcd --to /quark/inbox
printf 'abcd\n' | qrypt receive inspect https://pan.quark.cn/s/73ac68e19b29 --passcode
```

需要提取码而未提供时报 `share_passcode_required`，提取码错误报 `share_passcode_wrong`。`inspect`、`save`、`fetch` 都会在创建任务之前打开链接完成校验，因此提取码错误不会留下失败任务；CLI 会提示下一步用 `--passcode`（从 stdin 读一行）或 `--passcode=CODE`。

### 选择文件

`--select all`（默认）、`--select largest[:N]`、`--include`/`--exclude` 文件名 glob；`--select` 不能与 `--include`/`--exclude` 同时使用。provider 的策略没有声明选择支持时，除 `all` 外的选择会在创建任务前被拒绝。

## 任务

`receive save` 和 `receive fetch` 会返回持久化任务（`share_save` / `share_fetch`），Core 重启后会从持久化阶段恢复：

- 相同链接、账号、选择与落点的活动任务默认被复用（`--new` 强制新建，`--idempotency-key` 提供显式幂等键）。
- `receive cancel` 停止观察与传输，但**不会删除**已经开始的网盘副本；`receive retry` 从持久化记录继续，不会重复已完成的转存。
- `receive wait` 只观察；Ctrl-C 只停止等待，任务继续运行（退出码 130）。

## 同 provider 限制

v1 只支持**同 provider** 的转存：Quark 链接只能转存到 Quark 目录。目标挂载属于其他 provider 时会报 `share_cross_provider_unsupported`，并提示两步走：

```sh
qrypt receive fetch https://pan.quark.cn/s/xxxx --download-to ./tmp
qrypt fs put ./tmp /aliyun/inbox
```

跨网盘接收是"下载 + 上传"，不是 provider 侧复制，后续版本单独支持。

## 错误与退出码

接收失败使用稳定的 provider 中立错误码：

| 错误码 | 含义 |
| --- | --- |
| `share_link_invalid` | 链接无法解析，或没有配置的驱动认识它 |
| `share_link_provider_mismatch` | 执行账号与链接的 provider 不一致 |
| `share_cross_provider_unsupported` | 目标挂载是另一个 provider（v1 限制） |
| `share_unsupported` | 该挂载的驱动不支持分享/接收 |
| `share_not_found` | 链接不存在或已被删除 |
| `share_expired` | 链接已过期 |
| `share_passcode_required` | 需要提取码 |
| `share_passcode_wrong` | 提取码错误 |
| `share_own_save` | 试图转存自己的分享（provider 禁止） |
| `share_ticket_invalid` | 条目转存票据缺失/过期/不匹配 |
| `share_visit_limit` | 链接访问人数已达上限 |
| `share_quota` | 空间不足或转存数量超限 |
| `share_verification_required` | provider 要求人机验证（验证码/短信），cookie 无法满足，需先在官方客户端完成 |
| `share_rate_limited` | provider 限频，可稍后重试 |
| `share_timeout` | provider 的同步操作超出等待窗口 |
| `share_destination_unverified` | 无法在有限窗口内确认保存的条目真的出现在落点 |
| `share_provider_error` | 未能归类到上述中立码的 provider 失败 |

接收任务快照里的提取码默认显示为 `****`，`receive show/list/wait` 加 `--show-secrets` 才输出明文；`--json` 同样遵守这个规则。服务商会话令牌与条目票据不会出现在默认输出里。

退出码：`0` 成功，`1` 运行时/任务失败，`2` 用法错误，`130` 中断。
