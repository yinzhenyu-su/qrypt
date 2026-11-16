## 1. 夸克驱动读路径

- [x] 1.1 能力从 `internal_parallel_read` 改为 `parallel_range`，验证：`pkg/drivers/capability_test.go` 与夸克 profile 测试期望 `BulkStreams = 4`、无 `internal_parallel_read`。
- [x] 1.2 删除驱动内部 8 MiB 分片组装（`downloadParallel` / `fetchParallelPart` / `readDownloadPart`）与相关常量，`downloadBody` 只开一条 Range，验证：大 Range 只产生一条 Range 请求的单元测试。
- [x] 1.3 保留 strict range 语义与 URL 403 刷新，验证：现有严格模式测试仍通过。

## 2. 共享读层截断与短读

- [x] 2.1 `readWindowRangeAttempt` 在声明范围完整落在文件大小内时比较读取长度，短读返回 `io.ErrUnexpectedEOF`，验证：短 body fake 触发的单元测试。
- [x] 2.2 `readWindowRange` 对截断 body 幂等重放一次并记录 `fetch_body_retries`，重放仍失败则整窗失败，验证：重放成功与预算耗尽两个用例。
- [x] 2.3 `drive.readDirectPart` 同样校验分片长度并重放一次，验证：直读分片截断重放与失败用例。
- [x] 2.4 分片窗口的空闲超时仍然生效，验证：`fetchChunkWindowWithPlan` 单路子 Range 停住时在空闲上限内失败。

## 3. URL 共享请求

- [x] 3.1 `downloadURL` 在共享请求返回 `context.Canceled` 且本调用方 ctx 存活时重新发起一次，验证：预取取消 + 前台存活的并发用例。
- [x] 3.2 确认挂载关闭或调用方自身取消时不重试，验证：ctx 已取消的路径保持原错误。

## 4. 健康度

- [x] 4.1 `HealthTracker.RecordResult` 忽略 `context.Canceled`，验证：计数与等级单元测试。
- [x] 4.2 `HealthStatusFromMetrics` 跳过 `cancelled` 指标（缺失分类时回退按消息分类），验证：驱动指标健康度单元测试。
- [x] 4.3 取消事件仍保留在读事件与驱动指标中，验证：`/v1/reads` 与 `/v1/driver` 可查询到取消记录。

## 4a. 连接与总预算

- [x] 4a.1 夸克 `ReadProfile` 把单次取数总预算设为 90 s，验证：profile 单元测试与 `/v1/state` 快照 `fetch_total_timeout_ms=90000`。
- [x] 4a.2 下载 body 读失败后轮换下载客户端（旧连接池只关闭空闲连接），并按 5 s 节流，验证：body 失败轮换、节流窗口内不重复轮换、窗口过期后再轮换的单元测试。
- [x] 4a.3 真实挂载观察轮换：失败后首个请求 `reused_connection=false` 且 `remote_addr` 换成另一个 CDN 边缘 IP，验证：`/v1/driver` 指标与 `[QUARK] rotate download transport` 日志。
- [x] 4a.4 评估"吞吐下限看门狗"并否决：对照测量显示慢速时段是 provider 全局慢，下限只会打断合法慢读；结论记录在 `design.md`。
- [x] 4a.6（已由 optimize-seek-read-profiles 撤销）自适应慢读看门狗：按最近最佳速率的 1/8（下限 256 KiB/s）判定，5 s 宽限、每分钟最多中止一次；中止后由读层重放并换连接。验证：真实挂载日志出现 `download body failed: quark: download too slow` 并在轮换后恢复吞吐。
- [x] 4a.7 修复加密层截断处理：已知密文长度时，块内提前结束返回 `io.ErrUnexpectedEOF` 而不是解密认证失败；长度未知时继续接受 rclone 短尾块。验证：截断块/短尾块/完整块三个用例。
- [x] 4a.10 边缘惩罚列表：超时/慢读时记录远端 IP 5 分钟，下载拨号跳过惩罚 IP，全部惩罚时回退正常解析。验证：惩罚过期、跳过、回退、超时惩罚四个单元用例。
- [x] 4a.11 真机验证惩罚生效：慢读后日志出现 `penalize edge=... reason="body read failed"`，随后请求换到另一个 IP 且 `reused_connection=false`。
- [x] 4a.9 下载响应头等待降到 10 s，并在头超时后轮换连接，验证：真机 `driver_open_ms` 从约 120 s 降到单次 ≤10 s；6 次 1 MiB 冷读全部在 3.4 s 内完成。
- [x] 4a.8 读层把被关闭的 body（`response body closed`）也视为可重放截断。验证：closed-body 重放用例。
- [x] 4a.5 过滤生命周期取消：只有"已运行 ≥2 s 且速率 <512 KiB/s"或网络错误才轮换连接，避免 seek 触发的预取取消反复换连接；验证：5 次快速 kill 读不产生轮换日志，慢取消/网络错误仍轮换的单元测试。

## 5. 测试与真实挂载回归

- [x] 5.1 夸克包、`pkg/drive`、`pkg/vfs/read`、`pkg/vfs/diagnostics` 单元测试全部通过，验证：`go test` 对应包。
- [x] 5.2 重启 `quark-encrypt` 挂载后读取之前卡住的 11.4 GB 区域，验证：读完成、出现 `driver_body_ms`、无 5 分钟挂起。
- [x] 5.3 连续“顺序读 → 远距离 seek”，验证：无 EIO，URL 共享取消被重试吸收。
- [x] 5.4 取消预取后检查 `/v1/mounts/health`，验证：`ok / level=ok / errors=0`。
- [x] 5.5 跑全量本地 CI，验证：`scripts/ci/check-all.sh` 退出码 0 且输出 `== local CI check passed ==`。

## 6. 文档与生成物

- [x] 6.1 重新生成 `docs/for-user/support-drivers.md`，验证：`python3 scripts/schema/gen-driver-docs.py` 后 `git diff` 稳定。
- [x] 6.2 确认未新增用户配置字段，验证：配置 schema 与生成文档检查通过。
- [x] 6.3 更新 `CHANGELOG.md` 的 Unreleased 条目，并记录 `validation.md`，验证：条目与本变更行为一致。
- [x] 6.4 在 `docs/for-developer/driver-development.md` 说明有界 Range 必须流式返回，验证：文档描述与新规格一致。

## 7. 后续复测

- [x] 7.1 真实夸克挂载复测直读路径：`probe bench read`（绕过 VFS 的原始并发读）16 MiB 样本下 1 路 5.7/13.7 MiB/s，4 路聚合 5.8/15.1 MiB/s，两者都受账号约 15 MiB/s 上限约束；64 MiB 的 4 路长读还会触发慢读看门狗误判（per-stream 低于下限）。结论：4 路没有更快且更易误判，夸克默认 `ReadProfile.BulkStreams` 改为 1，mount override 仍可显式开启。
