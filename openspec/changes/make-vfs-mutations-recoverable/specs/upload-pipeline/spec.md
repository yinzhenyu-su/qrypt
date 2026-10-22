# Spec Delta

## MODIFIED Requirements

### Requirement: Staged uploads accept resumable random-offset writes

系统 MUST 支持按偏移写入暂存并在完整提交后异步上传。普通写入成功表示本地已接受且当前可读，不逐次承诺断电持久性；持久检查点或 Flush/commit 成功 MUST 保证其确认内容及恢复信息可靠落盘。检查点 MUST NOT 使未完整提交的内容可上传；恢复 MUST 保留未提交状态，不采用过期冻结声明或哈希。

#### Scenario: Mobile client writes and commits chunks
- **WHEN** 移动端创建 staging upload、写入多个数据块并提交
- **THEN** 系统按写入偏移重建完整 staging 内容，在持久化成功后冻结该上传代并将其加入后台上传队列

#### Scenario: Process resumes an interrupted staged upload
- **WHEN** 进程在 staging 写入或后台远端上传期间退出并随后恢复
- **THEN** 系统从可靠检查点和冻结状态恢复可继续的上传，不要求移动端重新提供已持久确认的数据，未确认尾部只按可验证状态处理

#### Scenario: Checkpoint does not publish incomplete input
- **WHEN** 文件尚未完整提交而部分内容已建立持久检查点
- **THEN** 重启后该内容可继续写入，但不会仅因检查点存在而开始远端上传

#### Scenario: Durable offset requires the full commit order
- **WHEN** 文件已 `fsync` 但账本提交仍失败，或平台不支持父目录 `fsync`
- **THEN** `durable_offset` 不推进；系统暴露存储健康错误或按平台文档说明降级，而不把文件长度当成可恢复位置

#### Scenario: A frozen generation stays immutable while new writes continue
- **WHEN** 已冻结的代内容尚未被读取，同一对象又接受新的写入
- **THEN** 系统从代文件复制出新的可变工作内容并在其基础上写入，已冻结代内容与哈希保持不变；恢复不会把新字节当成先前冻结代，也不会使用旧哈希进行秒传，未提交内容保持本地等待恢复或提交

#### Scenario: Sparse or out-of-order input has a larger file length
- **WHEN** 随机偏移写入使暂存文件长度大于可靠连续输入范围
- **THEN** 系统不把文件长度当作移动端可恢复偏移，不冻结存在未确认输入缺口的流式完整文件

#### Scenario: A live session continues from accepted input
- **WHEN** 活跃输入项已接受写入但最近检查点落后，调用方继续观察同一会话
- **THEN** `resume_offset` 仍可用于本会话继续写入，快照同时给出不高于检查点的 `durable_offset`；进程重启后 `resume_offset` 不得高于持久位置

#### Scenario: Superseded or uncertain upload does not seed published cache
- **WHEN** 一次上传在发布前被取代，或远端结果仍待对账
- **THEN** 系统不把其暂存内容作为已发布内容写入有效视图或读缓存；后续提交再按新的发布结果播种/失效

### Requirement: Upload completion preserves shared result semantics

direct、显式 staging 和 direct fallback MUST 对冲突策略、进度、取消、重试、远端 Entry、view 可见性和缓存失效提供一致语义。远端替换允许短暂缺少正式名称，但破坏旧对象前 MUST 确保新内容可恢复。请求结果不明确时 MUST 对账，不能盲目回滚或删除唯一副本；新上传代存在时旧结果不能清除它。

#### Scenario: Overwrite replaces an existing remote file
- **WHEN** 上传使用 overwrite 或 replace 策略并且目标已存在
- **THEN** 系统先使新内容具备可恢复副本，再完成旧对象替换；最终 view 只暴露新对象，部分失败保留恢复所需的对象和状态

#### Scenario: Retry does not duplicate active upload runners
- **WHEN** 上传进入 retry wait 并收到用户立即重试请求
- **THEN** 系统唤醒当前 runner 重新尝试，不得为同一 task 启动第二个并发 runner，也不得把过期上传代恢复为当前代

#### Scenario: Final rename fails after old content was removed
- **WHEN** 新候选对象已上传、旧对象已删除，但最终改名失败或超时
- **THEN** 系统保留新候选及恢复信息，对账实际结果后继续或报告问题，不再删除候选作为普通失败回滚

#### Scenario: Rename succeeded but its response was lost
- **WHEN** 远端实际改名成功而调用方仅收到超时
- **THEN** 系统能对账并接受既有发布结果，不盲目重复删除或生成第二份逻辑发布

