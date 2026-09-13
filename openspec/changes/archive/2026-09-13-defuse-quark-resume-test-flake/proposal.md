## Why

`TestDriverPutMultipartUploadResumesPersistedParts`（`pkg/drivers/quark/driver_test.go`）在并行负载下会失败：它用固定的 `context.WithTimeout(..., 150ms)` 打断第一次上传，然后断言 part 1 已经上传过一次（`partUploads["1"] == 1`）。150 ms 内要跑完 pre → update/hash → auth#1 → PUT part 1 → auth#2 五个本地往返（OSS 还是 TLS 测试服务器），机器一忙就会在 part 1 落地前超时，断言以 `part 1 uploads after first attempt = 0, want 1` 失败。

实测证据：给假服务器每个请求人为加 50 ms 延迟后，旧写法两次运行都必然失败（0.16s / 0.17s 即失败）；这条 150 ms deadline 自 2026-07-20 的 ea59ef8 起未变，属于既有 timing flake，而不是被测代码的问题。

## What Changes

- 用**协议可见的同步点**替换墙钟：OSS 假服务器在收到会挂起的 part 2 PUT 时关闭 `part2Started`。驱动只在 part 1 被确认并且其 ETag 已持久化（`savePart` 同步落盘）之后才会发出 part 2 的请求，因此在收到该信号后取消 context，可以确定"resume 状态已在磁盘上"，不再和调度延迟赛跑。
- 第一次上传改为在 goroutine 中运行，失败信号通过带缓冲 channel 回传；新增两条诊断分支：尝试在 part 2 之前就结束 → 直接报出真实错误；30 s 内始终没到 part 2 → 取消并报"never reached part 2"，避免测试挂死。
- 顺带合并 OSS PUT 分支里重复的两次加锁。
- 断言不变（part 1 只上传一次、resume 后 part 2 至少 2 次、part 3 恰好 1 次、pre/hash 各一次），覆盖的语义与原来完全一致。

非目标：不改动驱动代码；不提高其它用例的超时；不引入测试专用等待工具。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

无。只改测试同步方式，不改任何可观察行为，因此 `.openspec.yaml` 声明 `skip_specs: true`。

## Impact

- `pkg/drivers/quark/driver_test.go` 一个用例（测试内同步 + 诊断）。
- 无生产代码改动，无 wire/持久化/配置变化。
