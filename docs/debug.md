# qrypt Debugging

This document describes qrypt's debug tools for upload recovery, live runtime
state, cache behavior, encryption name mapping, and remote consistency checks.

The debug surfaces are intentionally read-only unless documented otherwise.
They are designed for both CLI troubleshooting and future UI integration.

## Offline Checks

Offline checks read cache files directly and do not need a running mount.

### Mount Drivers With Debug Socket

```sh
go run ./cmd/qrypt \
  -config ./qrypt.toml \
  -debug-socket /tmp/qrypt.sock \
  mount
```

### Pending Uploads

```sh
go run ./cmd/qrypt -config ./qrypt.toml pending
go run ./cmd/qrypt -config ./qrypt.toml pending --verbose
```

`pending --verbose` reports:

- virtual path
- expected size
- staging file path
- staging status
- retry count
- last upload error
- last and next attempt timestamps

### Pending Journal

```sh
go run ./cmd/qrypt -config ./qrypt.toml debug journal
```

This checks each selected mount cache and reports:

- `pending.jsonl` entry counts
- invalid journal lines
- pending entries whose staging files are missing
- staging size mismatches
- orphan `.staging` files that are not referenced by pending journal state

Use `-mount-name NAME` to inspect one configured mount.

```sh
go run ./cmd/qrypt -config ./qrypt.toml -mount-name quark debug journal
```

## Live Control Socket

Live debug data is exposed through HTTP over a local Unix socket. The socket is
not enabled by default.

Start qrypt with a debug socket:

```sh
go run ./cmd/qrypt \
  -config ./qrypt.toml \
  -debug-socket /tmp/qrypt.sock \
  mount
```

Query the running process from another shell:

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live health
```

The CLI prints the same JSON returned by the control API. A UI can consume the
same endpoints directly through the Unix socket.

### Security Notes

- The socket file is created with `0600` permissions.
- If an existing socket is live, qrypt refuses to replace it.
- Stale socket files are removed during startup.
- Logs and events pass through the existing sanitizer.
- `remote_name` is only returned by explicit resolve requests.

## Live Commands

### Health

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live health
```

Endpoint:

```text
GET /v1/health
```

Returns API version, timestamp, and basic process reachability.

### Full State

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live state
```

Endpoint:

```text
GET /v1/state
```

Returns a structured snapshot of all mounts:

- pending uploads
- active uploads
- upload queue length
- upload/delete timers
- deleted and overlay state
- restored directories
- copy-hidden entries
- read cache summary
- driver debug snapshot, if the driver supports it

Detailed upload history and operation events are exposed by the focused
`/v1/uploads?history=1` and `/v1/ops` endpoints instead of being inlined here.

### Pending

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live pending
```

Endpoint:

```text
GET /v1/pending
```

Returns a flat pending upload list. For namespace mounts, paths are prefixed
with the mount name, such as `/quark/file.txt`.

### Uploads

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads /quark/file.txt
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads /quark/file.txt --history
```

Endpoint:

```text
GET /v1/uploads
GET /v1/uploads?path=/quark/file.txt
GET /v1/uploads?path=/quark/file.txt&history=1
```

Upload states are generic and driver-independent:

- `scheduled`
- `retry_wait`
- `preparing`
- `removing_existing`
- `uploading`
- `completed`
- `failed`
- `superseded`

Each item includes `op_id`, path, byte progress, retry count, timestamps, and
the last error when available. qrypt keeps the most recent 100 completed,
failed, or superseded upload records in memory.

### Recent Events

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live events
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live events error 50
```

Endpoint:

```text
GET /v1/events?level=warn&limit=100
```

Returns recent `WARN` and `ERROR` events from an in-memory ring buffer. The
maximum retained event count is 500.

### Operation Events

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live ops
```

Endpoint:

```text
GET /v1/ops?limit=100
```

Returns recent normal filesystem operations, separate from warning/error logs:

- pending created
- upload queued
- upload started
- upload retry wait
- upload completed
- delete scheduled
- delete completed

The operation stream is useful for UI timelines because it records successful
state transitions that do not belong in the warning/error event stream.

### Remote List

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live list /quark/docs
```

Endpoint:

```text
GET /v1/list?path=/quark/docs
```

This performs a live remote list through the current VFS/driver stack and
bypasses VFS directory cache and pending overlay. It is useful for checking
whether an upload is visible on the backend.

When encryption is enabled, names are returned in the decrypted virtual view.
Use `resolve --remote-name` for a specific encrypted name mapping.

