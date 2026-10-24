## Context

See `proposal.md - Why`. Current state and constraints that shape the approach:

- The provider-neutral offline framework already exists: `drive.OfflineDownloader`,
  `drive.OfflineFetcher`, `drive.CapabilityOfflineDownload`, `drive.OfflineSourceKinds`, the
  `offline_download` / `offline_fetch` task types, and the Core runner. Quark is the only driver
  that declares the capability.
- Both 115 drivers are first-class qrypt drivers. `115` uses cookie authentication and
  `github.com/SheltonZhu/115driver` against `lixian.115.com`; `115_open` uses refresh-token
  authentication and `github.com/OpenListTeam/115-sdk-go` against `proapi.115.com/open`.
- 115's offline API is task-list oriented. A task is identified by `info_hash`, not by a
  per-task id returned from a detail endpoint. The result is described by `file_id` (produced
  entry) and `wp_path_id` (parent directory), and the file list must be reconstructed by listing
  the produced tree.
- 115 exposes no inspect-without-submit API, no file selection through `add_task_urls`, and no
  accelerated download session. The cookie web API has a restart operation, but the Go SDK does
  not expose it and the open platform API does not document one.
- `pkg/core` records `ProviderRootID` only from the create response. 115's add response does not
  contain the produced entry id, so cleanup would not know which resource folder to remove after
  a fetch.
- Core cleanup calls `driver.Remove` with a bare `drive.Entry{ID, Name}`. `p115open.Remove`
  currently forwards `entry.ParentID`, which is empty in that path, while the open API's delete
  endpoint expects the parent directory id.
- This change is stacked on `add-offline-download` and `improve-offline-fetch`, which introduce
  the offline capability and the staged-path/availability contracts it uses.

## Goals / Non-Goals

**Goals:**

- One shared 115 offline adapter that both drivers configure with their own auth, SDK, and
  provider error translation.
- Correct task observation for both single-file and multi-file 115 results, including relative
  paths that recreate the resource folder.
- A fetch that works with an explicit `--parse-to` and with only `--mount`, without pretending
  that 115 supports acceleration.
- Cleanup that removes only the files and resource folder the task created.
- Explicit, stable errors for 115 duplicate, quota, invalid-source, auth, and missing-task cases.

**Non-Goals:**

- 115 inspect-without-submit.
- File selection for 115, including the `add_task_bt` `wanted` path.
- Accelerated fetch.
- Provider-native retry or a retry contract change.
- ed2k source normalization; qrypt has no neutral ed2k source kind in this change.
- A cross-process task cache; the pagination cache is per driver instance and only an
  optimization.

## Decisions

### D1. One shared adapter with a narrow backend interface

Put the provider-neutral 115 logic in `pkg/drivers/internal/p115offline` and give each driver a
small backend implementation:

- `AddTask(ctx, source, dirID) (hash string, err error)`
- `ListTasks(ctx, page int) (tasks []Task, pageCount int, err error)`
- `DeleteTask(ctx, hash string) error`
- `ListDir(ctx, dirID string) ([]drive.Entry, error)`
- `DownloadTarget(ctx, fileID string, size int64) (drive.DownloadTarget, error)`
- `DefaultDirID() string`

The shared adapter owns task lookup, status mapping, result-tree walking, path construction,
pagination caching, and the `OfflineDownloader` / `OfflineFetcher` methods. Drivers own auth,
rate limiting, SDK DTO decoding, and mapping provider errors into `drive.OfflineError`.

Rationale: the two 115 APIs have the same task semantics but different clients and error
surfaces. Duplicating the lifecycle would duplicate the riskiest logic (tree walking, task
matching, state mapping). A shared adapter also lets one fake-backend suite cover the lifecycle.

Alternatives considered:

- Two independent adapters. Rejected: the second copy would drift on edge cases such as a
  single-file result, missing task, or late result listing.
- A generic offline framework abstraction over every provider. Rejected: Quark's parse/submit
  and accelerated flow is different enough that a shared cross-provider layer would be larger
  than the problem.

### D2. Task identity is `info_hash`, and lookup scans pages with a cache

A 115 offline task's stable identity is its `info_hash`. `CreateOfflineDownload` returns that
hash as `drive.OfflineDownload.ID`. `GetOfflineDownload` finds the matching task in the provider
list and maps its state.

The adapter remembers the last page on which a task was found. Each observation tries that page
first, then page 1, then the remaining pages up to the provider's page count. The cache is
per-process, mutex-protected, and discarded when a lookup misses.

Rationale: 115 has no documented open-API per-task detail endpoint. Scanning every page on every
5-second poll would turn a long task into unnecessary provider traffic, while always trusting
page 1 would miss older tasks.

Alternatives considered:

- Page 1 only. Rejected: correctness depends on 115's ordering and on how many tasks the account
  retains.
- A single-task cookie endpoint. Rejected for the shared layer: it is not available on the open
  platform API, and the list API is sufficient for both.

### D3. Resolve the result tree from `file_id` and `wp_path_id`

