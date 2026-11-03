## Purpose

定义 VFS 读路径与批量传输如何按调用方意图和文件生命周期选择读取策略：单文件窗口内有界并行 Range、跨文件有界并发、顺序流跨窗口预取、交互与探测单流低延迟，并保证删除、关闭或取消能够停止后台预取。

## ADDED Requirements

### Requirement: 读意图由调用方声明，缺省保持单流

系统 SHALL 接受每次读的意图声明，取值为 `bulk`、`interactive` 或 `probe`。未声明意图的调用方 MUST 默认使用现有单流窗口行为；只有满足「读会话根据访问模式自适应」条件的已打开读会话可以升级为并行。意图 MUST 随请求传播到读调度层，而不是由读 API 或驱动类型推断。

#### Scenario: 未声明意图的读保持现有行为

- **WHEN** 调用方发起读且未声明意图
- **THEN** 该读使用单流窗口与现有预取策略，不发起并行 Range

#### Scenario: 未声明意图的顺序会话可自适应升级

- **WHEN** 一个未声明意图的已打开读会话对超过阈值的大文件保持连续正向读取
- **THEN** 该会话的后续窗口可以按自适应规则使用并行 Range

#### Scenario: bulk 意图允许并行

- **WHEN** 调用方以 `bulk` 意图读取一个超过并行阈值的文件
- **THEN** 读调度层可以在该文件的读窗口内发起多个 Range 请求

#### Scenario: probe 意图不触发预取

- **WHEN** 调用方以 `probe` 意图读取文件的一小段
- **THEN** 该读只发起一次小 Range，且不调度后台预取

### Requirement: bulk 意图使用有界并行 Range

对 `bulk` 意图，系统 SHALL 将一个读窗口拆分为至多 N 个连续子 Range 并发拉取，并按 offset 拼接为同一窗口。N MUST 受该文件的总并发子 Range 上限和全局读槽位上限共同约束；该上限按文件总计，MUST NOT 按窗口重置。小于并行阈值的文件 MUST 保持单流。驱动已经实现内部并行的文件 MUST 保持单流，系统 MUST NOT 在其上叠加 VFS 并行。

#### Scenario: 大文件 bulk 拆分为多个 Range

- **WHEN** 文件大小超过并行阈值且 `bulk` 意图可用并行流
- **THEN** 一个读窗口被拆成多个子 Range 并发拉取，结果按原 offset 拼接

#### Scenario: 小文件 bulk 保持单流

- **WHEN** 文件大小低于并行阈值
- **THEN** 即使意图为 `bulk`，该文件仍使用单 Range

#### Scenario: 并行受上限约束

- **WHEN** 单文件配置的并行流数超过驱动或全局上限
- **THEN** 实际并发不超过上限，且不会因超限而失败

#### Scenario: 驱动已内部并行时不叠加

- **WHEN** 驱动已经对单次读做内部并行
- **THEN** 该驱动上的 `bulk` 读保持 VFS 单流，实际并发由驱动内部管理

### Requirement: interactive 与 probe 不得 fan-out

`interactive` 意图 MUST 对同一文件、同一窗口保持单流，并使用小窗口（默认 1–2 个块）与有界预取；`probe` 意图 MUST 只发起一次小 Range（1 个块）且不预取。两者 MUST NOT 因文件大小或驱动能力而升级为并行 Range。

#### Scenario: seek 保持单 Range

- **WHEN** 调用方以 `interactive` 意图跳转到新 offset 读取
- **THEN** 该次读只发起一个 Range，并取消该文件已有的后台预取

#### Scenario: probe 只读目标区间

- **WHEN** 调用方以 `probe` 意图读取元数据或小片段
- **THEN** 系统不预取相邻块，也不发起额外 Range

#### Scenario: probe 不受跨块优化放大

- **WHEN** `probe` 读未对齐且跨越块边界
- **THEN** 系统仍只读取目标区间所在的单个块，不套用普通读的两块边界优化

#### Scenario: 多句柄并发不放大

- **WHEN** 同一文件被多个句柄以 `interactive` 意图并发读取
- **THEN** 每个句柄各自保持单流，系统不得按句柄数再放大并行 Range

### Requirement: Range 不被支持时回退单流

并行 Range 的语义由驱动保证。声明并行 Range 能力的驱动 MUST 让子 Range 请求返回 206；无法满足时 MUST 返回明确的不支持错误，而不是把 200 全量 body 当作子 Range 结果。系统收到该错误后 MUST 对该文件回退为单流读取，MUST NOT 继续拆分子 Range，也 MUST NOT 把并行退化为多次全量下载。系统 MUST NOT 通过读取 body 长度或额外字节猜测服务端是否忽略 Range。

