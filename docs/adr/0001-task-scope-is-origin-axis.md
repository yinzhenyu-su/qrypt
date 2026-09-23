# Task Scope 只表示任务来源，可见性独立成轴

任务的 `Scope` 混过两个轴：来源（谁产生）与可见性（app 是否可见），`descriptor.go` 曾用 `UserVisible` 推导 `Scope`，`pkg/vfs` 的记账记录则借用 `ScopeSync` 表示"app 不可见"。决定：`Scope` 只表示来源，取值 user（用户/应用发起）、sync（syncer 作业产生）、internal（挂载写入路径的内部记账记录）；可见性是独立概念，任何来源的任务都可能可见或不可见。wire 值 `"sync"` 保留为兼容契约，但语义收敛为"syncer 作业产生"。

## Considered Options

- **Scope 表示可见性，sync = app 不可见**：与 `pkg/syncer` 的存在冲突，且丢掉来源信息。拒绝。
- **两个轴各留一个字段，Scope 现状不动**：保留了已知的语义混用，误用会继续扩散。拒绝。

## Consequences

- `ScopeForType` 以 `UserVisible` 推导 `Scope` 的做法需收敛：来源由创建路径声明，可见性另行声明。
- `pkg/vfs/task.go` 和 `pkg/vfs/delete_task.go` 的记账记录从 `ScopeSync` 迁移到 internal 归属。
- `pkg/vfs` 记账记录迁移后，`ScopeSync` 的实际使用方只剩 syncer，wire 值不变。
