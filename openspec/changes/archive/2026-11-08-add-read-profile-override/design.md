## Context

`add-read-intent-policy` already resolves read behavior from
`drive.ReadProfileOf(driver)`: `vfs.New` stores it in `read.ReaderDeps.Profile`,
and `drive.ReadParallel` uses it for `drivecopy` and share-fetch fallback.
`ReadProfile` is explicitly not user configuration today, and the current
driver defaults for Koofr/Drime are single-stream.

Config already supports per-mount overrides for `read_cache` and `upload`
through `MountConfig`; `buildMount` resolves both before constructing the VFS.
The same point can resolve a read-profile override and pass it to every read
path for that mount.

## Relationship to `add-read-intent-policy`

`add-read-intent-policy` deliberately chose "no user-tunable read policy" and
its proposal/design say so. This change reopens that design decision only for an
optional per-mount opt-in; it does not change the `read-pipeline` normative
requirement that configuration must not be *required*. The earlier change should
reference this follow-up rather than keeping the absolute "no new user
configuration" wording. `add-read-intent-policy` must land first.

## Goals / Non-Goals

**Goals:**

- Keep driver defaults and behavior unchanged when no override is configured.
- Allow one mount to opt into a measured stream/window profile without a
  rebuild and without affecting other mounts.
- Reuse the existing capability gating, limiter, intent, cancellation, and
  cache rules.
- Make the effective profile and its origin visible in diagnostics.

**Non-Goals:**

- No global `[read]` or top-level read-profile setting.
- No CLI `mount set` flags in this change; operators edit `qrypt.toml` (a CLI
  surface can be added later without changing the spec).
- No adaptive/automatic network detection and no change to `read_cache`
  eviction semantics.
- No new driver defaults; Koofr and Drime remain single-stream unless the
  mount opts in.

## Decisions

### 1. Config shape: `[mounts.read_profile]`

Add a per-mount table:

```toml
[[mounts]]
name = "koofr"
type = "koofr"

[mounts.read_profile]
streams = 4
window_chunks = 16
```

`streams` maps to `BulkStreams`; `window_chunks` maps to `BulkWindowChunks`
(512 KiB units). Zero or omitted means inherit. This mirrors the existing
per-mount `[mounts.read_cache]` / `[mounts.upload]` pattern and avoids a new
top-level read section.

Alternative rejected: adding fields to `read_cache`; it mixes cache capacity
with scheduling and would make `read_cache` the wrong abstraction.

### 2. Resolve once in `pkg/config`, apply everywhere

Add `Config.ReadProfileFor(mountName)` returning an optional override, and a
`pkg/drive` helper:

```go
func ReadProfileOfWithOverride(d Driver, override *ReadProfile) ReadProfile
```

It merges in the existing order (override, driver profile, defaults), applies
the existing capability gates, and clamps to `ParallelReadLimiter`. `ReadProfileOf`
delegates to it with nil so existing callers are unchanged.

`buildMount` resolves the profile and passes it to:

- `vfs.Options.ReadProfile` → `read.ReaderDeps.Profile` for VFS reads;
- the source profile carried by direct-copy/share-fetch operations for
  `drive.ReadParallel`.

Alternative rejected: wrapping the driver with a profile-aware decorator.
`pkg/vfs` and `pkg/core` type-assert optional interfaces (capabilities, share,
resolver, state store); a wrapper would hide them unless it forwards every
interface, which is brittle.

### 3. Direct reads use an explicit profile argument

Keep `drive.ReadParallel(ctx, d, entry, offset, size)` as the driver-default
form, and add:

```go
func ReadParallelWithProfile(ctx context.Context, d Driver, entry Entry,
    offset, size int64, profile ReadProfile) (io.ReadCloser, error)
```

`ReadParallel` delegates with `ReadProfileOf(d)`. `drivecopy` and share-fetch
fallback receive the source mount's effective profile from core, so an
overridden mount uses the same values for VFS and direct reads. This avoids
context-value coupling and keeps the profile visible at the call site.

### 4. Validation at mount build and config load

`pkg/config.ValidateMount` constructs the driver for existing parameter
validation, so it can also inspect capabilities:

- `streams` must be 0 (inherit) or 1–32; `window_chunks` 0 or 1–64.
- `streams > 1` requires `CapabilityParallelRange` and must not be combined
  with `CapabilityInternalParallelRead`.
- Effective streams are still clamped by `ParallelReadLimiter`; the diagnostic
  records the clamp instead of rejecting the config.

`window_chunks` is only used for `bulk` intent; it is accepted with
`streams = 1` so a user can tune bulk window size without parallel streams.

### 5. Diagnostics expose effective values and source

Add a per-mount read-profile block to the debug snapshot:

```json
"read_profile": {
  "streams": 4,
  "window_chunks": 16,
  "prefetch_depth": 1,
  "parallel_threshold": 8388608,
  "source": "mount_override",
  "clamped": false
}
```

`source` is one of `driver`, `mount_override`, `driver_limiter`; no secrets.
This lets support distinguish a user override from a driver default without
reading the config file.

## Conflict Review

| Severity | Finding | Evidence | Resolution |
|---|---|---|---|
| semantic inconsistency | `add-read-intent-policy` design says "no new user configuration"; this change adds an optional per-mount override | `openspec/changes/add-read-intent-policy/design.md` decision 3; `proposal.md` Impact | Keep the normative "must not require config" requirement; explicitly supersede the stricter design wording and update the earlier change to reference this follow-up |
| spec gap | `ReadProfile` comments and developer docs say it is not user configuration | `pkg/drive/read_profile.go:3-6`, `pkg/vfs/read/reader.go:43-45`, `docs/for-developer/driver-development.md` | Update comments/docs to "driver default, optionally overridden per mount"; tasks 6.7 |
| scope gap | Direct `drive.ReadParallel` paths bypass VFS | `pkg/vfs/drivecopy/directcopy.go:488`, `pkg/core/share_fetch.go:390` | Include explicit profile propagation in this change (tasks 4.2/4.3) instead of claiming VFS-only coverage |
| semantic inconsistency | `CapabilityInternalParallelRead` plus a user stream override is ambiguous | `pkg/drive/read_profile.go:58-80` | Reject `streams > 1` for internal-parallel drivers in validation |
| configuration ownership | `[read_cache]` must not absorb scheduling fields | `pkg/config/config.go:123-125` | Use a separate `[mounts.read_profile]` table and leave `read_cache` unchanged |

No hard contract conflict remains after these resolutions.

## Risks / Trade-offs

- **User enables a profile that is slower on their link** → the default remains
  driver-owned and conservative; docs call this an expert opt-in and the
  read-tuning skill provides the measurement workflow.
- **Direct-path wiring is easy to miss** → add a test that an overridden mount
  uses the same stream count in `drive.ReadParallel` and VFS reads.
- **Internal-parallel drivers (Quark) silently ignore streams** → validation
  rejects `streams > 1` for them instead of accepting a no-op.
- **Limiter clamp surprises** → snapshot reports `clamped` and the effective
  value; validation still accepts the config because it is a safe upper bound.
- **Scope creep into CLI** → v1 is config-file only; `mount set` flags are a
  follow-up with no spec change.
- **Old configs** → absent table means no behavior change; no migration needed.

## Migration Plan

1. Add config fields and validation with no runtime effect.
2. Thread the resolved profile into VFS and direct read paths.
3. Add diagnostics and docs.
4. Rollback: remove the table from config; code can keep the resolver because
   nil override is a no-op.
