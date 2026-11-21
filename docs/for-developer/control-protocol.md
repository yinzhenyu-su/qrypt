# Control Protocol

This document specifies the wire protocol between a qrypt Core and its clients.
It is written for people who build a client: the CLI data plane, the HTTP
gateway, the desktop and mobile shells, and third-party integrations such as the
DSH plugin. Read [Architecture](architecture.md) first for the layering rules.

Status: protocol `v1` is implemented as described in
[Implemented surface](#implemented-surface-protocol-v1). The error envelope,
capability discovery, the health aggregate and the task journal shipped in
0.7.2. Everything under [Planned additions](#planned-additions) is a proposal,
not shipped behaviour.

## Roles and boundaries

One storage root is written by exactly one runtime: the resident Core. It owns
the single-writer lease (`work_dir/owner.lock`), the VFS, the overlay, the
listing cache, the upload journal and the worker pool. Everything else is a
client.

| Component | Role |
| --- | --- |
| `qrypt core serve` | Hosts the Core and the control endpoint |
| `pkg/fsclient` | Adapts the control endpoint back into `vfs.FileSystem` for `qrypt fs ...` |
| `internal/httpapi` | Read-only HTTP gateway; a client of the Core, never a writer |
| `pkg/mobile` | In-process binding; calls `pkg/core` directly, no wire protocol |
| Third-party integrations | Clients of the control endpoint when they run on the same host |

Rules that follow from the single-writer model:

- A client must not open its own VFS against a storage root that a Core owns.
  The lease makes the second writer fail rather than corrupt state, but the
  client is still broken.
- Mutations go through the control endpoint. A client that writes files in the
  work directory directly bypasses the overlay, the journal and the cache.
- Clients never listen. The Core never listens on TCP; the only TCP surface is
  the opt-in gateway in `internal/httpapi`.

## Transport

The control endpoint is an HTTP/1.1 server on a unix socket:

```text
<run-dir>/control.sock     mode 0600, owner only
```

`CommandServer` rejects a TCP endpoint at construction
(`pkg/control/command.go:82-84`); the socket directory is created mode 0755, a
stale socket file is removed before listening (`command.go:91`, `command.go:94`),
and the listening socket is chmod 0600 (`command.go:103`).

The run directory is derived in this order (`pkg/core/core.go:543`):

1. `QRYPT_HOME` → `$QRYPT_HOME/run`
2. `storage.work_dir` from the config → `<work_dir>/run`
3. otherwise a per-configuration directory under the default run dir:
   `~/.qrypt/run/<first six bytes of sha256(absolute config path), hex>`

`core.ControlSocketPath(cfg, runtime)` returns `<run-dir>/control.sock`
(`pkg/core/core.go:529`). Two configs therefore never share one socket, which
is what lets several Cores run on one machine.

Clients should keep the connection alive and reuse it. Request latency on a warm
connection is sub-millisecond, while one connection per operation costs the
handshake every time.

## Identity and negotiation

`GET /v1/control/status` is the handshake. Its answer
(`pkg/control/command.go:31-41`):

| Field | Meaning |
| --- | --- |
| `pid` | Core process |
| `protocol_version` | Wire protocol version, currently `1` (`command.go:29`) |
| `version` | qrypt build version |
| `config_path` | Absolute config path the Core loaded |
| `fingerprint` | Config fingerprint; changes when the file changes |
| `work_dir` | Storage root |
| `started_at`, `uptime_seconds` | Process age |
| `busy` | Long-running maintenance or shutdown in progress |

`internal/cli/core.EnsureClient` (`internal/cli/core/core.go:298`) compares the
protocol version, build version, config path and fingerprint before handing out
a data-plane client, and tells the user to restart the Core on a mismatch. Any
other client must do the same check for the same reason: two builds or two
configs writing one work directory is the failure this protocol exists to
prevent.

Versioning rules:

- `protocol_version` increments only for a change that breaks existing clients.
- New endpoints, new response fields and new enum values are compatible
  additions; they do not bump the version.
- Clients must ignore unknown fields and unknown enum values.
- A client that needs a feature asks for it (see
  [`/v1/control/capabilities`](#capability-discovery)) instead of inferring it
  from the version or the build string.

## Conventions

**Methods.** State reads use `GET`; actions and creates use `POST`; resource
removal uses `DELETE`; a share link update uses `PATCH`. `/v1/fs/read` streams a
response body; an upload chunk `PUT` consumes a request body.

**Success bodies.** JSON. Lists are wrapped in an object so they can grow:
`{"path": "...", "entries": [...]}`, `{"mounts": [...]}`, never a bare array.
Mutating endpoints answer a small confirmation object such as
`{"path": "...", "ok": true}`.

**Paths.** Virtual absolute paths of the form `/<mount>/<rest>` on the command
endpoint, UTF-8, percent-encoded in query strings and decoded by the server
(`pkg/control/fs.go:196`). There is no scheme and no host part.

**Sizes and time.** Byte counts are `int64`. Timestamps are RFC 3339 UTC;
unset timestamps use the zero value `0001-01-01T00:00:00Z`, and clients must
render them as "never" rather than a date.

**Errors (v1).** Every failure answers a JSON envelope:

```json
{"error": {"code": "not_found", "message": "mount not found: nope",
           "retryable": false, "details": {"path": "/nope"}}}
```

The HTTP status stays coarse (it routes retries and monitoring); `code` is the
field clients switch on. `message` is human text for logs and prompts — never
parse it. `details` is optional and per-code.

| Status | Meaning | Typical codes |
| --- | --- | --- |
| 400 | Malformed request | `invalid` |
| 404 | Path or resource not found; also unknown `/v1/fs/<op>` | `not_found` |
| 405 | Wrong method | `invalid` |
| 409 | Operation failed at the backend or conflicted with current state | `conflict`, `forbidden`, `unsupported` |
| 429 | Client over a concurrency or rate budget | `rate_limited` |
| 503 | Service not installed on this Core (for example no mount service) | `unavailable` |
| 504 | Timeout or deadline exceeded | `timeout` |

Stable codes: `not_found`, `invalid`, `conflict`, `forbidden`, `unsupported`,
`unavailable`, `timeout`, `rate_limited`. Clients retry only when `retryable`
is true, and only with the same `X-Qrypt-Operation-Id`.

Status `409` is deliberately the only bucket where "the backend said no" and
"state conflicts" share a status; the code distinguishes them. Every
command-facing error is written by `pkg/control/errors.go`, and
`classifyFSError` in that file is the single source of both the status and the
code, so a client classifies failures by `code` and never parses prose.

**Idempotency.** Every mutating `/v1/fs/*` request may carry
`X-Qrypt-Operation-Id` (`pkg/control/operation.go:15`). The Core records the
successful response for ten minutes and replays it verbatim for a repeated id
(`operation.go:52`, `pkg/control/fs.go:112`). Mutations are `mkdir`, `remove`,
`rename`, `truncate`, `copy`, the upload session calls (`upload.begin`,
`upload.commit`, `upload.abort`) and the task actions. Only 2xx responses
are recorded, so retrying an operation that failed is still allowed, and a
client that retries a request over a fresh connection gets the same answer
instead of a second mkdir.

**Body limits.** An upload chunk carries at most 64 MiB. Larger files are a
sequence of offset chunks followed by one commit.

## Implemented surface (protocol v1)

Routes are registered in `pkg/control/command.go:110-128`. `GET` endpoints read
state; `POST`, `PATCH` and `DELETE` mutate — except the helpers that carry a body
(`/v1/fs/wait`, `/v1/fs/idle`, `*/inspect`, `*/wait`), which answer read-only
requests.

### Runtime

| Method | Path | Request | Response |
| --- | --- | --- | --- |
| GET | `/v1/control/status` | — | `RuntimeStatus` |
| POST | `/v1/control/shutdown` | — | `202`, empty body |

`shutdown` asks the Core to stop; it returns once accepted, and the process
exits asynchronously. A client that needs the files to be settled first calls
`/v1/fs/idle` and then shutdown.

### Capabilities

| Method | Path | Request | Response |
| --- | --- | --- | --- |
| GET | `/v1/control/capabilities` | — | `CapabilitiesResponse` |

`CapabilitiesResponse` is `{protocol_version, version, features, mounts,
write_bytes}` (`pkg/control/surface.go`). The `fs.*` features are always
present because those routes are registered unconditionally: `fs.list`,
`fs.stat`, `fs.read`, `fs.write`, `fs.mkdir`, `fs.remove`, `fs.rename`,
`fs.truncate`, `fs.copy`, `fs.space`, `fs.pending`, `fs.wait`, `fs.invalidate`.
The remaining features are appended only when the matching service is installed,
so their absence is the signal: `runtime.status`, `runtime.shutdown`,
`mounts.attach`, `mounts.detach`, `tasks.control`, `tasks.list`,
`health.mounts`, `offline.fetch`, `share`. `mounts` is the configured mount
catalog `{name, path, encrypted, state, error}`, not the attached FUSE sessions
(those are in `health`). `write_bytes` is the per-request body cap (64 MiB).

A client decides what to render from `features`, never from the version string:
no `mounts.attach` hides the mount controls, no `tasks.list` shows the task
center as unavailable.

### Health

| Method | Path | Request | Response |
| --- | --- | --- | --- |
| GET | `/v1/control/health` | optional `?mount=name` | `RuntimeHealth` |

`RuntimeHealth` is `{status, checked_at, runtime, mounts, attached_mounts,
tasks}` (`pkg/control/surface.go`), plus `runtime_error` and `mounts_error` when
a section could not be read. `status` is `ok` or `degraded`; `runtime` carries
the same identity as `/v1/control/status`; `mounts` holds per-mount
`MountHealth` (availability, level, error, last-operation counters) and is an
empty array, not `null`, when the Core has no health source; `attached_mounts`
lists live FUSE sessions; `tasks` is `{available, persistence_error}`. One call
is enough for a status strip, and a client should render a missing section as
"unknown" rather than failing the whole view.

### Mounts

| Method | Path | Request | Response |
| --- | --- | --- | --- |
| GET | `/v1/control/mounts` | — | `{"mounts":[MountStatus]}` |
| POST | `/v1/control/mounts` | `MountAttachRequest` | `MountStatus` |
| DELETE | `/v1/control/mounts/{id}` | — | `{"id": id}` |

`MountStatus` is `{id, name, mount_point, read_only, started_at}`
(`pkg/control/mounts.go:13`); `MountAttachRequest` is
`{name, mount_point, read_only}`. Attaching and detaching is FUSE session
ownership, not configuration: drivers, credentials and roots stay in the config
file. A Core with no mount service answers 503, which is how a client detects
"this Core cannot host mounts".

### Filesystem data plane

All operations live under `/v1/fs/<op>` (`pkg/control/fs.go:97`). The write
path is the [upload session](#upload-session-the-only-write-path); `create`,
`write` and `flush` below are the internal plumbing it replaces.

| Op | Method | Request | Response |
| --- | --- | --- | --- |
| `list` | GET | `?path=` | `{"path","entries":[FSEntry]}` |
| `stat` | GET | `?path=` | `FSEntry` |
| `read` | GET | `?path=&offset=&size=&raw=1` | streamed bytes |
| `create` | POST | `{"path"}` | `{"path","ok":true}` |
| `write` | POST | `?path=&offset=`, body ≤ 64 MiB | `{"written":n}` |
| `flush` | POST | `{"path"}` | `{"path","ok":true}` |
| `mkdir` | POST | `{"path"}` | `FSEntry` |
| `remove` | POST | `{"path","dir":bool}` | `{"path","ok":true}` |
| `rename` | POST | `{"source","destination"}` | `{"source","destination","ok":true}` |
| `truncate` | POST | `{"path","size"}` | `{"path","ok":true}` |
| `invalidate` | POST | `{"path"}` | `{"invalidated":path}` |
| `copy` | POST | `FSCopyRequest` | `driver_copy` document |
| `space` | GET | — | `{"total","free"}` |
| `spaces` | GET | — | `{"mounts":[FSMountSpace]}` |
| `pending` | GET | — | `{"pending":[PendingUpload]}` |
| `wait` | POST | `{"path","timeout_ms"}` | `{"path","settled":true}` |
| `idle` | POST | `{"timeout_ms"}` | `{"idle":true}` |

`FSEntry` is `{id, parent_id, name, path, is_dir, size, mod_time}`
(`pkg/control/fs.go:21`); it deliberately omits driver-private extras.
`PendingUpload` is `{path, fid, parent_id, name, local_path, size, mod_time,
updated_at}` (`pkg/vfs/vfstypes/vfstypes.go:16`) — the staged file that the
journal still has to commit.
`FSCopyRequest` is `{source, destination, recursive, force}` and `copy` invokes
the driver's direct copy, then invalidates the affected listings — the Core
hands back the driver document unchanged (`pkg/control/fs.go:28`).

Notes that clients depend on:

- `read` streams: the VFS reader is copied straight to the response body, so a
  4 GiB file never sits in memory on either side. `offset`/`size` implement
  range reads; `raw=1` returns backend bytes without decryption or read-cache
  transforms and is what `fs cat --raw` uses.
- `create`, `write`, `flush` are positional overlay primitives. They are not
  part of the client contract: clients use the upload session and its commit,
  which owns retry, resume and the staging/direct choice. The three ops are
  removed from the protocol when `pkg/fsclient` moves onto the session.
- `wait` is path-scoped: it waits only for the pending uploads and tasks of one
  path, so a busy Core does not fail an unrelated `fs put --wait-timeout`.
- `idle` is whole-filesystem and blocks until no pending uploads or tasks
  remain. It is reserved for commands with a global contract (`fs sync`) and
  for pre-shutdown drains; using it for per-file work serialises every client.
- `invalidate` drops persisted and in-memory listings for one path, which is how
  `fs list --refresh` sees backend changes.

### Upload session (the only write path)

```text
POST   /v1/fs/upload                {"path","size"?,"mode"?,"overwrite"?}
  → 201 {"upload_id","path","chunk_bytes":67108864,"mode":"staging"|"direct",
         "staged":true,"expires_at":"2026-11-21T15:00:00Z"}
GET    /v1/fs/upload/{id}           → {"upload_id","path","received","size",
                                       "chunk_bytes","staged","state"}
PUT    /v1/fs/upload/{id}?offset=N  body = chunk
  → {"received":67108864,"complete":false}
POST   /v1/fs/upload/{id}/commit    → {"path","bytes","fid"?,"mod_time"?}
DELETE /v1/fs/upload/{id}           → {"upload_id","aborted":true}
```

Rules:

- `mode` defaults to `auto`: staging when the driver cannot accept a direct
  staged upload, direct otherwise. The Core decides from driver capabilities
  (`drive.CapabilitySourceUploader`, `drive.CapabilityResumableUploader`),
  not from client hints.
- `size` is optional; when present the Core can fail fast on quota and the
  client can compute progress. Without it, `complete` is only known at commit.
- Chunks are positional. Re-sending the same `offset` with the same
  `X-Qrypt-Operation-Id` is a no-op replay; a different length at an offset that
  was already written answers `409 conflict`. Offsets do not have to arrive in
  order, so a client can retry one chunk or upload ranges in parallel.
- `GET` is the resume probe: it reports `received` so an interrupted upload
  continues instead of restarting. A session with no chunk for the lease
  window (`expires_at`) is garbage-collected.
- `commit` publishes the file. It answers after the journal has accepted the
  operation, so a client that needs the cloud-visible file additionally calls
  `/v1/fs/wait` for that path (same contract as `fs put -`).
- `DELETE` aborts and discards staged bytes. Committing twice with the same
  operation id replays the first answer; without it, the second commit answers
  `409 conflict`.
- Limits: 64 MiB per chunk, one open session per path per Core, session count
  bounded by the Core's staging budget.

Why a session instead of the primitives: `create` → `write` → `flush` exposes
overlay internals and forces every client to reimplement retry, resume, chunk
sizing and staging/direct policy. The mobile layer already models the semantics
(`staging_only` / `prefer_direct`, resumable item streams); the wire contract is
where they belong. `pkg/fsclient` and therefore `qrypt fs put` move onto this
session in the same change, so there is exactly one write path in the product.

### Tasks

| Method | Path | Request | Response |
| --- | --- | --- | --- |
| GET | `/v1/control/tasks` | `?id=&mount=&path=&type=&state=&limit=` | `{"tasks":[task.Task]}` |
| GET | `/v1/control/tasks/{id}` | — | `{"task":..., "items":[...]}` |
| POST | `/v1/control/tasks/cancel` | `{"id"}` | `{"id"}` |
| POST | `/v1/control/tasks/retry` | `{"id"}` | `{"id"}` |

The journal is a first-class control surface: listing and reading tasks sit next
to cancel and retry, so a client never needs the diagnostic endpoint. `type` and
`state` accept comma-separated repeated query parameters; `limit` must be
positive. An unknown id answers `404 not_found`, and a Core without a journal
answers `501 unsupported`. The same shapes are served by the HTTP gateway's
`/v1/tasks`.

### Offline download

| Method | Path |
| --- | --- |
| GET, POST | `/v1/control/offline/tasks` — list with `limit`/`mount`/`state`/`all`, or create |
| GET | `/v1/control/offline/tasks/{id}` |
| POST | `/v1/control/offline/tasks/{id}/cancel`, `/retry`, `/dismiss` |
| POST | `/v1/control/offline/finished` |
| POST | `/v1/control/offline/inspect` |
| POST | `/v1/control/offline/wait` |

`wait` is a long poll; a client should send a deadline (in the request or via
its own context) and re-issue rather than holding a connection indefinitely.
The shapes are defined in `pkg/control/server_handlers_offline.go`; the list
envelope is `{"schema_version":1,"tasks":[...]}`.

### Share

| Method | Path |
| --- | --- |
| GET, POST | `/v1/control/share/links` — list with `mount`/`limit`/`cursor`, or create |
| GET, PATCH, DELETE | `/v1/control/share/links/{id}` |
| POST | `/v1/control/share/resolve` |
| POST | `/v1/control/share/inspect` |
| GET, POST | `/v1/control/share/tasks` — list, or create a receive task |
| GET | `/v1/control/share/tasks/{id}` |
| POST | `/v1/control/share/tasks/{id}/cancel`, `/retry` |
| POST | `/v1/control/share/wait` |

Shapes are defined in `pkg/control/server_handlers_share.go`. Like offline tasks, `wait`
is a long poll.

### Diagnostic endpoint (separate)

The Core also serves a debug endpoint on `<run-dir>/debug.sock` from
`pkg/control/server.go:148-179`: `/v1/health`, `/v1/state`, `/v1/tasks`,
`/v1/task-events`, `/v1/uploads`, `/v1/ops`, `/v1/reads`, `/v1/driver`,
`/v1/mounts/health`, `/v1/events`, `/v1/list`, `/v1/resolve`, `/v1/cache`,
`/v1/staging`, `/v1/consistency`, `/v1/runtime`, `/v1/goroutines`, plus
`/v1/debug/*` and `/v1/bench` when the mount enables test endpoints. Payloads
there are optimised for diagnosis, not for clients.

**What "unstable" means here**, concretely:

- No compatibility promise: fields may be renamed, removed, or restructured in
  any release, including patch releases, without a `protocol_version` bump.
- No deprecation window and no changelog entry. The only consumer is `qrypt
  debug raw` (a human typing it) and `qrypt core logs`-style diagnostics.
- Payloads describe internals, not user operations: `/v1/state` reaches
  megabytes on a 12-mount config and includes staging paths, cache counters and
  goroutine state.
- The mutating endpoints (`/v1/debug/*`, `/v1/bench`, `/v1/driver/test`) only
  exist when `test_enabled` is set, so their absence is normal.
- Adoption is not signalled anywhere: a client that reads it cannot tell that
  the field it wants moved.

**Exit plan.** Three product features still read it today, and each one has a
named replacement on the control endpoint:

| Debug read | Used by | Replacement |
| --- | --- | --- |
| `GET /v1/health` | DSH `qrypt_status` core-running probe | `/v1/control/status` (already exists) |
| `GET /v1/mounts/health` | DSH status strip per-mount health | `/v1/control/health` (shipped 0.7.2) |
| `GET /v1/tasks` | DSH task tab | `/v1/control/tasks` (shipped 0.7.2) |

All three replacements ship in 0.7.2, so no product feature needs the debug
endpoint and the rule below is mechanical instead of aspirational. The DSH
plugin keeps a single fallback path behind a capability check (`features` lacks
`tasks.list` → show the task tab as unavailable) rather than parsing debug
payloads, so the fallback cannot silently rot.

## Planned additions

Ordered by value for existing clients. These land before the v1 freeze; after
the first third-party client ships, changes follow
[Compatibility rules](#compatibility-rules-for-future-changes) instead.

### Event stream

```text
GET /v1/control/events?filter=tasks,uploads,mounts
→ SSE: task.created|task.updated|task.finished, upload.progress,
       mount.attached|mount.detached, core.busy, config.reloaded
```

Why: every client currently polls. The desktop and the DSH panel poll the task
journal every few seconds, which costs a request per client per interval and
still shows stale progress. The HTTP gateway already exposes SSE `/v1/events`
over the Core's task journal; the control endpoint should expose the same stream
so local clients stop polling.

### Async operation handles

```text
POST /v1/fs/remove   {"path": "/x", "recursive": true}  → 202 {"operation_id": "..."}
POST /v1/fs/copy     {"source": "/a", "destination": "/b", "recursive": true}
GET  /v1/control/operations/{id}   → {"state": "running", "progress": {...}, "result": {...}}
POST /v1/control/operations/{id}/cancel
```

Why: recursive delete, cross-mount copy, `fs sync` and offline downloads are long
operations. Today a client can only block on the request or time out, and the
idempotency key records the final answer but exposes no progress. Operation ids
are the natural generalisation of `X-Qrypt-Operation-Id`: the same key would both
deduplicate and name the handle.

### Query surface

| Endpoint | Why |
| --- | --- |
| `GET /v1/fs/list?path=&cursor=&limit=&recursive=` | Large directories need paging; mobile already has `next_cursor` |
| `GET /v1/fs/find?path=&name=&type=&limit=` | `fs find` exists in the CLI but not on the wire |
| `GET /v1/fs/thumbnail?path=&size=` | Desktop grid view |
| `POST /v1/fs/batch` | Batch remove/mkdir/copy with per-item results, instead of N round trips |

### Operational endpoints

| Endpoint | Why |
| --- | --- |
| `GET /v1/control/healthz` | Cheap liveness for supervisors and scripted wait loops (`{"ok":true}` and nothing else) |
| `GET /v1/control/metrics` | Prometheus text: op latency, queue depth, cache hit rate, pending uploads |
| `qrypt api schema --json` | One generated schema for the protocol, replacing four hand-written copies of the DTOs |

## Client recipes

**Attach.** Connect to `control.sock`, call `capabilities` and `status`, compare
`protocol_version` / `version` / `config_path` / `fingerprint`, keep `features`
to decide what to render, then use the connection. If the socket is missing, start a Core (`qrypt core serve` or the
CLI's lazy start via `clicore.EnsureCore`, `internal/cli/core/core.go:267`) and
retry once.

**Read a file.** `stat` for size, then one `read` for small files or ranged
`read` calls for large ones. Do not read a file twice to decide whether it is
binary; look at the first few KiB with `size=`.

**Upload a file.** `POST /v1/fs/upload`, then `PUT` chunks of at most 64 MiB,
each with its own operation id so a retry of that chunk replays instead of
rewriting. Report progress from `received`; an interrupted upload resumes from
`GET /v1/fs/upload/{id}`. After `commit`, call `/v1/fs/wait` for the path if the
caller needs the file visible in the cloud — the same contract as `fs put -`.

**Cancel work.** Task cancellation is by id and applies to the task, not to the
client's request. A client that wants to abandon its own long poll should close
the connection (which cancels the server context via `BaseContext`,
`pkg/control/command.go:131-137`) rather than waiting for the timeout.

**Retry.** Retry transport failures (connection reset, 503, 504) and
`retryable: true` envelopes with the same operation id for mutations and plain
`GET`s for reads. Do not retry `invalid`, `conflict`, `forbidden` or
`unsupported` without changing the request; those are decisions, not flakes.

## Requirements coverage

This section is the completeness check for the surface above: it maps every
operation a real client performs to the endpoint that serves it. The reference
client is the DSH Harness qrypt plugin (`dsh-qrypt`: 13 agent tools plus a file
manager panel), because it is the most demanding local consumer today — it needs
browsing, uploads, downloads, batch actions, a task center and mount management.

| Client need | Served by | Status |
| --- | --- | --- |
| Is a Core running; version, protocol, config path, work dir | `GET /v1/control/status` | exists |
| Mount list for the browser (name, type, root) | `GET /v1/control/mounts` | exists |
| Attach and detach a mount | `POST /v1/control/mounts`, `DELETE /v1/control/mounts/{id}` | exists |
| Per-mount health for the status strip | `GET /v1/control/health` | exists (0.7.2) |
| List a directory | `GET /v1/fs/list` | exists |
| Page a large directory | `GET /v1/fs/list?cursor=&limit=` | P1 |
| Entry metadata | `GET /v1/fs/stat` | exists |
| Read a text or binary range | `GET /v1/fs/read?offset=&size=&raw=` | exists |
| Download to a local file | `GET /v1/fs/read` stream | exists |
| Upload text, base64 or a local file | `POST /v1/fs/upload` + `PUT` chunks + `/commit` | **P1** (blocks removing `create`/`write`/`flush`; also moves `pkg/fsclient` and `fs put`) |
| Pending upload queue | `GET /v1/fs/pending` | exists |
| Create a directory | `POST /v1/fs/mkdir` | exists |
| Move or rename | `POST /v1/fs/rename` | exists, **same mount only** (see gap 2) |
| Delete file or directory | `POST /v1/fs/remove` | exists |
| Copy, recursive | `POST /v1/fs/copy` | exists |
| Task list with type, state, progress, target, actions | `GET /v1/control/tasks`, `/tasks/{id}` | exists (0.7.2) |
| Cancel or retry a task | `POST /v1/control/tasks/cancel`, `/retry` with `{"id"}` | exists |
| Share links: list, create, delete | `/v1/control/share/links*` | exists |
| Share receive tasks: list, cancel, retry | `/v1/control/share/tasks*` | exists |
| Classify a failure without parsing prose | error envelope with `code` | exists (0.7.2) |
| Decide which controls to show (no mount service, no share, no task center) | `GET /v1/control/capabilities` | exists (0.7.2) |
| Discover socket path, protocol version, fingerprint | `qrypt core endpoint --json` + `capabilities` | exists (0.7.2) |
| Live task and upload progress instead of polling | `GET /v1/control/events` (SSE) | P1 |
| Progress and cancellation for long operations | operation handles | P1 |
| Batch delete/move/copy in one request | `POST /v1/fs/batch` | P2 (clients loop today) |
| Browse diagnostics as a human | debug endpoint | unstable, never a product dependency |

**Gaps found by this check.** Three needs are neither implemented nor covered by
the P0/P1/P2 plan as originally written, and two of them are client-visible
defects rather than missing endpoints:

1. **Per-mount health had no control-face owner.** The panel's status strip read
   `/v1/mounts/health` from the debug socket. `/v1/control/health` now serves the
   same data (0.7.2), so that debug read can be removed.

2. **Cross-mount move is impossible at the Core**, not at the client.
   `pkg/vfs/namespace.go:19` rejects it with `ErrCrossMount`, so a panel that
   offers "move to another mount" fails after the user picks a destination.
   Either the client hides cross-mount destinations, or the Core composes
   copy+remove behind one operation id (P1 handles are the natural carrier).
   Decision for 0.7.2: clients hide cross-mount destinations until the Core can
   compose the move; the protocol can express both, so no client change is
   needed when it lands.
3. **Task and upload progress has no push channel**, so every client polls. The
   panel polls the task journal every few seconds today; acceptable until P1
   ships the event stream, but the plan must keep it as a deadline, not a
   permanent design.

Interactive completeness is otherwise satisfied: every operation the plugin
performs through the CLI has a control endpoint. The remaining work is ordering,
not invention.

## Rollout order

The additions above are sized so each step can ship on its own. Nothing below
requires a `protocol_version` bump.

| Step | Scope | Acceptance |
| --- | --- | --- |
| P0 (shipped 0.7.2) | `capabilities`, `core endpoint --json`, `/v1/control/tasks` list+get, `/v1/control/health`, error envelope, this document as the contract | No product feature reads the debug endpoint; a client decides what to show from `capabilities`; no client re-derives the socket path; failures are classified by `code` |
| P1 | event stream, async operation handles, upload session (with `pkg/fsclient` and `fs put` moved onto it, and `create`/`write`/`flush` removed), list paging, batch actions | The DSH panel renders tasks without polling; a recursive delete reports progress; an interrupted upload resumes; one write path in the product |
| P2 | HTTP gateway parity (write endpoints, same envelope, SSE), remote auth, generated client from `qrypt api schema` | The same client code works over the socket and over HTTPS with only the transport swapped |

The ordering is deliberate: P0 removes guessing, P1 removes polling and
blocking, P2 removes the local-only assumption. Doing P2 first would freeze
shapes that P1 is about to change.

## Compatibility rules for future changes

1. Additive only inside a protocol version: new endpoints, new object fields,
   new enum values, new optional request fields.
2. Removing a field or changing its type or meaning requires a new
   `protocol_version` and a deprecation window of at least one release.
3. A client that sees an unknown `protocol_version` must refuse to write. Reads
   may still work, but the client should surface the mismatch instead of
   guessing.
4. Anything a product depends on must be reachable from the control endpoint.
   The diagnostic endpoint is for humans and is allowed to change at any time.

## References

- `pkg/control/command.go` — control routes, `ProtocolVersion`, runtime status
- `pkg/control/fs.go` — data-plane operations, `FSEntry`, error mapping
- `pkg/control/operation.go` — `X-Qrypt-Operation-Id` replay store
- `pkg/control/mounts.go` — mount attach/detach
- `pkg/control/server_handlers_offline.go`, `server_handlers_share.go` — offline and share endpoints
- `pkg/control/offline.go`, `share_client.go` — client helpers for those endpoints
- `pkg/control/client.go` — Go client used by the CLI and the gateway
- `pkg/core/core.go` — `RuntimeLayout`, `ControlSocketPath`, `RuntimeRunDir`
- `internal/cli/core/core.go` — `EnsureCore`, `EnsureClient`
- `internal/httpapi` — read-only HTTP gateway over the same data plane
- [Architecture](architecture.md) — layering, single writer, listing cache
- [Mobile Interface](mobile-interface.md) — the in-process binding and its call semantics
