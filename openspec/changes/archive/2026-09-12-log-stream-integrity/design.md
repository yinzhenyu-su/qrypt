## Context

见 proposal.md - Why。设计相关的现状约束：

- 三条写行路径各自决定 sink：`logf`（`pkg/logging/log.go:217-228`）、`logfEvery`（`:260-265`）、`logEveryFunc`（`:306-315`），三处都是同一个 `if warn && errWriter != nil / else if writer` 形状，因此必须一起改，否则采样行与普通行的去向会不一致。
- `Logger` 已经持有全部需要的信息：`writer`、`errWriter`、`lj`/`errLj`（lumberjack 句柄），且 `ReplaceDefault`（`log.go:417-442`）已经实现了原地改写 + 关闭旧句柄；缺的只是让 CLI 也走它。
- 未配置文件输出时 `New` 令 `writer == errWriter == os.Stderr`（`log.go:181-184`），因此"同一 sink"是默认路径而非边缘情况，重复行会直接打到终端。
- `sanitize` 在 `fmt.Sprintf` 之后、取锁之前执行（`log.go:212`、`:241`、`:301`），是唯一的脱敏点；`drive.Snippet` 与 `driverutil.URL` 各自保护错误串与指标 URL，与日志行脱敏是三套独立机制。
- `internal/cli/runtime_config.go:51` 的 `logging.L = newLogger` 是包级变量的无同步写，而 `pkg/core/core.go:743` 用的是 `ReplaceDefault`。

## Goals / Non-Goals

**Goals:**

- 主日志成为可单独按时间顺序阅读的完整记录；错误日志退化为它的 warn+ 视图。
- 日志器安装收敛到一个入口，并让"旧句柄被关闭"与"全局引用稳定"成为可断言的契约。
- 已知令牌参数名在写入任何 sink 之前被掩码，覆盖当前已知的泄漏点（quark Info 级 `upload_id=`）。

**Non-Goals:**

- 不引入结构化日志、字段化输出、JSON 格式或日志关联（mount/op 串接）——那是独立的 change。
- 不做通用 DLP 或"猜测敏感值"：只补充已知参数名的名单，不改变"黑名单 + 参数名匹配"的既有模型。
- 不改级别语义、采样行为、`Events` 环形缓冲与 `/v1/events` 契约。
- 不新增配置项（例如"错误日志是否重复写入"的开关）。

## Decisions

### 1. 主日志全量、错误日志为其子集，而不是反过来

写入顺序为先主日志、后错误日志（当 `errWriter != writer` 且级别 >= warn）。考虑过的替代方案：

- **只写错误日志、主日志保持 info/debug**：等于维持现状，`tail qrypt.log` 仍看不到错误。否决。
- **只写主日志、取消错误日志**：破坏"用一个文件快速看错误"的既有用途，且用户配置里的 `logging.error_file` 会失去意义。否决。
- **主日志写全量、错误日志写全量**：错误日志不再是过滤器，体积与主日志等同。否决。

代价是 warn+ 会落两份；相对 info/debug 的量级，这个代价换来主日志可单独阅读，是划算的。

### 2. 单 sink 时按 writer 身份判断，而不是按路径字符串比较

`errWriter != writer` 用接口身份比较（含 `os.Stderr` 与 lumberjack 句柄两种情况），比在 `New` 里比较路径字符串更可靠——路径可能一个是相对路径一个是绝对路径而实际指向同一文件，字符串比较会漏判并产生重复行。`Logger` 已有的 `errLj != lj` 判断（`log.go:392`、`:406`）用的是同一原则，保持一致。

### 3. 掩码名单按"参数名 + 值形态"精确扩展

新增模式匹配 `name="value"` 与 `name=value` 两种形态，覆盖：`upload_id`/`uploadId`、`access_token`、`refresh_token`、`token`、`Authorization:`、`OSSAccessKeyId`、`Signature`。不采用"凡是含 token 的键都掩码"这类宽泛规则，避免把 `tokenizer=`、`token_expired=true` 之类误伤成乱码——日志可读性本身也是诊断能力。

**实现形态：每个值形态一条 alternation，而不是每个参数名一条正则。** 理由：`sanitize` 对每一条日志行执行，正则次数直接乘在热路径上。实测（`pkg/logging` 内新增的基准）每条典型行的 `sanitize` 成本约 7-9 µs / 36 次分配；按参数名展开会新增 14 次正则执行，合并为两条 alternation 后只新增 2 次（总模式数 11），代价降回可忽略区间。值替换用捕获组回填参数名（`$1="***"`），行为与逐名匹配一致；`\b` 边界避免与既有的 `ctoken=` 规则互相干扰。

`token=` 会与已有的 `ctoken=` 规则重叠，但替换结果一致（都掩码），无冲突。

## Risks / Trade-offs

- **[warn+ 落两份带来额外磁盘占用与写入放大]** → warn 以上相对 info/debug 稀疏；滚动配置（100 MB × 7）不变，且这是主日志可读性的必要代价。若实测占比异常，再考虑按级别分流为独立文件的方案。
- **[掩码名单永远不完整，新增驱动可能引入未覆盖的凭据参数名]** → 本 change 把名单集中在一处并加测试；同时以 `drive.Snippet` 与 `driverutil.URL` 作为纵深防御。真正的根治需要结构化日志（字段级标注敏感），已在 Non-Goals 中显式排除。
- **[`token=` 之类的通用规则理论上会掩码非敏感内容]** → 只匹配 `name=` 形态且值必须存在，误伤上限是某行少一段文本；相比凭据泄漏，这个方向是安全的。
- **[CLI 安装路径改为 `ReplaceDefault` 会关闭旧 logger 的 writer]** → 这正是要修的行为；但需确认没有别处仍持有并继续使用旧 logger 的 writer（`logging.L` 之外没有其他持有者，`internal/cli` 与 `pkg/core` 都在启动阶段调用一次）。
