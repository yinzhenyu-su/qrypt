## Why

qrypt already declares an optional `OfflineDownloader` provider capability, but it is not connected to the task model or CLI. Users cannot submit, observe, cancel, or recover a cloud-side download task, and the destination mount is ambiguous when a configuration contains multiple drives.

The first implementation should target `115_open`, whose existing SDK and driver already expose creation, listing, status, and cancellation primitives. Establishing the task and CLI contract now also leaves room for other providers without coupling the command surface to 115-specific identifiers.

## What Changes

- Add a persistent `offline_download` task type under the stable `download` operation.
- Define creation, polling, cancellation, retry, and restart-recovery semantics for provider-owned offline tasks.
- Implement the `OfflineDownloader` capability for the `115_open` driver using its existing SDK.
- Add CLI commands for creating and inspecting offline downloads, with the destination expressed as a mount-qualified virtual path such as `/quark-test/downloads`.
- Resolve the first virtual-path component as the destination mount name, then resolve the remainder using that mount's driver and remote namespace.
- Reject cross-mount destinations in one offline task; cross-provider movement remains a separate transfer/copy operation.
- Keep provider task identifiers in task detail and map provider states into qrypt's canonical task state model.
- Preserve the existing `fs get` flow for downloading a completed remote file to local storage.

## Capabilities

### New Capabilities

- `offline-download`: Submit and manage provider-side offline download tasks through Core and the CLI.

### Modified Capabilities

- `task-api`: Add a durable provider-backed download task type while preserving stable operation semantics, idempotency, lifecycle, recovery, and terminal-state rules.

## Impact

- `pkg/task`: task type, descriptor, creation path, retry/recovery policy, and task request metadata.
- `pkg/core`: offline task creation and runner/recovery integration, mount-qualified destination resolution, and task control behavior.
- `pkg/drive`: complete the existing `OfflineDownloader` contract where needed for provider state and cancellation semantics.
- `pkg/drivers/p115open`: adapt the 115 Open SDK's offline task APIs and map its status model.
- `internal/cli`: add `offline add/list/show/cancel/retry` commands without exposing socket paths or provider-specific flags.
- `pkg/control` and task snapshots/events: expose the same task through existing query and control surfaces.
- Documentation, schemas, tests, and CI coverage for task persistence, idempotency, mount selection, and provider status mapping.
