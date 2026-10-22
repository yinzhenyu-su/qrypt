# Spec Delta

## Purpose

为挂载内尚未收敛的文件修改提供可靠的本地状态，使写入、改名、删除和远端执行在并发及进程恢复后仍归属正确对象。保障持久确认内容不会因旧操作、维护或孤儿清理丢失，并为迁移、故障诊断与安全恢复提供一致依据。

## ADDED Requirements

### Requirement: Local mutations preserve object and content ownership

系统 MUST 区分路径绑定、逻辑对象、上传代和执行尝试。改名 MUST 保留内容归属；删除后同路径重建 MUST 建立不同对象身份。旧操作 MUST NOT 覆盖或清理新的对象/上传代；过期操作造成的实际远端结果 MUST 仍被记录供对账。

#### Scenario: A deleted path is recreated while remote removal is running
- **WHEN** 旧对象的远端删除已开始，用户在相同路径成功重建并写入新对象
- **THEN** 旧删除结束只完成旧对象的操作，新对象的内容、检查点、待传状态和可见性不被清理
- **AND** 新对象向冲突远端位置发布前等待旧删除结果明确

#### Scenario: A retry races a newer generation
- **WHEN** 用户重试旧上传代时，同一对象已接受新的上传代
- **THEN** 重试不能恢复旧代为当前代，也不能移除新代的内容引用

#### Scenario: A frozen generation is never rewritten in place
- **WHEN** 内容已冻结并绑定到上传代，同一对象又接受写入或 Truncate
- **THEN** 系统在新的工作内容上写入并从其建立新代，不修改已冻结代，哈希与已发布引用保持有效

#### Scenario: An obsolete upload has already changed the remote
- **WHEN** 一个被取代的上传尝试实际完成远端切换
- **THEN** 系统记录该远端事实，但不将旧代重新选择为当前期望，也不清除更新的上传代

### Requirement: Local mutation commits recover atomically

一次相关的本地状态转换 MUST 整体提交或整体不成立。成功的持久确认 MUST 在进程重新打开存储后仍成立；通知丢失、状态维护或压缩 MUST NOT 遗漏已确认转换。工作队列和可重建缓存 MUST NOT 成为内容恢复的唯一依据。

#### Scenario: Process stops at a multi-object commit boundary
- **WHEN** 源、目标及待传关系的一次本地转换在提交过程中遭遇进程退出
- **THEN** 恢复结果只包含完整的提交前或提交后状态，不出现部分路径更新而内容归属未更新的组合

#### Scenario: State maintenance follows a successful save
- **WHEN** 保存成功的转换恰好触发内部存储维护并随后重启
- **THEN** 该转换和所引用的内容仍可恢复，不被当成孤儿删除

#### Scenario: Scheduling notification is lost
- **WHEN** 可执行工作已持久提交但唤醒调度的通知丢失
- **THEN** 系统能从持久状态重新发现工作，不要求用户再次 Flush 或重建内容

### Requirement: Pending content and remote existence are independent

系统 MUST 独立表达本地待传内容与远端对象的存在。删除带有本地修改的已有远端文件 MUST 同时撤销其当前本地期望并安排正确远端删除，不能仅丢弃暂存后让旧远端内容重新可见。仅本地对象的删除 MUST NOT 因后端不可用而要求远端操作。

#### Scenario: Remove an edited remote file
- **WHEN** 已上传文件被本地修改并存在待传内容，随后用户删除该文件
- **THEN** 删除成功后旧远端内容不会重新出现在有效视图，远端删除按其对象身份执行或进入可观察的失败恢复

#### Scenario: Remove a local-only file
- **WHEN** 文件仅存在于本地暂存且用户删除它
- **THEN** 系统撤销该对象的本地修改，不访问不存在的远端对象，并仅回收不再被操作引用的内容

### Requirement: Directory removal respects visible and pending descendants

目录删除 MUST 默认拒绝仍含可见或本地待传子项的目录，并返回可识别的非空错误；`可见` 定义为远端列表可见子项、本地待传子项与本地创建目录三者之和。rmdir MUST 在子树预约下先确认目标为空（含平台噪声文件），只有调用方显式删除后代后才可撤销目录。目录删除 MUST 与后代在途发布/删除协调，不得在父目录已删除后继续向旧父标识发布，也不得静默丢弃子项内容；外部并发导致的非空 MUST 返回可重试错误。

#### Scenario: Rmdir a directory with a pending child
- **WHEN** 目录中存在本地待传子文件，调用方执行目录删除
- **THEN** 删除返回非空错误，子文件内容与 pending 状态保持不变

#### Scenario: Descendants are explicitly removed first
- **WHEN** 调用方先显式删除目录中的本地待传后代，再删除空目录
- **THEN** 系统按对象身份撤销后代本地期望并安排必要的远端清理，随后目录删除成功，不留下孤立内容引用

#### Scenario: A child publication is still switching when the directory is removed
- **WHEN** 目录删除与后代远端发布交错，且发布可能重建旧父目录下的对象
- **THEN** 系统等待发布收敛或返回可重试冲突，不静默允许父目录消失后继续发布

