## Context

See `proposal.md` — Why. Current state that shapes the approach:

- Fetch placement is flat: `fetchTargets` joins the destination with `file.Name` and only knows
  `Index/Name/Size/RemoteID` per result file (`pkg/core/offline_fetch.go`, `pkg/core/offline_state.go`).
  Local writers truncate (`openLocalWriter` uses `O_TRUNC`), so a name collision loses data silently.
- The accelerated path short-circuits: when `ResultFiles` is non-empty the runner transfers before the
  provider task terminalizes (`pkg/core/offline_runner.go`), and Quark's create response already sets a
  single `RemoteID`, so the transfer can be pinned to one identity.
- The CLI blocks with no progress: `waitWithReconnect` long-polls to a terminal state and prints once.
- Two provider data shapes do not align: Quark's parse carries `path`/`file_dirs`/`torrent_name`, but
  the task observation carries only `fid`/`file_name`/`pdir_fid`/`size`.
- This change is stacked on the in-flight `add-offline-download` change and modifies requirements that
  change introduces.

## Goals / Non-Goals

**Goals:**

- Place fetched files at their real staged relative path, recreating the provider's folders.
- Keep the layout decision provider-neutral: a provider reports a path, Core consumes it.
- Never transfer a partial result set, while keeping accelerated download URLs.
- Make destination writing safe against hostile paths and symlinks.
- Free the terminal by default and show progress when blocking.

**Non-Goals:**

- Deleting the task's cloud folder after a fetch (deferred; cleanup still removes only staged fids).
- Reconstructing the tree from parse metadata by joining on name/size.
- `--flat` beyond a documented escape hatch; no per-provider path policy.
- Changing selection semantics (`--select`/`--include`/`--exclude` still decide *which* files).

## Decisions

### D1. Derive the layout from the provider's actual staged tree

Resolve paths by walking the provider's real directory structure from the task's output root, using each
result file's parent-directory identity. For Quark this is `pdir_fid` plus `/file/sort` listing, starting
at the `fid` the submit response returns as the resource root.

- Alternative: rebuild the tree from parse `path`/`file_dirs` and join to result files by `(name,size)`.
  Rejected — it is least reliable exactly where mirroring matters (duplicate names), and needs a second
  persisted list.
- Alternative: have the provider echo a path in every observation. Not available in Quark's task
  endpoint; the driver must compute it anyway.
- Listing the output root also surfaces files the result list omitted, scoped to the task's own root, so
  the mirror is not silently truncated by a thin result list.

### D2. Extend the provider-neutral contract with a path and an availability signal

- `drive.OfflineFile` gains `Path` (path relative to the staging destination, POSIX style, no leading
  slash). A provider that can locate a staged file MUST fill it; Core only validates and joins.
- `drive.OfflineDownload` gains an availability signal meaning "the staged result set is complete and no
  longer changing". Quark maps `download_task_finish` and terminal state onto it.
- Both are optional additions: older snapshots and clients stay readable, and a file with no path falls
  back to its name.

### D3. Gate the transfer on availability, not on a create-time fid

The fetch MUST NOT start transferring until it has the availability signal or a terminal observation.
This replaces the current `ResultFiles`-non-empty short-circuit and removes the single-`fid` path. The
accelerated session URL is still used for the transfer itself.

- Alternative: always wait for the terminal state. Rejected — it drops the pre-terminal capability and
  needlessly delays instant-available sources.
- Alternative: keep the short-circuit. Rejected — it can transfer a partial set and is the likely cause
  of a single-file transfer for a multi-file source.

### D4. Validate and confine every derived path in Core

Sanitize each component (reject absolute, `.`/`..`, empty, NUL/control, Windows-reserved, trailing dots
or spaces), refuse writes through an existing symlinked ancestor that leaves the destination root, and
fail the task with `offline_path_invalid` on any violation or on two files resolving to the same
destination. The check applies to flat placement too, closing the current `filepath.Join` traversal.

- Alternative: fall back to the file name on an invalid path. Rejected — it reintroduces the collisions
  the change exists to remove and hides hostile input.
- Alternative: sanitize in each driver. Rejected — security policy is provider-independent.

### D5. Flip the CLI default and share one progress renderer

`offline fetch` returns after the first provider observation by default, printing the snapshot and how to
observe it; `--wait` blocks. Progress renders to stderr (never stdout, so `--json` stays a single
document) and is shared by `fetch --wait` and `offline wait`. `--no-wait` becomes a deprecated no-op.

- Alternative: keep blocking and only add progress. Rejected — it does not free the terminal, which is
  the user's main ask.
- Alternative: TTY-aware default. Rejected — same command behaving differently under a pipe is
  surprising.

### D6. Persist what recovery needs

The staged relative path rides on the existing result-file entries in task detail, and the task's output
root identity is persisted so a recovered fetch resolves the same tree. A snapshot without these fields
still decodes and falls back to file names.

### D7. `--flat` escape hatch

A `--flat` form forces name-only placement for callers that depend on the old layout. Default remains
mirroring.

### D8. Cleanup also removes the resource folder the task created

A magnet makes the provider create a resource folder, and removing only its files leaves an empty shell
behind. After the staged files are removed, cleanup also removes that folder, but only when every file
under it is one this cleanup removed; a folder holding an unrelated entry is left untouched, the parse
destination is never removed, and a retention request keeps the folder. The folder is identified as the
single top-level directory the staged relative paths share, so a single-file source or files staged
directly in the parse destination have none and are unaffected.

- Alternative: delete the output root unconditionally by its provider identity. Rejected — a provider
  can merge a same-named pre-existing folder, and a recursive provider delete would then take unrelated
  entries with it.
- Alternative: remove nested empty directories one by one. Rejected — the provider's directory delete is
  already recursive, and the ownership check is what makes that safe.

## Risks / Trade-offs

- [Result list omits files] → derive from the output-root listing (D1), scoped to the task root, so
  siblings are discovered rather than missed.
- [Extra provider calls for the tree] → resolve once when availability/terminal arrives and persist the
  result; do not resolve during polling.
- [Hostile or exotic names rejected] → validation is component-wise and conservative; the error names
  the refused path so the user can act.
- [Breaking CLI default] → document in `--help`, `docs/for-user/cli.md`, and the offline spec; keep
  `--no-wait` as a no-op; `--wait` restores the old contract.
- [Stacked on an unarchived change] → archive `add-offline-download` before this change; the INFO from
  `openspec validate` is expected until then.
- [Symlink checks on every write] → check existing ancestors once per resolved directory, not per chunk.
- [Windows path rules] → reject rather than mangle, so behavior is identical across platforms.
- [Folder cleanup races a lagging listing] → ownership is decided by fid, not by presence, so a listing
  that still shows the files this cleanup removed is treated as empty; the delete is retried briefly and
  is best-effort, never failing the task.

## Migration Plan

1. Land the additive contract fields first (`OfflineFile.Path`, availability signal) with drivers and
   Core tolerating their absence.
2. Ship the Quark tree resolution behind the availability gate.
3. Flip the CLI default and add `--wait`/progress in the same release; keep `--no-wait` accepted.
4. Rollback: revert the default flag and ignore the new fields; persisted paths are inert if unused.
5. Archive `add-offline-download`, then this change.

## Open Questions

- Whether progress should later adapt to a TTY (spinner vs line log); does not change specs or tasks.
- Whether to add a parse-metadata cross-check for providers that report paths inconsistently; deferred
  until a second provider exists.
