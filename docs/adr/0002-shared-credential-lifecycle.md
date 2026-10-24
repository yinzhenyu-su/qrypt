# 凭据是独立于挂载的共享对象，挂载只引用

同一驱动的同一账号被挂载到多个目录时，凭据必须在配置里只存在一份、在运行期只有一个生命周期。决定把凭据拆成两层：**配置层**新增 `[[credentials]]`（name/type/params）与挂载的 `credential` 引用，由 `pkg/core` 在 `drive.New` 之前合并成驱动仍然认识的扁平 `drive.Params`；**运行期**新增进程级 `CredentialManager`，按凭据名维护一个共享会话对象（`pkg/drive.CredentialSession`），承载 cookie 轮换、access token 刷新、单飞刷新、版本化提交和跨挂载订阅，会话以不透明 payload 持久化到 `state/credentials/<name>/session.json`。凭据身份是配置里显式声明的名字而不是 provider 账号：账号 ID 只有认证后才知道，无法用于离线校验，自动按账号合并会让新增挂载隐式改变其它挂载的凭据生命周期；账号 ID 只作为元数据用于展示与重复告警。挂载本地状态（`root_path`/`rootID`、上传会话、读缓存、离线布局）仍按挂载名隔离，共享的是凭据而不是 driver 实例。会话只在凭据参数指纹不变时复用：参数被替换即整体作废并重新认证，这是当前 quark 按挂载持久化轮换 cookie 导致串号的架构级修复。

## Considered Options

- **每个挂载各自保存并轮换凭据**（现状）：N 个目录就要抄 N 份 secret；每个挂载独立轮换，单次使用的 refresh token 会互相踢下线；旧会话还能掩盖被替换的凭据，导致挂载显示错误账号。拒绝。
- **按凭据参数内容哈希自动去重**：改动小，但配置文件仍然重复；重新导出 cookie 会改变哈希；没有稳定身份承载生命周期元数据（账号、版本、引用关系）。只可作为隐式凭据的内部实现细节，不能作为主模型。拒绝。
- **以 provider 账号 ID 作为凭据身份**：需要联网认证后才能确定，破坏配置校验的离线性；且同一账号的不同导出 cookie 会被迫等价，掩盖真实配置差异。拒绝。
- **多个挂载共享一个 driver 实例以共享会话**：会连带共享 `root_path` 解析结果、上传会话、离线任务与缓存，破坏挂载隔离。拒绝。
- **把会话共享塞进现有 `drive.StateStore`**：同一 store 里混有挂载本地文件与凭据文件，靠文件名路由既脆弱又无法版本化提交。拒绝。

## Consequences

- 配置与 schema 需要新增 `[[credentials]]`、挂载 `credential` 字段，并为每个驱动参数声明作用域（credential / mount / both）；`schema/qrypt.schema.json`、生成器、生成文档与控制面摘要都要同步。
- `pkg/drive` 增加 `CredentialSession` 契约与 `CredentialSessionInstaller` 可选接口；轮换类驱动改为读取快照、提交更新、订阅他人更新，纯静态驱动零改动。
- `CredentialManager` 必须是**进程级**对象（按绝对 state 目录分域），因为 `Core.Reload` 先建新 core 再关旧 core，mobile 也可能同时持有多个 core；每核一份会退化成不共享。
- 跨进程（同一 state 目录被多个 qrypt 进程打开）只能靠 `flock` + 版本比较保证不丢更新；单进程内以共享对象为准。
- 旧配置（挂载内联凭据）继续可用：core 合成隐式凭据并保持原有 `state/driver/<mount>/` 路径；共享迁移通过显式命令完成，不自动删除用户状态。
- 调试信息需要暴露凭据名、账号、来源、版本、更新时间与被哪些挂载引用，但绝不暴露 secret 值。
