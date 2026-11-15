## Context

See `proposal.md - Why`. Current state and constraints that shape the approach:

- `pkg/drive` already declares `OfflineDownloader` and `CapabilityOfflineDownload`, but no
  driver implements it and no creation path, runner, CLI, or control route exists.
- `pkg/task` already owns durable task types, descriptors, creation paths, idempotency
  (`IdempotencyKey` + `OperationFingerprint`), retry/recovery policy, terminal-state
  protection, and a local `task.Controller` used by the control socket and mobile facade.
- A running `qrypt mount` process is the only long-lived owner of a Core (task store, runner,
  control socket). One-shot `fs` commands build a short-lived filesystem, and the task journal
  has no cross-process locking.
- Quark's flow is verified end to end: `parse -> submit` for magnets and direct HTTP links;
  provider task states `0..4`; observation by `task_id`; cancel/retry through `POST /task/status`.
  Crucially, testing confirmed that **both** accelerated and normal magnet downloads require the
  resource to be parsed into the provider's cloud storage first; the accelerated session only
  changes how the resulting cloud file is downloaded, it is not a way to obtain bytes without a
  cloud copy.

## Goals / Non-Goals

**Goals:**

- Durable, restart-safe `offline_download` (cloud-side) and `offline_fetch` (cloud staging plus
  local transfer) tasks.
- A provider-neutral CLI whose only provider selector is the parse destination mount.
- Quark as the first working provider, exercised end to end for both task types.
- Explicit file selection, a read-only `inspect` preview, and a `wait` command.
- Deterministic destination, error, exit-code, and de-duplication behavior that scripts can rely on.

**Non-Goals:**

- No new `OperationKind`; both task types stay under `download`.
- No cross-provider server-side transfer inside a task.
- No mounted-folder trigger for offline downloads.
- No per-file live progress in v1 (result files are written on completion).
- No `--allow-plaintext` override for encrypted parse destinations in v1.
- No batch submission of multiple sources in one command.
- No local BitTorrent engine; a magnet is always resolved provider-side.
- No browsing or mutation of provider-side task history that qrypt did not create.
- No provider-specific flags, socket paths, or provider task ids as required CLI input.

## Decisions

### Control-plane CLI backed by a running Core

`offline` commands talk to the Core over the existing local control socket. `list`/`show` and
`fetch` use the same socket; there is no second "read the journal without a Core" path.

Rationale: the task journal has no cross-process locking, only a running Core can own the
poller, and `offline_fetch` needs Core-owned writers (VFS or local). The socket is local-only,
mode 0600, and already rejects TCP endpoints.

Alternatives considered:

- One-shot Core per command: unsafe alongside a running mount and unable to own a long fetch.
- A headless `qrypt core` daemon: the cleaner long-term owner, out of scope; because the CLI only
  knows the control socket, such a daemon can host the same routes later without CLI changes.

### `offline add` returns at the first observable provider state

`add` waits until the provider task is observable (status `0..4`) rather than returning as soon
as the create call succeeds, and exits non-zero when that first state is already failed or canceled.

Rationale: Quark can fail a task within milliseconds; returning "accepted" on the HTTP success
alone would report success for a task that never starts.

### Provider-neutral command surface and split destinations

The CLI exposes `add`, `inspect`, `fetch`, `list`, `show`, `cancel`, `retry`, `wait`. Provider
selection is the parse destination mount; there is no `--provider`, driver name, or provider
boolean. Destinations are split:

- `--parse-to /MOUNT/DIR` names the provider-side staging directory. It is optional and defaults
  to the provider's magnet/system directory; it must live on one provider and must not be an
  encrypted mount.
- `--download-to PATH` names the final writable path for `offline fetch`. It accepts a qrypt
  namespace path, a path inside a mounted FUSE tree, or a plain local path, and may be an
  encrypted mount.

Rationale: the provider requires a cloud staging destination for every magnet, while the user's
actual goal for `fetch` is a local or re-uploaded artifact. Splitting the two makes the
encryption rule precise and removes the need for a separate accelerator selector.

### One unified create path for Quark: parse + submit

The Quark adapter resolves the parse destination to a `pdir_fid`, sends
`offline/download/parse` followed by `offline/download/submit`, and observes the provider task by
`task_id`. Accelerated vs normal only changes download-URL acquisition:

