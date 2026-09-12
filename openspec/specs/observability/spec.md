# observability Specification

## Purpose
定义 qrypt 运行期观测面必须留下的信号及其有界性，使"盘为什么变慢"这类问题能在进程运行期间被回答，而不依赖事后复现。

## Requirements

### Requirement: Read-event history retains multiple complete reads

每个挂载的读事件历史 SHALL 同时保留最近多次**完整读取**的汇总事件与其阶段明细事件。保留量 MUST NOT 小到让同一次读取产生的明细事件挤掉该次读取自身的汇总事件，也 MUST NOT 让任意一次读取的明细挤掉更早读取的汇总事件。可观察结果：在连续发生 N 次读取之后（N 不超过保留量所保证的下界），调试快照与 `/v1/reads` MUST 同时包含这 N 次读取各自的汇总事件。

#### Scenario: Consecutive reads remain visible together

- **WHEN** 一个挂载上连续发生多次读取，且每次读取产生 1 条汇总事件与随 chunk 数量增加的明细事件
- **THEN** 调试快照的读事件历史 MUST 包含这些读取各自的汇总事件，且每条汇总事件 MUST 保留其字节数、缓存命中/未命中与阶段耗时字段

#### Scenario: A detail-heavy read does not evict earlier summaries

- **WHEN** 一次读取产生的明细事件数达到单次读取的常见上界（多 chunk 窗口加相邻预取）
- **THEN** 先前读取的汇总事件 MUST 仍在读事件历史中可见

#### Scenario: Read errors stay diagnosable

- **WHEN** 一次读取以错误结束
- **THEN** 该次读取的汇总事件 MUST 保留在历史中，并携带其错误状态，使该错误不因后续成功读取而被立即覆盖

### Requirement: Read-event retention is bounded

读事件历史 SHALL 有固定的编译期上界，MUST NOT 随进程运行时长或读取次数增长。保留量的变更 MUST 同时作用于所有消费者，使调试快照与 `/v1/reads` 观察到同一份历史与同一个上界。

#### Scenario: Retention does not grow without bound

- **WHEN** 一个挂载持续处理的读取次数远超保留量
- **THEN** 历史条目数 MUST 停留在上界，且历史占用的内存 MUST NOT 随读取次数增长

#### Scenario: All consumers report the same history

- **WHEN** 通过调试快照与 `/v1/reads` 分别读取同一挂载的读事件历史
- **THEN** 两者 MUST 返回同一组事件与同一保留上界

### Requirement: The durable log contains every level

落盘的主日志 SHALL 包含所有通过级别过滤的日志行，使单独读取主日志即可获得按时间顺序的完整记录。错误日志 SHALL 是主日志中 warn 及以上行的子集，而不是这些行唯一的去处。当主日志与错误日志指向同一输出目标时，每行 MUST 只写入一次。

#### Scenario: Errors are readable in the main log

- **WHEN** 一条 error 级日志被写入
- **THEN** 主日志包含该行，且其时间戳与错误日志中同一行一致

#### Scenario: The error log stays a filtered view

- **WHEN** 一条 info 级日志被写入，随后一条 warn 级日志被写入
- **THEN** 错误日志不包含该 info 行，且包含该 warn 行；主日志同时包含两者

#### Scenario: A single output target is written once

- **WHEN** 主日志与错误日志指向同一个输出目标（例如都回退到标准错误）
- **THEN** 每一条日志行在该输出中只出现一次，不因级别产生重复

### Requirement: The logger is installed through a single path

日志器的安装 SHALL 只通过一个入口完成。安装时全局日志器 MUST 保持同一对象，使任何已取得该引用的调用点在安装之后继续写入新配置的输出目标。

安装 MUST 只释放**日志器自己打开的**输出 sink（其从路径创建的日志与错误文件），MUST NOT 关闭任何被交进来的 writer。进程级共享的 writer（标准错误、标准输出）不属于日志器所有，关闭它会使进程此后无法再向该目标输出，因此无论它是否实现关闭接口都 MUST 保持打开。`Close` MUST 遵循同一所有权规则。

