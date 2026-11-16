## ADDED Requirements

### Requirement: Direct reads keep the mounted-read shape

Reads outside the FUSE window path — a whole-object copy and the share fetch
fallback — MUST use the same effective profile as a mounted read: the fixed part
size and the split threshold decide the part layout, and the per-file in-flight
budget caps how many parts run at once. A profile that only divides evenly MUST
keep the historical bounded part size, so a whole-file stream is never cut into
unbounded parts.

#### Scenario: Whole-object copy uses the mount part size

- **WHEN** a mount sets `part_chunks = 1` and an in-flight budget of one
- **THEN** a whole-object copy issues one 512 KiB Range at a time

#### Scenario: Split threshold applies to direct reads

- **WHEN** a mount sets `min_split_chunks = 16` and the request is 4 MiB
- **THEN** the direct read uses a single Range

## MODIFIED Requirements

### Requirement: Per-mount read profile override

A mount MAY declare an optional `[mounts.read_profile]` table with `streams`,
`window_chunks`, `part_chunks`, and `min_split_chunks`. When every field is
unset, the mount MUST use the driver's `ReadProfile` exactly as before. When one
field is set, the other fields MUST continue to inherit the driver's value. An
override MUST affect only its own mount.

`part_chunks` sets the size of one parallel sub-range; an explicit zero divides
a window evenly across the granted streams. `min_split_chunks` is the smallest
window, in 512 KiB chunks, that may be split; an explicit zero allows splitting
any window wider than one chunk. When both are set, a window MUST be cut into
parts of that size, the part count MUST NOT exceed the granted streams, and the
final part MUST absorb any remainder so the window stays fully covered.

#### Scenario: No override keeps driver defaults

- **WHEN** a mount has no `read_profile` table
- **THEN** its effective stream count, window, prefetch depth, threshold, part
  size, and split threshold are the driver's `ReadProfile` values

#### Scenario: Partial override inherits the rest

- **WHEN** a mount sets only `part_chunks`
- **THEN** the effective part size is the configured value and the window,
  streams, prefetch depth, and threshold still come from the driver profile

#### Scenario: Override is isolated to one mount

- **WHEN** two mounts use the same driver and only one declares an override
- **THEN** the other mount keeps the driver's default profile

#### Scenario: Fixed part size keeps the measured shape

- **WHEN** a mount sets `streams = 3`, `part_chunks = 16`, and the effective
  window is 48 chunks
- **THEN** the window is fetched as three 16-chunk sub-ranges

#### Scenario: Split threshold keeps small windows single-stream

- **WHEN** a mount sets `min_split_chunks = 8` and a window is 4 chunks
- **THEN** that window is fetched as one sub-range even though streams allows
  more

#### Scenario: Explicit zero clears a driver value

- **WHEN** a mount sets `part_chunks = 0` or `min_split_chunks = 0` while the
  driver profile fixes that value
- **THEN** the effective value is zero (even division or no threshold) instead
  of the driver's fixed value

#### Scenario: Part size larger than the window stays single-stream

- **WHEN** a mount sets `part_chunks = 16` and a window is 8 chunks
- **THEN** the window is fetched as one sub-range

### Requirement: Validation and error reporting

`streams` MUST accept values from 1 through 32. `window_chunks`,
`part_chunks`, and `min_split_chunks` MUST accept values from 1 through 64. An
omitted field MUST inherit the driver profile. An explicit zero is a value, not
an omission: zero `part_chunks` means an even division and zero
`min_split_chunks` means no threshold, even when the driver fixed that value.
Out-of-range values MUST fail mount validation with an error that names the
mount, field, and accepted range.

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

#### Scenario: Out-of-range part size rejected

- **WHEN** a mount sets `part_chunks = 65`
- **THEN** configuration validation fails with a range error naming
  `read_profile.part_chunks`

### Requirement: Effective profile observability

The debug snapshot MUST expose each mount's effective read profile, including
the stream count, window chunks, prefetch depth, parallel threshold, part size,
split threshold, and whether the value came from the driver, a mount override,
or a driver limiter clamp. Secrets and unrelated configuration MUST NOT be
included.

#### Scenario: Snapshot shows override source

- **WHEN** a mount configures `streams = 4`
- **THEN** its debug snapshot reports the effective stream count and marks the
  source as the mount override

#### Scenario: Snapshot shows split policy

- **WHEN** a mount configures `part_chunks = 16`
- **THEN** its debug snapshot reports the effective part size

#### Scenario: Snapshot shows driver default

- **WHEN** a mount has no override
- **THEN** its debug snapshot reports the driver's effective value and marks the
  source as the driver
