# 实施上下文：按消费推进的会话预取

本文供新会话或其他开发者接手 `adapt-session-prefetch-to-consumption`，集中列出完成规范必需的背景、代码入口、基线、约束和验证条件。它是 **2026-11-16 的交接快照**；行为要求以 [规格](specs/read-pipeline/spec.md) 和 [设计](design.md) 为准，进度以 [任务清单](tasks.md) 为准。后续工作树、账号和临时产物可能变化，开工前必须复核。

## 1. 接手时先确认什么

1. 阅读仓库 [AGENTS.md](../../../AGENTS.md) 与本机 `~/.claude/CLAUDE.md`；工具链、格式化和提交检查以当前仓库文件为准。
2. 阅读本 change 的 [proposal](proposal.md)、[spec](specs/read-pipeline/spec.md)、[design](design.md)、[tasks](tasks.md)。四份规划已完成，20 项实施任务均未勾选，本次预取信用算法尚未实现。
3. 阅读前置 change 的 [tasks](../unify-read-policy-execution/tasks.md)、[read-pipeline delta](../unify-read-policy-execution/specs/read-pipeline/spec.md) 和 [validation](../unify-read-policy-execution/validation.md)，尤其最后两节慢链路实验与未知长度门禁。
4. 阅读 [读取执行架构](../../../docs/for-developer/read-policy.md)、[调优 skill](../../../.agents/skills/qrypt-read-tuning/SKILL.md) 与 [播放会话测量方法](../../../.agents/skills/qrypt-read-tuning/references/playback-measurements.md)。云端实验前必须掌握其中的缓存、FD、交替顺序、构建冻结及清理要求。
5. 重新记录 `git status --short --branch`、`git rev-parse HEAD`、`git diff HEAD --stat`、`git ls-files --others --exclude-standard`、`go version`。不要清理已有索引或工作树来制造“干净基线”。

导出时仓库根目录为 `/Users/yinzhenyu/Code/github/qrypt-org/qrypt`，分支为 `fix-remain-issue`，HEAD 为 `7e99b88eb950f4ef6e9e46806f6af58833aff9ba`，模块与本地工具链为 Go 1.27.4，平台 darwin/arm64；[CI workflow](../../../.github/workflows/ci.yaml) 也指定 Go 1.27.4。

**HEAD 不包含完整性能基线。** 当前索引中已有多项前置修复，本 change 目录尚未跟踪。仅 checkout HEAD、仅保存未暂存 `git diff`，都会漏掉必要内容。冻结 A 必须覆盖 HEAD 以上的已暂存、未暂存和必要的新文件；先确认来源与范围，不执行会覆盖他人工作的 reset/checkout。

## 2. 要解决的问题与已有证据

用户先前关注 Quark seek 首帧/连续播放性能，已完成读取策略统一、按块交付、播放会话滑动预取等工作。后续在 SFTP 高 RTT 场景修复了读取生命周期和固定三秒接管，但短会话仍有较大预取尾部。

本 change 直接承接的最后一份 SFTP 结果：

| 指标 | 历史最终构建结果 |
| --- | ---: |
| 调用方成功读取 | 16 MiB |
| 执行器明文 body_bytes | 52,838,404 字节，约 50.39 MiB |
| 执行字节放大 | 3.149414× |
| Range 次数 / 来源峰值并发 | 34 / 4 |
| 三秒等待后接管 | 0 |
| 8 MiB 连续段 | 56.281 秒，0.142143 MiB/s |
| 四个 seek 首个 1 MiB 的 P95 | 7688.104 ms |
| 四个 seek 各读 2 MiB 的合计耗时 | 65.659 秒 |
| Range 开始/结束配对 / 关闭后活动读取 | 恰好一次 / 空 |

最终 canceled/superseded 的 19 个预取加载合计收到 42.875 MiB；其中包含已交付前缀，**不能把它们全算浪费**。3.149× 是完整短负载的执行字节倍率，既不是纯重复下载倍率，也不是线缆流量。