#### Scenario: 驱动返回不支持错误

- **WHEN** 声明并行能力的驱动无法提供子 Range，返回明确的不支持错误
- **THEN** 系统对该文件回退单流，并对后续窗口保持单流

#### Scenario: 回退后不再并行

- **WHEN** 某文件已判定不支持子 Range
- **THEN** 该文件后续读窗口保持单流，直到新的读会话或显式重试

#### Scenario: VFS 不猜测 200 全量

- **WHEN** 驱动只返回 `io.ReadCloser` 而没有状态信息
- **THEN** 系统不通过读取额外字节或 body 长度判断服务端是否忽略 Range，回退责任在驱动

### Requirement: 读策略由驱动默认值决定，不要求用户配置

系统 SHALL 从驱动声明的读策略或驱动默认值中取得并行流数、窗口块数与预取深度，MUST NOT 要求用户配置这些值。驱动 MAY 声明并行 Range 能力；若驱动通过可选接口声明最大并行读数量，实际并发 MUST NOT 超过该数值。默认值 MUST 为：`bulk` 并行流数 4、`interactive` 与 `probe` 并行流数 1；驱动未声明并行能力时 `bulk` 也按 1 流处理。

#### Scenario: 驱动默认值生效

- **WHEN** 用户没有提供任何读策略配置
- **THEN** 系统按驱动默认值选择并行流数、窗口与预取深度

#### Scenario: 驱动能力限制上限

- **WHEN** 驱动声明的并行 Range 上限低于默认值
- **THEN** 实际并发被限制到驱动上限

#### Scenario: 驱动未声明并行能力

- **WHEN** 驱动没有声明并行 Range 能力
- **THEN** 即使默认策略允许并行，该驱动的 `bulk` 读仍按 1 流处理

#### Scenario: 不新增用户可调字段

- **WHEN** 用户查看配置文档或 schema
- **THEN** 读策略不出现新的用户可调字段，`read_cache` 仍只承载 `max_size`

### Requirement: 读意图决定持久读缓存写入

显式 `bulk` 与 `probe` 意图 MUST 默认跳过持久读缓存写入；`interactive` 意图 MUST 默认写入。FUSE 读会话自适应升级为 `bulk` 后 MUST 继续写入缓存，避免挂载内重读全部 miss。缓存容量为零时，所有意图 MUST 继续短路且不产生缓存文件。配置 MAY 覆盖默认，但覆盖 MUST NOT 改变读取结果的正确性。

#### Scenario: bulk 整文件传输不写缓存

- **WHEN** 调用方以 `bulk` 意图顺序读取一个完整文件用于落地
- **THEN** 该次读取不向持久读缓存写入块文件，也不产生缓存索引条目

#### Scenario: probe 小读不写缓存

- **WHEN** 调用方以 `probe` 意图读取一小段元数据
- **THEN** 该次读取不向持久读缓存写入块

#### Scenario: interactive 保留缓存

- **WHEN** 调用方以 `interactive` 意图读取文件内容
- **THEN** 该次读取仍按现有策略写入持久读缓存

#### Scenario: FUSE 自适应 bulk 仍写缓存

- **WHEN** FUSE 读会话因持续顺序读取从交互策略升级为并行策略
- **THEN** 该会话仍写入持久读缓存，后续重读可以命中

#### Scenario: 缓存禁用时仍然短路

- **WHEN** 缓存容量为零，且任意意图发起读取
- **THEN** 不产生缓存文件或索引，读取结果仍然正确

### Requirement: 读会话根据访问模式自适应

对于没有显式意图的已打开读会话，系统 SHALL 根据同一会话的访问模式选择策略：持续顺序读取且文件超过阈值时可以升级为 `bulk`；随机跳转、seek 或多句柄并发 MUST 保持 `interactive`。自适应 MUST NOT 改变读取结果的正确性。

#### Scenario: 持续顺序读升级

- **WHEN** 同一读会话对一个大文件保持连续正向读取
- **THEN** 系统可以对该会话的后续窗口使用并行 Range

#### Scenario: 随机读保持交互策略

- **WHEN** 同一读会话出现 offset 跳转或 seek
- **THEN** 系统保持单流，并取消旧预取

#### Scenario: 并发句柄不升级

- **WHEN** 同一文件同时存在多个活跃读句柄
- **THEN** 系统不为任一单句柄升级为并行 fan-out

### Requirement: 生命周期事件取消后台预取

