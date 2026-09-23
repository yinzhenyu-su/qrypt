# qrypt

云盘加密挂载与传输工具。核心工作由"任务"承载：一次可追踪、可恢复的传输或变更工作。

## Language

### 任务域

**Task（任务）**:
一次可追踪、可恢复的工作单元，例如一次上传、下载、删除、复制或移动。
_Avoid_: job

**Operation（操作）**:
任务所属的稳定操作类别：upload、download、delete、copy、move 五者之一。
_Avoid_: action（action 另指任务当前可用的动作）、type

**Task Type（任务类型）**:
任务的执行形态分类。类型名后缀（remote、batch、stream、direct）为历史遗留命名，不携带统一语义。
_Avoid_: 从类型名后缀推断行为

**Scope（任务来源）**:
任务的产生来源，与可见性无关。

- **User（用户任务）**：由用户或应用显式发起。
- **Sync（同步任务）**：由 syncer 作业产生。
- **Internal（内部任务）**：挂载写入路径的内部记账记录。

_Avoid_: 用 Scope 表达可见性、background

**Visibility（可见性）**:
任务是否出现在 app 默认任务列表中。与 Scope 正交，任何来源的任务都可能可见或不可见。
_Avoid_: Scope

### 幂等

**Idempotency Key（幂等键）**:
调用方提供的去重键；同键同内容的重复提交返回同一任务，同键异内容视为冲突。
_Avoid_: OperationKey、request key

**Operation Fingerprint（操作指纹）**:
对请求内容（不含幂等键）的派生哈希，用于识别同键异内容的冲突。
_Avoid_: key

### 任务条目

**Item（任务条目）**:
批量任务中的单个工作对象，通常是一个文件或路径。其请求输入面与执行追踪面是同一概念的两个投影；追踪面覆盖全生命周期，不是仅结果。
_Avoid_: ItemResult、result

### 状态

**State（状态）**:
任务或条目在状态机中的当前位置。任务级与条目级是各自独立的状态机。
_Avoid_: phase

**Phase（阶段标签）**:
面向展示的进度阶段文字，仅供呈现。不得复用 State 的词汇——指不得从 State 派生（把 State 的字符串或枚举常量写成阶段标签）；独立声明的展示词与某个状态词同形（如 failed）不算复用。
_Avoid_: state

**等待写入（waiting for input）**:
条目级握手状态：条目已就绪，等待 app 写入待传数据。
_Avoid_: paused、queued

**等待输出（waiting for output）**:
条目级握手状态：数据已备好，等待 app 读取。
_Avoid_: paused、queued

**Handshake（流式握手）**:
app 与任务按"打开输入 / 提交输入 / 打开输出"交换数据的动作序列。
_Avoid_: action（action 是动作本身，不是整个序列）

### 挂载与命名空间

**Namespace（命名空间）**:
把多个挂载聚合在一个虚拟根下的层。虚拟路径的第一段是挂载名。
_Avoid_: drive set

**Mount（挂载）**:
挂载名与一个文件系统实例的配对，命名空间的组成单元。运行期可动态增删。
_Avoid_: driver（driver 是远端云盘后端契约）

**挂载契约（MountedFileSystem）**:
挂载必须提供的接口面：文件操作、生命周期、路径刷新与任务来源内省。
_Avoid_: API

**Virtual Path（虚拟路径）**:
虚拟树中的绝对斜杠路径，与宿主 OS 的路径语义无关。
_Avoid_: path、filepath

**Mount Failure（挂载失败）**:
配置了但初始化失败、被排除出命名空间的挂载。命名空间照常打开，失败原因单独上报。
_Avoid_: error

### 本地视图与可见性

**View（本地视图）**:
远端树在本地的镜像状态：已解析条目、目录缓存，以及本地自述状态（本地新建目录、本地修改时间）。
_Avoid_: cache、snapshot

**Entry（远端条目）**:
云盘上的一个文件或目录，按虚拟路径缓存在本地视图中。
_Avoid_: DirEntry、FileEntry

**Overlay（可见性遮蔽）**:
施加在本地视图之上的隐藏规则集合：已删除条目、改名遮蔽、复制隐藏与恢复标记。
_Avoid_: view（View 是被遮蔽的本地状态）

**Effective View（有效视图）**:
本地视图叠加遮蔽与待传投影后，列举时实际呈现的结果。
_Avoid_: view

**Rename Shadow（改名遮蔽）**:
远端改名在途期间隐藏旧路径的遮蔽项，直到旧路径消失、新路径出现。
_Avoid_: lock

**Copy-hidden Children（复制隐藏）**:
目录复制后短暂隐藏其子项，避免暴露远端尚未填满的半成品状态。
_Avoid_: cache

