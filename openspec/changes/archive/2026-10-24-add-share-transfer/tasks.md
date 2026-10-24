## 1. Task and provider contracts

- [x] 1.1 Add the `share` operation kind and declare `share_save` under it and `share_fetch` under `download`, each with an explicit creation path, persistence, visibility, recovery, and retry policy; verify descriptor and task-policy tests cover both types and reject an undeclared type at creation.
- [x] 1.2 Add `task.ShareDetailKey` and make `taskDurableKey` digest share detail as well as offline detail; verify a share phase or provider-id change is journaled while progress-only updates are skipped.
- [x] 1.3 Define the provider-neutral share types (`ShareLink`, `Share`, `ShareSession`, `ShareEntry`, `ShareSaveTask`) and the four capability names; verify JSON round-trips and that capability sets are preserved by the crypt, bandwidth, and observability wrappers.
- [x] 1.4 Define `ShareLinkParser` and make the Quark driver recognize `https://pan.quark.cn/s/<id>` including a `?pwd=` query; verify a foreign link and a malformed link are both rejected by the driver and that no provider name is needed by callers.
- [x] 1.5 Define stable provider-neutral share error codes with retryable flags and classified transport errors (usage / not_found / runtime), including human-verification and throttling; verify mapping tests cover passcode, not-found, expired, own-save, ticket, visit-limit, quota, verification, rate-limit, and transient cases.
- [x] 1.6 Add `SharePolicy` (passcode required/optional/unsupported, accepted expiries, visit limit, update, list, rename) declared per driver, and validate link requests against it before any provider call; verify a mandatory-passcode provider, a passcode-less provider, an unsupported visit limit, an unsupported expiry, and a provider without a my-links list.
- [x] 1.7 Model save results as per-entry outcomes plus an optional opaque wait handle, and map them to succeeded / partially-failed / failed; verify a synchronous batch with mixed per-entry results, an asynchronous task handle, and a monitor-URL-style handle all complete without provider-specific branches in core.
- [x] 1.8 Add contract tests with two non-Quark fake providers (one mandatory-passcode synchronous batch with partial failures and drive-id destinations, one passcode-less provider with a monitor handle and no my-links list) and verify the core and CLI need no special case for either.

## 2. Share links (export)

- [x] 2.1 Implement link creation for one mount with mount-qualified paths, rejecting paths from different mounts in a single invocation; verify a missing path, a path without a mount, and a cross-mount selection create no link, and that an encrypted mount can still publish a link.
- [x] 2.2 Implement link options: `--expires` over the neutral set (`1d`, `7d`, `30d`, `forever`), `--passcode[=CODE]` with local generation, `--limit-visits`, and `--name`; verify provider-unsupported options are rejected before the provider call and that a generated passcode is returned in the result.
- [x] 2.3 Implement synchronous provider-task handling inside the driver: bounded polling when the provider answers `task_sync:false`, returning a typed `share_timeout` instead of hanging; verify an immediate link, a queued link that finishes inside the window, and one that does not.
- [x] 2.4 Implement list, show (by native id or link id), update, and remove, including liveness/risk fields, visit counters, and passcode visibility for the owner; verify removal by link id resolves to the native id and that an unknown id returns `share_not_found`.
- [x] 2.5 Redact passcodes in command output unless `--show-secrets` is passed, and never log a passcode; verify human and JSON output in both modes.

## 3. Receive: save lifecycle

- [x] 3.1 Implement receive creation: link parsing, acting-mount resolution (explicit, destination-derived, or unique), capability checks, same-provider enforcement, and structured errors for every case; verify link/mount mismatch, cross-provider destination, unsupported mount, and ambiguous multi-mount cases create no task.
- [x] 3.2 Implement destination resolution for `--to /MOUNT/DIR` and `--mount NAME` (root) with `--mkdir`, reusing the offline path rules; verify a missing directory fails without `--mkdir`, an encrypted cloud destination is rejected before the provider call while a local download destination inside an encrypted mount stays allowed, and the resolved remote id is persisted.
- [x] 3.3 Implement the open/list/save runner: reopen the link, list the selected directory with paging, apply the neutral selection, persist the provider wait handle (when any) before polling, fold per-entry outcomes, and map provider progress and errors; verify queued, running, success, partial success, failure, quota, verification-required, and visit-limit observations.
- [x] 3.4 Implement destination verification with bounded retries: after a successful save, re-list the target until the saved entries appear (or the provider returned them), then expose them as task items; verify a lagging listing succeeds, a never-appearing entry fails as unverified, and a provider-reported result short-circuits the listing.
- [x] 3.5 Map save outcomes onto task state: all entries saved → succeeded, some → partially failed, none → failed, with every selected entry present as an item; verify a mixed Aliyun-style batch and a 115-style per-file response produce the same task shape.
- [x] 3.6 Implement cancellation and retry: cancel stops observation and reports canceled without deleting the user's copy; retry re-runs the runner from persisted detail, does not repeat a completed save, and starts a new execution generation.
- [x] 3.7 Implement restart recovery for `share_save`; verify a non-terminal task resumes from the persisted mount, link, and provider task id, and that stale updates from a previous runner are ignored.
- [x] 3.8 Implement read-only inspection: open the link, list one directory, report entries and metadata, create no task, and persist no session token; verify a passcode-protected link reports `share_passcode_required`/`share_passcode_wrong` rather than a provider message.
- [x] 3.9 Implement de-duplication and `--new` over link identity, resolved mount, selection, and destination, plus explicit idempotency keys; verify a repeated active save returns the existing task and that a completed task allows a new one.

