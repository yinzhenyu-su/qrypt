# Validation Record — quark-shared-read-bounds

Date: 2026-11-13 / 2026-11-14 (Asia/Shanghai)

## Local gates

- `go test ./...` — pass.
- `scripts/ci/check-all.sh` with the repository toolchain `go1.27.4` — pass,
  output `== local CI check passed ==`.

## Observed failure before the change (quark-encrypt, real mount)

- Player read of `MIRD-237-U/MIRD-237-U.mp4` at offset 11,415,846,912 waited on a
  window whose driver `Read` was buffering the whole 8 MiB+2 KiB range.
- `/v1/ops` showed 4 active `vfs_prefetch/fetch_window` entries at 233 s and one
  foreground `vfs_read/read_range` plus one `vfs_wait/wait_window`.
- Goroutines were parked in `quark.(*Driver).downloadParallel` → `sync.WaitGroup.Wait`
  and `readDownloadPart` → `io.ReadAll` → `http2` body read.
- At the 5-minute total budget (`fetch_total_timeout`) all four windows failed with
  `fetch_timeout_kind=total`; the foreground FUSE read returned after `5m14.4s`.
- Two later incidents were unrelated to the stall: a seek that cancelled prefetch
  windows also cancelled the shared `file/download` singleflight request and made
  the foreground read fail with EIO, and those cancellations pushed mount health
  to `unhealthy`.

## After the change (same mount and file, read-only)

- Effective profile: `streams=4`, capability `parallel_range`, no
  `internal_parallel_read`.
- Previously stuck region: 16 MiB sequential read in 7.9 s (2.0 MiB/s, limited by
  the CDN's single-stream rate at that time); slowest single call 5.9 s; no
  five-minute stall.
- Window events now report `driver_open_ms` ≈ 0.8 s and `driver_body_ms` ≈ 6.8 s,
  which means the body is drained by the read layer's `ReadBodyLimited` (idle
  watchdog active). Before the change the same events reported
  `driver_open_ms=300009` with no body timing.
- `/v1/state` runtime: `read_timeouts=0`, `prefetch_timeouts=0`.
- Three rounds of "sequential read → immediate far seek": no EIO.
- After several cancelled prefetch windows:
  `/v1/mounts/health?mount=quark-encrypt` → `ok: true, level: ok, success: 272,
  errors: 0` (`download 30/0`, `download_url 31/0`, `read 200/0`). Before the
  change the same cancellations showed 6 `download` errors and `unhealthy`.

## Connection bound and rotation (2026-11-14)

- `ReadProfile()` now returns `FetchTotalTimeout = 90s`; the live mount reports
  `fetch_total_timeout_ms=90000` with the idle bound unchanged at 30s.
- On a body read failure the driver marks the download connection stale and
  swaps the client before the next request (the swap is applied lazily so the
  retry that follows the failure cannot race it). Cold reads of
  `start-010.mp4` showed the intended effect in `/v1/driver`: requests before
  the failure were served from one CDN edge with `reused_connection=true`
  (e.g. `222.186.17.203:443`, later `180.97.232.68:443`), and the first request
  after a cancelled read used a different edge with `reused_connection=false`
  (`124.239.228.19:443`, later `119.167.175.34:443`).
- Killing a read mid-flight reproduces the swap on demand: the log shows
  `[QUARK] rotate download transport reason="download body failed: context canceled"`
  and the next request reports `reused_connection=false`.
- Throughput while the CDN was healthy: 32 MiB cold reads at 6.1–6.6 MiB/s, no
  regression versus the pre-change mount. A separate slow period (0.56 MiB/s)
  was provider-wide and recovered without any config change.
- Lifecycle cancels no longer rotate: an early version rotated on every failed
  body read, which includes the prefetch cancellations a seek produces. During
  a seek-heavy 4K session the log showed one rotation in the middle of the
  session (`http2: response body closed`), each rotation paying a TLS handshake
  and possibly a colder edge. The driver now treats a cancellation as a bad
  connection only when it ran at least 2 s below 512 KiB/s; five rapid
  read-kill cycles after restart produced zero rotations, while the stalled /
  network-error paths still rotate (unit tests).
- Provider edge state dominates the observed peak: at ~14:00 the pinned edge
  served 0.28 MiB/s through the mount while fresh curl connections to the same
  file and nearby offsets got 2.5 MiB/s; after the 90 s budget expired once and
  the connection rotated, the mount recovered to 3.2–4.8 MiB/s.