读缓存失效、读会话释放和调用方取消 MUST 停止对应文件的后台预取。取消 MUST 释放该预取占用的读槽位与预取名额，并 MUST NOT 让等待同一窗口的前台读永久阻塞。已完成的缓存填充 MUST NOT 因会话释放而丢弃；只有未完成的后台预取被取消。移动端句柄关闭或取消 MUST 通过显式入口停止该文件的后台预取，即使没有前台读在途。

#### Scenario: 删除文件停止预取

- **WHEN** 一个文件被删除并触发读缓存失效
- **THEN** 该文件在途的后台预取被取消，不再继续下载

#### Scenario: 关闭句柄停止预取

- **WHEN** 一个读会话被释放
- **THEN** 该会话对应的后台预取被取消，已完成缓存填充保留

#### Scenario: 移动端关闭句柄停止预取

- **WHEN** 移动端关闭或取消一个文件句柄，且该文件仍有后台预取在途
- **THEN** 该文件的预取被停止，不依赖前台读的 context 是否仍在

#### Scenario: 调用方取消停止预取

- **WHEN** 调用方取消正在进行的读请求
- **THEN** 该请求触发的后台预取一并停止

#### Scenario: 等待者不被永久阻塞

- **WHEN** 一个正在等待窗口完成的前台读遇到该窗口被取消
- **THEN** 等待者收到取消结果或自行重试，不得永久阻塞

### Requirement: 并行读保持公平与优先级

`bulk` 读默认使用低优先级，`interactive` 读默认使用高优先级；调用方显式设置的优先级 MUST 优先于意图默认值。并行读 MUST 继续复用全局读槽位，并 MUST 保证单个文件的总并发子 Range 不会饿死其他文件或交互读。

#### Scenario: 交互读优先获得槽位

- **WHEN** 多个 `bulk` 读已占满低优先级槽位，同时到达一个 `interactive` 读
- **THEN** 交互读使用高优先级预留槽位，不被批量读阻塞

#### Scenario: 显式优先级覆盖意图默认

- **WHEN** 调用方同时给出显式优先级和读意图
- **THEN** 调度使用显式优先级，意图只提供缺省值

#### Scenario: 多文件公平共享

- **WHEN** 两个 `bulk` 读并发读取不同文件
- **THEN** 任一文件的总并发子 Range 不超过单文件上限，两者共享全局槽位

#### Scenario: 单文件不无限扩张

- **WHEN** 一个 `bulk` 读持续读取同一文件并跨多个预取窗口
- **THEN** 其总并发子 Range 不超过配置上限，不因窗口数量而放大

### Requirement: 合并窗口保持每个等待者自己的缓存策略

系统 MAY 合并同一窗口的并发读。参与合并的读 MUST 在取数形态上兼容（并行/单流一致）；不兼容时等待者 MUST NOT 加入该窗口，MUST 自行发起读取。每个等待者 MUST 在收到数据后按自己的缓存写入策略处理，MUST NOT 因 loader 跳过缓存而丢失自己的缓存写入。

#### Scenario: 兼容窗口合并

- **WHEN** 两个相同意图、相同取数形态的读同时请求同一窗口
- **THEN** 它们共享一次底层窗口加载

#### Scenario: 不兼容窗口不合并

- **WHEN** 一个 `bulk` 读和一个 `interactive` 读同时请求同一窗口
- **THEN** 等待者不加入不兼容的加载，自行发起读取或按兼容策略等待

#### Scenario: 等待者按自己的策略写缓存

- **WHEN** 一个跳过缓存的 loader 与一个要求缓存的等待者共享同一次加载
- **THEN** 等待者在收到数据后仍按自己的策略写入缓存

### Requirement: 显式禁用预取的上下文优先

`WithoutReadPrefetch` 等显式禁用预取的上下文 MUST 优先于读意图；即使意图为 `bulk`，也不得调度后台预取。`probe` 意图本身也 MUST 禁用预取。显式禁用 MUST NOT 关闭前台读或改变读取结果。

#### Scenario: bulk 与显式禁用同时存在

- **WHEN** 调用方以 `bulk` 意图读取，同时传入 `WithoutReadPrefetch`
- **THEN** 该读可以使用并行 Range，但 MUST NOT 调度后台预取

#### Scenario: probe 禁用预取

- **WHEN** 调用方以 `probe` 意图读取
- **THEN** 系统不调度后台预取

#### Scenario: 显式禁用不影响前台读

- **WHEN** 调用方传入 `WithoutReadPrefetch` 并执行前台读
- **THEN** 读取结果与未传入时一致，只有预取被关闭

### Requirement: 批量落地使用有界跨文件并发

