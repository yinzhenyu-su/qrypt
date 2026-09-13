## 1. 端到端读取计数用例（pkg/crypt）

- [x] 1.1 扩展 fixture：`countingSHA256Source` 增加 `bytesRead`，`Open` 返回包一层的 `countingReadOnlyFile` 统计 `Read`/`ReadAt` 拉取的明文字节；既有两个只断言 `opens` 的用例保持通过
- [x] 1.2 新增 `windowedHashDriver`：在 `hashRequiringRawDriver`（source uploader + RequiredUploadHashes=md5+sha1）之上按固定窗口流式读完 `req.Source`，记录流出字节
- [x] 1.3 新增 `TestDriverPutSourceReadsEachPlaintextBlockOncePerPass`：262267 字节源（4 整块 + 部分尾块），4 KiB 与 32 KiB 两个窗口各跑一次真实 `Driver.PutSource`（content_dedup），断言 opens==2、流出密文==完整加密长度、明文读取 ≤ 2×源大小 + 2×块大小。实测两个窗口都是 524534 字节（恰好 2×），耗时 0.06s
- [x] 1.4 负向验证：把 `encryptedBlock` 的缓存命中改成恒 false，用例以 `window 4096: read 5243126 plaintext bytes for a 262267 byte source (limit 655606)` 失败（20×）；恢复后通过

## 2. 验证

- [x] 2.1 `gofmt -l pkg/crypt/` 无输出、`go test ./pkg/crypt/ -count=1` 全绿
- [x] 2.2 `scripts/ci-check.sh` 全绿（含 staticcheck / golangci-lint / race / vfs-stability / smoke）
