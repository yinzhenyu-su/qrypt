## 1. 基准

- [x] 1.1 在 `quark-encrypt` 上用只读 `probe test read --path` 记录迁移前基准，写入 `benchmark-baseline.md`，验证：同一文件 3 次冷读内容哈希一致。
- [x] 1.2 迁移后用相同命令复测并更新对比表，验证：冷读中位、冷 seek、`read_streams`、回退次数均有记录。

## 2. 严格 Range

- [x] 2.1 严格模式下 200 响应返回 `ErrUnsupported`，验证：`TestDriverReadStrictRangeRejectsWholeObject`。
- [x] 2.2 非严格模式保留 200 回退，验证：同一测试的非严格分支仍读到完整 body。
- [x] 2.3 读层子 Range 收到 `ErrUnsupported` 后整文件转单流，验证：现有 fake 驱动回退测试。

## 3. 能力与并发

- [x] 3.1 Quark capability 迁移尝试：`internal_parallel_read` → `parallel_range`，验证：能力矩阵测试。
- [x] 3.2 `ReadProfile()` 返回 `BulkStreams: 3`，验证：`ReadProfileOf` 测试。
- [x] 3.3 单段超过 8 MiB 的直接读继续使用内部并行，验证：内部并行测试观察到多个 part。
- [x] 3.4 有界读取不再触发内部并行，验证：4 MiB 窗口只发一次 Range。

## 4. 真实云盘与结论

- [x] 4.1 `quark-encrypt` 基准复测：事件出现 `read_streams=3`，无 `ErrUnsupported` 回退。
- [x] 4.2 冷读吞吐对比：3 流比单流低约 25–30%，迁移回退；数据见 `benchmark-baseline.md`。
- [x] 4.3 回退后 `quark-test` / `quark-download` 的 `probe test read` 和 contract 通过。
- [x] 4.4 更新 `CHANGELOG.md`（crypt 能力转发），验证：`git diff --check`。
