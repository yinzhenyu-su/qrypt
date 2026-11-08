## Why

Read parallelism is environment-dependent. Local 64 MiB measurements on
2026-11-08 show `BulkStreams: 4, BulkWindowChunks: 16` regressing fast links
(Koofr 4.3–5.3 → 2.3 MB/s, Drime 8.1–10.7 → 5.6–6.3 MB/s), while the
contributor's 250–900 KB/s link improved by roughly 1.8–4.3×. A single driver
default cannot serve both, and the current `read-pipeline` design deliberately
has no user configuration. Add an optional per-mount override so constrained
links can opt in without changing the safe defaults for everyone else.

## What Changes

- Add an optional per-mount `[mounts.read_profile]` table with `streams`
  (1–32) and `window_chunks` (1–64). Unset fields inherit the driver's
  `ReadProfile`.
- Effective profile precedence: mount override > driver profile > built-in
  defaults. The override applies only to that mount.
- The override applies to `bulk` reads through the VFS and to the direct
  read/copy helper for that mount. `interactive` and `probe` reads stay
  single-stream.
- Validate the ranges, require `CapabilityParallelRange` for `streams > 1`,
  reject `streams > 1` for `CapabilityInternalParallelRead`, and clamp to
  `ParallelReadLimiter` when present.
- Expose the effective profile and its source (driver default vs mount
  override) in the debug snapshot.
- Update the config schema, generated `full-config.md`, and user/developer
  docs. Do not change the driver defaults for Koofr/Drime; they stay
  single-stream unless the user opts in.
- No global read configuration and no new `read_cache` fields.
- **BREAKING**: none.

## Capabilities

### New Capabilities

- `read-profile-config`: optional per-mount read scheduling overrides, their
  validation, precedence, scope, and observability.

### Modified Capabilities

None. `read-pipeline` is delivered by the in-flight
`add-read-intent-policy` change; this change builds on it without changing its
requirements.

## Impact

- `pkg/config`: `MountConfig`, validation, and effective-profile resolution.
- `schema/qrypt.schema.json` and generated `docs/for-user/full-config.md`.
- `pkg/core`: pass the mount's override into VFS construction and direct-copy
  tasks.
- `pkg/vfs`: carry the effective profile into `read.ReaderDeps` and expose it
  in debug output.
- `pkg/drive` / `pkg/vfs/drivecopy` / `pkg/core/share_fetch.go`: thread the
  effective profile through direct read paths.
- Documentation and tests for validation, precedence, capability gating, and
  opt-in behavior.
