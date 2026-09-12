## 1. 日志字段与作用域（pkg/logging）

- [x] 1.1 `Event` 增加 `Mount` 与 `Component`；`Component` 在写入时从既有的 `[TAG]` 前缀派生（无前缀为空），既有消息文本不变；验证 `go test ./pkg/logging/...` 通过，且新增用例覆盖"有前缀/无前缀"两种行
- [x] 1.2 新增 `Scope`（`Logger.WithMount`）与四个级别的普通及采样方法；`Scope` 持有全局 logger 指针，`ReplaceDefault` 之后写入新配置；验证用例断言替换后 scope 的行落在新 writer 与新级别
- [x] 1.3 采样键按挂载隔离（`mount + "\x00" + key`）；验证用例断言两个挂载的同一采样键互不抑制、各自的 `suppressed` 计数独立
- [x] 1.4 未接入作用域的调用点行为不变（无 mount 字段、采样键不含前缀）；验证 `go test ./pkg/logging/...` 与既有用例全绿

## 2. 查询面按字段过滤（pkg/control）

- [x] 2.1 `/v1/events` 新增 `mount=` 参数并按 `Event.Mount` 字段过滤；验证用例断言只返回该挂载的行
- [x] 2.2 `component=` 改为按 `Event.Component` 字段比较（语义不变、不再解析消息）；验证用例覆盖"有前缀匹配"与"无前缀不匹配"
- [x] 2.3 反例用例：挂载 A 的行其消息文本包含挂载 B 名称时，按 B 过滤不返回；验证该用例通过
- [x] 2.4 `path` 的既有文本过滤保持原状（不在本 change 范围）；验证既有 `/v1/events` 用例通过

## 3. 落地两个 store（pkg/vfs）

- [x] 3.1 `newStores` 增加挂载名参数并把 scope 传给 `readcache.NewStore` 与 `newUploadStore`；`VFS.New` 传入 `opts.Name`；验证 `go test ./pkg/vfs/...` 通过
- [x] 3.2 `readcache.Store` 的 16 处调用点改为 scope 调用（含 `NewStore` 内的局部 scope）；验证 `git grep -n "logging\.L\." pkg/vfs/readcache` 为空且该包用例通过
- [x] 3.3 `upload.PendingStore` 的 12 处调用点改为 scope 调用；验证 `git grep -n "logging\.L\." pkg/vfs/upload/store.go` 为空且该包用例通过
- [x] 3.4 端到端断言：读取触发缓存告警后，`/v1/events?mount=NAME` 能取到该行、按另一个挂载名取不到；验证该用例通过

## 4. 文档与验证

- [x] 4.1 更新 `docs/for-developer/debug.md`：`/v1/events` 的 `mount`/`component` 字段过滤用法、挂载作用域的覆盖范围（哪些包已接入、哪些还没有）；验证文档与实现一致
- [x] 4.2 格式与静态检查：`gofmt -l .` 无输出，`go vet ./...`、staticcheck、golangci-lint 通过
- [x] 4.3 全量与竞态：`go test -count=1 ./...` 与 `go test -race ./pkg/logging/... ./pkg/vfs/...` 全绿
- [x] 4.4 本地 CI 门禁：`scripts/ci-check.sh` 退出码 0
