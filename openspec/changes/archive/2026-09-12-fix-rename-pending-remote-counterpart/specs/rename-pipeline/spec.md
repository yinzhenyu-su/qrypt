## ADDED Requirements

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
