## Why

qrypt's offline download framework currently has one provider implementation: Quark. Both 115
drivers (`115` cookie and `115_open` access-token) expose an offline download API, but neither
declares the capability, so users must leave qrypt to stage a magnet, HTTP, or FTP source into
115. Adding 115 closes the largest remaining provider gap without changing the provider-neutral
task model.

## What Changes

- Implement `drive.OfflineDownloader` for both `115` and `115_open`, using a shared internal
  adapter for the 115 task model: `info_hash` task identity, paginated task lookup, status
  mapping, result-tree resolution, cancellation, and download-URL acquisition.
- Declare `offline_download` and report accepted source kinds `magnet`, `http`, and `ftp`.
- Resolve staged result files into provider-neutral `drive.OfflineFile` entries with a POSIX
  `Path` relative to the parse destination, so `offline fetch` mirrors the resource folder the
  provider created.
- Implement `drive.OfflineFetcher` so a fetch still works when no explicit `--parse-to` is given
  and Core has no staging path to read through the VFS.
- Keep `offline inspect`, non-`all` selection, accelerated fetch, and provider-native retry out
  of scope. 115 has no inspect-without-creating-a-task API, `add_task_urls` cannot select files,
  and there is no accelerated session.
- Document 115's retry limitation: a failed task keeps its `info_hash`, so re-creating it may
  return `offline_duplicate` until the user removes the failed task from 115.
- Record the staged resource root from later observations, not only from the create response, so
  cleanup can remove a 115 resource folder after a fetch.
- Teach `p115open.Remove` to resolve a missing parent directory id, because Core cleanup removes
  staged entries by provider id without a parent.
- Update the user-facing offline guide and regenerate the driver capability matrix.

## Capabilities

### New Capabilities

<!-- none: this change extends the in-flight `offline-download` capability -->

### Modified Capabilities

- `offline-download`: add 115 and 115_open as offline providers, define their accepted source
  kinds and capability boundaries, and record the retry and cleanup behavior specific to 115.

## Impact

- `pkg/drivers/internal/p115offline`: new shared adapter for task lookup, pagination caching,
  status mapping, result-tree walking, path construction, cancellation, and download URLs.
- `pkg/drivers/p115`: offline backend on the cookie API (`lixian.115.com`), capability and
  source-kind reporting, provider error mapping.
- `pkg/drivers/p115open`: offline backend on the open platform API (`proapi.115.com/open`),
  full per-item add-task error handling, capability and source-kind reporting, and parent-id
  resolution in `Remove`.
- `pkg/core`: persist `ProviderRootID` when a provider reports the produced root after creation.
- `pkg/drivers`: capability contract test expectations for both 115 drivers.
- Documentation and tests: offline guide, generated support matrix, fake-provider/backend tests,
  and a real-account smoke procedure.

## Dependency

This change is stacked on the in-flight `add-offline-download` and `improve-offline-fetch`
changes: it modifies the `offline-download` capability those changes introduce. Archive
`add-offline-download` first, then `improve-offline-fetch`, then this change. Until then,
`openspec validate` may report expected stacking information.