## 4. Receive: fetch and the shared transfer core

- [x] 4.1 Extract the offline fetch transfer core (writer selection, bounded-range chunked download, resume, URL refresh, progress accounting, `1–16` workers / `1–64 MiB` chunks) into `pkg/core/transfer.go`; verify the offline fetch suite and the chunk/resume tests pass unchanged after the move.
- [x] 4.2 Implement `share_fetch` as a durable multi-phase task (`save` → `download` → `cleanup` → `complete`) that persists the phase and both destinations; verify a restart resumes at the correct phase without repeating a completed save.
- [x] 4.3 Implement direct download (no `--to`): resolve share download targets for the selected entries and transfer them with the shared core; verify namespace, in-tree FUSE, and plain local writers, and that missing parent directories are created.
- [x] 4.4 Implement save-then-download: after a completed save, resolve targets for the saved entries and transfer them; verify the download starts from the saved entries rather than re-listing the share.
- [x] 4.5 Implement cleanup: after a successful download, remove only the entries qrypt saved, unless `--keep-cloud`; verify a partial failure removes nothing and that a direct download never deletes anything.
- [x] 4.6 Wire `--workers`, `--chunk-size`, and the selection flags into the fetch task and validate their bounds before task creation; verify out-of-range values are usage errors and that a recovered task keeps its tuning.

## 5. Quark provider

> The verified provider flow this group implements is summarized in `design.md` Appendix and in
> `charles-quark/pc/SHARE_SAVE_FLOW.md`.

- [x] 5.1 Implement link creation: `POST /1/clouddrive/share` with `fid_list`, `url_type`, `passcode`, `expired_type`, optional `visit_user_limit`, and read the native id from `task_resp.share_id`; verify a public link, a passcode link (`task_sync:false`), and the provider's option-validation errors.
- [x] 5.2 Implement link listing, show, update, and delete through `GET /share/mypage/detail`, `POST /share/password`, `POST /share/update`, and `POST /share/delete`, mapping `pwd_id`/`share_id`, expiry, passcode, and visit counters into the neutral types; verify a dangling share and an unknown id.
- [x] 5.3 Implement link opening (`POST /share/sharepage/token`) with passcode mapping (41008/41007) and session tokens, and stating the owner-bypass behavior as provider-specific and not relied upon; verify guest and logged-in calls with correct, missing, and wrong passcodes.
- [x] 5.4 Implement browsing (`GET /share/sharepage/detail`) with `ver=2`, paging, `pdir_fid` navigation, and `share_fid_token` mapped onto `ShareEntry.Ticket`; verify root listing, a subdirectory listing, page boundaries, and the deleted-content case (41004 and the empty-root rule).
- [x] 5.5 Implement provider-side save (`POST /share/sharepage/save` + `GET /1/clouddrive/task`) including directory saves, `save_as.save_as_top_fids`, and the provider dedupe behavior; verify single-file, directory, own-link (41017), bad ticket (41020), missing ticket, and anonymous cases.
- [x] 5.6 Implement direct download targets with the share form of `POST /1/clouddrive/file/download` (`fids`, `pwd_id`, `stoken`, `fids_token`) using the PC-client User-Agent required for this path; verify the URL is returned and that a non-client UA surfaces the provider's 23018 as a readable error.
- [x] 5.7 Register `CapabilityShare`, `CapabilityShareReceive`, `CapabilityShareSave`, and `CapabilityShareFetch` and the `SharePolicy` only for the completed implementation, and add provider tests with a fake HTTP server for every request above; verify `115_open`, `webdav`, `sftp`, `s3`, and `localfs` report no share capability.
- [x] 5.8 Record the per-driver share-shape notes (design "Provider variation") as the checklist for the next adapter, and verify a second adapter can be added without changing `pkg/core`, `pkg/control`, or the CLI beyond a flag the policy declares.

## 6. Control, CLI, docs, and coverage

- [x] 6.1 Implement the control service and routes (`/v1/control/share/links*`, `/v1/control/share/tasks*`, `/v1/control/share/inspect`) with provider-neutral request types and classified errors; verify route tests, no-Core behavior, and that no provider name, driver name, or provider task id is required input.
- [x] 6.2 Implement the `share` CLI group (`create`, `list`, `show`, `update`, `remove`) with argument hints, `--json`, `--show-secrets`, and i18n; verify usage errors for a missing mount, a non-existent path, a bad expiry, and a malformed passcode.
- [x] 6.3 Implement the `receive` CLI group (`inspect`, `save`, `fetch`, `list`, `show`, `cancel`, `retry`, `wait`) with link/passcode resolution (flag, `?pwd=`, stdin), same-provider errors, and blocking/`--no-wait` behavior; verify interrupt leaves the task running and exits 130.
- [x] 6.4 Add shell completion and localized help for both groups; verify the help ladder and completion tests.
- [x] 6.5 Write `docs/for-user/share.md` and extend `docs/for-user/cli.md`; add glossary terms to `CONTEXT.md` (分享链接, 转存, 接收任务, 分享会话).
- [x] 6.6 Add end-to-end tests against the fake provider for the full matrix (public link, passcode link, save, direct download, save-then-download, cleanup, passcode retry, restart recovery) and a live Quark smoke test documented in the design appendix.