原固定三秒接管的三轮 A/B 中，放大约 4.51×→3.15×、请求 70→34、接管 19→0，连续速率提高约 58–59%。该收益来自目标进展复用；新规范要解决剩余的提前下载，不应撤销这个修复。

这些结果不能证明长播放满带宽、每个 seek 都更快或所有驱动均受益。历史控制的第一个 seek 可缓冲命中，而较快候选尚未预取到该处；所有点都必须保留，不能挑掉不利点。

## 3. 必须保留的前置工作树修复

以下均已存在于导出时工作树，属于 A 控制基线，不是本 change 的待开发算法：

| 修复 | 主要文件 | 防止再次引入的问题 |
| --- | --- | --- |
| SFTP 打开与 body 生命周期分离 | `pkg/drivers/sftp/operations.go`、`read_lifetime_test.go` | 原 15 秒操作 context 不再截断持续推进的大 body；body 使用调用方 context，关闭不取消调用方或破坏兄弟 Range |
| 正确区分 provider deadline 与共享 total | `pkg/drive/fetch_range.go`、`fetch_range_timeout_test.go` | provider 自己的 DeadlineExceeded 不能冒充共享总期限；调用方取消单独分类 |
| 同会话尾页重叠保持连续读取趟 | `pkg/vfs/read/state.go`、`aligned_session_test.go` | macFUSE 的不足一块尾页重读不能每次触发 seek；无新增后缀不推进游标；真回退/跳空仍是 seek |
| 目标块进展感知等待 | `pkg/vfs/read/prefetch_wait.go`、`reader.go`、`slow_prefetch_test.go` | 查询合并和注册合并均复用仍在推进的目标；目标停顿仍有界接管 |
| 带偏移的唯一接收前沿 | `pkg/drive/read_trace.go`、`read_source.go`、`read_progress_test.go` | 重放计流量但不刷新进展时刻；并行前沿不能当作连续有效数据 |
| 未知长度渐进门禁 | `windowLoad.progressive`，位于 `state.go`，在 `reader.go` 创建加载时设置 | 未知长度整窗校验不能因收到暂不可交付的前缀而延长原固定等待 |
| 来源范围与预取计划观测 | `pkg/vfs/read/helpers.go`、`prefetch_session.go`、`pkg/vfs/diagnostics/read.go`、`pkg/drive/read_source_test.go`、`pkg/vfs/read/stage_observability_test.go` | 区分计划/准入/实际来源请求，能核对开始结束与真实就绪块，不因观测改变热缓存 |
| 测量方法与只读 runner | `.agents/skills/qrypt-read-tuning/`、`scripts/dev/measure-playback-session.py`、开发者文档与前置 change 文档 | 保留缓存/同 FD/取消尾部口径，不把短测当完整验收 |

检查 `git diff HEAD` 时还会见到相关测试、说明和 skill 改动；不要只挑主实现文件构建 A。真实读取字节、Driver.Read、加密认证和缓存格式均须保留。

## 4. 最少必须理解的代码流程

```text
挂载装配创建并持有 ReadSource（同一源/对象共享预算）
  → Reader.Read 读取 hint / 判断读取趟 / 解析阶段计划
  → 可选 offset-zero 首窗预热
  → readRange：缓存、已有加载合并或前台取数
  → 真实成功返回的数据与当前请求身份核验
  → schedulePrefetch / updateSessionPrefetch
  → fillSessionPrefetch 选择最近缺口、完整非阻塞准入
  → 渐进预取发布完整块；完成或容量释放触发边界内补充
```

