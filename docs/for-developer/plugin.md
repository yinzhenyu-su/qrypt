# VFS Policy Plugins

qrypt exposes a small VFS policy surface for code that needs to observe or
deny VFS operations without owning VFS internals.

The core rule is:

> VFS owns state transitions. Policies provide decisions and observation.

Policies must not mutate VFS internal state. They receive stable
`drive.MetricEvent` values and return ordinary Go errors when they need to deny
an operation.

## Supported Policies

Policies are ordinary Go values. A value may implement one or more small
interfaces. VFS collects the capabilities once in `vfs.New`.

### OperationHook

```go
type OperationHook interface {
	BeforeOperation(ctx context.Context, event drive.MetricEvent) (context.Context, error)
	AfterOperation(ctx context.Context, event drive.MetricEvent)
}
```

`AfterOperation` is an event observer, not strictly a paired callback. It may
be called for events that did not pass through the same hook's
`BeforeOperation`, such as completed read events and upload phase events routed
through VFS metrics dispatch.

### PathPolicy

```go
type PathPolicy interface {
	IsReadOnlyPath(path string) bool
	IgnorePath(path string) (ignore bool, reason string)
}
```

`IsReadOnlyPath` denies mutating operations with `vfs.ErrReadOnly`.
`IgnorePath` hides the path from `Stat`, `List`, and `Read`, and makes
mutating operations on that path a no-op. The returned `reason` is a
low-cardinality diagnostic label such as `apple_metadata` or `temporary`.

### UploadPolicy

```go
type UploadPolicy interface {
	UploadDelay(ctx context.Context, req UploadDecision) time.Duration
	UploadWorkers(defaultWorkers int) int
	ShouldIgnoreUpload(ctx context.Context, req UploadDecision) (ignore bool, reason string)
}
```

Upload delays are merged by taking the largest positive duration. Worker
policies run in configured order and the final count is clamped to at least 1.
Ignored uploads remove the pending staging entry instead of retrying.

### DeletePolicy

```go
type DeletePolicy interface {
	DeleteDelay(ctx context.Context, req DeleteDecision) time.Duration
}
```

Delete delays are merged by taking the largest positive duration.

### ReadPolicy

```go
type ReadPolicy interface {
	ShouldUseReadCache(ctx context.Context, req ReadDecision) bool
	Prefetch(ctx context.Context, req ReadDecision) PrefetchDecision
}
```

If any read policy returns `false` from `ShouldUseReadCache`, VFS reads the
requested range directly from the driver and does not seed the read cache.
If any prefetch decision has `Enabled=false`, prefetch is disabled and chunk
counts are ignored. Otherwise VFS uses the largest requested before/after chunk
counts, with the normal VFS defaults as the baseline.

Attach policies through `vfs.Options`:

```go
fs, err := vfs.New(driver, vfs.Options{
	CacheDir: "/tmp/qrypt-cache",
	Policies: []vfs.Policy{
		metricsPolicy,
		denyPolicy,
	},
})
```

If `Policies` is empty, VFS behaves as before.

## Ordering

Policies run in the order configured.

- `BeforeOperation` runs from first to last.
- `AfterOperation` runs from last to first.
- If a `BeforeOperation` returns an error, later `BeforeOperation` hooks are
  skipped.
- `AfterOperation` still runs for hooks whose `BeforeOperation` already ran.
- `IgnorePath` and `ShouldIgnoreUpload` use first-match-wins.
- delay policies use max-delay-wins.

Place mandatory audit or metrics policies before policies that may deny an
operation.

## Error Contract

A denied operation is wrapped as a VFS policy denial and marked non-retryable
for writeback:

```go
errors.Is(err, vfs.ErrPolicyDenied)
drive.IsNonRetryable(err)
```

The shared error category for policy denials is `invalid_request`.

Policy implementations can return a plain error from `BeforeOperation`; VFS
will convert it into a non-retryable `vfs.PolicyError`.

## Concurrency

Policy methods may be called concurrently by:

- concurrent FUSE requests
- upload workers
- delete timers
- read prefetch goroutines
- debug and benchmark probes

Stateful policies must protect their own state with `sync.Mutex`, atomics,
channels, or another concurrency-safe mechanism. VFS does not serialize policy
calls behind a global lock.

Example:

```go
type CountingPolicy struct {
	mu     sync.Mutex
	counts map[string]int
}

func (p *CountingPolicy) BeforeOperation(ctx context.Context, event drive.MetricEvent) (context.Context, error) {
	return ctx, nil
}

func (p *CountingPolicy) AfterOperation(_ context.Context, event drive.MetricEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.counts == nil {
		p.counts = map[string]int{}
	}
	p.counts[event.Operation]++
}
```

Policy methods should return promptly. Avoid long network calls in foreground
operation paths.

## Events

VFS routes existing logical events through operation hooks instead of creating a
second metric stream.

- ordinary operations emit `kind=vfs_operation`, `phase=operation`
- reads emit the existing `kind=vfs_read` event
- upload phases emit the existing `kind=vfs_upload` events

This avoids double-counting in debug snapshots and benchmark reports.

## Not Plugin API

The following remain internal implementation details:

- `*vfs.VFS`
- cache internals
- staging files
- pending maps
- upload timers
- delete timers
- list caches
- direct driver mutation

Policy request and decision structs are part of the stable plugin-facing
surface. VFS internals remain private.