On success, or when the result set becomes complete, the adapter:

1. Lists the task's `wp_path_id`.
2. Finds the entry whose id is `file_id`.
3. If the entry is a file, reports one `OfflineFile` whose `Path` is the file name.
4. If the entry is a directory, walks it recursively and reports every file with a POSIX path
   prefixed by the resource folder name.
5. Sorts the files by path for a deterministic task snapshot.

`RemoteID` is the provider entry id. `Size` comes from the listing. The adapter caps recursion
depth and tracks visited directory ids defensively.

Rationale: `add_task_urls` returns only the hash. The task list later exposes `file_id` and
`wp_path_id`, and the produced tree is the only authoritative source of relative paths. This is
the same provider-neutral contract `improve-offline-fetch` established: the provider reports a
path, Core validates and joins it.

Alternatives considered:

- Reuse the task name as the folder name and infer paths from the source. Rejected: a provider
  can merge a same-named folder, and a single-file result has no folder at all.
- Return flat file names. Rejected: it loses the resource folder and causes collisions.

### D4. Availability is reported only after a complete result tree is visible

A 115 task becomes `succeeded` at status `2`. The adapter reports `Available = true` only when
that status and a resolvable result tree coincide. If status `2` arrives before the listing
shows the produced entry, `GetOfflineDownload` returns a retryable
`offline_provider_unavailable` error so Core's polling loop retries instead of treating an empty
file set as a successful fetch.

Rationale: Core's fetch gate requires `Available` plus at least one result file. Returning
success with no files would produce a task that cannot transfer anything.

Alternatives considered:

- Return `succeeded` with an empty file list and let fetch fail later. Rejected: it loses the
  retry window and produces a confusing terminal error.
- Poll inside `GetOfflineDownload` until the listing appears. Rejected: it blocks the runner's
  observation loop and duplicates Core's retry/backoff policy.

### D5. Implement the fetcher for default-destination fetches

For an explicit `--parse-to`, Core can read each staged entry through the VFS and does not need a
provider URL. For `offline fetch` with only `--mount`, there is no staged path, so Core falls
back to `OfflineFetcher`. The adapter therefore implements `OfflineDownloadURL`:

- `115`: resolve the file id to a `pick_code`, request a download URL, and return the URL plus
  the headers 115 requires.
- `115_open`: resolve `pick_code` through the open API, request `downurl`, and return the URL
  with the driver's download user agent.

The adapter does not declare `CapabilityAcceleratedFetch`; the URL path is a fallback, not an
acceleration claim.

Rationale: the CLI documents `--parse-to` as optional. A 115 fetch that only names a mount must
still work.

Alternatives considered:

- Require `--parse-to` for 115. Rejected: it would make 115's command surface inconsistent with
  Quark and with the documented `--mount` selector.
- Always declare acceleration to force URL transfer. Rejected: it would misrepresent provider
  capabilities and change Core's transfer selection.

### D6. Error translation lives in each backend

The shared adapter passes through `*drive.OfflineError` returned by a backend. Each driver maps
its own provider surface:

- `115`: `ErrOfflineNoTimes` to quota, `ErrOfflineInvalidLink` to source invalid,
  `ErrOfflineTaskExisted` to duplicate, and login sentinels to auth required.
- `115_open`: call `add_task_urls` through `AuthRequest` directly so the per-item
  `state/code/message` array is visible. Map `10010` to quota, `10004` to source invalid,
  `10008` to duplicate, and `99` / `401*` to auth required.
- Unmapped task-list/transport failures in an observation become
  `offline_provider_unavailable` so a transient network problem is retried; unmapped create
  failures become a non-retryable generic provider error.
- A missing task is `offline_provider_task_missing`, which Core may reconcile from the parse
  destination.

Rationale: the two APIs expose different error shapes. A shared code table would either lose
the open API's per-item detail or force the cookie driver to guess.

Alternatives considered:

- Use the SDK's `AddOfflineTaskURIs` wrapper for `115_open`. Rejected: it filters failed items
  and returns an empty hash list with a nil error, which cannot be classified.
- Treat every provider error as retryable. Rejected: quota, policy, and duplicate failures are
  terminal and should not consume the transient retry budget.

### D7. Do not advertise inspect, selection, accelerated fetch, or retry

The drivers declare only `offline_download`. Core's existing capability checks then:

- reject `offline inspect` with `offline_inspect_unsupported`;
- reject any selection other than `all` before a task is created;
- never request acceleration.

Provider-native retry is omitted. A failed 115 task retains its `info_hash`, so a re-create can
return `offline_duplicate`. The docs state that the user must remove the failed task in 115
before retrying, or cancel the qrypt task first when cancellation deletes the provider task.

Rationale: all four features are provider capabilities 115 does not offer through the APIs qrypt
uses. Advertising them would produce late, confusing failures.

Alternatives considered:

- Implement inspect by creating a task and immediately canceling it. Rejected: it mutates the
  provider account and can leave residue.