| 代码入口 | 实施时必须理解的点 |
| --- | --- |
| [read_policy.go](../../../pkg/drive/read_policy.go) | 完整 ReadPolicy 与稀疏 ReadOverride 区别；Initial/Seek/Sequential 的窗口和预取模式；默认值、尺寸/能力约束 |
| [plan.go](../../../pkg/vfs/read/plan.go) 的 `ReadPlan` / `ResolveStage` | 当前只保留解析后的窗口/深度，不保留模式；新算法需要区分 adaptive 与 fixed，不能根据字段来源猜 |
| [reader.go](../../../pkg/vfs/read/reader.go) 的 `Read` | 有 hint 时访问序列在取数前更新；首窗预热可能先于会话控制器准入；成功返回后还有 stale 身份检查 |
| 同文件 `schedulePrefetch` / `startPrefetchWindow` | 有效会话进入控制器，其余保留批次路径；加载创建、来源准入、渐进交付与登记清理均需保留 |
| [prefetch_session.go](../../../pkg/vfs/read/prefetch_session.go) | scope 内的控制器，消费/完成/容量通知驱动最近缺口；目前 horizon 是 cursor + chunks×depth；完成不能自行推进 cursor |
| [state.go](../../../pkg/vfs/read/state.go) 的 `observeReadAccess` / `readAccessCurrent` | requestID、run、连续/seek/尾页重叠与 stale 门禁；请求预测末端并非成功交付末端 |
| 同文件 `adaptivePrefetchLimit` | 当前主要看容量与前台压力；不是消费速率或预取利用率反馈 |
| [scope.go](../../../pkg/vfs/read/scope.go) | 会话/文件作用域、取消依赖、空闲退休与索引；保持锁序，取消不能等待持 planner 锁的工作形成反转 |
| [prefetch_wait.go](../../../pkg/vfs/read/prefetch_wait.go) | 只有已知长度、单流、目标块内部唯一进展才续等；ready 优先、调用方与原 total 仍有效 |
| [read_source.go](../../../pkg/drive/read_source.go)、[fetch_range.go](../../../pkg/drive/fetch_range.go) | TryFetch/RunChunks、整窗 Fetch、块发布、原期限下后缀重试、共享预算和实际 body 计数 |
| [scheduler.go](../../../pkg/readbudget/scheduler.go) | 全局/对象容量完整准入、优先级及 CapacityChanged；预取失败不持部分许可等待，订阅先于尝试以免丢唤醒 |
| [read_trace.go](../../../pkg/drive/read_trace.go)、[diagnostics/read.go](../../../pkg/vfs/diagnostics/read.go) | body_bytes、source/range 身份和延迟口径；调试投影只读，不驱动调度 |

导出时：块大小为 512 KiB，`PrefetchChunks=8`，`SequentialPrefetchChunks=16`（8 MiB），预取深度最高为 4，控制器空闲退休为 30 秒。单块 adaptive 连续计划可据此产生 32 MiB 前方上限；这些是当前代码事实，不是种子或所有请求的实际大小。

驱动建议快照：Quark bulk 为 4 MiB 窗口、三 worker、8 MiB part 上限、4 MiB 并行门槛、对象预算七，interactive 阶段为 adaptive；4 MiB 窗口小于 part 上限，不能因 streams=3 就声称执行了三请求。Koofr 将默认 bulk streams 设为一。SFTP 未声明自己的 ReadPolicy 方法，使用公共默认建议及能力解析。实际挂载必须核对 effective policy，不能直接用建议值推断实际执行形状。

## 5. 新规范已经确定的设计

详细决策在 [design](design.md)，实施不需要重新发明控制器：

- **适用路径**：已知长度、当前 SessionID/RequestID、非 concurrent、阶段为 adaptive 的挂载读。explicit interactive 可用，但窗口内仍单流。fixed/disabled、explicit bulk、probe、无会话、未知长度、ReadStream 自有 scope、直读保持原路径。
- **参数**：`Wcap` 为阶段单窗上限，`Dcap` 为允许的活动窗口上限，`Hcap=Wcap×Dcap`；会话预算 H 从 `min(Hcap,Wcap,1 MiB)` 起步，按块粒度处理，种子只准入一个窗口。
- **消费信用**：只计真实成功返回范围的唯一正向后缀。首次成功计真实字节；重复页、失败、stale、网络进展和填缓存不计。不能拿 `observeReadAccess` 预先登记的请求末端授信。
- **扩张**：消费信用达到 H，扣除 H，才能将 H 至多翻倍并受 Hcap 限制；允许一个大成功调用逐档扣除，已使用信用不可重用。到达上限后不囤多余信用。阶段上限增大不直接增大 H；降低时裁剪并清信用。
- **执行**：`W=min(Wcap,H)`，`D=min(Dcap,ceil(H/W))`；实际边界由 cursor+H 决定。先扩大单窗，再增活动窗数；就绪与在途范围都占边界，首窗预热也要纳入计数。已有兼容加载不为改尺寸而取消重开。
- **收缩/重置**：真实 seek、新读取趟、空闲退休、模式切换按设计重置；短暂停仅填现有范围。首版没有带宽 EWMA、播放码率预测或额外的历史丢弃率控制器。
- **状态与配置**：O(1) 新会话字段，复用原有限缓存/完成标记；不新增用户开关、驱动公共接口或第二套数据缓存。fixed 的尺寸和深度语义保留。

