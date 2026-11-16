# read-profile-config Specification

## Purpose
Lets an operator opt a single mount into a measured read scheduling profile
without changing driver defaults or exposing per-read tuning knobs to every user.

## Requirements

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

### Requirement: 阶段建议与旧配置兼容

系统 SHALL 允许驱动独立建议 seek 前台窗口、连续读取前台与预取窗口、预取深度及文件总在途预算。未提供的建议 MUST 继承兼容默认，显式零预取深度 MUST 表示禁用，非法负值 MUST 被拒绝。bulk 覆盖 MUST 只作用于 bulk，不得放大 seek、interactive 或 probe；新阶段建议 MUST 不增加用户必填字段、不改变缓存配置或绕过能力门禁。有效诊断 MUST 展示阶段值及来源。

#### Scenario: bulk 覆盖不放大交互与 seek

- **WHEN** 挂载只配置 mounts.read.bulk 的 streams 与 window
- **THEN** 配置生效且只改变 bulk，seek 与 interactive 不被该覆盖放大

#### Scenario: FUSE 顺序升级保留阶段深度

- **WHEN** 未声明意图的 FUSE 连续读取升级到 bulk 流数，且连续阶段只声明了预取深度
- **THEN** 升级只借用 bulk 意图与流数，阶段的前台/预取窗口与显式预取深度仍然生效

#### Scenario: 显式禁用与继承不同

- **WHEN** 驱动显式声明预取深度为零
- **THEN** 对应策略不预取，而未声明的值继续继承

#### Scenario: 仅设置可选字段的覆盖生效

- **WHEN** 挂载覆盖只提供阶段建议、文件在途预算或预取深度
- **THEN** 该覆盖生效，未覆盖的字段继续继承驱动值，深度单独声明时同样约束预取窗口数量

#### Scenario: 非法建议被拒绝

- **WHEN** 驱动提供非法负值或超过约定上限的建议
- **THEN** 初始化返回可定位到驱动和字段的错误，不创建失控读取策略

#### Scenario: 驱动硬限制约束在途预算

- **WHEN** 驱动声明最大并行数为 1，而挂载配置了更大的文件在途预算
- **THEN** 有效在途预算被钳制为 1，并把来源标记为驱动上限

#### Scenario: 包装驱动保持建议

- **WHEN** 云盘通过加密包装提供读取
- **THEN** 有效阶段建议仍按包装能力与底层硬限制解析，且不改变密文校验或明文结果
