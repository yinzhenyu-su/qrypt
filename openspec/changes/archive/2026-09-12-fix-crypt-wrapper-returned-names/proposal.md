## Why

加密挂载上出现了一个**以密文命名的可见文件**，读它返回 EIO。今天在用户机器上实地定位到的例子（`~/Qrypt/quark-encrypt`）：

```
~/Qrypt/quark-encrypt/13-pqvLKZ9V4FZYclyJEjiyOIjdMmu6CQSRKTqCWJx3f3axjxA2K-UaS5llXudaS
  → 用该挂载自己的密钥解密 = ".www.98T.la@EBWH-290.restored-S.mp4.js"（证明是 qrypt 自己的密文名）
  → /v1/resolve 给出 remote_id=fb3a0f42…，而该 id 在远端已 404
  → /v1/consistency: status="missing", "no remote entry and no pending upload"
  → 读它：quark: get file download url … 404 Not Found (file not found [fb3a0f42…]) → EIO
```

根因在 `pkg/crypt/drive_wrapper.go:299`（`Rename`）与 `:295`（`Move`）：包装层把 `newName` **加密后**交给驱动（正确），却把驱动返回的 entry **原样传回**——而驱动只认识密文名，于是 `entry.Name` 是密文。同文件的其它方法都做了反向映射：`PutSource`（`:344-349`）`entry.Name = req.Name` 且填 `Extra.RemoteName`，`Mkdir`（`:285-292`）同样，`List`（`:139-149`）逐个解密、解不开就跳过。只有这两个"改名类"方法漏了，所以这是漏写而非设计。

后果链条（每环都有代码或日志支撑）：`pkg/vfs/upload/engine.go:188` 拿到这个 entry → `:195 entry = renamed` → `pkg/vfs/upload/commit.go:35` → `pkg/vfs/view/commit.go:63 entries.Set(path, entry)`。视图的**键**是明文路径，条目的 **`Name` 字段却是密文**，列表按 `Name` 渲染，于是挂载里出现一个用户无法寻址、且不符合 `._`/`.DS_Store` 过滤规则的名字。替换上传随后把该远端对象换掉/删掉，视图条目仍指向旧 id，读它即 EIO。

触发条件很常见：任何"覆盖写 + 替换上传"或"重命名"都会经过这里。用户机器上同目录里还有第二例 `7acmnW-keI_E9YjaUBQtMg`（解密 = `.DS_Store`），说明 Finder 每次重写 `.DS_Store` 都在复现它。

现在修的理由：它直接让用户看到并可能误删一个假文件，且每次重命名/覆盖写都会再生；同时它违反了 `rename-pipeline` 既有要求（"返回该对象在当前后端的条目"）——只是那条要求没写清用哪套词汇，才让这个漏写活了下来。

## What Changes

- **包装层返回条目必须用调用方的词汇**：`Rename`/`Move` 在成功返回前把名称映射回明文并填充 `Extra.RemoteName`（与 `PutSource`/`Mkdir`/`List` 一致），使"返回的 entry 是明文命名"成为包装层的统一契约；后端名仍可通过 `drive.EntryRemoteName` 取到。驱动按 `entry.ID` 操作（`pkg/drivers/quark/quark_list.go:133` 用 `fid`），所以改 `Name` 不影响任何驱动调用。
- **视图条目的名称必须与其路径键一致**：两个按路径写视图的提交点（`view.CommitUploadedEntry`、`view.CommitRemoteRename`）在写入前校验 `entry.Name == path.Base(path)`；不一致时按路径键归一化并记一条 warn（点名路径、两者名字与 id），使同类上游契约破坏**立刻可见**而不是以假文件的形式呈现给用户。
- **回归测试**：包装层用假驱动断言 `Rename`/`Move` 返回明文名与 `Extra.RemoteName`（此测试在修复前失败，已实测）；视图层断言不匹配的名字被归一化为路径基名。
- **不做**：不改驱动、不改 `rename-pipeline` 的既有行为（重命名语义、rename shadow、pending 迁移都不动）；不做"视图与远端对账自动删除条目"——远端列表**并非穷尽**（`List` 会跳过解密失败与非法明文名，`ForeignEntries` 单独报告），据此删除条目会误删合法项，属独立的、需要更可靠信号的设计。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `rename-pipeline`: 新增两条要求——（1）包装层返回给调用方的条目 MUST 使用调用方词汇的名称（明文），后端名称 MUST 通过条目的附加信息单独可取得；（2）按路径写入视图的条目 MUST 以该路径的基名为名称，名称与路径不一致时以路径为准并显式记录。

## Impact

- `pkg/crypt/drive_wrapper.go`：`Rename`、`Move` 的返回路径（新增反向映射；复用 `Extra` 既有的 `drive.EntryExtraWrapper` 约定）。
- `pkg/vfs/view/commit.go`：`CommitUploadedEntry`、`CommitRemoteRename` 的写入前校验（新增一个包内小助手 + 一条 warn 日志）。
- 测试：`pkg/crypt`（`Rename`/`Move` 契约）、`pkg/vfs`（视图名称归一化）。
- 已存在的残留条目：修复后重新提交的点都正确，但用户机器上那两条已写入的条目仍在内存视图里，需要重启挂载清除（远端数据未受影响）。
- 无配置项、无 wire 字段、无 CLI 改动。
