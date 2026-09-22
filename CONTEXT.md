# qrypt

云盘挂载与传输工具。核心工作由"任务"承载：一次可追踪、可恢复的传输或变更工作。

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

**等待供数（waiting for input）**:
条目级握手状态：条目已就绪，等待 app 写入待传数据。
_Avoid_: paused、queued

**等待取数（waiting for output）**:
条目级握手状态：数据已备好，等待 app 读取。
_Avoid_: paused、queued

**Handshake（流式握手）**:
app 与任务按"打开输入 / 提交输入 / 打开输出"交换数据的动作序列。
_Avoid_: action（action 是动作本身，不是整个序列）
