## Context

The repository already has a provider capability marker and an `OfflineDownloader` interface in `pkg/drive`, but no task creation path or CLI integration. The task model uses a stable operation plus an explicit task type descriptor, and the namespace model treats the first virtual-path component as a mount name. The first provider target is `115_open`; its existing SDK can create, list, and delete offline tasks, while the driver already resolves remote paths and downloads completed entries.

## Goals / Non-Goals

**Goals:**

- Add a durable provider-backed offline download task with restart-safe observation.
- Make the destination mount explicit through `/mount_name/remote_path`.
- Implement the first working provider adapter for `115_open`.
- Provide a discoverable CLI for submission, observation, cancellation, and retry.
- Preserve `fs get` as the local retrieval operation after the remote task completes.

**Non-Goals:**

- Do not make offline download a new `OperationKind`; it remains `download`.
- Do not implement cross-provider transfer as part of an offline task.
- Do not implement every provider in the first change.
- Do not expose provider-specific flags, socket paths, or provider task IDs as required CLI inputs.
- Do not delete completed provider task records as part of the qrypt task contract.

## Decisions

### Use a distinct task type under the download operation

Add `offline_download` as a task type with its own creation and recovery path. A cloud-side import has different persistence, polling, cancellation, and completion semantics from a local-stream download, so representing it as ordinary `download` would hide meaningful lifecycle differences. The operation remains `download` so callers do not need a new top-level operation category.

Alternatives considered:

- Reuse `download` with a strategy field: rejected because the runner and recovery semantics would be indistinguishable in task policy and snapshots.
- Add `offline_download` as a new operation: rejected because operation categories describe user intent, and both paths download content.

### Resolve destination as a mount-qualified virtual path

The canonical CLI form is `qrypt offline add URL --to /<mount_name>/<remote_path>`. The CLI extracts the first component, validates that it names a configured mount, and passes only the remaining remote path to the selected driver. An explicit `--mount NAME --to /remote/path` spelling can be supported for scripts; if both forms are present they must agree.

An offline task has one destination mount. A path that names another mount is not passed through to the provider. Cross-provider movement remains a separate `fs copy` or future transfer task.

### Keep provider identifiers in task detail

The qrypt task ID is the stable application identity. The provider task ID, source URI, provider status, remote destination ID, and observed error belong in task detail or provider-specific tracking fields. This avoids making the common task schema depend on 115's `info_hash` while still enabling restart recovery and diagnostics.

### Adapt provider state through the existing capability

The driver adapter implements the existing `OfflineDownloader` capability. It resolves the destination directory using the selected mount, calls the provider's create operation, polls by provider task ID, and requests provider cancellation. The Core runner owns canonical task transitions and terminal-state protection; the driver only returns normalized provider observations.

For `115_open`, the adapter maps the provider's queued, running, completed, and failed statuses. Since provider task lists may remove completed entries, completion is confirmed by resolving the destination entry when the provider task disappears. A missing task with no destination entry becomes an explicit unavailable or failed observation rather than an automatic success.

### Use durable polling and conservative retry

`offline_download` is persistent and recoverable. Core persists the mount, source, destination, provider ID, and current observation before or together with runner admission. Restart recovery resumes polling from that record. Cancellation first changes the qrypt task state to the cancellation path, then asks the provider to cancel; later provider observations cannot resurrect the task. Retry creates a new provider task under a protected execution generation and retains the previous provider ID in history/detail.

### CLI surface

The initial command group is:

```text
qrypt offline add URL --to /mount/path [--idempotency-key KEY] [--json]
qrypt offline list [--mount NAME] [--state STATE] [--json]
qrypt offline show TASK_ID [--mount NAME] [--json]
qrypt offline cancel TASK_ID [--mount NAME] [--json]
qrypt offline retry TASK_ID [--mount NAME] [--json]
```

`--mount` on inspection/control commands is an optional safety filter; the task record remains authoritative. No command accepts a socket path or driver name. `--wait` and continuous watch output are deferred until the basic lifecycle is proven.

## Risks / Trade-offs

- [Provider task list is eventually consistent] → Poll with bounded backoff, preserve the provider ID, and confirm completion by resolving the destination entry.
- [Provider removes completed tasks] → Treat destination-entry visibility as the completion fallback and retain the last provider observation in task detail.
- [Duplicate provider submission after a lost response] → Persist and enforce qrypt idempotency before provider admission; use provider IDs and operation fingerprints to reconcile ambiguous responses where possible.
- [Remote destination path is renamed or removed while polling] → Report a readable failed/unavailable state and do not silently redirect to another mount.
- [115-specific status or identifiers leak into the common API] → Keep the common contract normalized and isolate provider fields in detail/tracking data.
- [A provider lacks offline capability] → Fail before remote task creation with a capability error; do not fall back to a local download implicitly.

## Migration Plan

1. Add the capability/task/CLI contracts and tests without changing existing upload/download behavior.
2. Implement and validate the `115_open` adapter against a controlled fake provider and a real test account with a small HTTP or magnet source.
3. Enable the CLI only when the selected mount advertises offline capability; existing commands remain unchanged.
4. Validate restart, cancel, retry, and terminal-observation behavior before documenting the feature as supported.

Rollback is deleting or disabling the new offline creation path; existing task records must remain readable, and unsupported `offline_download` records must surface as recoverable/unavailable rather than being silently discarded.