“消费”在这里指挂载层成功交付，包括内核可能主动发起的读取；它不等于播放器已观看/解码字节。不能宣称能完全识别播放器真实缓冲需求。

## 6. 历史短测如何精确重现

历史只读样本是现有 SFTP 的 `512mb.bin`，大小 **536,870,912 字节**；导出时仅确认本地报告仍存在，没有重新访问云端确认该文件或凭据可用。重新实测时从本地配置解析挂载别名和路径，不把凭据、主机、账号或完整配置复制进本文件。

单臂步骤：隔离只读挂载/配置/runtime/work；全局和挂载持久读缓存都关闭；清一次 VFS 状态；开一个 FD；Darwin F_NOCACHE；1 MiB pread；先从零连续读 8 MiB，再按下面顺序各读 2 MiB；第二点后停 3 秒；关闭 FD，等取消排空并检查活动请求。总有用读取为 16 MiB，历史单臂期限 600 秒。

| 顺序 | 起始偏移（字节） |
| --- | ---: |
| seek-01 | 25,165,961 |
| seek-02 | 503,316,617 |
| seek-03 | 50,331,785 |
| seek-04 | 478,150,793 |

历史公式为 `point×(file_size−8 MiB)//21 +137`，point 顺序 `[1,20,2,19]`。**虽然每点只读 2 MiB，偏移公式仍按 8 MiB 跨度计算，分母仍为 21。** 仓库 runner 使用传入 seek-count/seek-mib 重新生成点，因此直接改成 `--seek-count 4 --seek-mib 2` 不能复现上述四点；应复用/整理私有短测方法，或显式支持固定点后对照，不能静默改变负载。

每个读取记录 offset、length、SHA-256、耗时；四点首个 1 MiB 取 nearest-rank P95，实际上是四点最大值。报告总流量时计入所有完成/取消/重试窗口及 FD 关闭尾部；不要漏掉尾部来降低倍率。

该实验每臂只清一次 VFS，seek 间保留热块、在途合并和系统 readahead；不是逐点冷读，更不是完全绕过 VFS。全零样本的 hash 只能证明跨臂相同，不能独立证明偏移正确；用非均匀本地测试补足。

## 7. 接手需要的本机产物与外部条件

导出时确认本机私有目录 `/private/tmp/qrypt-slow-link` 仍有以下产物。它是临时目录，**不属于仓库，也不会随本文自动导出到另一台机器**；需要转移时仅转移经检查的源码快照/方法与必要报告，凭据仍通过本地配置提供。

| 本地产物 | 用途与限制 |
| --- | --- |
| `final-build-manifest.json`、`final-src/` | 历史最终生产源 hash/快照；二进制 SHA-256 为 `258ca126177d24da134a75329653b152e4dbf0ee81853d1c21d61c7f024f2fff` |
| `b4-final/result.json`、`summary.json`、`read-trace.log` | 上表最终实测依据；本次已核对 complete、字节、倍率、Range 配对和清理状态 |
| `a1/`、`a2/`、`a3/`、`b1/`、`b2/`、`b3/`、`comparison.json` | 上一项“目标进展接管”修复的交替实验，A 是旧接管、B 是新接管；**它们的 A/B 命名不是本 change 的 A/B** |
| `build-manifest.json`、`control-src/`、`candidate-src/`、overlay 文件 | 上一个实验的构建边界；最初 B 早于最终未知长度门禁，不得当作最终全部源码 |
| `run-mounted.py`、`run-series.py`、`summarize.py`、`compare.py` | 私有隔离挂载与短测方法；脚本依赖本机配置/旧路径，重新使用前要审阅并核验 CLI help，不盲目运行 |
| `regression-before.log`、`unknown-before.log`、race 日志 | 旧三秒接管、未知长度门禁的失败证据与修复后验证 |
| `ci.log`、`ci-exit-code` | 历史完整 CI 退出 0 且包含成功标记；之后已新增规划文件，不能作为本次后续提交的当前 CI 证明 |