#### Scenario: Replacing closes the previous output targets

- **WHEN** 一个已经打开自身文件 sink 的日志器被新的日志器替换
- **THEN** 旧日志器自己打开的文件 sink 被关闭（不泄漏文件句柄），且新的输出目标开始接收后续日志行

#### Scenario: A borrowed writer survives replacement

- **WHEN** 全局日志器当前是被交进来的 writer（例如标准错误）且被文件日志器替换
- **THEN** 该 writer 保持打开，替换之后进程仍能向它写出内容

#### Scenario: CLI output reaches the terminal after installing the logger

- **WHEN** 命令在安装文件日志器之后向标准错误写下输出（例如挂载 banner 或 cobra 打印的错误信息）
- **THEN** 该输出出现在终端，而不是因为标准错误已被关闭而静默丢弃

#### Scenario: A captured reference follows the replacement

- **WHEN** 调用方在替换发生前取得了全局日志器的引用，替换后才写入日志
- **THEN** 该引用写入的是替换之后的输出目标与级别配置

### Requirement: Known credential parameters are masked before output

写入任何输出目标之前，已知令牌参数名的值 SHALL 被掩码。掩码 MUST 在级别过滤之后、写入之前对每一条日志行执行，因此主日志与错误日志同等受保护。已知令牌参数名 MUST 至少覆盖：断点续传的会话标识、访问令牌与刷新令牌、通用令牌参数、授权头，以及带签名的对象存储访问凭据参数。

#### Scenario: An upload resume credential is masked at info level

- **WHEN** 一条 info 级日志包含 multipart 断点续传的会话标识参数
- **THEN** 主日志与错误日志中出现的是掩码值而不是该标识

#### Scenario: Token parameters and authorization headers are masked

- **WHEN** 一条日志行包含访问令牌、刷新令牌、通用令牌参数或授权头
- **THEN** 这些值在写入时被掩码，且掩码不改变行内其余字段

#### Scenario: Masking applies to both sinks

- **WHEN** 一条包含上述参数的 warn 及以上日志被写入
- **THEN** 主日志与错误日志中的该行都已完成掩码，不存在只有一侧受保护的情况

### Requirement: Per-mount operation counters are cumulative and bounded

每个挂载 SHALL 提供累计的操作数、错误数与字节数计数，记录路径 MUST 为常数开销（不取锁、不分配、不存储单次操作），且计数占用的存储 MUST 与操作次数无关。计数 MUST NOT 因时间窗口滑动而重置，因而能与有界的事件历史并存：快照同时给出累计值与保留窗口，读者可以区分"自启动以来的总量"与"最近一段的样本"。

#### Scenario: Counters survive the event window

- **WHEN** 一个挂载完成的操作数远超其事件历史保留量
- **THEN** 累计计数反映全部操作，而事件历史只保留最近一段；两者同时在快照中可见且不互相影响

#### Scenario: Failed operations are counted separately

- **WHEN** 若干操作成功、若干操作失败
- **THEN** 累计操作数包含全部尝试，累计错误数等于失败次数，且失败操作携带的字节数不污染成功字节统计

#### Scenario: Recording does not allocate

- **WHEN** 一次读取完成并记录计数
- **THEN** 记录过程不产生堆分配，也不获取任何互斥锁

### Requirement: Latency distribution is available as a bounded histogram

每个挂载 SHALL 提供固定桶的延迟直方图，桶数与边界为编译期常量、不随操作次数增长。直方图 MUST 覆盖从毫秒级到秒级的量程并包含溢出桶，使高分位（p95/p99）可由直方图派生而无需保留单次操作的耗时。派生的均值与分位数 MUST 与直方图内容一致。

#### Scenario: Tail latency is derivable

