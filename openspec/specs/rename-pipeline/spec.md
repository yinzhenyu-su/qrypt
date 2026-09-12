# rename-pipeline Specification

## Purpose
定义重命名/移动从后端适配器到本地视图的端到端一致性契约：操作必须交出结果条目的身份，视图必须以后端身份为准，且可见性 shadow 必须在后端收敛后清除。

## Requirements

### Requirement: Rename and move report the resulting entry

后端适配器执行重命名或移动成功时 MUST 返回该对象在后端的当前条目（标识、父标识、名称与属性），不得只报告成功。当后端的标识由路径或键派生时，返回的标识 MUST 反映操作后的新位置；当后端使用稳定的不透明标识时，返回条目 MUST 保留该标识并携带新的父标识与名称。

#### Scenario: Renaming on a path-derived backend

- **WHEN** 在标识由路径或键派生的后端上重命名一个对象且远端操作成功
- **THEN** 返回条目的标识对应新位置的标识，而不是操作前的位置

#### Scenario: Moving on an opaque-identifier backend

- **WHEN** 在标识由后端分配且跨操作稳定的后端上移动一个对象且远端操作成功
- **THEN** 返回条目保留原标识，并携带目标父标识与当前名称

#### Scenario: Backend does not support the operation

- **WHEN** 目标后端不支持重命名或移动
- **THEN** 操作返回不支持错误，且不返回可用于后续访问的条目

### Requirement: The view commits the backend-reported identity

系统 MUST 使用后端返回的条目更新本地视图，不得仅改写名称与父标识后继续沿用操作前的条目。重命名/移动成功后，按新路径解析得到的标识 MUST 与后端返回的标识一致，使后续读取、写入前准备、目录列表与删除都作用在新位置。

#### Scenario: Reading the renamed path right after the rename

- **WHEN** 重命名成功后在本地视图解析并读取新路径
- **THEN** 读取按后端返回的标识执行，成功返回新位置的内容，且不访问旧位置

#### Scenario: Removing the renamed path

- **WHEN** 重命名成功后在本地视图删除新路径
- **THEN** 删除按后端返回的标识执行，作用于新位置的对象

#### Scenario: Backend keeps the identifier unchanged

- **WHEN** 后端在重命名或移动后返回与操作前相同的标识
- **THEN** 视图提交行为与既有语义一致，条目的名称与父标识为操作后的值

### Requirement: Renamed directories expose backend-consistent descendant identities

目录被重命名或移动后，系统 MUST 保证其子孙在新的本地路径下解析出的身份与后端一致：要么丢弃操作前缓存的后代身份并由后端重新提供，要么由后端推导出新的身份。系统 MUST NOT 把操作前的后代身份直接当作新位置的身份继续使用。

#### Scenario: Reading a descendant after its directory was renamed

- **WHEN** 目录重命名成功后，通过新路径读取该目录下的一个文件
- **THEN** 读取按该文件在新位置的后端标识执行，成功返回内容

#### Scenario: Listing a descendant directory after the rename

- **WHEN** 目录重命名成功后，通过新路径列出其子目录
- **THEN** 返回的是该子目录在新位置的远端内容

### Requirement: Rename shadows converge

系统 MUST NOT 因重命名/移动而永久隐藏旧路径。系统 MUST 在旧名字已从后端列表中消失且目标名字已出现时清除旧路径的隐藏 shadow；当有不同对象被提交到旧路径时 MUST 立即清除该 shadow；隐藏状态 MUST 具有兜底过期，使之后在旧路径重新创建的对象最终能够被列出。

#### Scenario: A new object is created at the old path after the rename

- **WHEN** 重命名成功后，在旧路径创建一个同名对象
- **THEN** 该对象在目录列表与解析中可见

#### Scenario: The backend reports a different identity than expected

- **WHEN** 目标路径已出现，但后端报告的标识与操作前记录的标识不同
- **THEN** shadow 仍被清除，旧路径不再被隐藏

#### Scenario: The backend has not converged yet

- **WHEN** 远端尚未在目标位置反映该重命名
- **THEN** 在兜底期限内容器继续隐藏旧路径，不暴露尚未迁移完成的陈旧目录项

### Requirement: Rename over an existing target does not serve stale content

当重命名或移动覆盖一个已存在的目标时，系统 MUST 使对该目标的后续解析与列目录反映覆盖后的内容，不得返回被覆盖对象的旧内容。

#### Scenario: Listing the target right after an overwriting rename

- **WHEN** 重命名覆盖了一个已存在的目录且远端操作成功
- **THEN** 紧接着列出该目标时返回覆盖后的内容，而不是被覆盖目录的旧子项

### Requirement: Local-directory markers follow a rename

系统 MUST 在目录被重命名或移动后保留其"本地新建"状态语义，使该目录及其子项在远端收敛前仍按本地状态解析。

