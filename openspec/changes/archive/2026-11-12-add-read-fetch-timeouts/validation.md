# Validation Record — add-read-fetch-timeouts

Date: 2026-11-12 (Asia/Shanghai)

## Local gates

- `go test ./...` — pass.
- `go test -race ./pkg/drive ./pkg/vfs/read ./pkg/vfs ./pkg/crypt ./pkg/contracttest` — pass.
- `golangci-lint run ./...` — 0 issues; `staticcheck ./...`, `go vet ./...` — clean.
- `scripts/ci/check-all.sh` with the repository toolchain `go1.27.3` stops at
  **Vulncheck**: `GO-2026-6859` (`net/http` MaxBytesReader race) is fixed in
  `go1.27.4`, so the pinned 1.27.3 toolchain is flagged. This is toolchain pin
  drift, not this change.
- `GOTOOLCHAIN=go1.27.4 scripts/ci/check-all.sh` — **== local CI check passed ==**
  (vet, staticcheck, golangci-lint, architecture boundaries, vulncheck, format,
  schema metadata, generated docs, install script, fast suite, race suite,
  VFS stability, localfs smoke).

## Fetch-bound model

- Defaults: 30 s idle, 5 min total (`drive.DefaultFetchIdleTimeout`,
  `DefaultFetchTotalTimeout`).
- 115 / 115-open override for non-VIP throttling: 90 s idle, 30 min total.
- One idempotent retry after an idle timeout; one deadline covers both attempts.
- Prefetch timeout is a silent cancellation; a foreground reader waiting on it
  takes the load over.
- Driver HTTP transports bound the first-packet phase (115 SDK and content
  client, 115-open, 189).
- `probe test read` bounds one read call at 10 min so a throttled provider's own
  timeout reports first, while a stuck FUSE callback still fails within a
  bounded time.

## Fake-driver scenarios (task 7.1)

`pkg/drive/fetch_timeout_test.go`, `pkg/drive/parallel_read_test.go`,
`pkg/vfs/read/fetch_timeout_test.go` cover:

- stalled body → idle timeout, body closed, read slots and prefetch permits to zero;
- slow but progressing body → succeeds; idle bound keeps resetting;
- trickling body → total bound fails the fetch;
- prefetch timeout → silent cancellation, `prefetch_cancel_reason=timeout`,
  counters to zero, no goroutine leak (goleak);
- one idempotent retry; retry exhaustion fails the whole window;
- window waiters receive the loader's timeout; foreground read takes over a
  timed-out prefetch;
- invalidate / seek / session release / close cancellation reasons are not
  overwritten by a racing timeout;
- `ReadStream` and direct reads (`drive.ReadParallelWithProfile`) use the same bounds;
- snapshot exposes `read_timeouts`, `prefetch_timeouts`, `last_timeout`;
- summary events carry `cancel_reason=timeout`, `wait_us`, `fetch_us`.

## Real 115 (task 7.2)

The user-space session was checked first and had expired
(`40101032 请重新登录`). After installing the user-supplied cookie and removing
the stale `state/driver/115/115_cookie.json`, `qrypt fs df 115` reported
15 GiB free. A temporary manual mount was used so the user-space config and
session were reused.

| Run | Binary state | Result |
|---|---|---|
| 64 MiB cold, old cookie | 403 on a prefetch window | probe failed with structured `EIO` in <1 ms; `cleanup` ok; mount `ls` ok; unmount ok |
| 64 MiB cold, valid cookie | before SDK header bound | **stalled at 17,825,792 bytes (17.0 MiB)** for 2 min; probe reported `mounted read timed out ... offset=17825792`; `cleanup` ok; mount `ls` ok; unmount ok |
| 16 MiB cold | final | pass in 2m46s, 101 KB/s, TTFB 11.3 s, 99.6% cache hits, 16 read calls |
| 32 MiB cold | final | pass in 4m27s, 126 KB/s, TTFB 14.6 s, 99.8% cache hits, 32 read calls |

The 64 MiB run reproduced the incident's exact stall point. The read failed in
bounded time instead of blocking the mount; `ls` and unmount kept working.
The final 16/32 MiB runs show a heavily throttled non-VIP 115 account still
completing: a slow but moving body is not falsely killed by the 90 s idle bound.

## Real 123 (task 7.3, 123 portion)

Temporary isolated mount, `--size 32m --samples 1 --cache-mode both --pattern sequential`.
Baseline is a detached `HEAD` worktree built without this change.

| Build | Cold end-to-end | Cold TTFB | Cold cache hit | Warm end-to-end | Warm cache hit | Read calls |
|---|---|---|---|---|---|---|
| baseline (HEAD) | 3.56 MB/s | 7.90 s | 99.7% | 349.1 MB/s | 100% | 32 |
| this change | 5.63 MB/s | 4.30 s | 99.7% | 343.1 MB/s | 100% | 32 |

Cold throughput improved (network variance favorable); warm throughput is within
~2% and cache-hit rates and read-call counts are identical. No read-throughput or
cache-hit regression observed on 123.

## Real quark-test (task 7.3, quark-test portion)

Temporary isolated mount, `--size 32m --samples 1 --cache-mode both --pattern sequential`,
with a refreshed `quark` credential cookie. Baseline is a detached `HEAD` worktree
built without this change.

| Build | Cold end-to-end | Cold TTFB | Cold cache hit | Warm end-to-end | Warm cache hit | Read calls |
|---|---|---|---|---|---|---|
| baseline (HEAD) | 5.81 MB/s | 1.17 s | 99.66% | 336.0 MB/s | 100% | 32 |
| this change | 11.26 MB/s | 1.28 s | 99.71% | 314.1 MB/s | 100% | 32 |

Cold throughput improved (network variance favorable); warm throughput is within
~6.5% on one sample, and cache-hit rates and read-call counts are identical. No
structural read or cache regression observed.

## Task 7.3 result

Both real mounts passed with unchanged cache-hit behavior and read-call counts:
123 (baseline 349.1 vs 343.1 MB/s warm) and quark-test (336.0 vs 314.1 MB/s warm),
with cold reads equal or faster. Verified via `probe test read` before/after.