### Requirement: Garbage collection preserves recoverable content

本地或远端内容只要仍被当前上传代、持久检查点、活动读取或未完成操作引用，系统 MUST NOT 将其回收。无法证明所有权或恢复完整性时 MUST 保留内容并报告问题，而不是按路径、文件名前缀或单次列表缺失推断其可删除。

#### Scenario: Replacement candidate is the only recoverable copy
- **WHEN** 旧对象已删除而候选对象仍被未完成发布引用
- **THEN** 启动清理和失败处理均保留候选对象，直到发布或显式恢复处理解除引用

#### Scenario: Referenced staging is missing or corrupt
- **WHEN** 恢复发现记录指向的暂存内容丢失或与持久声明不一致
- **THEN** 受影响工作进入可识别的恢复错误，不上传伪造空内容，也不清理其他可能相关的内容副本

#### Scenario: Collection races a new content owner
- **WHEN** 内容检查与实际删除之间出现新的合法引用
- **THEN** 系统的回收协议阻止删除该引用所需的字节，或拒绝建立指向已回收内容的引用

### Requirement: A state directory has one mutation owner

系统 MUST 为同一状态目录提供排他的写入所有权，覆盖挂载、迁移、重试和离线维护命令。另一个进程不能通过绕过挂载的维护入口并发改写状态；所有权不可获得时 MUST 返回可识别的占用错误，不修改内容。

#### Scenario: Offline maintenance targets a running mount
- **WHEN** 挂载持有状态目录所有权，另一个进程请求 prune、replay 或迁移
- **THEN** 维护命令被拒绝或通过现有持有者执行受控动作，不独立重写存储

#### Scenario: Owning process terminates
- **WHEN** 持有所有权的进程异常退出
- **THEN** 后续进程可以取得所有权并恢复持久操作，而不会把残留的锁标记当成永久占用

### Requirement: Legacy state migration has one authoritative result

升级 MUST 在排他访问下识别并单次迁移旧待传日志与暂存，保留路径、内容、冻结声明、失败信息与已记录的替换对象。迁移 MUST 把旧文件放到旧二进制不会解析或清扫的 `legacy/` 位置，新内容使用独立目录；不得复用旧暂存目录。迁移中断 MUST 可恢复且不得同时产生两个写入真相；启用新格式后 MUST NOT 自动回退旧格式。无法可靠解释的记录及暂存 MUST 被保留，并通过现有维护输出提供显式恢复动作，而不是只留本地无入口。

#### Scenario: Legacy transactional records are imported
- **WHEN** 旧日志包含 dirty、clean、rename、clean_batch 及替换记录
- **THEN** 导入结果反映完整重放后的状态，已改名路径不会回到旧名，已删除对象不会被恢复为待传

#### Scenario: Migration stops before activation
- **WHEN** 新状态尚未启用时迁移中断
- **THEN** 原日志和内容保持完整，重新启动可安全重新导入或继续迁移，不执行孤儿清理

#### Scenario: A new-format write has been accepted
- **WHEN** 新存储已启用且接受新的修改后发生重新启动或维护
- **THEN** 系统只使用新状态恢复，不因新存储损坏或不可打开就从旧日志静默恢复一个过期版本

#### Scenario: Legacy files are parked outside the old runtime path
- **WHEN** 旧 journal 和暂存已迁移，随后用户误跑未升级的旧版本
- **THEN** 旧版本看不到或清扫 `localstate/` 中的新内容，也不会从旧 journal 复活过期上传

#### Scenario: Legacy frozen hashes cannot be trusted
- **WHEN** 旧格式记录无法证明冻结字节与缓存哈希仍然匹配
- **THEN** 迁移不直接采用该哈希进行秒传；系统保留字节并重新验证，无法确定提交状态时要求显式恢复

#### Scenario: An unresolvable legacy record is reported with an action
- **WHEN** 迁移或维护发现无法自动恢复的旧记录
- **THEN** `fs journal` 的兼容输出列出路径、原因和稳定代码，并提供可先 dry-run 的显式 `recover` 动作；`prune` 不得删除该记录

#### Scenario: Migration needs more space than available
- **WHEN** 复制旧暂存前预检发现可用空间不足
- **THEN** 迁移在修改原文件前失败并报告所需空间，旧 journal 与暂存保持完整

### Requirement: Uncertain external changes do not justify destructive guesses

系统 MUST 协调单实例内部冲突，并尽力对账外部修改。当身份冲突、最终一致列表或请求结果不明确导致无法确定安全步骤时，MUST 暂停受影响的破坏性操作并保留可恢复内容，提供可识别的冲突/对账原因；不承诺跨客户端锁定或远端恰好执行一次。

#### Scenario: Another client changes the intended destination
- **WHEN** 发布或删除前对账发现目标身份与记录不一致
- **THEN** 系统不把新出现的外部对象当成旧操作的目标继续删除，保留本地内容并报告冲突

#### Scenario: One remote listing does not show the candidate
- **WHEN** 超时后的首次查询未列出候选对象，但没有可靠证据证明其不存在
- **THEN** 系统继续受控对账或等待恢复，不据此删除其他副本或盲目重放破坏性步骤
