## MODIFIED Requirements

### Requirement: Per-mount read profile override

A mount MAY declare mounts.read with max_in_flight and a bulk table containing streams, window, part_size, and min_parallel_size. Omitted values MUST inherit the driver's complete profile. Explicit values MUST override only the named mount; bulk values MUST NOT enlarge interactive or probe reads. Sizes MUST use bytes or supported KiB/MiB units rather than VFS chunk counts. No legacy table aliases SHALL be introduced.

#### Scenario: No override keeps driver defaults

- **WHEN** a mount omits mounts.read
- **THEN** it uses the driver's complete default policy subject to hard limits

#### Scenario: Partial override inherits the rest

- **WHEN** only bulk.part_size is provided
- **THEN** other fields inherit while the explicit part size applies to bulk execution

#### Scenario: Override is isolated to one mount

- **WHEN** only one of two independent mount sources declares an override
- **THEN** the other source retains its own resolved defaults

### Requirement: Precedence and capability gating

The system MUST resolve mount overrides over driver defaults over built-in defaults, then apply driver hard limits and intent gates. Configured parallel streams MUST be rejected for unsupported or internally parallel drivers. File request budgets and actual stream counts MUST NOT exceed hard limits. Recommended single-stream execution MUST NOT by itself restrict a source to one total request.

#### Scenario: Parallel override requires parallel range

- **WHEN** a mount requests multiple streams without parallel_range
- **THEN** validation fails with a field-specific error before starting the mount

#### Scenario: Internal parallel driver rejects extra streams

- **WHEN** the driver already parallelizes internally and the override requests extra streams
- **THEN** validation fails rather than stacking another parallel layer

#### Scenario: Driver limiter caps the override

- **WHEN** the requested file budget and streams exceed the driver hard limit
- **THEN** both effective values are clamped and their diagnostic reasons identify the limit

### Requirement: Override scope and intent

Bulk overrides MUST apply consistently to mounted bulk windows and direct copy windows of the same source. Interactive and probe MUST remain single-stream per window; probe MUST have no prefetch. max_in_flight MUST bound every scheduled request on the source object, including probe and prefetch, without changing cache admission.

#### Scenario: Bulk reads use the override

- **WHEN** mounted and direct bulk reads use equal windows and resolved overrides
- **THEN** their logical partition shapes and effective concurrency rules match

#### Scenario: Interactive reads stay single-stream

- **WHEN** interactive reads occur on a mount with multiple bulk streams
- **THEN** each interactive window remains single-stream

#### Scenario: Probe reads stay single-stream

- **WHEN** probe reads occur
- **THEN** they remain single-stream with no prefetch and still acquire the source budget

#### Scenario: Direct read path uses the same profile

- **WHEN** a copy or share-fetch fallback reads through a mounted source
- **THEN** it uses that source's effective policy and shared budget rather than creating an independent one

### Requirement: Validation and error reporting

streams and max_in_flight MUST accept 1..32; window and fixed part_size MUST be positive and at most 32 MiB; min_parallel_size MUST accept zero through 32 MiB. part_size MUST accept auto for even division. Omission MUST mean inheritance; explicit zero MUST be rejected except for min_parallel_size. Legacy mounts.read_profile or mixed old/new tables MUST fail before mounting with migration guidance. Invalid driver profiles MUST fail initialization with driver and field context.

#### Scenario: Out-of-range streams rejected

- **WHEN** streams is 33
- **THEN** initialization fails with the mount, field and accepted range

#### Scenario: Zero means inherit

- **WHEN** an operator migrates the legacy zero-as-inherit streams value by omitting streams in mounts.read.bulk
- **THEN** the driver's stream recommendation is inherited; explicit zero in the new table is rejected

#### Scenario: Legacy table has migration guidance

- **WHEN** legacy streams = 0 is encountered inside mounts.read_profile
- **THEN** the legacy table is rejected with instructions to omit streams in the new table to inherit

#### Scenario: Out-of-range window rejected

- **WHEN** bulk.window exceeds 32 MiB
- **THEN** validation fails before starting requests

#### Scenario: Unset fields inherit

- **WHEN** only bulk.window is set
- **THEN** other fields inherit their complete defaults

#### Scenario: Explicit auto clears fixed splitting

- **WHEN** bulk.part_size is auto over a fixed driver default
- **THEN** effective splitting uses even division without retaining the fixed size

### Requirement: Effective profile observability

Diagnostics MUST expose read_policy with source scope, driver recommendations, hard limits, resolved effective values and field-level source/clamp reasons. Per-read events MUST distinguish the plan from actual grants, and expose stage, foreground/prefetch sizes and depth, queue/open/body/local-wait/validation/delivery timing, retry and cancellation reasons. Unmeasured CDN TTFB MUST be absent or null; driver-open time MUST NOT impersonate TTFB or player first-frame time. Secrets MUST NOT be exposed. Legacy JSON fields are not preserved.

#### Scenario: Snapshot shows override source

- **WHEN** the mount overrides only part size
- **THEN** that field shows override provenance and unrelated fields show their real origins

#### Scenario: Snapshot shows driver default

- **WHEN** no override is configured
- **THEN** diagnostics distinguish driver defaults from hard-limit clamping

#### Scenario: Planned streams differ from grant

- **WHEN** scheduling grants fewer streams than recommended
- **THEN** diagnostics expose both values and the resulting actual shape

### Requirement: Conservative defaults stay unchanged

No global read configuration or read_cache fields SHALL be introduced. Drivers without measured tuning MUST keep their accepted defaults, including Koofr and Drime single-stream behavior. All built-in drivers MUST use the shared policy path; adopting the new interface MUST NOT be presented as evidence of speed improvement. Changing a driver's tuning default MUST require repeatable performance validation.

#### Scenario: Koofr default remains single-stream

- **WHEN** Koofr has no override or new validated tuning
- **THEN** bulk windows remain single-stream

#### Scenario: No global override

- **WHEN** an operator needs different policies on multiple mounts
- **THEN** policies are explicitly configured per mount, with no global read table

#### Scenario: Missing real-cloud evidence

- **WHEN** a driver passes deterministic tests but has not been measured with a real account
- **THEN** it is reported as contract-tested and unmeasured rather than performance-improved
