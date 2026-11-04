## 1. 基准

- [x] 1.1 在 `quark-encrypt` 上用只读 `probe test read --path` 记录迁移前基准，写入 `benchmark-baseline.md`，验证：同一文件 3 次冷读内容哈希一致。
- [ ] 1.2 迁移后用相同命令复测并更新对比表，验证：冷读中位、冷 seek、`read_streams`、回退次数均有记录。

## 2. 严格 Range

- [ ] 2.1 `downloadRange` 在 `drive.StrictRangeFromContext` 为真且状态非 206 时返回 `ErrUnsupported`，验证：200 响应测试。
- [ ] 2.2 非严格模式保留 200 回退，验证：整文件读和旧调用测试不变。
- [ ] 2.3 读层子 Range 收到 `ErrUnsupported` 后整文件转单流，验证：fake 驱动回退测试。

## 3. 能力与并发

- [ ] 3.1 Quark capability 从 `internal_parallel_read` 改为 `parallel_range`，验证：能力矩阵测试。
- [ ] 3.2 实现 `ReadProfile()` 返回 `BulkStreams: 3`，验证：`ReadProfileOf` 测试。
- [ ] 3.3 `size <= 0` 的整文件读继续使用内部并行，验证：整文件读测试观察到多个 part。
- [ ] 3.4 有界读取不再触发内部并行，验证：4 MiB 窗口只发一次 Range。

## 4. 真实云盘

- [ ] 4.1 `quark-encrypt` 基准复测：事件出现 `read_streams=3`，无 `ErrUnsupported` 回退。
- [ ] 4.2 冷读吞吐不低于基线中位，冷 seek 不劣化；若限流则降低 `BulkStreams`。
- [ ] 4.3 `quark-test` / `quark-download` 的 `probe test read` 和 contract 通过。
- [ ] 4.4 更新 `docs/for-developer/architecture.md` 与 `CHANGELOG.md`，验证：`git diff --check`。
