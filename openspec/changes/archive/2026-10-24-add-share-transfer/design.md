# Add share links and receive (转存) — design

## Context

qrypt already has the two shapes this feature needs:

- **provider-neutral operations with destinations resolved through mounts** — `offline` selects a
  provider by the parse-destination mount and never exposes provider names;
- **durable, recoverable provider-backed tasks** — `offline_download` / `offline_fetch` persist
  their detail, recover on restart, and retry by re-running the runner.

Sharing adds two directions:

```text
               ┌────────────── share ───────────────┐
  my files ───▶│ create link · list · update · rm  │───▶ https://pan.quark.cn/s/<link-id>
               └────────────────────────────────────┘

               ┌───────────── receive ──────────────┐
  link ───────▶│ inspect · save (转存) · fetch      │───▶ my drive directory / local path
               └────────────────────────────────────┘
```

The Quark provider flow behind both directions is verified live and recorded in the appendix.

## Goals

- A user can hand out a link to content already in their drive, with expiry, passcode, and visit
  limits, without naming a provider.
- A user can look inside someone else's link, copy selected content into their own drive
  (provider-side, no byte round trip), or download it locally — including "download without
  saving".
- Both directions are observable, cancelable, retryable, and survive a Core restart.
- No provider identifier, driver name, socket path, or provider task id is required input.

## Non-goals

- Cross-provider receive (a Quark link saved into an Aliyun drive). v1 rejects it explicitly.
- Sharing to a person, group, or chat; only link sharing exists in the provider-neutral contract.
- Downloading a share into the local tree through the provider's web page semantics (preview,
  online play, thumbnail) — only whole-file transfer.
- A provider-neutral "shared with me" inbox: receiving is always an explicit link the user passes
  in. (Providers that expose a received-share list can add it later behind a capability.)

## Decisions

### Two CLI groups: `share` and `receive`

`share` is about links the user owns; `receive` is about links someone else owns. Splitting them
keeps `list` unambiguous (links vs. receive tasks), matches the two directions of the feature, and
lets `receive` mirror `offline`'s task verbs (`inspect/list/show/cancel/retry/wait`) without
overloading `share`.

Rejected: one `share` group with `save`/`fetch` mixed in (ambiguous `list`), and a nested
`share receive` (one more level, inconsistent with `offline`).

### Link management is synchronous; only save and fetch are durable tasks

A share link is an immediate result: create, update, and delete are provider calls that finish in
about a second, and the user needs the URL in the same breath. Making them durable tasks would add
a type, a runner, recovery, retry policy, and CLI verbs for no user-visible benefit. A provider that
answers asynchronously (Quark returns `task_sync:false` for passcode links) is polled **inside the
driver** with a bounded timeout (30s, then a typed `share_timeout` error); the user can always
re-run, and `share list` shows the truth.

`save` and `fetch` are different: a provider-side copy of a large tree can take minutes, is
observable with progress, can fail on quota or visit limits, and must survive a restart. They are
durable tasks.

### v1 is same-provider only

A native save is a provider-side copy; it only exists inside one provider. `--to` must belong to the
same provider as the link. A cross-provider destination is rejected with
`share_cross_provider_unsupported`, and the error hints at the composition that already works today:

```bash
qrypt receive fetch LINK --download-to ./tmp
qrypt fs put ./tmp /aliyun/inbox
```

Implementing cross-provider receive properly (download → scratch → upload → cleanup → merged
progress → rollback) is a separate feature with its own failure modes; it is additive later.

### A share entry carries an opaque provider ticket

Quark requires a per-entry `share_fid_token` to save; other providers may require nothing, a
signed handle, or a per-directory token. The neutral contract therefore keeps the transfer ticket
**on the entry**, opaque to qrypt, rather than adding a provider-specific field to `Entry` or a
global "token list" the core would have to keep aligned with a parallel slice. Callers that do not
need it leave it empty.

### Save is per-entry and may be synchronous

Providers disagree about what a save *is*:

- Quark returns **one provider task** with a status and a result list;
- Aliyun answers a **synchronous batch** call with one result per file;
- Baidu and 115 answer **per file** (`errno` / `state`), with no task at all;
- OneDrive returns an **asynchronous copy** addressed by a monitor handle, not by a task id.

