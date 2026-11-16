## Context

现状依据 d0f5332：ReadProfile 使用平铺字段；PlanResolver 使 interactive 固定单流、默认两块；未声明意图保留 FUSE 窗口启发式，连续后可使用 bulk 流数。Reader.perFileLimit 从 BulkStreams 推导预算；后台预取单流。readWindowRangeAttempt 完整取回并校验窗口后才能交付。Quark 在限速包装之外统计速率，并可将低速取消判为坏连接。

v0.6.1 已有 512 KiB 前台窗口、未对齐 seek 两块合并、连续后 8 MiB 预取；Quark 按 8 MiB 分片，仅超过 8 MiB 的请求实际产生多路。旧版性能快的具体原因尚未实测确认，不能归因于小 seek 的内部并行。

## Goals / Non-Goals

**Goals:** 让所有驱动使用统一的阶段解析与能力回退，分别优化 seek 可交付延迟和顺序吞吐；保持数据、会话隔离与资源上限正确。

**Non-Goals:** 不承诺每个云盘都提速；不添加自学习吞吐控制器、竞速重复请求、亚块缓存、边下载边发布未校验窗口、全新用户调参表或驱动内部并行。此次不改变 interactive 单窗口单流契约。

## Decisions

### 1. 意图与访问阶段正交

保留 bulk/interactive/probe 意图，阶段为 initial、seek、sequential。复用现有会话访问识别，不增加第二套探测状态机；无会话信息的请求采用保守 initial，不根据文件后缀猜测视频。不改写显式意图或缓存准入。FUSE 无意图连续读可沿用既有 bulk 升级；interactive 连续读仅扩大预取，不 fan-out。

### 2. 增量扩展 profile，保持 Go 调用兼容

保留 BulkStreams、BulkWindowChunks、InteractiveWindowChunks、ProbeWindowChunks、PrefetchDepth、ParallelThreshold 和超时字段。新增可选阶段建议（Seek、Sequential），建议结构仅包含 ForegroundChunks、PrefetchChunks、PrefetchDepth；不为每阶段增加 Streams，避免绕过现有意图门禁。新增可选 MaxInFlightRanges 表达单文件硬预算，单位为底层在途 Range。新字段使用可选值表达：nil 继承，深度显式 0 禁用；负值拒绝。块数有效范围 1..64，预取深度 0..4，并发预算 1..32。

未声明新字段时以现有行为作为兼容基线，而非强制所有驱动改成新窗口。seek 一块是实验候选；跨块合并仍保留，单次窗口单流不代表整个高层 Read 永远只有一次请求。旧 Go profile 字段不删除；mount 的 streams/window_chunks 继续只覆盖 bulk，相应预算解析必须能容纳已获准的覆盖且受驱动硬限制约束。

替代方案：重写为四个嵌套 policy 会扩大迁移与公开 Go API 风险；仅加入 SeekWindowChunks 不能解决预取窗口和预算耦合。

### 3. 一次解析，统一执行

ReadPlan 携带意图、阶段、前台窗口、预取窗口、预取深度、推荐流数、预算和优先级。策略独立于实际 grant。复用 Scheduler 和 ScopeRegistry，不与 simplify-read-pipeline 重复建设。预算始终按文件总计；单流建议仍可允许前台和有界后台流水，但不能突破 MaxParallelReads 或全局限制。前台优先于尚未启动的预取，释放任一可用槽位须唤醒符合条件的等待者。

seek 只取消该会话过时预取；其他会话前台与有用缓存保留。无法识别会话时不得猜测并取消其他会话。缓存命中优先；不等待与小目标加载形态不兼容的大预取窗口。禁止靠无界重复下载获取低延迟。

### 4. Quark 连接恢复保持语义正确

主动取消通过 context cause/明确生命周期信息识别，不通过已读速率猜测。正常 seek、关闭、调用方取消不惩罚 IP 或标脏连接池；真实网络失败继续恢复。删除固定吞吐下限触发的失败与重放，保留 idle/total 超时和截断校验。idle 只反映等待远端进展，不把本地限速等待计为网络停顿。保留可复用连接、URL 缓存和既有暖连接流程，不在本次引入新的额外请求。

### 5. 验证决定调优，不以最大瞬时速度判定

记录 queue、driver open、body、校验/缓存到可交付的耗时；区分 CDN TTFB、VFS 首批可交付与播放器首帧。复用现有诊断字段，增加阶段、前后台窗口、推荐/实际流数、预算及取消/轮换原因；字段仅用于观察。

使用 v0.6.1、实施基线 HEAD 与候选构建，同文件、同播放器、同网络和限速，独立缓存/运行目录，旧新交替至少三轮。每轮固定至少 20 个 seek 点，分别统计冷/热缓存的 P50/P95，以及连续读取有效吞吐、下载放大、错误与重放数。不能清空用户当前运行实例的缓存，也不能停止该实例。

真实云覆盖 Quark、一个高 RTT 单流云盘、一个并行获益驱动；localfs 与不支持 Range/内部并行使用确定性测试覆盖。无账号的驱动明确标记未实测。候选相对同窗口内旧版与实施基线：seek P95 不恶化超过 10%，连续吞吐不下降超过 10%；Quark 至少恢复到旧版此范围。声称提速需至少两轮改善 20% 且无正确性回退；超出阈值或抖动导致结论不稳定则暂停默认参数发布，保留原默认。性能测量不作为依赖公网的 CI 硬门禁。

## Risks / Trade-offs

- 小窗口增加 RTT → 保留跨块合并并独立扩大连续预取，以实际首帧验收。
- 提高预算造成下载放大 → 硬上限、会话取消、统计实际字节与预取浪费。
- CDN 波动掩盖结果 → 同时间窗交替，报告原始样本；不把既有单次测量当结论。
- 两个在途变更修改同一读域 → 保持 simplify-read-pipeline 的对象边界；对现有未提交修复先复审再集成。

## Migration Plan

先增加基线与兼容测试，再扩展 profile/plan、调度与调用路径，最后修复 Quark 故障分类并实测。所有驱动可零修改继承兼容行为；只有实测通过才提交特定驱动建议。公开配置不变，生成文档须稳定。回退时撤销驱动建议及策略选择，不恢复取消误判或移除截断保护；缓存无需迁移。
