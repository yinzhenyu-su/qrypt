## 1. 驱动契约签名（纯重构，行为不变）

- [x] 1.1 `pkg/drive`：`Driver.Rename`/`Move` 改为返回 `(Entry, error)`，`UnsupportedOperations` 同步；在接口文档写明"标识不透明的后端必须跨重命名/移动保持标识稳定"；验证 `go build ./...` 通过
- [x] 1.2 更新 13 个 in-tree 驱动与 `pkg/drive/fake.go` 的 `Rename`/`Move`：返回传入条目（语义等价，不改变行为）；验证 `go test ./pkg/drivers/... ./pkg/drive/...` 全绿
- [x] 1.3 更新包装与后端适配：`pkg/crypt/drive_wrapper.go`、`pkg/drivers/scopedfs`、`pkg/mobile/scopedfs.go` 返回并透传结果条目；验证 `go test ./pkg/crypt/... ./pkg/drivers/scopedfs/... ./pkg/mobile/...` 全绿
- [x] 1.4 更新 VFS 适配层：`pkg/vfs/mutation`（`Remote`/`RemoteRenamer`/backend）、`pkg/vfs/upload`（`RemoteOps`/`replace.go`/`target_index.go`）、`pkg/vfs/source_upload.go` 按新签名传递条目；验证 `go test ./pkg/vfs/... -count=1` 全绿且行为无变化
- [x] 1.5 更新测试假驱动与 fixture 至新签名；验证 `go test ./... -count=1` 全绿（此提交不改变任何可观察行为）

## 2. 驱动返回真实身份

- [x] 2.1 路径/键标识驱动（`localfs`、`s3`、`webdav`、`sftp`、`baidunetdisk`）的 `Rename`/`Move` 返回操作后条目，复用各自内部已算出的新标识，零额外远端调用；验证各驱动包测试与新增单测断言"返回标识等于新位置的标识"通过
- [x] 2.2 不透明标识驱动（`quark`、`aliyundrive`、`onedrive`、`p115`、`p115open`、`p189`、`yun139`）返回保留标识并携带新父标识与名称的条目；验证各驱动包测试全绿且返回值与既有语义一致
- [x] 2.3 修正 `pkg/drive/fake.go` 的 `Rename`：先改名再 rekey，使 `Rename`/`Move` 返回真实新标识（含子孙）；验证 `go test ./pkg/drive/...` 全绿并新增用例断言返回标识等于新路径标识
- [x] 2.4 `pkg/contracttest` 新增断言：重命名/移动成功后，目标父目录列表必须包含标识等于返回条目的对象，对最终一致后端带有限重试；验证契约矩阵测试通过

## 3. 视图提交与一致性

- [x] 3.1 重命名协调器把驱动返回的条目提交给视图（不再仅改写名称/父标识后沿用旧条目），部分成功路径仍提交中间态；验证 VFS 级单测：重命名后按新路径解析出的标识等于返回标识
- [x] 3.2 目录重命名时丢弃携带陈旧身份的子孙缓存条目（停止按新路径重键沿用）；验证：目录重命名后经新路径读取文件与列子目录都作用在新位置
- [x] 3.3 重命名/移动覆盖已存在目标时失效目标目录的列表缓存；验证：覆盖后立即列目录不返回被覆盖目录的旧子项
- [x] 3.4 `localDirs`（本地新建目录）标记随重命名/移动迁移；验证：本地新建目录重命名后其子项仍按本地状态解析
- [x] 3.5 shadow 收敛判据改为名字优先、标识仅作提示，并加入兜底过期；验证：标识变化与最终一致场景下 shadow 收敛、旧路径重建对象可见、远端未收敛时旧路径仍隐藏
- [x] 3.6 复用新返回值简化或移除 `pkg/vfs/source_upload.go` 的"列父目录 + 名字匹配"补救逻辑；验证上传替换相关测试全绿

## 4. 测试保真与回归

- [x] 4.1 新增 VFS 级端到端回归：`localfs` 与不稳定标识 fake 上执行 `rename → Stat/Read/Readdir/Remove`，覆盖删除重命名后对象（含幂等删除语义的 fake 变体）；验证：修复前失败、修复后通过
- [x] 4.2 复核 `fake.Rename` 修复后暴露的既有测试红灯，逐条判定归属并修正（断言了错误行为的修测试，暴露实现缺陷的修实现，不弱化断言）；验证 `go test ./... -count=1` 全绿且无新增跳过
- [x] 4.3 覆盖"最终一致后端尚未反映重命名"的场景：目标位置暂时缺失时不得提交错误身份、不得提前暴露旧路径；验证：基于 fake 的滞后列表（`ListStaleness`）用例通过

## 5. 验证与收尾

- [x] 5.1 格式与静态检查：`gofmt -l .` 无输出、`go vet ./...`、staticcheck、golangci-lint 全部通过
- [x] 5.2 全量与竞态：`go test -count=1 ./...` 与关键包 `go test -race`（`pkg/vfs`、`pkg/vfs/upload`、`pkg/drive`、`pkg/core`）全绿
- [x] 5.3 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
- [x] 5.4 规格一致性：`openspec validate fix-rename-entry-identity --strict` 通过
