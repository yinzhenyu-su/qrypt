# Read Profile Baseline — 2026-11-08

## Environment

- Branch/commit: `feat/read-intent-policy` @ `178a498`
- Host: macOS 27.2, local home network
- Mounts: real Koofr and Drime accounts; `test_enabled = true`
- File: random 16 MiB / 64 MiB `.txt` fixture, read with `--path`
- Cache: `--cache-mode cold --pattern sequential --block-size 1m`
- Mounted probe: `--samples 2` per command, two interleaved cycles
- Raw probe: `probe bench read --size 16m --concurrency 1,2,4,8 --samples 2`

## Mounted 64 MiB

| Mount | Cycle | s1 MB/s | s4w16 MB/s | Change |
|---|---|---:|---:|---:|
| Koofr | 1 | 5.29 | 2.30 | -56% |
| Koofr | 2 | 4.34 | 2.37 | -45% |
| Drime | 1 | 8.07 | 5.62 | -30% |
| Drime | 2 | 10.68 | 6.35 | -41% |

## Mounted 16 MiB

| Mount | Cycle | s1 MB/s | s4w16 MB/s | Change |
|---|---|---:|---:|---:|
| Koofr | 1 | 3.43 | 3.69 | +7% |
| Koofr | 2 | 4.13 | 3.23 | -22% |
| Drime | 1 | 3.43 | 4.45 | +30% |
| Drime | 2 | 5.10 | 5.50 | +8% |

`s4w8` was consistently slower than both s1 and s4w16 on 16 MiB (Koofr
2.07–2.10 MB/s, Drime 2.95–3.05 MB/s). The 16 MiB results are too noisy and
too close to the 8 MiB threshold to drive a default.

## Raw driver 16 MiB

Aggregate MB/s by concurrency:

| Concurrency | Koofr sample 1/2 | Drime sample 1/2 |
|---:|---:|---:|
| 1 | 3.24 / 4.35 | 5.11 / 5.13 |
| 2 | 5.08 / 4.02 | 8.58 / 8.60 |
| 4 | 4.40 / 2.63 | 9.20 / 11.51 |
| 8 | 5.24 / 4.23 | 7.03 / 8.07 |

Koofr did not scale consistently; Drime scaled to 4 streams, then dropped at 8.
This is the raw driver path, not the mounted VFS path, and does not justify a
mounted-read default on its own.

## Decision

Keep Koofr and Drime `ReadProfile{BulkStreams: 1}`. The contributor's
250–900 KB/s environment benefits from `4/16`, but this fast-network environment
regresses 30–56% at 64 MiB in both interleaved cycles. A single driver default
cannot serve both, so parallel reads remain opt-in per mount via
`add-read-profile-override`; the driver default stays conservative.

## Reproduction

```sh
# Mount one real mount with an isolated QRYPT_HOME, create a 64 MiB .txt
# fixture through the FUSE mount, and wait until /v1/uploads is empty.
# Then, for two interleaved cycles:
qrypt probe test read --config ./qrypt.toml --mount <mount> \
  --mount-point <mnt> --path /<fixture>/bench.txt \
  --streams 1 --window-chunks 0 --samples 2 \
  --cache-mode cold --pattern sequential --block-size 1m

qrypt probe test read --config ./qrypt.toml --mount <mount> \
  --mount-point <mnt> --path /<fixture>/bench.txt \
  --streams 4 --window-chunks 16 --samples 2 \
  --cache-mode cold --pattern sequential --block-size 1m

qrypt probe bench read --config ./qrypt.toml --mount <mount> \
  --size 16m --concurrency 1,2,4,8 --samples 2
```