**Restored Dir（恢复标记）**:
被删除后重建的目录留下的标记，在限定期限内把其后代视为恢复中。

**Directory Takeover（目录接管）**:
取消目录内所有待删、并阻止其后代删除启动的接管状态。
_Avoid_: pause、paused

### 上传与暂存

**Staging File（暂存文件）**:
承载待传内容的落盘文件，上传提交前的字节来源。
_Avoid_: temp file、write-back

**Upload Generation（上传代）**:
同一虚拟路径的一次暂存内容版本。新写入开启新代；旧代的上传被顶掉即为被取代。
_Avoid_: version

**Frozen（冻结代）**:
已被快照、字节对读取方不可变的上传代。
_Avoid_: locked

**Superseded（被取代）**:
被更新的上传代顶掉、回滚且不留条目的上传结局。
_Avoid_: cancelled

**Pending Upload（待传记录）**:
已暂存但未提交到远端的文件记录。
_Avoid_: job、task（Task 另指任务域的 Task）

**Journal（暂传日志）**:
待传记录的预写式持久日志，崩溃后据此恢复未完成的上传。
_Avoid_: queue

**Quiet Window（静默窗口）**:
写入安静多久后才调度上传的防抖窗口。
_Avoid_: debounce delay

**Admission（上传准入）**:
并发上传的准入策略：同时只跑一个大文件，或多个小文件。
_Avoid_: throttle、limit

**Upload Snapshot（上传快照）**:
一次上传在提交前冻结出的内容标识与哈希。领域内唯一称 snapshot 的概念；其余同名类型只是调试投影。
_Avoid_: snapshot state

**Direct Copy（驱动直拷）**:
不经本地中转、由驱动直接搬运字节的复制方式。
_Avoid_: server-side copy

### 读取与缓存

**Read Cache（读缓存）**:
落盘的文件块缓存：块批次加分片索引，有容量上限，容量为零即禁用。
_Avoid_: cache mode（此词只在契约测试基准里使用）

**Read Window（读窗口）**:
读取、预取与合并并发读的块区间单元。
_Avoid_: buffer

**Hot Chunk（热块）**:
绕过落盘读缓存的内存块快路径。
_Avoid_: cache

**Read Run（读取趟）**:
对一个文件的一次连续正向读取。只有被另一趟命中才算复用，本趟回读自己的预读不算。
_Avoid_: session、pass

**Unproven / Promotion（未验证 / 晋升）**:
块进入缓存后处于未验证状态，直到被另一读取趟命中才晋升，防止一次性顺序流污染缓存。

**Read Session（读会话）**:
一个已打开文件句柄的读流，承载自适应预取提示；句柄关闭时丢弃其提示。
_Avoid_: open-file session、upload session（后者是 driver 层的续传会话，不是挂载域概念）

**Sequential Read Hint（顺序读提示）**:
由近期访问模式得出的预取提示。并发或跳跃读会重置提示，不影响读取正确性。
_Avoid_: detection

**Directory Prefetch（目录预取）**:
目录浏览侧的后台预取，与文件内容的预取是两个概念。
_Avoid_: prefetch

### 删除、变更与能力

**Delayed Delete（延迟删除）**:
本地提交后延迟执行的远端删除，含计时器调度、失败记录与目录接管。
_Avoid_: task（Task 另指任务域的 Task）、scheduled delete

**Mutation（变更协调）**:
建目录、改名、删除的端到端协调协议：解析、远端执行、视图提交。远端部分生效时提交中间态，以免本地与远端分歧。
_Avoid_: operation（Operation 另指任务域的稳定操作类别）

**Consumer Capability（消费能力）**:
文件系统暴露给调用方的可选接口面，有可枚举的名字，调用方按名取用。
_Avoid_: feature、permission

**Path Capability（路径能力）**:
单个路径上的动作许可矩阵：可读、可列、可上传、可建目录、可改名、可移动、可删除、可查空间。
_Avoid_: permission、Consumer Capability

### 失效

**Kernel View Invalidation（内核视图失效）**:
挂载层告知宿主内核：某虚拟路径的内核缓存已过期。发布的是失效路径，动作落在内核侧，不动本地任何缓存。
_Avoid_: Invalidation（裸词）、cache invalidation、refresh

**Read Cache Invalidation（读缓存失效）**:
丢弃读缓存中某文件的已缓存块。
_Avoid_: Invalidation（裸词）、refresh

**List Cache Invalidation（目录缓存失效）**:
丢弃某目录的缓存列举，使下一次列举重新拉取。
_Avoid_: Invalidation（裸词）、refresh
