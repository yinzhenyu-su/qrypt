---
name: vfs-upload-recovery
description: Diagnose and recover qrypt VFS uploads stuck in pending, retrying, permanent_fail, or staging corruption, including probe wait_upload timeouts and leftover __qrypt_* fixtures. Use when fs output shows dirty/pending, uploads never drain, journal prune/replay is needed, or a mount reports permanently failed uploads.
---

# VFS Upload Recovery

## First decide: is the mount running?

- **Running mount**: inspect with `qrypt fs pending` and `qrypt fs journal` (read-only). Do not hand-edit `pending.jsonl` or `staging/`; the process holds the authoritative in-memory state and will overwrite external edits.
- **Stopped mount**: run journal maintenance. `fs journal prune`, `prune --failed`, and `replay` are offline operations. Stop the mount (`Ctrl+C`, or unmount) before running them.

Never delete `pending.jsonl` or staging files as a first move. That loses recoverable data and can resurrect stale remote content.

## Where the state lives

Default layout (override with `storage.work_dir`, `storage.upload_dir`, or `QRYPT_HOME`):

```text
~/.qrypt/upload/<mount>/pending.jsonl   journal (dirty/clean records)
~/.qrypt/upload/<mount>/staging/*.staging   local bytes awaiting upload
```

`QRYPT_HOME=/some/dir` replaces the whole work dir; use it to inspect an
isolated or test instance. `--cache-dir` overrides the upload root (normally
`~/.qrypt/upload`); with a config qrypt appends the mount name, so pass the
root unless you are using the no-config single-directory form.

## Read the pending record

`qrypt fs pending --verbose` (or `--json`) shows the fields that matter:

| Field | Meaning |
|---|---|
| `path` | virtual path the user sees |
| `local_path` | staging file; compare its size with `size` |
| `frozen` | generation sealed by flush and eligible for upload |
| `retry_count` / `next_attempt_at` | retryable failure bookkeeping |
| `last_error` | last upload error |
| `permanent_fail` | terminal: the worker will not retry this generation |
| `staging` | runtime-only staging diagnostics, not journal state |

`permanent_fail` is set for `drive.NonRetryable` errors and for auth,
permission, invalid-request, and unsupported error categories. It is also used
when a frozen staging file shrank or disappeared.

## Triage flow

1. Inspect:
   ```bash
   qrypt fs pending --verbose
   qrypt fs journal
   qrypt fs journal --json
   ```
2. Classify each entry:

| Observation | Meaning | Action |
|---|---|---|
| `pending` with future `next_attempt_at`, no `permanent_fail` | retryable, normal | wait; do not prune |
| `permanent_fail` + staging `ok` | terminal failure, bytes intact | decide: retry after fixing the cause, or discard |
| staging `missing` or `size-mismatch` | local bytes are gone/invalid | offline `journal prune` |
| `orphan_staging` | staging file no journal entry references it | swept on next mount; leave it alone unless disk pressure forces cleanup |
| idle wait blocked by a terminal entry | `permanent_fail` is not active work | probe now fails fast at `wait_upload`; use this skill to recover |

## Recovery decisions

### Retry after the cause is fixed

Typical causes: expired credentials, transient permission changes, account
re-login. Unmount, reset the failure bookkeeping, then remount:

```bash
# mount stopped
qrypt fs journal prune            # first: drop entries whose staging is gone
qrypt fs journal replay           # reset permanent_fail / retry / last_error
qrypt mount --config ./qrypt.toml
```

`replay` does not recreate missing staging data; prune first.

### Discard local changes

Only when the staged bytes are no longer wanted:

```bash
# mount stopped
qrypt fs journal prune --failed
```

`--failed` removes `permanent_fail` records **and** their staging files.
Plain `prune` keeps them: a permanent failure may still be recoverable with
`replay`, so discarding is explicitly opt-in.

### Staging corruption

- `missing`: plain `journal prune` drops the record.
- `size-mismatch`: plain `journal prune` drops the record because the size no
  longer matches the journal.
- `orphan_staging`: no journal entry points at it; the next mount sweeps it.
  Do not delete a staging file that a pending record still references.

### `rm` on a mounted path

`rm` first checks the pending record and tries to resolve an older remote
object. If the same auth/list failure that caused the permanent failure also
blocks resolution, `rm` returns an error and keeps the record on purpose: it
cannot tell whether an older remote version exists, and silently dropping the
pending would make that version reappear.

When that happens:

```bash
# mount stopped
qrypt fs journal prune --failed
```

If remote listing works, `rm` removes the pending record normally.

## Probe failures and leftover fixtures

`probe test fs` now cleans with a context detached from the request:

1. removes the test file;
2. discards pending uploads under the test directory;
3. waits for delayed remote deletes;
4. removes the test directory.

A terminal upload makes `wait_upload` report the failed paths immediately
instead of waiting two minutes. Cleanup failures are recorded as a `cleanup`
step in the report.

A hard kill (`kill -9`, crash, OOM) can still leave fixtures. Known temporary
prefixes:

- `__qrypt_fs_test_*`
- `__qrypt_resume_test_*`
- `__qrypt_batchupload_test_*`
- `__qrypt_batchmove_test_*`
- `__qrypt_read_test_*`
- `__qrypt_xfer_test_*`

Delete only these test prefixes after confirming they are not user data.

## Useful diagnostics

```bash
# Pending view (works while mounted)
qrypt fs pending --config ./qrypt.toml --json

# Journal report; add --mount NAME or --cache-dir DIR
qrypt fs journal --config ./qrypt.toml --json

# Daemon-side upload/staging state
qrypt debug raw '/v1/state?mount=NAME' --config ./qrypt.toml
qrypt debug raw '/v1/staging?mount=NAME' --config ./qrypt.toml
```

Relevant logs carry the mount name and op id:

```bash
grep -E 'upload failed permanently|upload failed; requeue|Resume.*permanent|prune failed upload' \
  ~/.qrypt/logs/qrypt-error.log | tail -n 50
# Explicit [logging].error_file overrides this path; see docs/for-developer/debug.md.
```

## Escalation

For a bug report or a stuck reproduction, collect and redact:

- `qrypt fs journal --json`
- `qrypt fs pending --json`
- `qrypt probe test fs --mount NAME --json` result (steps + final state)
- relevant log lines with `op_id`, `path`, `error category`

Never include cookies, tokens, app passwords, signed URLs, or full
`qrypt.toml` credentials.

## Source of truth

- `pkg/vfs/upload/store.go`: journal, replay, prune, staging sweep
- `pkg/vfs/upload/engine.go`: retry/permanent classification
- `pkg/vfs/upload/domain.go`, `pkg/vfs/upload/service.go`: scheduling and idle
- `pkg/vfs/upload_recovery.go`: resume behavior for frozen/damaged staging
- `pkg/contracttest/driver_fstest.go`: probe idle and cleanup semantics
- `docs/for-user/troubleshooting.md`, `docs/for-developer/debug.md`
