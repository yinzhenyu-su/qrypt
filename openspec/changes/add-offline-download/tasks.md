## 1. Task and provider contracts

- [ ] 1.1 Add `offline_download` as a declared `download` task type with explicit persistence, visibility, recovery, retry, and creation-path policy; verify descriptor and task-policy tests cover the new type.
- [ ] 1.2 Refine the `OfflineDownloader` request/result contract so source, mount-relative destination, provider task ID, remote identity, provider state, and provider error can survive persistence; verify serialization and unsupported-capability tests.
- [ ] 1.3 Extend operation-level request validation and mapping for offline download selection without adding a new `OperationKind`; verify invalid strategy, empty source, and missing destination cases are rejected before task creation.

## 2. Core offline task lifecycle

- [ ] 2.1 Implement offline task creation for one selected mount, including mount-qualified path parsing, remote destination resolution, capability checking, and idempotency; verify unknown mounts, cross-mount paths, unsupported drivers, and duplicate idempotency keys create no duplicate provider task.
- [ ] 2.2 Implement the provider-backed runner that submits the source, persists the provider identifier before polling, maps provider observations to canonical task states, and records provider detail without changing the common task schema; verify queued, running, success, failure, and unavailable observations.
- [ ] 2.3 Implement restart recovery, cancellation, and retry for `offline_download`; verify recovery resumes from the persisted mount/provider ID, cancellation cannot be resurrected by a later remote observation, and retry does not run two active generations.
- [ ] 2.4 Implement completion reconciliation against the destination entry when the provider removes completed tasks; verify a visible destination succeeds, while a missing task with no destination entry becomes an explicit failure or unavailable result.

## 3. 115 Open provider implementation

- [ ] 3.1 Implement `OfflineDownloader` for `pkg/drivers/p115open` using the existing SDK, resolving the mount-relative destination path to the 115 folder ID; verify the adapter sends the correct source and destination ID to a fake SDK/HTTP server.
- [ ] 3.2 Map 115 queued, running, completed, and failed statuses and implement provider-task cancellation by its returned identifier; verify status mapping, provider error propagation, and cancellation requests.
- [ ] 3.3 Register `CapabilityOfflineDownload` only for the completed 115 Open implementation and preserve existing token refresh, rate limiting, and driver state behavior; verify the existing p115open driver test suite remains green.

## 4. CLI and control surface

- [ ] 4.1 Add `qrypt offline add URL --to /MOUNT/PATH` with optional explicit `--mount NAME --to /PATH` compatibility, `--json`, and idempotency-key support; verify mount extraction, conflicting selectors, usage errors, and structured task output.
- [ ] 4.2 Add `offline list`, `offline show`, `offline cancel`, and `offline retry`, reusing the Core-owned control endpoint without exposing socket paths, driver names, or provider task IDs as required inputs; verify command routing and JSON/human output.
- [ ] 4.3 Keep local retrieval in `fs get` and document the two-stage flow from cloud-side offline download to local file download; verify a completed offline task's remote destination can be passed to `fs get`.

## 5. Task API, events, and documentation

- [ ] 5.1 Expose offline tasks through existing task snapshots, filters, events, cancel, retry, and persistence APIs; verify task queries and event streams report the same canonical state and provider detail.
- [ ] 5.2 Update CLI, task, driver capability, and provider support documentation with mount-qualified destination semantics and the no-cross-provider rule; verify generated documentation checks remain synchronized.
- [ ] 5.3 Add schema or API contract coverage for `offline_download`, mount, destination, provider identifier, and provider observation fields; verify old task records and existing mobile/task clients remain readable.

## 6. End-to-end validation

- [ ] 6.1 Add a deterministic fake-provider integration test covering submit, poll, cancel, retry, restart recovery, idempotency, and terminal-state protection; verify it does not require external credentials.
- [ ] 6.2 Run a real 115 Open smoke test with a small HTTP or magnet source, confirming the task appears in the selected remote directory and can subsequently be downloaded with `fs get`; record the provider-account prerequisite separately from automated CI.
- [ ] 6.3 Run `./scripts/ci-check.sh` and the Windows compile/test gates, then verify `git diff --check` and the final task/API documentation before implementation is considered complete.
