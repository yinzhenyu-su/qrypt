---
name: qrypt-flake-triage
description: Diagnose and fix qrypt timing flakes deterministically — reproduce under parallel load, classify test-pinned ordering versus a missing production wakeup, and fix with live-registry assertions or real signal propagation instead of longer timeouts. Use when vfs-stability or -race fails intermittently, a test says "timed out waiting", or CI goes red only under load.
---

# Qrypt 时序 Flake 判定与修复

原则：**flake 是信息，不是噪声**。要么测试把时序写死了（改测试），要么生产漏了唤醒/取消（改生产）。
加超时、`t.Skip`、放宽断言只是把问题埋起来，不允许。

## 1. 先复现，别单跑

```bash
# 三路并行压同一测试：把调度抖动放大到可复现
for i in 1 2 3; do
  (go test -count=40 -run '^TestName$' ./pkg/vfs/read &) ; done; wait

scripts/ci/test-layers.sh vfs-stability     # 3 次重复，抓 pkg/vfs/... 时序 flake
go test -race -count=20 -run '^TestName$' ./pkg/<pkg>
```

记录失败率（例如 1/120、约 1/6 层运行）和机器负载；只出现一次就归因“偶发”没有意义。

## 2. 分类决策树

拿到失败现场先问：

1. **等待的是事件还是时间？** 测试等的东西是否有可观察状态（窗口 registry、inflight、
   上传队列、scope 状态）？有 → 测试应该读状态，而不是 sleep 或固定 offset。
2. **生产代码在状态变化时是否发信号？** 检查成对的 begin/end/abandon、attach/release、
   start/finish：只要有一处在删状态时不调用 `quiesceSignal()`/`changed` 通知，
   `Quiesce` 或等待者就会睡到超时。
3. **断言是否绑定了某一次调用的顺序/偏移？** 例如只 gate 第一个 lookahead 窗口、
   只认 `offset == ChunkSize` 的那条 Range；调度变化后“第一个”可能被取消而未发出。
4. **错误是否来自被取消的 join？** `context.Canceled` 沿 waitTarget → 窗口错误
   向上冒泡时，未必是调用方取消，可能来自被 seek/会话释放取消的窗口。

今天两个真实例子：

- `TestStageZeroDepthDisablesLookahead` 超时：`abandonWindow` 删除最后一个窗口时没有
  发 `quiesceSignal()`，等待者被 `scopes.onChange` 提前唤醒后再睡到 2 s 超时 → **生产 bug**。
- `TestScenarioFakeDriverPrefetchCancelsOnInvalidate`：测试只 gate 第一个 lookahead 窗口，
  但 W×D 调度会同时准入 4 个窗口，被 invalidate 取消的是尚未发出 Range 的那个 →
  **测试 bug**，改成 gate 全部 lookahead + 接受任意被取消的 prefetch 调用。

## 3. 临时埋点（可回滚）

- 埋点用 env 开关（如 `QRYPT_PFT=1`），放临时分支/工作区，定位后 `git checkout --` 回滚；
  提交前确认没有残留。
- 失败时 dump 现场比加日志更省事：记录到的调用（offset/size/gated/prefetch/err/plan）、
  runtime 窗口/预取计数、live loads、scope 状态。
- 读取 live 状态优先用已有的测试助手（如 `pkg/vfs/read/seek_window_test.go` 的
  `wideLookaheadStart`），不要从历史日志反推。

## 4. 修复原则

- **生产修复**：所有会让“域变空/任务结束”的路径都必须通知等待者；注释里写清
  begin/end/abandon 的成对语义。修完补一个直接调用该路径的回归测试（不依赖时序）。
- **测试修复**：让断言绑定“存在一个满足条件的调用/状态”，而不是“第几个调用、某个固定 offset”；
  用 `waitCall`/`Quiesce`/`Flush` 等收敛点，不用 `time.Sleep` 猜。
- 不允许：加大 timeout、重试循环、`-p 1` 串行化整个层、放宽到“任意错误”。

## 5. 收尾验收

- [ ] 达到原失败率的 10 倍以上迭代数仍稳定（例：720 次 / 6 轮 vfs-stability 全绿）。
- [ ] 删除全部临时埋点，工作区只剩预期改动。
- [ ] `gofmt` + `go vet` + 目标包 `-race` + 完整 `scripts/ci/check-all.sh` 全绿。
- [ ] 提交拆分：生产修复 `fix(scope)`，测试确定性 `test(scope)`；提交信息写清根因。
- [ ] 若是已知的老 flake（在干净基线上也复现过），在结论里明确标注“非本次回归”，
      并给出复现证据，不要静默吞掉。
