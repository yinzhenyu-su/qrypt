# upload-pipeline Specification

## Purpose

为移动端和挂载文件系统提供一致、可恢复且可观测的上传行为，同时允许具备能力的后端绕过本地 staging 直接读取应用提供的源数据。

## Requirements

### Requirement: Direct source uploads avoid local staging when supported

当目标后端支持 source upload 和必要的写入/替换能力时，系统 MUST 直接把可读取的源数据交给后端上传，不得为了 direct upload 创建 qrypt 本地 staging 文件。

#### Scenario: Direct-capable mount uploads an app source

- **WHEN** 移动端提交 direct upload，且目标 mount 支持 source upload
- **THEN** 系统从应用 source provider 读取数据并直接完成远端上传，qrypt 本地不产生该上传的 staging 文件

#### Scenario: Encrypted dedup mount requires source hashes

- **WHEN** direct upload 目标需要内容去重 hash
- **THEN** 系统可以在远端上传前重新打开并读取 source 计算所需 hash，但不得将 source 内容写入本地 staging 作为计算 hash 的前置条件

### Requirement: Unsupported direct uploads fall back to staging

当目标后端不支持 direct source upload 时，系统 MUST 自动切换到 staging 上传，并保持与显式 staging 上传相同的远端提交、覆盖和失败处理语义。

#### Scenario: Driver lacks direct source capability

- **WHEN** direct upload 目标不支持 source upload
- **THEN** 系统将 source 顺序写入 staging，提交 staging 后由后台上传 worker 完成远端上传

#### Scenario: Fallback upload fails while copying source

- **WHEN** source 读取或写入 staging 过程中失败
- **THEN** 系统取消或清理未提交的 staging 状态，并向 task 返回可识别的失败结果

### Requirement: Staged uploads accept resumable random-offset writes

系统 MUST 支持移动端将一个文件分成多个带顺序偏移的写入操作，并在 commit 后异步上传；已有 staging 内容不得因进程中断而丢失。

#### Scenario: Mobile client writes and commits chunks

- **WHEN** 移动端创建 staging upload、写入多个数据块并提交
- **THEN** 系统按写入偏移重建完整 staging 内容，冻结该版本并将其加入后台上传队列

#### Scenario: Process resumes an interrupted staged upload

- **WHEN** 进程在 staging 写入或后台远端上传期间退出并随后恢复
- **THEN** 系统能够从持久化 task 和 staging 状态恢复可继续的上传，不要求移动端重新提供已经成功写入的数据

### Requirement: Upload completion preserves shared result semantics

direct、显式 staging 和 direct fallback MUST 对冲突策略、上传进度、取消、重试、远端 Entry、view 可见性和缓存失效提供一致语义。

#### Scenario: Overwrite replaces an existing remote file

- **WHEN** 上传使用 overwrite 或 replace 策略并且目标已存在
- **THEN** 系统上传新对象后完成旧对象替换，最终 view 只暴露新对象，并在失败时执行可控回滚

#### Scenario: Retry does not duplicate active upload runners

- **WHEN** 上传进入 retry wait 并收到用户立即重试请求
- **THEN** 系统唤醒当前 runner 重新尝试，不得为同一 task 启动第二个并发 runner

### Requirement: Public upload entry points remain compatible

系统 MUST 保持现有移动端 JSON 上传入口、driver capability contract 和 FUSE 文件写入语义兼容；本次整理不得要求调用方区分内部 direct transport 与 staging transport。

#### Scenario: Existing mobile direct API remains usable

- **WHEN** 调用方继续使用现有 direct upload JSON API
- **THEN** 请求格式、task 类型、结果状态和错误边界保持兼容

#### Scenario: Existing FUSE write path remains usable

- **WHEN** Finder 或其他 FUSE 客户端执行 Create、Write、Flush
- **THEN** 文件仍按原有 staging、延迟队列和后台上传语义处理