- **WHEN** 一批操作的延迟跨越多个桶
- **THEN** 分位数由桶计数派生，且 p95 不小于 p50，p99 不小于 p95

#### Scenario: Overflow bucket keeps the distribution honest

- **WHEN** 存在超出最大桶边界的操作
- **THEN** 这些操作计入溢出桶，且分位派生不把它们丢出总量

#### Scenario: Histogram storage is constant

- **WHEN** 记录的延迟样本数增长若干个数量级
- **THEN** 直方图占用的存储不变，且快照中只有桶边界与计数

### Requirement: Counters are exposed per mount in the debug snapshot

累计计数与延迟分布 SHALL 出现在每个挂载的调试快照中，并与该挂载的事件历史来自同一次读取，使消费方无需额外聚合或多次请求即可获得吞吐与尾延迟。新增暴露 MUST 是快照的追加字段，不破坏既有消费方的解析。

#### Scenario: Snapshot carries counters per mount

- **WHEN** 消费方读取调试快照
- **THEN** 每个挂载都带有累计操作数、错误数、字节数、延迟直方图与派生的均值/分位数

#### Scenario: Zero-activity mount reports zeros without error

- **WHEN** 一个挂载自启动以来没有处理过任何操作
- **THEN** 其计数全部为零、直方图为空，且快照仍然可解析、派生分位数不产生除零错误

### Requirement: Log lines can be attributed to a mount

带挂载作用域的调用点产生的日志行 SHALL 携带该挂载的标识，该标识 MUST 在写入任何输出目标之前成为行的一部分，且 MUST NOT 依赖消息文本中的路径反推。挂载标识与行的其余部分 MUST 互不干扰：既有消息内容、级别与采样语义保持不变。

#### Scenario: Attributed lines carry their mount

- **WHEN** 一个带挂载作用域的调用点写入一条日志行
- **THEN** 该行携带对应挂载的标识，能在日志事件查询中作为字段读出

#### Scenario: Attribution survives logger replacement

- **WHEN** 日志器被替换为新的输出目标或级别配置，随后一个带作用域的调用点写入日志
- **THEN** 该行写入替换之后的输出目标，并仍携带该挂载标识

#### Scenario: Unattributed lines are unchanged

- **WHEN** 一个尚未接入作用域的调用点写入日志
- **THEN** 该行照常写入且不带挂载标识，不因缺少作用域而丢行或改变格式

### Requirement: Sampling state is isolated per mount

采样 MUST 按挂载隔离：两个挂载的相同调用路径使用同一采样键时，各自的采样状态 MUST 独立计算，一个挂载的抑制 MUST NOT 吞掉另一个挂载的行或把它的行计入 `suppressed`。

#### Scenario: One mount's suppression does not swallow another's line

- **WHEN** 两个挂载的同一采样键在采样间隔内各产生一条日志
- **THEN** 两个挂载各自输出自己的第一条，互不抑制

### Requirement: The log query surface filters on fields, not message text

日志事件查询 SHALL 支持按挂载标识与组件标识做服务端过滤，且这两个过滤 MUST 基于事件的字段而不是对消息文本的匹配。过滤结果 MUST 与该事件被写入时的字段值一致。

#### Scenario: Filtering by mount returns only that mount's lines

- **WHEN** 消费方按挂载标识查询日志事件
- **THEN** 结果只包含该挂载的行，其他挂载的行不出现

#### Scenario: A message that merely mentions another mount is not returned

- **WHEN** 一条由挂载 A 产生、消息文本恰好包含挂载 B 名称的日志行被写入
- **THEN** 按挂载 B 过滤时该行不出现

#### Scenario: Component filtering keeps its meaning

- **WHEN** 消费方按组件标识查询日志事件
- **THEN** 返回的是消息前缀组件等于该标识的行，与按消息文本解析前缀的结果一致，且没有前缀的行不被匹配
