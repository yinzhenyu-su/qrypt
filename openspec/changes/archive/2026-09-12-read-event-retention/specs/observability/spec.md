## Purpose

定义 qrypt 运行期观测面必须留下的信号及其有界性，使"盘为什么变慢"这类问题能在进程运行期间被回答，而不依赖事后复现。

## ADDED Requirements

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