### Resolve

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live resolve /quark/a.txt
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live resolve /quark/a.txt --remote-name
```

Endpoint:

```text
GET /v1/resolve?path=/quark/a.txt
GET /v1/resolve?path=/quark/a.txt&include_remote_name=1
```

Returns path mapping and object identity:

- virtual path
- parent path
- plain filename
- remote filename, only when explicitly requested
- remote object ID
- parent ID
- pending state
- size

For encrypted mounts, `remote_name` is the encrypted backend filename. For
unencrypted mounts or drivers without a custom resolver, it falls back to the
plain filename.

### Cache

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live cache
```

Endpoint:

```text
GET /v1/cache
```

Returns per-mount read cache state:

- configured max bytes
- cached bytes
- chunk count
- file count
- hit/miss/put/evict counters
- per-file chunk and byte counts

### Tasks

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live tasks
```

Endpoint:

```text
GET /v1/tasks
```

Returns a UI-friendly task list derived from VFS state:

- active uploads
- scheduled upload timers
- scheduled delete timers
- pending deletes

### Consistency

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live consistency /quark/a.txt
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live consistency --dir /quark/docs
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live consistency --dir /quark/docs --recursive
```

Endpoint:

```text
GET /v1/consistency?path=/quark/a.txt
GET /v1/consistency?dir=/quark/docs
GET /v1/consistency?dir=/quark/docs&recursive=1
```

Checks one path against pending state and a live remote list of the parent
directory. Directory mode checks the union of visible remote children and
pending uploads under the requested directory. Possible statuses include:

- `ok`: no pending upload and matching remote object exists
- `pending`: pending upload exists but no matching remote object was found
- `uploaded_pending_cleanup`: remote object exists and matches pending size
- `mismatch`: remote object exists but size differs from pending size
- `missing`: object is neither pending nor present remotely
- `error`: parent resolution failed

For pending zero-byte files, expected size remains zero; qrypt does not treat a
non-empty remote file as a match.

### Driver

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live driver
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live driver-health
```

Endpoint:

```text
GET /v1/driver
GET /v1/driver/health
```

Returns optional per-driver debug snapshots. Drivers implement
`drive.Debugger` to expose generic fields plus driver-specific `extra` data.

`/v1/driver/health` runs explicit driver health checks through
`drive.HealthChecker`. This endpoint may touch the backend, so it is kept
separate from `/v1/state`.

### Runtime

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live runtime
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live goroutines
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live goroutines 2
```

Endpoints:

```text
GET /v1/runtime
GET /v1/goroutines?debug=1
```

`/v1/runtime` reports Go runtime version, OS/arch, goroutine count, CPU count,
and memory statistics.

`/v1/goroutines` returns a goroutine dump as plain text. Use it when qrypt
appears stuck, uploads do not return, or reads are blocked.

### Bundle

```sh
go run ./cmd/qrypt \
  -debug-socket /tmp/qrypt.sock \
  debug bundle -out /tmp/qrypt-debug.zip
```

The bundle command collects a read-only diagnostic zip from the running
process. It includes:

- health
- state
- pending
- uploads with history
- warning/error events
- operation events
- cache
- tasks
- driver debug
- driver health
- runtime
- goroutine dump

Endpoint failures are written into the corresponding file inside the zip so a
partial bundle can still be useful.

## Driver Integration

The control API is intentionally generic. Drivers can opt into debug features
by implementing optional interfaces in `pkg/drive`:

- `drive.Debugger`
- `drive.RemoteNameResolver`
- `drive.HealthChecker`

Wrappers such as crypt and bandwidth limiting preserve or provide fallbacks for
these optional capabilities. A driver that does not support a feature should
still remain usable through the generic VFS debug endpoints.

## Common Workflows

### Upload Is Not Visible

1. Check active and historical upload state:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads /quark/a.txt --history
   ```

2. Check pending state:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live pending
   ```

3. Check live remote listing:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live list /quark
   ```

4. Run consistency check:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live consistency /quark/a.txt
   ```

### Upload Is Stuck

1. Check upload progress and retry state:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live uploads --history
   ```

2. Check recent warning and error events:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live events warn 100
   ```

3. Check recent normal operation transitions:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live ops
   ```

4. Check goroutines if the process appears stuck:

   ```sh
   go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live goroutines
   ```

5. Check offline journal and staging files:

   ```sh
   go run ./cmd/qrypt -config ./qrypt.toml debug journal
   ```

### Preparing a Bug Report

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug bundle -out /tmp/qrypt-debug.zip
```

Review the generated bundle before sharing it. qrypt sanitizes known secrets in
logs and events, but filenames and paths can still be sensitive.

### Filename Mapping Looks Wrong

```sh
go run ./cmd/qrypt -debug-socket /tmp/qrypt.sock debug live resolve /quark/a.txt --remote-name
```

Use this only for targeted checks because plaintext filenames and remote names
can both be sensitive.