- A slow-read watchdog now aborts a body that stays below max(256 KiB/s,
  best/8) after 5 s and lets the read layer replay it on the rotated
  connection, at most once per minute. On the real mount the log shows
  `rotate download transport reason="download body failed: quark: download too slow"`
  at 14:30:56 and 14:33:18 while a 512 KiB window was taking 6–20 s; after the
  swap the same region read at 1–2 MiB/s instead of 0.2 MiB/s.
- That path exposed an encryption-layer defect: a ciphertext block that ended
  early was fed to `secretbox.Open`, surfacing as a non-retryable
  `failed to authenticate decrypted block` and an EIO. `DecryptingReader` now
  takes the expected ciphertext length when the caller knows it, returns
  `io.ErrUnexpectedEOF` for a short block, and still accepts rclone's short
  final block when the length is unknown. The read layer also replays a body
  closed under the reader (`response body closed`).
- The old build spent **119,506 ms in `driver_open`** on one 1 MiB read at
  12,885,950,464 (`prefetch_window` completed after 1m59.5s), i.e. four 30 s
  response-header waits plus backoff. The download client now waits at most
  10 s for headers and rotates the connection on that timeout. After the change
  six fresh 1 MiB reads completed in 0.95–3.35 s with no 12 s deadline hit.
- Edge penalties work end to end: a slow body on `124.239.228.18` produced
  `[QUARK] penalize edge=124.239.228.18 reason="body read failed" ttl=5m0s`
  together with a transport rotation; the next requests were served by
  `124.239.228.19` with `reused_connection=false`. Raw curl on the same host
  showed the edge spread this targets: 6–7.7 MB/s on healthy IPs versus
  60 KB/s on a bad one, while eight connections across eight IPs still
  aggregated only 13–16 MiB/s (the account-wide ceiling).
- Direct reader concurrency re-measured with `probe bench read --mount quark-test
  --size 16m --concurrency 1,4 --samples 2`: one stream 5.7 / 13.7 MiB/s, four
  streams 5.8 / 15.1 MiB/s aggregate. Both sit at the account-wide ~15 MiB/s
  ceiling, so four streams bring no gain. A 64 MiB four-stream run also tripped
  the slow-read watchdog per stream (`unexpected EOF`); the driver default is
  therefore `BulkStreams = 1` and a mount can still opt in through
  `[mounts.read_profile] streams = N`.
- A fixed throughput floor was evaluated and rejected: during the slow period
  every fresh connection was slow too, so aborting a slow-but-progressing read
  would only retry against equally slow edges. Only failure-driven rotation is
  kept.

## Unit coverage

- `pkg/drivers/quark`: large range stays one request; capability/profile matrix;
  download URL retry after a shared cancellation; 90s total budget; download
  transport rotation on body failure, its 5s throttle, and rotation after the
  throttle expires.
- `pkg/vfs/read`: split-window idle timeout, truncated-body replay and replay
  budget exhaustion.
- `pkg/drive`: direct part truncated-body replay and failure after the budget;
  health ignores caller cancellation (tracker and metric paths).

## Known limitations

- The direct read path now uses the default 4 streams for Quark. The 2026-11-04
  benchmark for parallel small ranges does not cover this path, so its throughput
  is a follow-up measurement (task 7.1). The VFS read hot path remains
  single-stream (1-chunk foreground windows, single-stream prefetch windows).
- The validation was single-sample per region; Quark CDN throughput varies
  substantially over time, so absolute numbers are indicative only.

## 后续修正（2026-11-15，optimize-seek-read-profiles）

本变更引入的自适应吞吐下限看门狗已由
[`optimize-seek-read-profiles`](../../../changes/optimize-seek-read-profiles/proposal.md)
移除。真实挂载测量显示：seek 会主动取消预取，低速也可能来自预取竞争或本地限速；
按“相对历史最佳速率 1/16”判定会把正常取消和持续有进展的低速读判成坏连接，进而惩罚
CDN 地址 5 分钟、轮换连接池并重放已下载窗口。现在的语义是：只有真实传输错误才降级
连接，idle/total 边界仍按原样终止长时间无进展的读取。本节保留原始记录，但不再描述
当前行为。