- Use `add_task_bt` with `wanted` for selection. Rejected: it requires a torrent SHA1 and pick
  code for a stored torrent, while qrypt normalizes local torrents to magnets without uploading
  them.
- Extend `OfflineRetrier` to return a new provider id and implement delete-plus-add. Rejected
  for this change: it changes a cross-provider contract for a limitation that documentation can
  handle.

### D8. Persist a late-arriving `ProviderRootID`

Core currently records `ProviderRootID` only from `CreateOfflineDownload.RemoteID`. 115's create
response has no produced entry id. Change the observation fold so that a non-empty
`OfflineDownload.RemoteID` fills `ProviderRootID` when it is still empty. The field remains
durable in task detail and is used only by cleanup's resource-folder ownership check.

Rationale: without it, a successful 115 fetch removes the individual files but leaves the
resource folder behind, contradicting the cleanup contract.

Alternatives considered:

- Poll for the root inside create. Rejected: it adds latency to every add and may still return
  before the provider exposes the entry.
- Let the driver remember the root across restarts. Rejected: Core already persists the field,
  and it should learn it from the provider observation that actually contains it.

### D9. `p115open.Remove` resolves a missing parent id

When Core cleanup removes a staged file or the resource folder, it supplies only the provider
id and name. The open platform delete endpoint expects `parent_id`. The driver will resolve the
parent from `GetFolderInfo`'s path metadata when `entry.ParentID` is empty, and use the entry's
own parent when it is available.

Rationale: this keeps cleanup id-based rather than path-based, so it cannot delete an unrelated
entry that happens to occupy a staged path.

Alternatives considered:

- Add `ParentID` to `drive.OfflineFile` and thread it through Core. Rejected for now: it widens a
  provider-neutral contract for one driver's cleanup need.
- Delete by VFS path in Core. Rejected: it weakens the ownership guarantee of fid-based cleanup.
- Send an empty `parent_id`. Rejected: the open API documents the field as required, and a silent
  failure would leave staged files behind.

### D10. Default destination uses the mount's provider root

When Core does not supply `DestinationID` (no explicit `--parse-to`), the adapter uses the
driver's configured provider root (`rootID`, default `"0"`). It returns that id as
`OfflineDownload.Destination`, so Core persists the actual provider directory rather than an
empty default. A source added without an explicit directory lands under the mount's virtual
root, not the account root when the mount is rooted elsewhere.

Rationale: qrypt mounts can be rooted at a subdirectory. Saving to the account root would stage
files outside the user's selected mount.

Alternatives considered:

- Always pass `"0"`. Rejected: it ignores `root_path` and breaks the mount's virtual boundary.
- Resolve the source's "cloud download" system folder. Rejected: the behavior is less
  predictable and the API documents `0` as the default.

## Risks / Trade-offs

- [Task list ordering/page size changes] → The lookup tries a cached page, page 1, then all
  pages up to `page_count`; a miss clears the cache and falls back to a full scan.
- [A completed task disappears before its tree is observed] → Core reconciles a missing task
  from the parse destination; the adapter reports paths whenever the task is visible so the
  expected set is persisted early.
- [Large torrents produce many listing calls] → Tree resolution happens only when status `2`
  or availability is reached, not on every poll; the adapter caps recursion and uses the
  driver's existing paginated `List`.
- [115 rejects a re-created failed task as duplicate] → Surface `offline_duplicate`, never
  silently reuse the task, and document the removal step. Provider-native retry is a later
  change.
- [The open API returns `percentDone` on a different scale] → Normalize in the backend and
  verify with a real-account smoke test; the canonical field is always 0..100.
- [A 115 task with the same `info_hash` already exists in another directory] → Compare the
  task's `wp_path_id` with the expected parse directory when it is known; report a duplicate
  instead of resolving paths relative to the wrong directory.
- [Cleanup races a lagging listing] → Ownership is decided by provider ids and the observed
  paths, not by the current listing alone; folder removal remains best-effort.
- [Two drivers drift on error mapping] → The shared adapter owns lifecycle; each backend test
  pins its own provider error table.

## Migration Plan

1. Land the shared adapter and fake-backend tests with no driver wiring; no behavior changes.
2. Wire `115` and `p115open` backends, declare capabilities, update capability tests.
3. Add the Core `ProviderRootID` observation fold and the `p115open.Remove` fallback with
   focused tests.
4. Update the offline guide and regenerate the driver capability matrix.
5. Run the real-account smoke test for both drivers, then run the local CI gate.

Rollback: remove the two capability declarations and the adapter wiring. The drivers remain
fully functional without offline support, and no task type, schema, or persisted field is
removed. The `ProviderRootID` fold is backward compatible and can stay even if the 115 wiring is
reverted.

Archive order: `add-offline-download`, then `improve-offline-fetch`, then this change.

## Open Questions

- Whether a later change should add provider-native retry for the cookie driver, and whether the
  open platform API will expose an equivalent restart operation.
- Whether eD2k should become a neutral source kind; 115 accepts it, but qrypt does not normalize
  it today.