- accelerated: the submit response carries `speedup_dl_info.speedup_session`, and
  `POST /file/download {fids, parse_mode:22, speedup_session}` returns the accelerated URL;
- normal: the adapter waits for the provider task and `POST /file/download {fids}` returns the
  normal URL.

Rationale: verified for both magnets and direct HTTP links; one parse+submit path and one
observation model. The desktop client additionally branches single HTTP links onto a
`parse(auto_download) + save_to/progress` path for m3u8 and browser-save scenarios; that branch is
deferred until a concrete need is verified.

### Provider-neutral source normalization

Accepted sources are magnet links, HTTP(S) URLs, optionally FTP URLs, and local `.torrent` files.
A local `.torrent` is parsed locally into `magnet:?xt=urn:btih:<hash>` with `dn` and `tr` copied
from the torrent metadata; the torrent file itself is never uploaded. Share links and unsupported
schemes are rejected with typed errors.

### Parse destination and download destination must differ

`--parse-to` resolves to `(mountP, dirP)`. `--download-to` is resolved to `(mountD, dirD)` when it
names a configured mount or lies inside a configured FUSE mount point; otherwise it is a local-only
path. The command fails before creating any provider task when `mountP == mountD` and
`clean(dirP) == clean(dirD)`. When the download destination is a different directory on the same
provider, qrypt warns that the bytes will be downloaded and uploaded again rather than refusing.

Rationale: an identical destination would download a file and upload it back into the location it
was staged, potentially over the staging file itself.

### Active de-duplication plus an explicit idempotency key

Repeated submissions are de-duplicated against non-terminal offline tasks with the same normalized
source, parse destination, and selection; `--new` bypasses it and an explicit `--idempotency-key`
uses the existing task-manager semantics. `--download-to` is **not** part of the fingerprint, so two
fetches of the same source into the same staging directory share one staging task. Provider
`32007 任务重复` becomes a typed `offline_duplicate` error rather than a silent success.

### Cancel deletes, retry prefers the provider's in-place retry

`cancel` marks the qrypt task as canceling, then asks the provider to delete the task
(Quark `op_type: 0`). `retry` is allowed only for failed/canceled tasks: if the driver implements
the optional retry capability the provider task is re-queued in place (Quark `op_type: 1`, same
provider id, new qrypt execution generation); otherwise Core re-creates the task from the persisted
source/parse destination/selection and keeps the previous provider id in detail history. Terminal
protection is unchanged.

### Polling, completion, and tuple reconciliation

Core polls the provider task every 5s, using the provider hint when it is larger (capped at 30s),
with exponential backoff on transient errors and a bounded failure threshold. Provider states map to
`queued`/`running`/`succeeded`/`failed`/`canceled`. `status=2` is authoritative for success even if
the destination entry is not yet visible; the observation records `destination_verified=false`
instead of reporting a false failure. A missing provider task falls back to resolving the destination
entry, and only after a bounded window becomes a typed failure.

### `offline_fetch` is a Core-owned task with a staging phase and a transfer phase

`offline_fetch` runs under the `download` operation and composes two phases inside one durable task:

1. **staging**: parse + submit the source to the parse destination, then obtain a download URL
   (accelerated when available, otherwise after the provider task completes);
2. **transfer**: stream each selected file to `--download-to` and report per-file progress.

The task persists the parse destination, the final destination, the effective selection, provider
identifiers, the current phase, and the cleanup policy, so restart recovery resumes from the correct
phase. A fetch whose transfer is interrupted leaves the staging task intact; re-running the same
command re-uses it through active de-duplication instead of creating a second provider task.

Alternatives considered:

- Client-side streaming only (a one-shot CLI): cannot write to qrypt namespace destinations, cannot
  resume, and duplicates the offline task machinery.
- Adding an optional `download_to` to `offline_download`: muddles the cloud-only guarantee of `add`
  and its policy (cleanup, encryption, blocking).

### Fetch writer selection and URL refresh

The transfer phase chooses its writer from the resolved destination:

- a qrypt namespace path, or a local path inside the Core's own FUSE mount, is written through the
  VFS writer (which applies encryption and the destination mount's upload path);
- any other local path is written directly.

Download URLs are signed and expire (Quark ~30 minutes), so the transfer re-requests a URL when it is
missing or expired and resumes from the current offset where the writer supports it.

A missing final destination directory is created automatically before the transfer starts. This is
deliberately asymmetric with the parse destination, which fails without an explicit creation request:
cloud directories are user-visible shared state, while a local destination is cheap to create.

Rationale: mapping an in-tree FUSE path back to the namespace avoids writing through FUSE into the
same Core, which would be recursive.

### Transfer concurrency and chunk bounds

The provider URL transfer copies each file with bounded `bytes=start-end` requests, several in flight
at once. The caller may tune both numbers (`offline fetch --workers`, `--chunk-size`); unset values use
the Core defaults of 4 workers and 8 MiB chunks, and the task detail records whatever was chosen so a
recovered fetch resumes with the same tuning.

Both bounds come from live measurement of the first provider, not from taste:

| workers × chunk | aggregate | note |
| --- | --- | --- |
| 1 × 128 MiB | 7.1 MB/s | one request sustains the per-connection ceiling |
| 1 × 8 MiB | 5.5 MB/s | default chunk, one connection |
| 1 × 1 MiB | 1.6 MB/s | fixed per-request cost starts to dominate |
| 1 × 256 KiB | 0.8 MB/s | a 128 KiB request still costs ~0.33s |
| 8 × 8 MiB | 24 MB/s | default configuration, best measured |
| 8 × 1 MiB | 16 MB/s | many small requests do not beat fewer large ones |
| 8 × 256 KiB | 4 MB/s | overhead dominates |
| 16 × 1 MiB | 22 MB/s | more workers cannot recover a too-small chunk |

A bounded Range request carries roughly 0.3s of fixed latency, so the chunk floor is 1 MiB: below it a
request spends most of its life on overhead. The ceiling is 64 MiB: larger chunks barely improve
throughput, while every failed or expired request re-fetches its whole chunk, so a very large chunk
turns one hiccup into tens of seconds of lost work. Workers are bounded at 16 because measured gains
flatten there while concurrent-connection pressure keeps rising.

Rationale: defaults stay conservative (4 × 8 MiB already reaches the fastest measured configuration),
and the bounds exist to keep a caller from picking a combination that is measurably worse than the
default.

### Fetch cleanup and blocking defaults

After a successful transfer, `offline fetch` removes the cloud copy it created (only the fids it
staged, never other entries in the directory) unless `--keep-cloud` is given. `offline fetch` blocks
until the final destination is written; interrupt detaches the wait while the Core task continues,
and `--no-wait` returns as soon as staging is admitted.

Rationale: `add` is "keep it in the cloud", `fetch` is "bring it here"; a fetch that silently
accumulates cloud copies is surprising, while deleting only qrypt-created fids is safe.

### Offline error codes and exit codes

Provider failures map to stable `offline_*` error codes with an explicit retryable flag
(`offline_auth_required`, `offline_quota_exceeded`, `offline_source_invalid`,
`offline_source_forbidden`, `offline_space_insufficient`, `offline_file_too_large`,
`offline_duplicate`, `offline_selection_unsupported`, `offline_source_unsupported`,
`offline_inspect_unsupported`, `offline_provider_unavailable`,
`offline_provider_task_missing`, `offline_same_destination`, `offline_wait_timeout`). Exit codes are
`0` success, `1` runtime/provider/task failure (including canceled), `2` usage error, `130`
interrupt. A canceled task uses the same exit code as a failed task; scripts distinguish them
through `--json`.

### Task detail, results, and capability model

Provider identifiers and observations live in `Task.Detail` under stable keys; result files are
written into `Tracking.Items` when a task completes. Offline capabilities are split into
`offline_download`, `offline_inspect`, `offline_selection`, and an accelerated-fetch capability;
supported source kinds come from an optional reporting interface, and an absent reporter defers to
the provider's own error. Wrappers must preserve these capabilities, and the encrypted parse
destination rejection is a Core policy based on mount configuration rather than capability stripping.

### Inspect and wait

`inspect` is a read-only preview (`--parse-to` optional, the same selection flags accepted) that
parses, briefly retries asynchronous parses, and reports files, total size, instant-save prediction,
and parse-destination checks without creating a task or persisting a parse token. `wait` observes an
existing task to a terminal state with no default timeout; interrupt stops only the observation, and
a Core restart is followed by reconnecting and resuming by task id.

### Control routes

Offline control uses dedicated local routes (`POST /v1/control/offline/tasks`,
`GET /v1/control/offline/tasks`, `GET /v1/control/offline/tasks/{id}`,
`POST /v1/control/offline/tasks/{id}/cancel`, `POST /v1/control/offline/tasks/{id}/retry`). Creation
carries offline inputs (`source`, `parse_to`, `selection`, `mkdir`, `new`, and an optional
`download_to` plus `keep_cloud` for a fetch); its presence selects `offline_fetch`, otherwise
`offline_download`. Responses reuse the canonical task snapshot JSON.

### Quark first

The first implemented provider is Quark, for both `offline_download` and `offline_fetch`. Its flow is
verified and a test account is available; 115 Open follows once the shared contract is proven. The
specs use provider-neutral wording so provider order is not part of the contract.

## Risks / Trade-offs

- [Quark request-encryption keys are versioned constants] → keep the derived key set and
  re-extraction procedure documented; surface key-format failures as `offline_crypto_error`.
- [The desktop client's safe path covers m3u8 and browser-save sources] → v1 uses the unified
  `parse + submit` path and returns a typed error for sources it cannot submit.
- [Download URLs expire mid-transfer] → re-request on expiry and resume from the current offset.
- [A magnet always leaves a cloud copy] → default cleanup of qrypt-created fids on a successful
  fetch, with `--keep-cloud` to retain.
- [Provider task records can disappear] → confirm completion against the destination entry after a
  bounded window instead of assuming failure.
- [A provider duplicate can come from another client or device] → report `offline_duplicate` and
  never claim success without a provider task id.
- [Writing an in-tree FUSE path from the Core would recurse] → map in-tree paths to the namespace and
  use the VFS writer.
- [Two Core processes on one state directory would corrupt the journal] → the control-plane decision
  forbids opening a second Core for offline commands.
- [Quota errors are easy to misclassify] → dedicated `offline_*` codes with explicit retryable
  flags, not `drive.ErrRateLimit`.

## Migration Plan

1. Land the task types, contracts, capability flags, and control routes without changing existing
   task types.
2. Implement and validate the Quark adapter against a fake provider, then against a real account with
   a small HTTP or magnet source, for both `add` and `fetch`.
3. Enable `offline` commands per mount; commands on unsupported mounts fail before creating any
   provider task.
4. Existing task records stay readable; `offline_download` and `offline_fetch` records written by a
   future version remain recoverable and are never silently discarded if creation is rolled back.

Rollback is disabling the offline creation path; existing offline records must remain readable and
surface as recoverable/unavailable.

## Open Questions

- Per-file live progress for providers that expose it; deferred until a provider supports it.
- An explicit `--allow-plaintext` override for encrypted parse destinations; deliberately deferred.
- Batch submission of multiple sources in one command; deferred.
- Using the desktop client's `save_to/progress` path for m3u8 or page-URL sources; deferred until a
  verified need.

## Appendix: Quark provider flow (decrypted reference)

This appendix is the implementation reference for the Quark adapter (task group 3). It records the
reverse-engineered, live-verified flow. The `specs/*.md` files stay provider-neutral behavior
contracts; this section is where the Quark-specific "how" lives, so the change is self-sufficient.

Evidence: `charles-quark/pc/BT_OFFLINE_FLOW.md`, `charles-quark/pc/WSG_KEY_DERIVATION.md`,
`charles-quark/pc/create_offline_task.py`.

### A.1 Hosts, credentials, request envelope

- qrypt talks to `https://drive.quark.cn/1/clouddrive/...` with the mount cookie only. The desktop
  client's `drive-pc.quark.cn` additionally requires signed headers (`mt`, `x-u-kps-wg`,
  `x-u-sign-wg`, `x-u-vcode`) injected by the native `netInterceptKey:"clouddrive"` layer; the web
  host verified working with cookie-only in live tests.
- Offline endpoints use `?pr=ucpro&fr=pc&api_ver=2` (`api_ver=3` for the deferred `save_to/progress`).
- wg-encrypted requests carry `Content-Type: application/json` and `X-U-Content-Encoding: wg`.
- Responses observed as plaintext JSON (`code == 0` success); a `decryptResponseInterceptor`
  exists for wg-marked responses and the adapter must decode that case too.

### A.2 Request body encryption (wg)

```text
cipher_b64 = base64( [wsgNum 2 bytes big-endian] + AES-128-CBC(key=data_key, iv=data_key, PKCS7(plain)) )
data_key   = md5( AES-128-CBC-decrypt(entry48, key=master[:16], iv=master[:16]), PKCS7-stripped )[:16]
master     = "d0fa89f19af4fe4c106625c4742fdee0"   (first 16 bytes)
```

- `plain` is the raw JSON string, never base64.
- Entries are 48 bytes (32B ciphertext + 16B IV) at binary `0xdbfb9e8` for wsgNum
  13800/13801/14100/14199/65535; production `secureNo` is `14100` → key `e21247a68938d36f`.
- Golden vector: `encrypt("hello", 14100) == "NxTFGiL/qySwJSpexPNDuS1r"`.
- Keys are version constants; expose a configuration override and keep the re-extraction procedure
  documented (search the master string in `__TEXT`, walk back 5×48B entries, AES-decrypt, md5[:16]).

### A.3 Source normalization

- The client recognizes `http/https/ftp/magnet/ed2k/thunder/flashget/qqdl`; only `magnet:` is
  treated as BT (`isMagnet`).
- A local `.torrent` is read locally, its info hash computed, and turned into
  `magnet:?xt=urn:btih:<hex>` — the torrent bytes are never uploaded. The client keeps only the
  hash; qrypt additionally carries `dn` and `tr` from the torrent metadata.
- For magnet/BT scenarios `parse_mode = 0`; `entry` is always `"download"` in this client version
  (the `save_to` entry belongs to the separate cloud-collection flow).

### A.4 Parse

```text
POST /1/clouddrive/offline/download/parse?api_ver=2
{
  "url": "<magnet|url>", "fid": "<in-drive torrent fid, source=0 only>", "title": "<optional>",
  "parse_mode": 0, "cookie": "", "referer": "<page>", "page_url": "<page>", "entry": "download",
  "req_info": {"method": "", "body": "", "is_multipart": false},
  "conflict_mode": 4, "save_path": "<optional, usually absent>", "auto_download": false,
  "support_v2_play": true, "resource_hint": "<optional>"
}
```

Response:

```json
{ "token": "<...>_m", "finished": true,
  "files": [{"file_no": 0, "file_name": "...", "size": 3405469696, "path": "...",
             "torrent_name": "...", "file_dirs": ["..."], "format_type": "..."}],
  "title": "...", "total_size": 3405469696,
  "resource_info": {"download_id": "...", "download_task_finish": 1,
                    "detail_error_code": 0, "can_retry": 1} }
```

- `finished=false` → client waits 2s and re-parses once; qrypt retries within a bounded window.
- `download_task_finish=1` → instant/accelerated availability.
- `detail_error_code=9622` → no cloud cache, cannot instant-save (the task is still creatable).
- In parallel the client calls `POST https://res-pt.quark.cn/api/collect/prd {url,parse_mode,referer,page_url}`
  to learn the same `download_task_finish`/`download_id`.

### A.5 Staging directory

- Default: `POST /1/clouddrive/file/system_path {"scene":"offline_bt","init":true}` returns the fid
  of `来自：BT磁力链下载`; non-magnet links use `scene:"offline"` (`来自：云收藏`).
- A user `--parse-to` resolves the mount-relative path to a fid. Missing directory + `--mkdir` is a
  create; the adapter MUST reuse qrypt's existing Quark path resolution / `Mkdir` (which already
  drives Quark's `POST /1/clouddrive/file` `dir_path` behavior) instead of hand-rolling it.

### A.6 Submit

```text
POST /1/clouddrive/offline/download/submit
{ "token": "<parse token>", "entry": "download", "parse_mode": 0, "pdir_fid": "<dir fid>",
  "auto_select": false, "selected_files": [0,1,2], "title": "<optional>" }
```

- `selected_files` are `file_no` values; folder entries may be comma-joined (`"3,4,5"`).
- Response carries `task_id` (and `fid`/`dir_fid`); the client timeout is
  `10s × ceil(files/1000)`.
- The client only uses `submit` when `files.length > 1 || isMagnet(url) || fid`; otherwise it uses
  the safe path below. qrypt v1 uses `parse + submit` uniformly and documents the divergence.

### A.7 Safe path (client-only; deferred)

Single non-magnet link: re-`parse` with `{token, auto_download:true, save_path:"<relative>",
support_smooth_progress:1}`, then poll
`POST /1/clouddrive/offline/save_to/progress?api_ver=3 {task_ids, query_times, req_scene:"poll_wait", support_v2_play:true}`.
Progress rules: `status==2 || progress>=1000` done; `3` failed; `4` canceled; interval
`metadata.tq_gap || 1000ms`; 30-poll cap. Not implemented in v1.

### A.8 Observation

- `GET /1/clouddrive/task?task_id=<id>` returns
  `{task_id, event_id, task_type: 5, task_title, status, progress (0..1000), created_at,
    finished_at, offdl_result: {list: [{fid, file_name, pdir_fid, size, ...}]}}`.
- `task/current/list?task_type=5&status=0,1,2,3,4` exists, but the `status` filter is not reliably
  honored on the web host, so poll by `task_id`.
- Status mapping: `0 NEW → queued`, `1 RUNNING → running`, `2 FINISH → succeeded`,
  `3 ERROR → failed`, `4 CANCEL → canceled`.
- A missing task is HTTP `404` / code `14013`.
- `metadata.tq_gap` is ~500ms; the desktop client polls cloud tasks at `max(tq_gap, 5s)`.

### A.9 Download URL (accelerated and normal)

- Normal: `POST /1/clouddrive/file/download {fids, support_error_code:["41060"]}` returns
  `data[].download_url` — a signed URL valid ~30 minutes, refreshed when missing or expired.
- Accelerated: `submit` with `parse_mode = 22 (SPEEDUP_DOWNLOAD)` returns
  `speedup_dl_info.speedup_session`; then
  `POST /1/clouddrive/file/download {fids, parse_mode:22, speedup_session}` returns the accelerated
  URL. A session is not always returned; qrypt falls back to the normal URL after the provider task
  completes.
- Both paths require the source to be staged in the drive first; there is no peer-to-URL bypass.

### A.10 Cancel and retry

- `POST /1/clouddrive/task/status {task_ids:[...], op_type:0}` deletes (used as cancel);
  `op_type:1` re-queues in place (same `task_id`). There is no pause; status `4` is canceled.

### A.11 Provider error codes → qrypt offline codes

| Quark code | qrypt code | retryable |
|---|---|---|
| 31001 / 31004 / 43004 | `offline_auth_required` | no |
| 32005 / 32006 / 32018 / 32022 | `offline_quota_exceeded` | no |
| 25005 | `offline_source_invalid` | no |
| 14014 / 14016 / 22001 | `offline_source_forbidden` | no |
| 32003 | `offline_space_insufficient` | no |
| 32004 / 25003 / 25004 | `offline_file_too_large` | no |
| 32007 | `offline_duplicate` | no |
| 15000 | `offline_provider_unavailable` | yes |
| 14013 | `offline_provider_task_missing` | yes |
| 31007 | `offline_crypto_error` | no |
| unmapped | `offline_provider_error` | no |

### A.12 Client code map (evidence)

- `modal-parse-resource.js`: module `962684` (AppSource/ParseMode/entry/labels), `299829`
  (`parse`/`submit`/`progress`/`predict`), `488417` (`offlineRequest` wg wrapper), `950089` (cloud
  model: `parse`/`submit`/`save`/`download`), `476193` (`isMagnet`/`getFiles`/`createHttpParseResult`).
- `main.js`: `20878` (`openModalParseResource`), `521237` (`quarkDownloadsPrivate.download` wrapper),
  `65305` (`modal-create-magnet`), plus the `registerUriSchemeSwitch{magnet,torrent}` preference.
- `index.js`: transport store `createCustomTask`/`createSpeedupTasks`/`createNormalTasks` and
  `downloadManager.shipTask`.

### A.13 Live-verified notes

- `drive.quark.cn` + cookie + wg works for parse/submit/task query; `drive-pc.quark.cn` returns
  `401 / 31001 require login [guest]` without signed headers.
- Both magnet and direct-HTTP sources succeed through `parse + submit`; a 1 MiB HTTP task completed.
- `file/system_path` requires `Content-Type: application/json` and is intermittently 5xx (retry).
- `task/count` showed status `4` (canceled) as the most common historical state, confirming the
  5-state model.
- Submit success does not imply a downloadable resource: one magnet returned `task_id` then
  `status=3` within ~7ms.
