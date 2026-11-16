## 1. 配置与 profile

- [x] 1.1 `ReadProfile` 增加 `StreamPartChunks`/`ParallelMinChunks`，含校验、合并、归一化与 `hasReadProfileFields`；测试边界值、覆盖继承与归一化。
- [x] 1.2 `[mounts.read_profile]` 增加 `part_chunks`/`min_split_chunks`，配置校验、`readProfileOverride` 转换、schema 与生成文档同步。

## 2. 拆分形状

- [x] 2.1 `ReadPlan` 携带分片大小与门槛，`splitWindow` 支持固定分片并把余量并入最后一片；测试均分、固定分片、片数被流数封顶、门槛拒绝小窗口。
- [x] 2.2 诊断快照暴露两个有效值。

## 3. 验证

- [x] 3.1 gofmt、针对性测试、`openspec validate add-read-part-size --strict`、`scripts/ci/check-all.sh` 通过。

## 4. 评审修复

- [x] 4.1 分片字段改为可选指针：显式零覆盖驱动固定值、缺省继续继承；贯通配置、合并、克隆、校验、诊断与直读，测试 0/省略/边界与 TOML 解码。
- [x] 4.2 直读路径消费分片大小、拆分门槛与文件在途预算；测试 512 KiB 分片、预算 1 不并发、门槛拒绝小请求。
