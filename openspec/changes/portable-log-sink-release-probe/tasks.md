## 1. 探针跨平台（pkg/logging）

- [x] 1.1 把 `os.Rename(logPath, movedPath)` 从 `ReplaceDefault` 之前移到之后，删除 `t.Skipf("cannot move an open file on this platform")`
- [x] 1.2 新增 cleanup `oldSink.Close()`（可重复调用），保证任何失败路径都不会让 `t.TempDir` 在 Windows 上因文件被占用而清理失败
- [x] 1.3 更新用例注释：说明为何在替换之后移动文件，以及 Windows 上"移动失败"就是"句柄未释放"的同一句断言

## 2. 验证

- [x] 2.1 `go test ./pkg/logging/ -run TestReplaceDefault -count=1` 三个用例全绿（macOS）
- [x] 2.2 负向验证：把 `ReplaceDefault` 中 `oldLJ.Close()` 的条件改成恒 false，用例以 `the replaced logger's file sink stayed open` 失败，恢复后通过
- [x] 2.3 `scripts/ci-check.sh` 全绿（pre-push 钩子会再跑一次）
- [ ] 2.4 GitHub Actions 的 `Test (windows)` 由红转绿（本地 macOS 跑不到 Windows 矩阵，以 PR #1 的检查结果为准）