历史测试二进制、临时凭据配置和测试挂载已清理；接手必须重新构建与启动自己隔离的实例，不复用用户运行中的实例做配置实验。原始日志可能包含签名 URL/请求细节，只导出白名单数值与安全枚举，不直接贴整行或配置。

正式性能验收还需要：

- macFUSE/FUSE 可用，能够创建和清理独立只读挂载，能采集完整 READ 日志。
- 已配置且可访问的 SFTP 大文件、Quark 加密影视样片和 Koofr 大文件；具体配置与云端文件当次核验，不导出凭据。样本至少容纳 256 MiB 连续段及固定 seek。
- 足够长的串行测试时段和稳定的同机环境；高 RTT 全负载可能超过 runner 默认 600 秒，先设置各臂一致、足够的期限。CI 不与云端计时并行运行。
- 若要声称“已实证并行受益后端”的收益，需要额外证明实际并行确有收益；当前 SFTP 一/四流对照没有这种证据。缺资源就报告缺测，不能用 fake driver 补成云端通过。

## 8. 测试入口与必须守住的边界

| 现有测试文件 | 优先保留的约束 |
| --- | --- |
| [prefetch_session_test.go](../../../pkg/vfs/read/prefetch_session_test.go) | 完成/消费补充、外部容量释放唤醒、seek/退休、失败/驱逐不循环重取、EOF、已有预热计数、取消不等待 planner mutex |
| [aligned_session_test.go](../../../pkg/vfs/read/aligned_session_test.go) | 尾页重复不破坏连续 run；真 seek 和无会话路径仍被识别 |
| [slow_prefetch_test.go](../../../pkg/vfs/read/slow_prefetch_test.go) | 两条 join 路径的慢目标复用；stall、部分进展后 stall、远目标、unknown、caller、total 边界 |
| [prefetch_wait_test.go](../../../pkg/vfs/read/prefetch_wait_test.go) | 停顿接管、快速预取合并、形状兼容、预算与显式深度一 |
| [stage_policy_test.go](../../../pkg/vfs/read/stage_policy_test.go) | 阶段、意图与 fixed/disabled 优先级，不能把所有测试期望都改成 adaptive |
| [stage_observability_test.go](../../../pkg/vfs/read/stage_observability_test.go) | 策略/计划诊断真实，观测不晋升 LRU、不虚构 ready、不泄漏 URL |
| [read_progress_test.go](../../../pkg/drive/read_progress_test.go) | 非零偏移前沿、重放不续等但仍计流量 |
| [fetch_range_timeout_test.go](../../../pkg/drive/fetch_range_timeout_test.go) | provider、共享 total 与调用方期限分类不混淆 |
| [read_lifetime_test.go](../../../pkg/drivers/sftp/read_lifetime_test.go) | SFTP body 使用 caller 生命周期，取消/Close 后兄弟读取仍可用 |

建议最先增加：按成功唯一字节授信的纯序列测试、同阶段等量字节不同请求粒度、失败/迟到不授信、seek/模式切换/空闲重置、阶段 cap 升降，以及首窗没有绕过小预算的回归。网络故障/等待边界使用非均匀内容和 synctest，避免真实长 sleep。

完整目标不止“单测通过”：共享许可不可循环等待、预取准入非阻塞、前台依赖不能被错误取消、最后块校验不能提前省略、部分字节不能当完整块、total 不因续等或重试重置。预算一和跨会话必须覆盖。

