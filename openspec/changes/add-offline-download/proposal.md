## Why

qrypt already declares an optional `OfflineDownloader` provider capability, but it is not
connected to the task model or CLI. Users cannot submit, inspect, observe, cancel, or recover
a provider-side download, and cannot bring a provider-side source down to a local or encrypted
destination. Quark is the first target because its flow has been reverse-engineered and
verified end to end, including its request encryption, task state model, and the confirmed
fact that every magnet download must first land in the provider's cloud storage before any
download URL (accelerated or not) can be obtained.

## What Changes

- Add `offline_download` (cloud-side) and `offline_fetch` (cloud staging plus local transfer)
  task types under the stable `download` operation.
- Define creation, first-observation, polling, completion, cancellation, retry, and
  restart-recovery semantics for provider-owned offline tasks.
- Implement `OfflineDownloader` and the optional accelerated-fetch capability for the Quark
  driver, including its request encryption and download-URL acquisition.
- Introduce a provider-neutral offline CLI (`qrypt offline add|inspect|fetch|list|show|cancel|retry|wait`)
  that requires a running Core and never exposes socket paths, driver names, or provider task
  identifiers as required inputs.
- Split destinations: `--parse-to` names the provider-side staging location (optional,
  defaults to the provider's magnet/system directory, same provider, unencrypted) and
  `--download-to` names the final writable path (namespace, FUSE, or local; may be encrypted).
- Reject an offline fetch whose parse and download destinations resolve to the same location;
  warn when the download destination is a different directory on the same provider.
- `offline fetch` blocks until the destination is written by default, with `--no-wait`
  returning once staging is admitted, and removes the cloud copy it created after a successful
  transfer unless `--keep-cloud` is given.
- Accept a provider-neutral source surface: magnet links, HTTP(S) URLs, and local `.torrent`
  files normalized to magnet links; reject share links and unsupported schemes.
- Support provider-side file selection through neutral selectors
  (`--select all|largest[:N]`, `--include/--exclude GLOB`); providers without selection support
  reject selectors other than `all`.
- De-duplicate repeated submissions of the same active source, parse destination, and selection
  unless `--new` is given, while keeping an explicit idempotency key for scripts.
- Keep provider identifiers and status in task detail and map provider states into the canonical
  qrypt task state model.
- Preserve `fs get` for retrieving a completed remote entry into local storage.

## Capabilities

### New Capabilities

- `offline-download`: submit, inspect, observe, control, fetch, and wait for provider-side offline download tasks through Core and the CLI.

### Modified Capabilities

- `task-api`: add durable provider-backed offline download and offline fetch task types with provider detail, staging destination, and final destination while preserving the stable operation, idempotency, lifecycle, recovery, and terminal-state contracts.

## Impact

- `pkg/task`: `offline_download` and `offline_fetch` type/descriptor/creation paths, retry and recovery policy, and request metadata.
- `pkg/core`: offline creation with destination rules and de-duplication, the provider-backed runner, the fetch staging and transfer phases, writer selection, download-URL refresh, recovery, inspect, and wait.
- `pkg/drive`: refined `OfflineDownloader` request/result contract, offline capability flags, source-kind reporting, the accelerated-fetch contract, and offline error classification.
- `pkg/drivers/quark`: Quark offline adapter (request encryption, parse/submit, task query, cancel, retry, speedup session, download-URL acquisition).
- `pkg/control`: offline create/list/show/cancel/retry routes, including fetch creation with a final destination.
- `internal/cli`: `offline` command group including `fetch`, `.torrent` normalization, and same-location rejection.
- Documentation, schemas, tests, and CI coverage for destinations, selection, error mapping, and provider status mapping.
