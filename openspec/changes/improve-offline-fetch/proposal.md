## Why

`qrypt offline fetch` currently writes every staged file flat into `--download-to` by file name, so a
multi-file torrent loses its folder structure and two files that share a name silently overwrite each
other (`openLocalWriter` truncates). The accelerated path can also transfer only the single provider
`fid` returned at submit time, and the command blocks the terminal with no progress output, so a
normal long download looks like a hang.

## What Changes

- **Mirror the staged layout locally**: each fetched file lands at `<download-to>/<staged relative
  path>`, recreating the provider's folder(s) instead of flattening to file names.
- Add a provider-neutral **staged relative path** to offline result files; providers that can locate
  their staged entries MUST report it, and Core only consumes it.
- Add a provider-neutral **results-available** signal so an accelerated fetch can resolve the full
  layout and start transferring before the provider task reaches a terminal state, without ever
  transferring a partial result set.
- **Harden destination writing**: reject unsafe paths (absolute, `..`, empty, control characters,
  Windows-reserved), refuse a path component that escapes through an existing symlink, and fail the
  task on an in-task destination collision instead of truncating. Add a stable `offline_path_invalid`
  error code.
- **BREAKING**: `offline fetch` no longer blocks by default. It submits, waits for the first provider
  observation, prints the task snapshot with a pointer to observe later, and returns while Core keeps
  transferring. Add `--wait` to block until the final destination is written; blocking forms (both
  `fetch --wait` and `offline wait`) render progress.
- Keep the existing cloud-copy cleanup and `--keep-cloud` behavior unchanged.

## Capabilities

### New Capabilities

<!-- none: this change modifies capabilities introduced by the in-flight `add-offline-download` change -->

### Modified Capabilities

- `offline-download`: fetch destination layout (mirror the staged tree), the provider
  results-available signal, the default non-blocking fetch with `--wait` and progress, and the
  `offline_path_invalid` error code.
- `task-api`: offline result items and the retained snapshot carry the staged relative path while
  older snapshots without one stay readable.

## Impact

- `pkg/drive`: `OfflineFile.Path`, an `OfflineDownload` availability signal, and the
  `offline_path_invalid` error code.
- `pkg/drivers/quark`: resolve staged relative paths from the provider's parent-directory identity,
  and report availability from `download_task_finish`/terminal state.
- `pkg/core`: fetch target resolution and recursive destination creation, staging/reconcile/tracking
  paths, path sanitization and symlink-escape checks, and the accelerated transfer start condition.
- `internal/cli`: `fetch` default (non-blocking) and `--wait`, a shared progress renderer for
  `fetch --wait` and `offline wait`, and `--no-wait` deprecation.
- Docs and tests: `docs/for-user/cli.md`, `docs/for-developer/quality-gates.md`, offline
  spec/design, and provider/CLI test coverage.

## Dependency

This change is **stacked** on the in-flight `add-offline-download` change: its `offline-download`
and `task-api` requirements are the base this change modifies. Archive `add-offline-download` first,
then archive this change. If the base is archived first, the `MODIFIED` deltas here apply cleanly.