The neutral contract is therefore **per-entry outcomes plus an optional opaque wait handle**, not
"a task": `ShareSaveOutcome{Handle, State, Progress, Items[]}` where each item carries its source
id, its saved entry, or its own error. A driver that is synchronous returns `Handle == ""` and a
complete outcome; a driver that is asynchronous returns a handle the core polls through
`GetShareSave`. Partial success is a first-class state, not a whole-batch failure.

### Provider policy is declared, not assumed

The share policy surface is not uniform:

| Option | Quark | Aliyun | OneDrive | 115 / Baidu / 189 |
| --- | --- | --- | --- | --- |
| passcode | optional | **required** (always 4 chars) | **unsupported** | required (receive code / pwd / access code) |
| expiry | 1/7/30/∞ (+60) | `permanent`/`1d`/`7d`/`30d` | optional timestamp | days, `0` = permanent |
| visit limit | yes | no | no | no |
| update | yes | partial | n/a | limited |
| my-links list | yes | yes | **no** (permission list only) | yes (form-encoded) |

A driver therefore declares a `SharePolicy` (passcode support, the expiry set it accepts, visit
limit, update, list, rename policy), and the core validates a request against it **before** the
provider call. The CLI reads the same declaration for help text and early rejection. Without this,
the second provider either loses options silently or forces the core to special-case each driver.

### Identity is the driver-normalized link id, not the URL

The same link is reachable through several hosts (Aliyun `alipan.com` and `aliyundrive.com`, 123
mirrors, OneDrive `1drv.ms` and `onedrive.live.com`). `ShareLink.LinkID`, normalized by the
driver's parser, is the identity used for de-duplication and in task detail; the raw URL is
display-only.

### The link's provider is inferred; the acting mount is explicit or unique

The user never names a provider, but qrypt must know **which account** acts:

