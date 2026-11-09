---
name: qrypt-upload-triage
description: Diagnose stuck, slow, or endlessly retrying qrypt uploads — reading pending-upload state, classifying the error, and deciding whether the provider, the driver request budget, or a dead target is at fault. Use whenever an upload never finishes, retry counts keep growing, `bytes_uploaded` resets to zero, or a mount reports upload timeouts.
---

# qrypt Upload Triage

An upload that "hangs" is one of three things: the provider is slow/stalled, a
driver request timeout is too short for the file, or the target cannot accept
the write at all. This workflow separates them with state, logs, and one
repeatable upload measurement.

## Ground rules

- **Do not delete user data.** Pending uploads are local staging copies; only
  remove them when the user asks. Never edit `pending.jsonl` while a mount is
  running.
- **Use the probe for reproduction.** `probe test upload` creates and removes
  its own fixture. Do not upload into an existing remote path.
- **Keep credentials local.** `qrypt.toml` is gitignored; never paste cookies,
  tokens, or keys into chat, scripts, docs, or commits.
- **Restart to apply code fixes.** A running mount keeps the binary it started
  with; driver changes only take effect after restarting the mount.

## 1. Read the live upload state

```sh
qrypt fs pending -c qrypt.toml
# or, on a running mount:
qrypt debug raw '/v1/uploads?history=1' -c qrypt.toml
```

Per upload, look at:

| Field | Meaning |
| --- | --- |
| `state` | `uploading`, `retry_wait`, `completed`, `failed` |
| `bytes_uploaded` / `bytes_total` | progress **within the current attempt** |
| `retry_count` | attempts so far; a large number means a loop |
| `last_error` | the failure class (see next step) |
| `last_attempt_at` / `next_attempt_at` | retry cadence |
| `events[].phase` | `queue_wait`, `snapshot_hash`, `prepare_remote`, `driver_put_source`, `replace_existing`, cleanup |

Signals:

- `bytes_uploaded` climbs then **resets to 0** on each retry → the driver
  re-sends from the beginning (single-request upload, or no resume support).
- `bytes_uploaded` stops moving for minutes → stalled connection or provider
  throttling.
- `retry_count` grows by ~1 every 15 minutes with the same error → permanent
  failure misclassified as retryable, or a timeout bound too small.
- `last_error` contains `not found` / `ENOENT` → the target path is gone (see §4).

## 2. Classify `last_error`

| Error text | Likely cause | Action |
| --- | --- | --- |
| `drive: not found` / `file does not exist` | parent directory deleted, renamed, or is a read-only nested mount | fix the path or treat as permanent; retrying cannot help |
| `no progress for 2m` / `upload stalled` | provider stopped reading the body | retry; if frequent, the provider/link is the problem |
| `exceeded 15m/60s/...` / `context deadline exceeded` | request budget shorter than the file needs | increase the driver budget or use chunked upload (see §5) |
| `context canceled` | mount stopped, seek/invalidate, or user cancel | not a failure |
| `401/403/permission denied` | credential or remote permission | refresh credential; do not retry |
| `429` / `rate limit` | provider throttle | back off; expect retries |

Cross-check the mount log (`~/.qrypt/logs/qrypt.log`):

```sh
grep -a 'upload failed\|upload start\|driver_put_source' ~/.qrypt/logs/qrypt.log | tail -50
```

The VFS logs `upload start ... parent="<remote parent>"`, which is exactly what
the driver used as the target directory.

## 3. Driver timeout model

A whole-file upload under one request can never survive a short total timeout;
chunked drivers only need each part to fit. Known floors:

| Driver | Upload shape | Request bound | Minimum sustained rate |
| --- | --- | --- | --- |
| WebDAV | one PUT for the file | size-derived budget (≥5m, 24h cap) | none after fix |
| Koofr | one multipart POST | size-derived budget (≥15m, 24h cap) | none after fix |
| 139 | 100 MiB parts (512 MiB >30 GiB) | per-part budget (≥2m, 1h cap) | ~1.7 MB/s per part |
| Drime | 64 MiB parts (configurable) | 15m client timeout | ~73 KiB/s |
| Quark | server-assigned part size | 2m per OSS part | ~30–70 KiB/s |
| Ali yun Drive | 10 MiB parts | 60s client timeout | ~175 KiB/s |
| Baidu | 4 MiB parts | 60s client timeout | ~68 KiB/s |
| 123 | 16 MiB parts | no total timeout | none |
| OneDrive | 5 MiB parts | no total timeout | none |
| 189 | 10 MiB parts | no total timeout | none |
| S3 | 16 MiB parts | SDK | none |
| sftp | 8 MiB parts | 5m per part | ~27 KiB/s |

WebDAV and Koofr use `pkg/drivers/internal/driverutil` helpers:
`UploadBudget(size, minRate, min, max)` plus `NewUploadWatchdog` (2-minute
no-progress cancel). New single-request uploaders should use the same pattern.

## 4. Environment trap: the target is another mount

A remote path that lists normally can still reject writes. On a server that
runs qrypt itself, a directory like `/root/Qrypt` may be a FUSE mount root —
read-only for arbitrary entries — while a cached listing still shows files.

```sh
# on the server
findmnt -T /root/Qrypt          # fuse.qrypt?
sftp> ls -la /root/Qrypt        # dr-xr-xr-x, missing expected child
sftp> mkdir /root/Qrypt/x       # "No such file or directory"
```

Result: every upload returns ENOENT and retries forever. Fix the mount root
(point it at a writable subdirectory) or remove the pending uploads; do not
"wait for the provider to recover".

## 5. Reproduce and decide

```sh
# 64 MiB is enough to cross most chunk boundaries
qrypt probe test upload --mount <mount> --size 64m

# Prove a timeout threshold: pick a size that needs longer than the old limit
# at the current rate (e.g. 127 MiB took ~118s on a ~1 MB/s uplink).
./scripts/dev/debug-mount.sh <mount> upload --size 512m
```

The result reports `put` duration, `end_to_end_bps`, `verify_mode`
(`full` or `sample`), and the fixture is removed automatically. If the same
upload succeeds now but failed in the mount log with `context deadline
exceeded`, the fix is a driver budget/watchdog change, not the provider.

Failure paths can be made deterministic instead of waiting for a real stall:

```sh
qrypt probe test upload --mount <mount> --size 16m --fault truncate --fault-after 1m
qrypt probe test upload --mount <mount> --size 16m --fault stall --fault-after 1m   # ~2m
qrypt probe test upload --mount <mount> --size 4m  --fault notfound
```

`fault_outcome: "driver"` means the driver's own watchdog or request timeout
ended the attempt. `probe_deadline` means it did not, and the probe's 3-minute
bound fired — that driver lacks an upload watchdog.

### Sweep upload threads and chunk size

```sh
qrypt probe bench upload --mount <mount> --size 64m \
  --concurrency 1,2,4 --chunk-size 8m,16m --samples 1
```

- `--concurrency` is the number of simultaneous file uploads (threads);
- `--chunk-size` is applied only when the report says
  `supports_chunk_size: true` (Drime, 139, sftp, OneDrive, Ali yun Drive);
  other drivers keep their default part size and report it as unsupported;
- drivers that upload parts in parallel (Drime, 139) still use their configured
  part concurrency; `--concurrency` only changes how many files run at once;
- a flat `aggregate_bps` across concurrency levels means the provider/account
  is the ceiling; a peak followed by a drop means request overhead or
  throttling.

The bench creates unique payloads per thread (so dedup cannot short-circuit
it), and removes every fixture.

For a user-visible copy, compare `qrypt fs put <file> <dest>` timing as well —
the destination mount's write path and the source read path both count.

## Checklist

1. `fs pending` / `/v1/uploads` — state, retry count, bytes progress.
2. Classify `last_error`; check `parent="..."` in the log.
3. Compare against the driver timeout table.
4. If the target path is suspicious, check it is not a nested/read-only mount.
5. Reproduce with `probe test upload`; record duration and throughput.
6. Fix the path, the credential, the request budget, or the resume support —
   do not just raise the retry cap.