仓库已有 [同 FD runner](../../../scripts/dev/measure-playback-session.py) 负责打开/关闭自己的 FD、读取/计时/hash/报告；它不负责配置、挂载、清缓存或外层清理。默认 256 MiB 连续、20×8 MiB seek、3/35 秒暂停和 16 MiB 恢复，默认 600 秒限制需要按慢链路调整。正式命令先检查当前 `--help`。

## 9. 完成标准与验证顺序

1. 冻结新 **A=含所有前置修复的当前工作树**；实现新 B，只改变本 change。先跑确定性/race，再串行云端计时。
2. SFTP 原 16 MiB 短场景，三轮 A/B、B/A、A/B：每轮放大 ≤2×，至少两轮比 A 降低 ≥30%；seek 首 1 MiB P95 和连续段速率退化均 ≤10%。旧 3.149× 只作历史参考，必须同期重跑 A。
3. SFTP、Quark 加密视频、Koofr 正式 seek 每轮至少 20 固定点，冷/热与同 FD 分开：P95 退化 ≤10%，放大/请求数增加 ≤10%，错误/重试无新增；保留失败、慢点和中断。
4. 上述后端至少 256 MiB 长连续 + 3/35 秒暂停恢复：总速率及去除首尾各一个 8 MiB span 的稳态速率每轮退化 ≤10%，总放大和请求数遵守设计/任务门槛，无新周期性空洞。≤1.15× 长连续放大是参考目标，不是已取得结果。
5. 汇总构建身份、有效策略、缓存/FD、偏移/hash、完整率、原始样本路径、实际来源并发、吞吐与放大。P50/P95 使用 nearest rank；不跨冷/热混池，不用合并均值掩盖某轮失败。若波动越门槛则不确定，缺测就未验证。
6. 同步开发者文档与调优 skill；原始 JSON/日志留仓库外。使用当前模块 GOROOT gofmt 格式化所有改动 Go 文件，确认 `-l` 无输出，再执行相关测试、严格规格/skill/链接/差异检查。
7. 提交前使用工作流相同 Go 运行未修改默认并发的 `bash scripts/ci/check-all.sh`，要求退出 0 且输出 `== local CI check passed ==`。内容随后变化则重新运行；不能用历史日志代替，也不能降并发后声称默认 CI 通过。

本地检查入口：

```sh
go test ./pkg/drive ./pkg/readbudget ./pkg/vfs/read ./pkg/drivers/sftp
go test -race ./pkg/drive ./pkg/readbudget ./pkg/vfs/read ./pkg/drivers/sftp
openspec validate adapt-session-prefetch-to-consumption --strict
openspec validate unify-read-policy-execution --strict
git diff --check
```

这些命令是接手后的验证入口，本次导出文档没有重新执行 Go/race 或全量 CI，不代表新算法已经通过。

## 10. 与其他规范的关系及交付边界

主规格尚有旧版兼容/全文件取消等条款，不能只读 `openspec/specs/read-pipeline/spec.md` 就套回旧行为。先按前置 `unify-read-policy-execution` 的迁移和会话隔离 delta 理解当前实现，之后再应用本 change 的新增 adaptive 约束；主规格同步顺序同样先前置、后本 change。`chunks×depth` 对本 change 的 adaptive 是允许上限，实际当前范围由 H 决定；fixed 仍按原规则。

前置 change 的 6.2/6.3（旧版/实施基线/候选跨后端完整矩阵与参数决定）仍未完成。当前仓库已有“较快但某轮退化或波动过大”的 Quark/Koofr 历史结果，不能凭新短测或单测把这些任务勾完，也不能宣称已有稳定最大带宽。需要旧版对照时，按前置设计核验对应 release 和配置格式，不用新配置直接跑旧二进制。

完成本 change 的交付应包含实现、边界/并发测试、脱敏验证记录、开发者说明和调优方法；原始测量数据、用户文件、凭据和实验 runtime 均不进入提交。回退只撤本 change 的信用/预算控制及相关观测，保留前置正确性修复。账号或 FUSE 缺失只阻塞实测结论，已授权且不依赖外部条件的实现/验证仍可继续。
