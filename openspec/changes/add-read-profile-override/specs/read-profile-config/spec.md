## Purpose

Lets an operator opt a single mount into a measured read scheduling profile
without changing driver defaults or exposing per-read tuning knobs to every user.

## ADDED Requirements

### Requirement: Per-mount read profile override

A mount MAY declare an optional `[mounts.read_profile]` table with `streams`
and `window_chunks`. When both fields are unset, the mount MUST use the driver's
`ReadProfile` exactly as before. When one field is set, the other field MUST
continue to inherit the driver's value. An override MUST affect only its own
mount.

#### Scenario: No override keeps driver defaults

- **WHEN** a mount has no `read_profile` table
- **THEN** its effective stream count, window, prefetch depth, and threshold
  are the driver's `ReadProfile` values

#### Scenario: Partial override inherits the rest

- **WHEN** a mount sets only `streams`
- **THEN** the effective stream count is the configured value and the window,
  prefetch depth, and threshold still come from the driver profile

#### Scenario: Override is isolated to one mount

- **WHEN** two mounts use the same driver and only one declares an override
- **THEN** the other mount keeps the driver's default profile

### Requirement: Precedence and capability gating

The effective profile MUST be resolved in this order: mount override, driver
`ReadProfile`, built-in defaults. A configured `streams` value greater than 1
MUST be rejected unless the driver declares `CapabilityParallelRange`. It MUST
also be rejected for drivers that declare `CapabilityInternalParallelRead`,
because those drivers already parallelize inside `Read`. When the driver
implements `ParallelReadLimiter`, the effective stream count MUST NOT exceed
its limit.

#### Scenario: Parallel override requires parallel range

- **WHEN** a mount configures `streams = 4` for a driver without
  `CapabilityParallelRange`
- **THEN** configuration validation fails with an actionable error naming the
  mount and the unsupported capability

#### Scenario: Internal parallel driver rejects extra streams

- **WHEN** a mount configures `streams = 4` for a driver with
  `CapabilityInternalParallelRead`
- **THEN** configuration validation fails instead of silently ignoring the
  override

#### Scenario: Driver limiter caps the override

- **WHEN** a mount configures `streams = 8` and the driver's limiter reports 4
- **THEN** the effective stream count is 4 and the debug snapshot reports that
  the value was clamped

### Requirement: Override scope and intent

The override MUST affect `bulk` reads only. `interactive` and `probe` reads MUST
remain single-stream and keep their existing window behavior. The effective
profile MUST apply to VFS reads for the mount and to the direct read/copy path
used by `fs get`, direct copy, and share-fetch fallback for the same mount.

#### Scenario: Bulk reads use the override

- **WHEN** a mount sets `streams = 4` and `window_chunks = 16` and a caller
  performs a `bulk` read above the parallel threshold
- **THEN** the bulk read uses at most 4 concurrent sub-ranges over an 8 MiB
  window

#### Scenario: Interactive reads stay single-stream

- **WHEN** the same mount performs an `interactive` read
- **THEN** the read uses one stream and the interactive window, regardless of
  the bulk override

#### Scenario: Probe reads stay single-stream

- **WHEN** the same mount performs a `probe` read
- **THEN** the read uses one stream, one window chunk, and no prefetch

#### Scenario: Direct read path uses the same profile

- **WHEN** a direct copy or share-fetch fallback reads a large file from the
  overridden mount
- **THEN** it uses the same effective stream count as the mount's bulk reads

### Requirement: Validation and error reporting

`streams` MUST accept values from 1 through 32. `window_chunks` MUST accept
values from 1 through 64. Zero or an omitted field MUST mean "inherit the driver
profile". Out-of-range values MUST fail mount validation with an error that
names the mount, field, and accepted range.

#### Scenario: Out-of-range streams rejected

- **WHEN** a mount sets `streams = 33`
- **THEN** configuration validation fails with a range error

#### Scenario: Zero means inherit

- **WHEN** a mount sets `streams = 0` explicitly
- **THEN** validation succeeds and streams comes from the driver profile

#### Scenario: Out-of-range window rejected

- **WHEN** a mount sets `window_chunks = 65`
- **THEN** configuration validation fails with a range error

#### Scenario: Unset fields inherit

- **WHEN** a mount omits `streams` but sets `window_chunks`
- **THEN** validation succeeds and streams comes from the driver profile

### Requirement: Effective profile observability

The debug snapshot MUST expose each mount's effective read profile, including
the stream count, window chunks, prefetch depth, parallel threshold, and
whether the value came from the driver, a mount override, or a driver limiter
clamp. Secrets and unrelated configuration MUST NOT be included.

#### Scenario: Snapshot shows override source

- **WHEN** a mount configures `streams = 4`
- **THEN** its debug snapshot reports the effective stream count and marks the
  source as the mount override

#### Scenario: Snapshot shows driver default

- **WHEN** a mount has no override
- **THEN** its debug snapshot reports the driver's effective value and marks the
  source as the driver

### Requirement: Conservative defaults stay unchanged

No global read-profile configuration MUST be introduced. A mount without
`read_profile` MUST keep the driver's default, including the single-stream
defaults for Koofr and Drime. The override MUST NOT change `read_cache`
semantics or add fields to `read_cache`.

#### Scenario: Koofr default remains single-stream

- **WHEN** a Koofr mount has no `read_profile`
- **THEN** its bulk reads use one stream

#### Scenario: No global override

- **WHEN** an operator wants a different default for every mount
- **THEN** they must configure each mount explicitly; no top-level read-profile
  setting exists
