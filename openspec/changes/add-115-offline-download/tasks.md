## 1. Shared 115 offline adapter

- [x] 1.1 Add `pkg/drivers/internal/p115offline` with the `Task` model, the `Backend` interface, and canonical state mapping; verify the package builds and a table test covers `-1/0/1/2` -> `failed/queued/running/succeeded`.
- [x] 1.2 Implement paginated task lookup with a per-instance last-hit page cache; verify a fake backend finds a task on page 2 and a second lookup does not rescan earlier pages.
- [x] 1.3 Implement result-tree resolution from `file_id` + `wp_path_id`, including single-file results, nested folders, depth limits, and deterministic path sorting; verify multi-file and single-file fake-backend tests assert the expected `OfflineFile.Path` values.
- [x] 1.4 Implement `CreateOfflineDownload`, `GetOfflineDownload`, and `CancelOfflineDownload` over the backend; verify a fake backend covers queued -> running -> succeeded, cancellation, and idempotent cancel of a missing task.
- [x] 1.5 Implement `OfflineDownloadURL` over the backend; verify it rejects a request without exactly one `RemoteID` and returns the URL, headers, and size from a fake backend.
- [x] 1.6 Implement expected-directory validation and `RestoreOfflineLayout`; verify a task found in a different `wp_path_id` reports `offline_duplicate`, and a restored layout resolves the same task after a fresh adapter instance.
- [x] 1.7 Add the shared fake-backend test suite for missing tasks, lagging result listings, duplicate, provider unavailable, and empty result sets; verify `go test ./pkg/drivers/internal/p115offline/...` passes.

## 2. p115 cookie backend

- [x] 2.1 Implement the `p115` backend over `AddOfflineTaskURIs`, `ListOfflineTask`, `DeleteOfflineTasks`, `List`, `GetFile`, and `DownloadWithUA`; verify the adapter methods are reachable from `p115.Driver`.
- [x] 2.2 Map 115 cookie offline sentinels (`ErrOfflineNoTimes`, `ErrOfflineInvalidLink`, `ErrOfflineTaskExisted`, login errors) to the stable offline error codes; verify table tests cover quota, invalid source, duplicate, auth, and unknown errors.
- [x] 2.3 Handle add responses that contain an empty hash for a rejected item; verify the driver returns a typed provider error instead of a queued task with an empty id.
- [x] 2.4 Declare `CapabilityOfflineDownload` and report `magnet`, `http`, and `ftp`; verify `pkg/drivers/capability_test.go` expects the new capability and no inspect/selection/accelerated capability.
- [x] 2.5 Add a fake `lixian.115.com` test server for add/list/delete/tree/download-URL; verify the offline lifecycle test covers a multi-file result, a single-file result, missing task, and provider error mapping.

## 3. p115open backend

- [x] 3.1 Implement the `p115open` backend over the open API, including a direct `AuthRequest` call to `/open/offline/add_task_urls` so per-item `state/code/message` is visible; verify a fake response with one success and one rejected item reports the rejected item's code.
- [x] 3.2 Implement open-platform task listing, deletion, directory listing, and download-target resolution; verify the fake open API test covers pagination, `pick_code` lookup, and `downurl` headers.
- [x] 3.3 Map open-platform offline codes (`10010`, `10004`, `10008`, `99` / `401*`, unknown) to stable offline errors; verify table tests cover quota, invalid source, duplicate, auth, and generic provider failure.
- [x] 3.4 Make `p115open.Remove` resolve a missing parent directory id from `GetFolderInfo` path metadata before calling `DelFile`; verify a test removes a staged file and the resource folder with an empty `entry.ParentID`.
- [x] 3.5 Declare `CapabilityOfflineDownload` and report `magnet`, `http`, and `ftp`; verify `pkg/drivers/capability_test.go` expects the new capability and no inspect/selection/accelerated capability.
- [x] 3.6 Add a fake `proapi.115.com` test server for add/list/delete/tree/download-URL/remove; verify the offline lifecycle and cleanup tests cover a multi-file result, a single-file result, duplicate, and missing task.

## 4. Core and cleanup fixes

- [x] 4.1 Fold a late-arriving `OfflineDownload.RemoteID` into `offlineDetail.ProviderRootID` when it is still empty; verify a Core runner test observes the root after creation and persists it.
- [x] 4.2 Extend the offline cleanup test so a provider that reports its root only after creation still removes the resource folder; verify the staged folder is removed only when every file was owned by the task.
- [x] 4.3 Verify no Quark regression from the `ProviderRootID` fold; run the existing Quark offline and Core offline runner tests.
- [x] 4.4 Confirm `p115open.Remove` cleanup behavior through the Core cleanup path, not only through a direct driver test; verify the fake-provider integration test removes staged files and leaves unrelated entries.

## 5. Capabilities, documentation, and generated docs

- [x] 5.1 Update `docs/for-user/offline-download.md` with `115` and `115_open` support, accepted source kinds, unsupported inspect/selection/acceleration, and the failed-task retry workaround; verify the examples use only installed `qrypt` commands.
- [x] 5.2 Update `docs/for-developer/quality-gates.md` with the 115 real-account smoke procedure for both drivers; verify it names the account prerequisite and the add/fetch/cleanup checks.
- [x] 5.3 Regenerate `docs/for-user/support-drivers.md` from the schema/capability generator; verify the 115 rows show offline download and `git diff --exit-code` is clean after a second generation.
- [x] 5.4 Update the driver capability expectations and any offline provider examples that name Quark as the only implementation; verify no user-facing doc still claims Quark is the sole offline provider.

## 6. Verification and release gate

- [x] 6.1 Run the focused tests for `pkg/drivers/internal/p115offline`, `pkg/drivers/p115`, `pkg/drivers/p115open`, `pkg/drivers`, and `pkg/core`; verify all pass.
- [x] 6.2 Run `gofmt -w` on every changed Go file and confirm `gofmt -l` produces no output for them.
- [ ] 6.3 Run the real-account smoke test for `115` and `115_open`: stage a small HTTP file and a small multi-file magnet, run `offline fetch` with and without `--parse-to`, and verify the mirrored paths and cleanup.
- [x] 6.4 Run `openspec validate add-115-offline-download --strict`; record the expected stacking INFO from the unarchived `add-offline-download` / `improve-offline-fetch` changes.
- [x] 6.5 Run `scripts/ci/check-all.sh` with the CI Go version and confirm it exits 0 with `== local CI check passed ==`; re-run after any further change to the submitted files.
- [x] 6.6 Run `git diff --check` and confirm no whitespace errors remain.