#### Scenario: Candidate name exceeds a backend limit
- **WHEN** 用户文件名或加密后名称接近/超过后端允许的名称长度
- **THEN** 系统使用有界、唯一且可重新解析的候选名，不因拼接完整名称和操作标识而上传失败或清理错误对象

#### Scenario: A newer generation arrives during the switch
- **WHEN** 旧上传代已进入远端切换阶段且同一对象接受新上传代
- **THEN** 系统先记录或对账旧操作的真实结果，保留新代为当前期望，并继续安排其发布，不因旧代过期而删除可能已被后续状态引用的远端对象

#### Scenario: Preparation temporarily fails
- **WHEN** 获取上传快照或远端准备发生可重试错误
- **THEN** 系统持久记录可观察的失败及重试条件，后续自动尝试不会依赖再次 Flush；永久错误或结果不明确分别进入相应失败或对账流程

#### Scenario: Direct source is unavailable after restart
- **WHEN** 直传尚无可用候选且恢复时应用源不能重新打开
- **THEN** 系统保留操作关联并通过现有错误/可用动作表达需要重新提供源，不伪造可自行恢复的本地暂存

#### Scenario: Candidate can finish without the original direct source
- **WHEN** 直传候选已可靠记录，重启后应用源不可用而剩余工作仅为对账或切换
- **THEN** 系统保留并使用候选继续安全恢复，不因源不可用就丢弃已上传内容

### Requirement: Public upload entry points remain compatible

系统 MUST 保持现有移动端 JSON 上传入口、driver capability contract、任务类型和 FUSE 文件操作语义兼容，不要求调用方区分内部 direct 与 staging transport；需要持久恢复的偏移 MUST 遵循明确的检查点语义。

#### Scenario: Existing mobile direct API remains usable
- **WHEN** 调用方继续使用现有 direct upload JSON API
- **THEN** 请求格式、task 类型、主要结果字段和错误契约保持兼容，不要求为直传新增本地 staging

#### Scenario: Existing FUSE write path remains usable
- **WHEN** Finder 或其他 FUSE 客户端执行 Create、Write、Flush
- **THEN** 文件仍按暂存、Flush 后冻结、既有静默窗口及并发准入进行后台上传，普通 Write 不被当成已持久提交

## ADDED Requirements

### Requirement: File type follows the requested operation

系统 MUST 依据调用方请求的文件类型/操作创建对象，不得仅因名称缺少扩展名把普通文件当成目录。平台兼容适配 MUST 有明确的类型或平台证据，不能扩大为对所有无点名称的猜测。

#### Scenario: An extensionless regular file is created
- **WHEN** 客户端以普通文件类型创建 README 等无扩展名文件
- **THEN** 系统创建可写普通文件并返回有效句柄，不因文件名缺少点号而创建目录

#### Scenario: Finder creates an extensionless directory
- **WHEN** macOS Finder 通过目录创建操作创建无扩展名目录
- **THEN** 系统仍创建目录；该适配只作用于有证据的目录创建路径，不影响普通无扩展名文件

### Requirement: Resolution failures are not treated as absence

写入或截断前解析已有远端内容时，系统 MUST 只把明确的 NotFound 当作“不存在”。其他解析/列表错误 MUST 原样返回，不得初始化为空暂存或把网络错误变成成功的覆盖。

#### Scenario: Resolving existing content fails
- **WHEN** 写入或截断前解析已有远端内容遇到非 NotFound 错误
- **THEN** 系统返回该失败而不以空暂存替代原内容，不把网络错误变成成功的覆盖操作

### Requirement: Direct and staged uploads coordinate the same destination

直传与暂存上传 MUST 经过相同的对象和目标协调，不允许旧待传记录在直传成功后恢复成当前内容。冲突策略允许覆盖时，新接受的逻辑覆盖选择当前上传代；实际远端切换串行协调。不同来源仍 MUST 保持各自已声明的字节存放和恢复能力。

#### Scenario: Direct overwrite follows an existing pending upload
- **WHEN** 同一路径已有旧待传内容，随后直传以允许覆盖的策略成功完成
- **THEN** VFS 读取反映直传内容，旧待传任务不能稍后再次覆盖它

#### Scenario: Mounted writes follow an accepted direct upload
- **WHEN** 直传已被接受后，同对象又接受更新的本地内容
- **THEN** 更新内容保持当前期望，旧直传的完成不能清除该内容，最终按有效提交顺序收敛

#### Scenario: Conflict policy does not permit replacement
- **WHEN** 请求使用 fail 或 skip 策略且目标已有有效内容或冲突预约
- **THEN** 系统继续遵守该策略，不因统一发布流程而隐式覆盖目标
