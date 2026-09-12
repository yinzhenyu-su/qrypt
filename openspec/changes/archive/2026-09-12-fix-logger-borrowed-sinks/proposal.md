## Why

`qrypt mount` 的终端输出消失了：命令跑完，用户在终端看不到 `Mounting at ...`、`Mounted at ...`，也看不到任何 `Error: ...`。日志文件里一切正常，所以问题不在"没记日志"，而在**记完之后没有通向终端**。

根因是我在 `0d3a994`（observability 那次改动）里把 CLI 的安装入口从 `logging.L = newLogger` 改成 `logging.ReplaceDefault(newLogger)`（`internal/cli/runtime_config.go:47`）。`ReplaceDefault` 里有一段清理逻辑，把**被替换日志器的旧 writer 当 `io.Closer` 关闭**——而全局默认日志器的 writer 正是 `os.Stderr`（`pkg/logging/log.go:246-247`），`*os.File` 实现 `io.Closer`，于是 fd 2 被关掉；此后进程内每一笔 `os.Stderr` 写入都返回 `file already closed`。改动前那段清理逻辑只有测试在跑，所以缺陷一直潜伏，这次才第一次在生产路径上执行。

实测证据（两段都是可复现的命令，不是推断）：

```
# 探针程序：默认 logger（writer = os.Stderr）→ ReplaceDefault(文件 logger)
stderr fd before: 2
stderr fd after:  18446744073709551615     # ^uintptr(0)
stderr write failed: write /dev/stderr: file already closed

# 真实二进制 A/B（同一配置：localfs 挂载 + 显式 [logging] log_level，挂载点用只读路径让 Mount 失败）
HEAD   : stderr 为空；日志文件里有 "Mounting at ..." 与 "ERROR Mount failed: mkdir ..."
修复后 : stderr 打印 "Mounting at /System/qrypt-probe-denied ..." 与 "Error: mkdir ... operation not permitted"
```

影响面比 mount 更大：`initLogger` 位于所有命令的 `PersistentPreRunE`（`internal/cli/config_path.go:92`），所以那之后 cobra 打印的每一个 `Error: ...`、mount 的三条 banner、以及任何走 stderr 的组件输出都进了已关闭的 fd。用户只能翻日志文件才能看到错误，而终端一片安静——这正是被报告的现象。

诱因还有一层：既有 requirement「The logger is installed through a single path」写的是"安装 MUST 释放被替换的日志器的输出目标"，没有区分**自己打开的 sink** 与**别人交来的 writer**。按字面实现就会关掉 `os.Stderr`。本次把这条要求本身改掉。

## What Changes

- **所有权规则**：日志器只关闭自己从路径打开的 sink（`lj` / `errLj` 两个 lumberjack sink）；任何被交进来的 writer（`os.Stderr`、测试缓冲）MUST NOT 被关闭。`ReplaceDefault` 与 `Logger.Close()` 两处都按此规则（两处此前是同一种写法，`Close()` 的同一缺陷可被测试触发）。
- **保留真正的释放**：替换日志器时，旧日志器自己打开的文件 sink 仍然关闭——`0d3a994` 想修的"CLI 重装日志器泄漏文件句柄"不能被这次修复退回去（有测试专门守这一侧）。
- **回归测试**（四条，均验证过"去掉修复即失败"）：借来的 writer 在 `ReplaceDefault` 后仍打开；旧日志器自己的文件 sink 仍被释放（用"改走文件后再写"的可移植探针，不依赖 fd 列表）；`Close()` 不关闭借来的 writer；子进程探针确认 `ReplaceDefault` 之后进程的标准错误仍可写（该断言必须在子进程里跑，否则测试二进制自身的诊断会被一并关掉）。
- **修改 observability 的既有要求**：把"释放输出目标"改成"释放自己打开的 sink，且不得关闭借来的 writer"，并补上"终端输出在安装日志器后仍然可用"的场景。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `observability`: 修改既有要求「The logger is installed through a single path」——安装只释放日志器自己打开的 sink，MUST NOT 关闭借来的 writer（标准错误），并新增"安装后进程仍能向标准错误输出"的场景；保留"全局日志器保持同一对象"与"释放旧文件 sink"的要求。

## Impact

- `pkg/logging/log.go`：`ReplaceDefault`（删掉对任意 `io.Closer` 的关闭）与 `Close()`（删掉同一回退分支），并为两处补上所有权说明。
- `pkg/logging/log_test.go`：原「替换会关闭旧输出目标」的用例改为所有权契约（借来的不关、自己的仍关），新增标准错误子进程探针与 `Close()` 用例。
- 用户可见效果：`qrypt mount` 恢复打印 `Mounting at ...` / `Mounted at ...` / `Unmounting ...`；所有命令的错误信息重新出现在终端。日志文件内容不变（两版都完整记录）。
- 无配置项、无 wire 字段、无 CLI 参数、无 debug 接口改动。
