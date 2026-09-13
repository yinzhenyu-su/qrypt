## Why

`TestReplaceDefaultClosesPreviousLoggerFileSink`（`9468382` 的四个回归用例之一）在 Windows CI 上失败，两轮都是同一个错误：

```
--- FAIL: TestReplaceDefaultClosesPreviousLoggerFileSink
    log_test.go:258: cannot move an open file on this platform:
    rename ...qrypt.log -> ...qrypt.log.moved: The process cannot access the file
    because it is being used by another process.
    testing.go:1617: TempDir RemoveAll cleanup: unlinkat ...qrypt.log:
    The process cannot access the file because it is being used by another process.
```

用例的探针在 `ReplaceDefault` **之前**把日志文件 rename 走：POSIX 允许移动仍被打开的文件，随后写入旧 sink 时"已释放的 sink 会重建原路径、未释放的会继续写被移动的 inode"就能区分两种情况。Windows 不允许移动仍被其他进程持有的文件，于是第一次 rename 就失败并走 `t.Skipf`；但此时 lumberjack 仍持有该文件，`t.TempDir` 的清理删不掉它，Go 会把 cleanup 失败叠加在 skip 上，最终报成 FAIL。也就是说这条断言在 Windows 上既没有真正验证契约，还把测试拖成红色。

生产实现没有平台相关问题：`ReplaceDefault` 确实对旧 logger 自己打开的 lumberjack sink 调了 `Close()`，Windows 上关闭后句柄即释放。

## What Changes

- 把 rename 从 `ReplaceDefault` 之前移到之后，并去掉 `t.Skipf`：
  - POSIX：rename 照旧成功（平台允许），后续"写入旧 sink 后原路径应被重建、被移动文件不得包含新行"的断言仍然是原先那条判据；
  - Windows：rename 在句柄未释放时报错、释放后成功，等价判据提前一步发生，且以 `the replaced logger's file sink stayed open` 明确失败，而不是静默跳过。
- 新增一个 cleanup 关闭捕获到的旧 sink（`oldSink.Close()`，lumberjack 的 `Close` 可重复调用）：任何在 `ReplaceDefault` 之前失败的路径都不会再留下打开的日志文件，Windows 上不会再出现"断言失败 + TempDir 清理失败"的双重噪声。

非目标：

- 不改任何生产代码；`ReplaceDefault` / `Close` 只按"释放自己从路径打开的 sink、不动借用 writer"的既有契约工作。
- 不为 Windows 单独删掉这条覆盖：改成平台自适应后，两个平台都仍然能识破"替换后旧 sink 未释放"。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。仅调整测试的平台适用性，可观察契约不变，`.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/logging/log_test.go`：`TestReplaceDefaultClosesPreviousLoggerFileSink` 一个用例（探针顺序、cleanup、注释）。
- 覆盖不回退（负向验证）：把 `ReplaceDefault` 里 `oldLJ.Close()` 的条件改成恒 false 后，该用例以 `the replaced logger's file sink stayed open: stat ...qrypt.log: no such file or directory` 失败（POSIX 路径；Windows 会在前面的 rename 处给出同一句断言）。
- 平台差异说明：Windows 矩阵由 `.github/workflows/ci.yaml` 的 `Test (windows)` 覆盖，本地 macOS 的 `scripts/ci-check.sh` 跑不到，这次失败正是它抓出来的。