#### Scenario: Renaming a locally created directory

- **WHEN** 一个本地新建的目录被重命名或移动
- **THEN** 该目录在新路径下仍被视为最近本地创建，其子项解析不受远端尚未收敛影响

### Requirement: Rename results are verifiable against the backend listing

重命名/移动的结果 MUST 可被后端列表验证：操作成功后，目标父目录的列表 MUST 包含标识等于返回条目标识的对象；对最终一致的后端，该验证允许有限重试。

#### Scenario: Contract verification after a successful rename

- **WHEN** 在支持重命名/移动的后端上执行操作并随后列出目标父目录
- **THEN** 列表中存在的对象标识与操作返回条目的标识一致

### Requirement: A rename moves the backend object behind a pending local edit

当被重命名的源路径同时具有后端对象与更新的本地 pending 修改时，系统 MUST 把后端对象一并迁移到目标位置，不得只搬动本地 pending 记录。系统 MUST 先把后端对象重命名/移动到目标，再把 pending 记录重指到目标路径，使更新的本地内容随后上传并替换被移动的对象。当源路径在后端没有对象（仅本地 pending）时，系统 MUST 保持纯本地重命名，不访问或改动任何后端对象。后端对象迁移失败时，系统 MUST NOT 移动 pending 记录。

#### Scenario: A downloader renames a temp file whose generation already uploaded

- **WHEN** 下载器先写临时名（例如 `name.qkdownloading`），其中一代内容已上传到后端，随后继续写入并把临时名重命名为正式名
- **THEN** 后端在旧临时名下不再存在对象，正式名最终承载最新一代内容

#### Scenario: Renaming an edited remote file

- **WHEN** 一个已存在于后端的文件被本地修改（存在 pending 记录）后重命名
- **THEN** 后端在旧文件名下不再存在对象，新文件名承载本地修改后的内容

#### Scenario: The source exists only as a pending upload

- **WHEN** 源路径只存在于本地 pending、后端没有任何对象
- **THEN** 重命名只更新本地状态，不改动后端对象，也不因后端不可用而失败

#### Scenario: The containing directory was renamed first

- **WHEN** 临时文件所在的目录先被重命名，随后该临时文件再被重命名为正式名
- **THEN** 后端在重命名后的目录下只保留正式名对象

#### Scenario: The backend move fails

- **WHEN** 源路径既有后端对象又有 pending 修改，而后端重命名/移动失败
- **THEN** 重命名返回错误，且本地 pending 记录仍停留在原路径

### Requirement: Wrapper results are named in the caller's vocabulary

位于后端之上的包装层（如文件名加密层）在返回条目时 MUST 使用调用方词汇中的名称（明文名），MUST NOT 把后端名（如密文名）当作条目名称返回。后端名 MUST 通过条目的附加信息单独可取得，使需要它的调用方（诊断、后端名解析）仍能拿到。该要求与写入类操作已遵循的约定一致：包装层在上传与建目录成功时映射回明文名，列表时逐个解密。

#### Scenario: Rename returns the plaintext name

- **WHEN** 调用方在加密挂载上重命名一个文件且远端成功
- **THEN** 返回条目的名称是调用方给出的明文新名，而不是后端的密文名；后端密文名可通过附加信息取得

#### Scenario: Move returns the plaintext name

- **WHEN** 调用方在加密挂载上把一个文件移动到另一个目录且远端成功
- **THEN** 返回条目保留明文名称与新的父标识，而不是后端的密文名

#### Scenario: The backend still receives the backend name

- **WHEN** 包装层执行重命名或移动
- **THEN** 传给后端的名称仍是后端词汇中的名称，包装层不得因映射回传值而改变发往后端的参数

### Requirement: View entries are named by their path

按路径写入视图的每个提交点 MUST 保证条目名称等于该路径的基名；当上游给出的名称与路径不一致时，系统 MUST 以路径为准归一化，并显式记录这次不一致（路径、给定名称与标识），使上游的契约破坏可见而不是以"用户无法寻址的假条目"形式呈现。该不变量适用于所有按路径提交的入口，包括上传完成与远端重命名完成。

#### Scenario: A mismatched name is corrected and recorded

- **WHEN** 某个提交点收到名称与其路径基名不一致的条目
- **THEN** 视图按路径基名存储该条目，且记录一条包含路径、原名称与标识的告警

#### Scenario: Consistent entries are stored unchanged

- **WHEN** 提交点收到的条目名称与路径基名一致
- **THEN** 视图原样存储该条目，不产生告警，也不改变其它字段

#### Scenario: Listings never surface an unaddressable name

- **WHEN** 一次重命名或覆盖写完成后读取该目录的列表
- **THEN** 结果中不出现以后端名（密文名）命名的条目，用户看到的名称与其可寻址路径一致