| Command | Acting mount |
| --- | --- |
| `share create PATH...` | the mount in the mount-qualified path |
| `share list/show/update/remove` | `--mount`, or the only configured mount |
| `receive inspect LINK` | the link's provider; `--mount` picks the account when that provider has several mounts |
| `receive save LINK --to /MOUNT/DIR` | the `--to` mount (must match the link's provider) |
| `receive fetch LINK --download-to PATH` | the link's provider; `--mount` picks the account |

If a provider has several mounts and the command needs one, qrypt fails with a usage error listing
the candidates instead of guessing. Guessing would pick the wrong account and silently save content
into someone else's drive.

### Passcodes and session tokens are secrets

A passcode is user input; a session token is a provider credential. Both are persisted in task
detail because a recovered fetch must reopen the link without asking again, but:

- `show --json` and human output redact the passcode as `****` unless `--show-secrets` is passed;
- logs never print the passcode, the session token, or the per-entry ticket;
- `share show` **does** print the passcode of the user's own link (`share/password`), because that
  is the link's own metadata and the user needs it to share it.

### The offline transfer core is extracted, not copied

`offline_fetch.go` contains the measured transfer path (bounded-range chunking, 4 workers × 8 MiB,
resume at the contiguous prefix, writer selection, progress accounting, URL refresh). Share fetch
needs exactly that. The core is moved to `pkg/core/transfer.go` with an interface the two runners
satisfy; neither runner keeps a copy. The measured tuning bounds (`1–16` workers, `1–64 MiB`
chunks, defaults `4 × 8 MiB`) move with it and stay shared.

## Drive contract

New optional capabilities (`pkg/drive`):

```go
CapabilityShare        Capability = "share"          // link management
CapabilityShareReceive Capability = "share_receive"  // open + browse a link
CapabilityShareSave    Capability = "share_save"     // provider-side save
CapabilityShareFetch   Capability = "share_fetch"    // direct download of share content
```

Interfaces:

```go
// Link recognition: the CLI never names a provider.
type ShareLinkParser interface {
    ParseShareLink(raw string) (ShareLink, bool)
}

type ShareManager interface { // CapabilityShare
    CreateShare(ctx context.Context, req ShareCreateRequest) (Share, error)
    ListShares(ctx context.Context, req ShareListRequest) (SharePage, error)
    GetShare(ctx context.Context, id string) (Share, error)
    UpdateShare(ctx context.Context, req ShareUpdateRequest) (Share, error)
    DeleteShare(ctx context.Context, id string) error
}

type ShareReceiver interface { // CapabilityShareReceive
    OpenShare(ctx context.Context, link ShareLink) (ShareSession, error)
    ListShareEntries(ctx context.Context, session ShareSession, dir SharePath) (ShareEntryPage, error)
}

type ShareSaver interface { // CapabilityShareSave
    // SaveShareEntries either completes synchronously (Outcome.Handle == "") or
    // returns an opaque handle to poll through GetShareSave. Items may already
    // carry per-entry failures when the provider reports partial success.
    SaveShareEntries(ctx context.Context, req ShareSaveRequest) (ShareSaveOutcome, error)
    GetShareSave(ctx context.Context, handle string) (ShareSaveOutcome, error)
}

type ShareFetcher interface { // CapabilityShareFetch
    ShareDownloadTargets(ctx context.Context, req ShareFetchRequest) ([]DownloadTarget, error)
}

// SharePolicy is what a driver declares about its link surface. The core
// validates requests against it; the CLI renders help and rejects early.
type SharePolicy struct {
    Passcode   PolicySupport   // required | optional | unsupported
    Expiries   []time.Duration // accepted set; 0 means permanent
    VisitLimit bool
    Update     bool
    List       bool
    Rename     RenameSupport   // auto | overwrite | fail
}

func SharePolicies(d Driver) SharePolicy // zero value = no share surface
```

Neutral types (`pkg/drive/share.go`):

```go
type ShareLink struct {
    URL      string // as supplied, display only
    LinkID   string // driver-normalized identity (Quark: pwd_id)
    Passcode string
}

type ShareDestination struct {
    Entry  Entry        // resolved destination directory; Extra may carry drive/space ids
    Rename RenamePolicy // policy for name collisions
}

type ShareSaveRequest struct {
    Session ShareSession
    Entries []ShareEntry
    Dest    ShareDestination
}

// ShareSaveOutcome is per-entry: providers report partial success far more
// often than whole-batch success.
type ShareSaveOutcome struct {
    Handle   string          // "" = finished; otherwise poll GetShareSave
    State    SaveState
    Progress float64         // best effort; 0 when the provider reports none
    Items    []ShareSaveItem
}

type ShareSaveItem struct {
    SourceID string // ShareEntry.ID
    Saved    *Entry // nil when this entry failed
    Error    *Error // per-entry provider error, neutralized
}
```

type Share struct {
    NativeID   string // management identity (Quark: share_id)
    LinkID     string // link identity (Quark: pwd_id)
    URL        string
    Title      string
    Owner      ShareOwner
    Public     bool      // no passcode required
    Passcode   string    // owner view only
    ExpiresAt  time.Time // zero = permanent
    VisitLimit int
    VisitCount int
    EntryCount int
    Size       int64
    CreatedAt  time.Time
    UpdatedAt  time.Time
    Risk       string
}

type ShareEntry struct {
    Entry
    Ticket string // opaque per-entry save ticket (Quark: share_fid_token)
    RootID string // share-relative directory the entry lives in
}
```

`OpenShare` returns a `ShareSession` (link + opaque token + metadata). A passcode-protected link
that was opened without (or with a wrong) passcode MUST return `ErrSharePasscodeRequired` /
`ErrSharePasscodeWrong` so the CLI can prompt and retry instead of showing a provider message.

## Task model

| Type | Operation | Creation path | Persistent | Recoverable | Retry |
| --- | --- | --- | --- | --- | --- |
| `share_save` | `share` (new) | `CreationShareSave` | yes | yes | `RetryManager` |
| `share_fetch` | `download` | `CreationShareFetch` | yes | yes | `RetryManager` |

- `OperationShare` is a new stable operation kind because a provider-side copy of share content is
  neither a local download nor a within-drive copy; `OperationKind` is persisted and feeds
  `durableKey`, so reusing `copy`/`download` would mislabel history.
- `share_fetch` keeps `download` because its observable effect is "bytes arrive at a destination";
  it mirrors `offline_fetch`, including the cleanup policy.
- Startup recovery re-runs the runner for non-terminal tasks of both types; the runner rebuilds
  provider work from persisted detail (link, session, selection, destinations, provider ids).

Persisted detail (`task.ShareDetailKey`):

```go
type shareDetail struct {
    Link       string // raw link, display only
    LinkID     string // driver-normalized identity; with Mount it is the dedupe key
    NativeID   string
    Passcode   string // secret: redacted on output, never logged
    Mount      string // acting mount
    Owner      string
    Title      string
    Selection  shareSelection
    ToMount    string
    ToDir      string
    ToRemoteID string   // resolved destination directory
    SaveHandle string   // opaque provider wait handle ("" when the save was synchronous)
    SaveItems  []saveItem // per-entry outcome, including per-entry failures
    Saved      []shareEntry
    DownloadTo string
    DownloadMount string
    DownloadDir   string
    KeepCloud  bool
    Workers    int
    Chunk      int64
    Phase      sharePhase // open → list → save → download → cleanup → complete
    Observation shareObservation
}
```

`taskDurableKey` currently digests only the offline detail key; it MUST digest share detail too
(otherwise a phase change in a share task is not journaled).

## Creation and selection rules

- `share create PATH...`: every path MUST be mount-qualified (`/MOUNT/DIR/...`), existing, and on
  one mount. Paths on different mounts create separate links and are rejected in one invocation
  (a link is provider-scoped).
- `share create` options: `--expires 1d|7d|30d|forever` (provider may accept more; the neutral set
  is the intersection), `--passcode[=CODE]` (no value → generate 4 characters; provider validates
  the format), `--limit-visits N` (provider optional; unsupported providers reject a non-zero
  value), `--name TITLE`.
- `receive save LINK`: destination is `--to /MOUNT/DIR` or `--mount NAME` (root). A missing
  directory fails unless `--mkdir`. The destination mount's provider MUST equal the link's
  provider.
- `receive fetch LINK`: `--download-to PATH` is required. With `--to`, qrypt saves first and then
  downloads the saved entries; without it, it downloads the selected share entries directly. When
  both are present, cleanup after a successful download removes only the entries qrypt saved,
  unless `--keep-cloud`.
- The receive **cloud** destination MUST NOT be an encrypted mount: a provider-side copy never
  passes through qrypt, so the bytes would land unencrypted in a directory whose convention is
  ciphertext. A local download destination inside an encrypted mount stays allowed (those bytes are
  written through qrypt), and creating a link **from** an encrypted mount is allowed.
- Selection and tuning reuse the offline shapes: `all` (default), `largest[:N]`, `include/exclude`
  globs; `--workers` and `--chunk-size` use the shared bounds.
- De-duplication follows offline: the same link, resolved mount, selection, and destination while a
  matching task is active returns the existing task; `--new` bypasses; an explicit idempotency key
  is honored.

## Receive lifecycle

`share_save`:

```text
create → open link (bounded retries for transient provider errors)
       → list entries at the selected directory (paged)
       → apply selection
       → provider save; persist the opaque wait handle (if any) before polling
       → if a handle came back: poll GetShareSave to a terminal outcome
       → fold per-entry outcomes: saved entries vs per-entry failures
       → verify destination: re-list the target directory until saved entries appear
         (bounded attempts) or the provider's own result already carries them
       → succeed when every selected entry was saved, partially fail when some were not,
         fail when none were
```

Because providers report partial success (Aliyun batch, Baidu `extra.list[].errno`, 115 per file),
the task maps "all saved" to succeeded, "some saved" to `StatePartialFailed`, and "none saved" to
failed. The task's items carry every selected entry with its individual result.

`share_fetch`:

```text
create → (optional) save phase as above, reusing the same verification
       → resolve download targets: saved entries (provider URL or staged read) or direct share URLs
       → transfer with the shared chunked core (resume, workers, chunk, progress)
       → (optional) cleanup the entries qrypt saved, unless --keep-cloud
       → complete
```

Verification matters: provider listings are eventually consistent, and a "successful" save whose
entries never appear would otherwise be reported as success. Offline fetch already learned this
lesson for its destination; share save reuses the same bounded-retry verification.

Cancellation: cooperative through the task cancel path; a provider save that is still running is
not deleted (it is the user's own copy operation), but the task stops observing it and reports
canceled. Retry: re-run the runner from persisted detail — reopen the link, re-list, and save again;
a completed save is not repeated.

## Control surface

Mirrors `offline`:

```text
POST   /v1/control/share/tasks                 create (download_to → share_fetch, else share_save)
GET    /v1/control/share/tasks                 list (types, mount, state, limit)
GET    /v1/control/share/tasks/{id}
POST   /v1/control/share/tasks/{id}/cancel
POST   /v1/control/share/tasks/{id}/retry
POST   /v1/control/share/inspect               read-only browse
GET    /v1/control/share/links                 my links
POST   /v1/control/share/links                 create a link
GET    /v1/control/share/links/{id}            by native id or link id
PATCH  /v1/control/share/links/{id}            update
DELETE /v1/control/share/links/{id}            remove
```

Create input (`ShareCreateRequest`): `link`, `passcode`, `to`, `download_to`, `mount`, `selection`,
`mkdir`, `keep_cloud`, `workers`, `chunk_size`, `new`, `idempotency_key`, `title`. No provider,
driver, socket, or provider task id.

Errors use the same classified transport shape as offline (`ShareErrorClass`: usage / not_found /
runtime) so the CLI can choose an exit code without parsing text.

## CLI surface

```text
qrypt share create PATH... [--mount M] [--name T] [--expires 1d|7d|30d|forever]
                           [--passcode[=CODE]] [--limit-visits N] [--json]
qrypt share list   [--mount M] [--all] [--limit N] [--json]
qrypt share show   <LINK|SHARE_ID> [--show-secrets] [--json]
qrypt share update <SHARE_ID> [--name T] [--expires D] [--passcode[=CODE]] [--public] [--json]
qrypt share remove <SHARE_ID>...

qrypt receive inspect LINK [--passcode CODE] [--mount M] [--json]
qrypt receive save    LINK (--to /MOUNT/DIR | --mount M) [--select ...] [--mkdir]
                           [--no-wait] [--json]
qrypt receive fetch   LINK [--to /MOUNT/DIR] --download-to PATH [--passcode CODE]
                           [--keep-cloud] [--workers N] [--chunk-size SIZE] [--select ...]
                           [--no-wait] [--json]
qrypt receive list|show|cancel|retry|wait ...      # same verbs and semantics as offline
```

- `--passcode` without a value reads one line from stdin (keeps it out of shell history); a
  `?pwd=` in the link is equivalent.
- Exit codes reuse the offline set: `2` usage, `1` failure, `130` interrupt.
- `receive save` and `receive fetch` block until the destination is written; `--no-wait` returns
  once the task is admitted.

## Error codes

| Neutral code | Meaning | Quark source |
| --- | --- | --- |
| `share_link_invalid` | the link cannot be parsed or names no link id | — |
| `share_link_provider_mismatch` | the acting mount is a different provider | — |
| `share_cross_provider_unsupported` | v1 same-provider rule | — |
| `share_unsupported` | the mount's driver lacks the capability | — |
| `share_not_found` | no such link (or removed) | 41006 |
| `share_expired` | the link expired | 41019 |
| `share_passcode_required` | a passcode is needed | 41008 |
| `share_passcode_wrong` | the passcode is wrong | 41007 |
| `share_own_save` | saving one's own link | 41017 |
| `share_ticket_invalid` | entry ticket missing/stale/mismatched | 41020 |
| `share_visit_limit` | the link's visit limit was reached | 45058 |
| `share_quota` | not enough space, or the save-count limit | 32003 / 41014 / 41035 |
| `share_verification_required` | the provider demands a human verification step (captcha/SMS) that a cookie cannot satisfy | provider-specific |
| `share_rate_limited` | the provider throttled the account; retryable after a backoff | provider-specific |
| `share_timeout` | synchronous provider work did not finish in its bounded window | — |
| `share_destination_unverified` | qrypt could not confirm the saved entries appeared in the destination | — |
| `share_provider_error` | the provider failed without a code qrypt maps onto a neutral one | unmapped |

## Provider variation

The contract was checked against every driver qrypt ships. Shapes below are the expected provider
behaviour from public API documentation; each is verified when its adapter is implemented.

| Driver | Share surface | Expected shape |
| --- | --- | --- |
| `quark` | yes (verified live) | `pwd_id`; passcode optional; one provider task; destination = folder fid; has a my-links list; direct download needs the PC-client UA |
| `aliyundrive` | yes | `share_id`; passcode **required** (4 chars); **synchronous batch** with per-file results; destination = `drive_id` + `parent_file_id`; expiry as strings |
| `baidu_netdisk` | yes | `surl` + pwd; per-file `errno`; destination = target path; form-encoded; often needs a `vcode` captcha |
| `115` | yes | `share_code` + `receive_code`; **per file**; destination = `cid`; `state: true/false`; aggressive throttle and human verification |
| `115_open` | no | open-platform API has no share surface → declares no capability |
| `123` | yes | `shareKey` + `SharePwd`; per file; destination = `parentFileId` |
| `yun139` | yes | share id + access code; per file; destination = parent id |
| `189` | yes | `shareCode` + `accessCode`; per file; destination = `targetFileId` |
| `onedrive`, `onedrive_app` | yes (Graph) | share id = base64url of the link; **no passcode**; **asynchronous copy with a monitor handle**; destination = `driveId` + item id; no my-links enumeration |
| `webdav`, `sftp`, `s3`, `localfs` | no | no capability; the CLI reports `share_unsupported` |

What this changed in the contract:

- save outcomes became **per-entry with an optional wait handle** (Quark task / OneDrive monitor /
  Aliyun synchronous batch / 115 and Baidu per file);
- the destination became a resolved **`ShareDestination`** (entry + rename policy) so providers that
  need a drive/space id can carry it in `Entry.Extra`, and providers with clone/overwrite semantics
  can honour them;
- link options became a declared **`SharePolicy`**, so mandatory or unsupported passcodes, different
  expiry sets, missing my-links lists, and missing update become validated capability facts rather
  than core special cases;
- identity moved to the driver-normalized **`LinkID`**, because the same link has many URLs;
- two extra error codes cover human verification and throttling, which no cookie can bypass.

What already generalizes: per-driver link parsing, the opaque session token (Aliyun share token,
189 share token, Baidu randsk, OneDrive share id), the optional per-entry ticket, passcode as a
neutral string, selection, mount-based account choice, the fetch path (targets already carry
headers), and the durable task/recovery model.

`pkg/core/share_contract_test.go` is the executable checklist for the next adapter: one fixture is
a mandatory-passcode provider with a synchronous batch, partial per-entry results, and an opaque
destination id; the other is a passcode-less provider with a monitor handle and no link-management
surface. Both run through the same Core creation, verification, and folding paths. A new adapter
therefore needs only its driver methods, capability registration, and `SharePolicy`; `pkg/core`,
`pkg/control`, and the CLI stay unchanged, and a policy-declared flag becomes visible without a new
code path.

## Risks / trade-offs

- **Same-provider restriction** is a product limit, not a technical one. Documented with the
  two-step workaround; lifting it later is additive.
- **Direct download depends on provider anti-hotlink policy.** Quark only hands out share download
  URLs to its own PC client UA; qrypt's adapter therefore sends that UA for this path. If the
  provider changes this, `receive fetch --download-to` degrades to a clear provider error while
  `receive save` keeps working — hence the capability split.
- **Synchronous link management** means a provider queue longer than the bounded window surfaces as
  `share_timeout` even though the link may appear later. `share list` is the recovery path; this is
  the price for not adding a fourth task family.
- **Secrets in task detail** are needed for recovery. They are redacted on output and never logged,
  but anyone who can read the Core state directory can read them; the task store is already
  sensitive by construction.
- **Provider dedupe of saves**: Quark returns the previous task id for an identical repeated save.
  The runner therefore never assumes "one request, one new copy"; results come from the save task's
  own `save_as` payload.

## Migration plan

- Additive: no existing task type, operation kind, route, or CLI command changes meaning.
- `taskDurableKey` gains a share-detail digest; existing tasks keep their current digest because
  they carry no share detail key.
- The transfer-core extraction is behavior-preserving for offline fetch; the offline test suite and
  the chunk/resume tests must pass unchanged.

## Open questions

1. Should `share list` show only the acting mount's account, or merge every mount with a provider
   column? (v1: `--mount` selects one; the default is the unique mount.)
2. Should a received-but-saved share be recorded for later "update available" checks (Quark's
   `share/update_list` / `inc_update_list`)? Deferred; it needs a new persisted relationship.
3. Should `receive fetch --download-to` default to the terminal's current directory when `--to`
   is absent? v1 requires an explicit path.

## Appendix: Quark share flow (verified live, 2026-10-18)

Hosts `drive-pc.quark.cn`, `drive.quark.cn`, `drive-m.quark.cn`, `pan.quark.cn` all answer; the
calls are plain JSON (no wg body encryption), authenticated by the mount's cookie. See
`charles-quark/pc/SHARE_SAVE_FLOW.md` for full request/response samples.

| Step | Request | Notes |
| --- | --- | --- |
| create | `POST /1/clouddrive/share` `{fid_list,title,url_type,passcode,expired_type[,visit_user_limit]}` | `url_type` 1 public / 2 passcode (exactly 4 chars); `expired_type` 1 forever, 2 1d, 3 7d, 4 30d (server also accepts 5 = 60d); `title` is ignored — the server uses the first item's name; ≤100 files per link |
| create result | `data.task_resp.data.share_id`, `creation_snapshot{success_count,failed_count}` | `task_sync:false` for passcode links → poll `/task` |
| read link id | `GET /share/mypage/detail` or `POST /share/password {share_id}` | `pwd_id`, `share_url`, `passcode`, `expired_*`, visit counters |
| update / delete | `POST /share/update {pwd_id,...}`, `POST /share/delete {share_ids}` | delete takes `share_id`, link access takes `pwd_id` |
| open | `POST /share/sharepage/token {pwd_id,passcode}` | guests: 41008 without code, 41007 wrong code; a logged-in owner may pass without/with a wrong code (do not rely on it) |
| browse | `GET /share/sharepage/detail?pwd_id&stoken&pdir_fid=0&_page&_size&ver=2` | each entry carries `share_fid_token`; `ver=2` makes deleted content answer 404/41004; root empty list otherwise means "deleted" (client maps to 41009) |
| save | `POST /share/sharepage/save {fid_list,fid_token_list,to_pdir_fid,pwd_id,stoken,pdir_fid,scene:"link",exclude_fids:[]}` | returns `task_id`; own links → 41017; wrong/missing ticket → 41020; anonymous → 401 |
| save result | `GET /1/clouddrive/task?task_id&retry_index` | `task_type` 17 = share-save; `status` 2 = done; `save_as.save_as_top_fids` = new fids; `metadata.tq_gap` = poll interval |
| direct download | `POST /1/clouddrive/file/download` body `{fids,pwd_id,stoken,fids_token,support_error_code:["41060"]}` | returns signed `download_url`; **requires the PC-client User-Agent**, other UAs → `23018 download file size limit`; must be logged in |
| saved-from-share listing | `POST /share/v2/saveas/list` | entries carry `save_as_source:true`, `file_struct{fir_source:"saveas",sec_source:"share_save"}` |

Enums: `AsyncTaskStatus` 0 todo / 1 running / 2 finish / 3 fail / 4 pause. A share whose content was
deleted still returns a token; only `detail` reveals it.

### Live smoke test

Run against a real Quark mount after `qrypt mount` is up (the same sequence used for the live
verification above):

```sh
# export: public link, then a passcode link
qrypt share create /quark-download/report.pdf --expires 7d
qrypt share create /quark-download/report.pdf --expires forever --passcode=ab12 --limit-visits 5
qrypt share list --mount quark-download
qrypt share show <LINK_ID> --show-secrets

# receive: inspect, save, then save-then-download and cleanup
qrypt receive inspect <URL>
qrypt receive save <URL> --to /quark-download/inbox --mkdir
qrypt receive fetch <URL> --to /quark-download/inbox --download-to /tmp/qrypt-smoke
qrypt receive list --all

# cleanup
qrypt share remove <LINK_ID>
```

Expected: `share create` returns `pwd_id`/`share_url` synchronously (passcode links poll
`task_sync:false`); `receive save` ends `succeeded` and `share list` shows the destination entries;
`receive fetch --keep-cloud` leaves them, while the default run removes only the entries it saved.
A direct `receive fetch --download-to` leaves the provider unchanged. Re-running a completed
receive creates a new task; re-running an active one returns the existing task.
