## Why

qrypt can already move a user's own files in and out of a cloud drive, and it can hand a remote
source to a provider-side offline download. Two everyday operations are still missing: handing out
a link to content that is already in the drive, and taking someone else's link into the drive
(转存) or straight down to local storage.

The Quark share flow has been reverse-engineered and verified live end to end
(`create → token → detail → save → task`, plus the direct-download path), so the provider half is
no longer the unknown. What is missing is the provider-neutral contract, the task model, the
control surface, and the CLI that make the flow usable without naming a provider.

## What Changes

- Add a provider-neutral **share-link** surface: `qrypt share create|list|show|update|remove`.
  Link management is synchronous — a link is an immediate result, not durable work — and a
  provider's asynchronous create task is polled inside the driver under a bounded timeout.
- Add a provider-neutral **receive** surface:
  `qrypt receive inspect|save|fetch|list|show|cancel|retry|wait`. `inspect` is read-only; `save`
  copies share content provider-side into the user's drive; `fetch` either performs that save and
  then downloads the result, or — when no cloud destination is given — downloads directly from the
  share without saving.
- Add two durable task types: `share_save` under a new stable `share` operation, and `share_fetch`
  under the existing `download` operation. Both are persistent, recoverable, listed through the
  task API, canceled and retried like other user tasks, and retried by re-running their runner from
  persisted detail.
- Split the drive-layer share capability into link management (`share`), link opening and browsing
  (`share_receive`), provider-side save (`share_save`), and direct download (`share_fetch`), and add
  a link-parser interface so the CLI never requires a provider, driver, or socket name.
- Treat the share link as the source identity: the link's provider and the mount that acts on it
  must agree. v1 is **same-provider only**; a cross-provider destination is rejected with an
  actionable error instead of silently downloading and re-uploading.
- Support provider-neutral selection (`--select all|largest[:N]`, `--include/--exclude GLOB`) and
  transfer tuning (`--workers`, `--chunk-size`) with the same bounds as offline fetch.
- Extract the offline fetch transfer core (bounded-range chunked download, resume, writer
  selection, progress accounting) into a shared core so share fetch and offline fetch cannot drift.
- Implement the Quark adapter for links (`POST /share`, `GET /share/mypage/detail`,
  `POST /share/password`, `POST /share/sharepage/token`, `GET /share/sharepage/detail`,
  `POST /share/sharepage/save`, `GET /task`, and the share form of `POST /file/download`) and map
  its provider error codes onto neutral `share_*` codes.
- Keep passcodes and provider session tokens out of command output by default and out of logs
  entirely.

## Capabilities

### New Capabilities

- `share-links`: create, list, show, update, and remove share links for content that already lives
  in a mounted drive, with expiry, passcode, and visit-limit policy, through Core and the CLI.
- `share-receive`: inspect a share link without changing anything, save selected share content into
  the user's drive, and download it locally with or without saving, as durable, recoverable,
  observable tasks.

### Modified Capabilities

- `task-api`: add durable `share_save` and `share_fetch` task types with a stable operation
  identity, persisted share detail, result items, recovery, and retry, while preserving the
  existing snapshot, event, idempotency, and terminal-state contracts.

## Impact

- `pkg/drive`: share capability names, the link/open/browse/save/fetch interfaces, provider-neutral
  share types, and share error classification.
- `pkg/task`: `OperationShare`, `TypeShareSave`, `TypeShareFetch`, creation paths, descriptors,
  share request validation, detail-key durability.
- `pkg/core`: share link operations, receive creation with mount and destination rules, the save
  runner and its destination verification, fetch phases and cleanup, restart recovery, inspect,
  and the extracted transfer core.
- `pkg/control`: `/v1/control/share/links*`, `/v1/control/share/tasks*`,
  `/v1/control/share/inspect`, service interface, and error classes.
- `internal/cli`: `share` and `receive` command groups, link and path argument validation, passcode
  input, secret redaction, and JSON output.
- `pkg/drivers/quark`: Quark share adapter, error mapping, and the client-UA requirement for share
  downloads.
- Documentation, shell completion, i18n catalogues, tests, and CI coverage for the new surfaces.
