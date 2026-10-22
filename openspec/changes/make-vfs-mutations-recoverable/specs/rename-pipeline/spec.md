# Spec Delta

## MODIFIED Requirements

### Requirement: Renamed directories expose backend-consistent descendant identities

目录被重命名或移动后，系统 MUST 保证后代在新路径下的身份、直接父关系和上传目标与后端一致。系统 MUST 丢弃失效的后代定位并重新解析，或使用后端提供的新身份；MUST NOT 沿用已失效定位，也不得把所有后代的直接父标识改为被移动目录的标识。相关本地写入和恢复状态 MUST 跟随对象而不是旧路径。

#### Scenario: Reading a descendant after its directory was renamed
- **WHEN** 目录重命名成功后，通过新路径读取该目录下的一个文件
- **THEN** 读取按该文件在新位置的后端标识执行，成功返回内容

#### Scenario: Listing a descendant directory after the rename
- **WHEN** 目录重命名成功后，通过新路径列出其子目录
- **THEN** 返回的是该子目录在新位置的远端内容

#### Scenario: A nested pending file uploads after directory rename
- **WHEN** `/dir/sub/file` 存在待传内容，随后 `/dir` 成功改名为 `/moved`
- **THEN** 内容最终发布为 `/moved/sub/file`，不发布为 `/moved/file`，且不会覆盖后者的无关内容

#### Scenario: Child staging initialization overlaps a directory move
- **WHEN** 子文件操作已开始读取旧内容建立暂存，而祖先目录同时被请求移动
- **THEN** 系统协调依赖旧远端定位的操作，已接受写入最终属于正确对象并在新路径下可见和提交，不留下旧路径的孤立待传记录

#### Scenario: A path-derived descendant locator becomes invalid
- **WHEN** 后端的对象标识由路径派生且祖先已移动
- **THEN** 后续上传前重新获得正确直接父定位，不向消失的旧目录写入

### Requirement: A rename moves the backend object behind a pending local edit

源路径同时具有后端对象与更新的本地 pending 时，系统 MUST 先迁移后端对象，再将本地修改关联至其实际位置。仅本地对象 MUST 保持纯本地改名。远端完全失败时本地位置 MUST 不变；远端部分成功且不能回滚时，系统 MUST 一致地保存实际中间位置及其相关待传后代，返回可识别的部分失败并保留恢复所需状态。

#### Scenario: A downloader renames a temp file whose generation already uploaded
- **WHEN** 下载器先写临时名（例如 `name.qkdownloading`），其中一代已上传，随后继续写入并改为正式名
- **THEN** 后端旧临时名不再保留该对象，正式名最终承载最新一代内容

#### Scenario: Renaming an edited remote file
- **WHEN** 已有后端文件被本地修改后重命名
- **THEN** 后端对象与本地修改一起迁移，新文件名最终承载本地修改后的内容

#### Scenario: The source exists only as a pending upload
- **WHEN** 源对象仅存在于本地且其本地状态可证明没有后端对象
- **THEN** 改名只更新本地状态，不因后端不可用而失败，也不以网络查询失败推断远端不存在

#### Scenario: The containing directory was renamed first
- **WHEN** 临时文件所在目录先被重命名，随后临时文件改为正式名
- **THEN** 后端在重命名后的目录下只保留正式名对象，待传内容引用正确的新位置

#### Scenario: The backend move fails
- **WHEN** 源同时有后端对象和 pending 修改，而远端迁移未生效或已成功回滚
- **THEN** 改名返回错误，本地 pending 仍停留在原位置

#### Scenario: Directory rename partially succeeds and rollback fails
- **WHEN** 目录改名成功、移动失败且回滚也失败
- **THEN** 系统将目录及相关待传后代一致地关联到实际中间位置，不仅修改视图，并保留继续对账或恢复的操作

### Requirement: Wrapper results are named in the caller's vocabulary

包装层返回条目时 MUST 使用调用方词汇中的名称，后端名称 MUST 通过附加信息单独可得。传入原始后端的对象名称及操作参数 MUST 使用后端词汇；改名或移动不能把公开明文名称写入要求密文名称的后端。包装后结果 MUST 保持正确对象身份、父关系及可读取属性。

#### Scenario: Rename returns the plaintext name
- **WHEN** 调用方在加密挂载上重命名文件且远端成功
- **THEN** 返回名称是明文新名，后端密文名仍可通过附加信息取得

#### Scenario: Move returns the plaintext name
- **WHEN** 调用方在加密挂载上把文件移动到另一个目录且远端成功
- **THEN** 返回条目保留明文名称、新的父标识和正确的内容属性

#### Scenario: The backend still receives the backend name
- **WHEN** 包装层执行重命名或移动
- **THEN** 发往原始后端的名称仍是后端词汇中的名称，回传值映射不改变请求参数

#### Scenario: Backend move echoes the input name in a request
- **WHEN** 加密文件执行同名跨目录移动，原始后端将输入条目的名称放入移动请求
- **THEN** 请求携带密文名，后端列表仍能被包装层正确解析，文件不会因明文名而从加密视图消失

## ADDED Requirements

### Requirement: Rename conflicts cover source destination and descendants

系统 MUST 协调改名的源、目标及受影响子树。与目标写入或后代暂存并发时，成功结果 MUST 等价于受保护的合法操作次序，不丢失已接受内容。依赖旧远端定位的步骤 MUST 在定位变化前结束或安全重试；无关子树不应因全挂载网络锁而停止工作。

#### Scenario: Pending rename overlaps destination staging
- **WHEN** 目标正在建立暂存时，另一个待传对象被改名覆盖到目标
- **THEN** 操作被协调为合法次序，源内容不会被基于旧目标读取的迟到暂存无条件替换

#### Scenario: Local writes already own a child generation
- **WHEN** 子文件已有本地可写上传代且祖先目录开始移动
- **THEN** 已接受写入继续归属该对象，目录迁移不会改变其内容身份或把它绑定给同名新对象

#### Scenario: An unrelated directory is active
- **WHEN** 一处目录移动正在等待远端，而无关子树执行本地写入
- **THEN** 无关写入不被一把跨网络调用的全挂载锁阻塞