`fs get` 目录与 `fs sync` 等批量落地操作 SHALL 使用有界跨文件 worker 池，固定默认 worker 数为 4，MUST NOT 要求用户配置。每个 worker MUST 以 `bulk` 意图读取；跨文件并发与单文件并行 MUST 共享全局读槽位，单个大文件 MUST NOT 因并行子 Range 挤占全部 worker 或槽位。取消 MUST 停止未开始的 worker 并释放已占用槽位；进度 MUST 保持单调。

#### Scenario: 多个小文件并发落地

- **WHEN** 一个目录包含多个小文件
- **THEN** 最多 4 个文件同时读取，其余文件排队，进度单调前进

#### Scenario: 大文件不饿死其他文件

- **WHEN** 一个目录同时包含一个大文件和多个小文件
- **THEN** 大文件的并行子 Range 与跨文件 worker 共同受全局槽位约束，小文件仍能获得 worker

#### Scenario: 取消停止未开始的 worker

- **WHEN** 用户在批量落地过程中取消操作
- **THEN** 未开始的 worker 不再启动，在途 worker 被取消并释放槽位

### Requirement: ReadStream 跨窗口预取

顺序流读取 SHALL 在当前窗口被消费的同时预取下一个窗口，预取深度 MUST 有界并受读策略约束。预取 MUST NOT 改变返回字节顺序，MUST NOT 让内存随文件大小增长。取消或生命周期事件 MUST 停止在途预取并释放槽位；缓存写入 MUST 遵循流的读意图。

#### Scenario: 窗口之间保持吞吐

- **WHEN** 顺序流消费完当前窗口且文件仍有后续内容
- **THEN** 下一个窗口已开始或完成预取，读取不因窗口边界出现停顿

#### Scenario: 预取内存有界

- **WHEN** 顺序流读取一个远超窗口大小的文件
- **THEN** 在途预取窗口数不超过配置深度，内存不随文件大小增长

#### Scenario: 取消停止流预取

- **WHEN** 调用方取消顺序流或关闭读会话
- **THEN** 在途的下一窗口预取被停止并释放槽位

#### Scenario: 流缓存遵循意图

- **WHEN** 顺序流以显式 `bulk` 意图读取
- **THEN** 该流不写持久缓存；以 `interactive` 意图读取时按现有策略写入

### Requirement: 直读路径使用相同的并行策略

`fs copy` / `copy` 任务 / `share fetch` 等绕过 VFS 的 `driver.Read` 直读路径 SHALL 使用与 VFS 读相同的驱动能力、默认并行流数和单文件总并发上限；MUST NOT 写入 VFS 读缓存。驱动未声明并行能力或已内部并行时 MUST 保持单流。取消 MUST 停止在途子 Range 并释放并发名额。

#### Scenario: drivecopy 大文件并行

- **WHEN** `fs copy` 或 `copy` 任务读取一个超过并行阈值的大文件，且驱动声明并行 Range 能力
- **THEN** 该直读路径使用有界并行子 Range，并按 offset 拼接

#### Scenario: share fetch 回退直读并行

- **WHEN** `share fetch` 无法使用 provider URL，回退到 `driver.Read`
- **THEN** 该回退路径使用相同的并行策略与单文件上限

#### Scenario: 驱动未声明能力时单流

- **WHEN** 直读路径使用的驱动没有声明并行 Range 能力
- **THEN** 该路径保持单流

#### Scenario: 直读不写 VFS 缓存

- **WHEN** `fs copy` / `copy` 任务 / `share fetch` 通过直读路径读取
- **THEN** 不产生 VFS 读缓存块或索引条目

#### Scenario: 取消停止直读子 Range

- **WHEN** 调用方取消直读路径
- **THEN** 在途子 Range 被停止并释放并发名额

### Requirement: 现有调用方与配置保持兼容

未声明意图的调用方、未配置读策略的挂载以及既有驱动 MUST 在升级后保持原有读取行为与结果。`drive.Driver.Read` 的契约 MUST NOT 因本能力改变。

#### Scenario: 旧调用方行为不变

- **WHEN** 既有调用方不声明意图直接读取
- **THEN** 读取结果、缓存命中与预取行为与升级前一致

#### Scenario: 旧配置可继续加载

- **WHEN** 配置文件不包含读策略字段
- **THEN** 配置加载成功，并使用安全默认值

#### Scenario: 驱动无需改动

- **WHEN** 驱动只实现现有 `Read` 契约且未声明并行 Range 能力
- **THEN** 该驱动的读保持单流，不影响正确性

#### Scenario: ReadRaw 不受影响

- **WHEN** 调用方使用 `ReadRaw`
- **THEN** 该路径绕过读意图、并行与缓存策略，行为与升级前一致
