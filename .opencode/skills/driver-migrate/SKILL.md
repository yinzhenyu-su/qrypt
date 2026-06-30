---
name: driver-migrate
description: Migrate storage drivers from OpenList/OpenListTeam implementations into the qrypt Go codebase. Use when Codex is asked to add a new qrypt driver based on OpenList source code, port an OpenList cloud drive API implementation, compare OpenList driver behavior with qrypt driver contracts, or debug a partially migrated qrypt driver for list/read/write/path/config/schema/cache/staging behavior.
---

# Driver Migrate

## Workflow

1. Resolve the target driver before coding.
   - If the user named a driver, use that driver as the migration target.
   - If the user did not name a driver, inspect OpenList driver packages and local qrypt drivers, identify OpenList drivers that qrypt does not have, and ask the user which one to migrate.
   - If `gh` is installed locally, prefer `gh` CLI for reading OpenList repository contents and source files.
   - Do not start implementation until the target driver is explicit.

2. Read the existing qrypt driver patterns before coding.
   - Inspect `pkg/drive/drive.go`, `pkg/drive/debug.go`, and nearby drivers under `internal/driver/`.
   - Prefer the closest existing qrypt driver shape over copying OpenList structure.
   - Read `references/qrypt-driver-contract.md` when implementing or reviewing driver behavior.

3. Inspect the OpenList source as a source of API behavior, not as architecture to copy.
   - Identify auth, root selection, list pagination, path/id semantics, read/download URL flow, upload flow, mkdir/remove/rename/move support, time parsing, and error mapping.
   - Preserve API request details and edge cases; rewrite integration to qrypt interfaces.

4. Implement the qrypt driver in small layers.
   - Register the driver and `ParamDef` schema.
   - Add config/schema/docs entries only for user-facing parameters.
   - Implement `drive.Driver` first: `Init`, `Drop`, `List`, `Read`.
   - Add `drive.Uploader`, `drive.FileUploader`, `drive.Writer`, `drive.Debugger`, `drive.HealthChecker`, or `drive.SpaceQuerier` only when the backend supports them.

5. Keep VFS boundaries clear.
   - Drivers return backend entries; VFS handles cache, staging, pending uploads, directory/list caching, and FUSE semantics.
   - Drivers must not know about qrypt staging or read cache.
   - Crypt wrapping happens above the raw driver; raw drivers should implement plain backend semantics.

6. Validate behavior with targeted tests before broad tests.
   - Unit test config validation, list parsing, read range behavior, path escaping, root path/root ID handling, time parsing, and upload/write operations.
   - For live debugging, prefer `debug live state`, `resolve`, `list`, `consistency`, `events`, `cache`, and `staging`.
   - Run the narrow package tests, then `go test ./...`.

## Driver Discovery

When the user asks to migrate a driver but does not specify which one:

1. Discover local qrypt drivers from `internal/driver/*`.
2. Discover OpenList drivers from `OpenListTeam/OpenList`, preferring `gh` CLI when it exists.
3. Compare normalized driver names and remove drivers already present in qrypt.
4. Present the missing OpenList drivers as a concise candidate list.
5. Ask the user which driver to migrate and wait for that choice before editing code.

Use `gh` commands for source discovery when available:

```bash
gh api repos/OpenListTeam/OpenList/contents/drivers
gh api repos/OpenListTeam/OpenList/contents/drivers/<driver>/driver.go
```

If `gh` is unavailable or unauthenticated, use the next available repository access method and state the limitation.

## Migration Checklist

- Driver package lives under `internal/driver/<name>`.
- `init()` registers the driver name and parameter schema.
- Required params are represented in both driver `ParamDef` and `qrypt.schema.json`.
- Secrets are marked `Secret: true` and never logged unmasked.
- `Init` validates params and resolves configured root state.
- `List(parentID)` returns stable `ID`, correct `ParentID`, plaintext `Name`, `IsDir`, `Size`, and real `ModTime` when available.
- `Read(entry, offset, size)` follows qrypt range semantics.
- `Put`/`PutFile` uploads exactly the passed plaintext body for raw drivers.
- `Mkdir`, `Remove`, `Rename`, and `Move` return qrypt-compatible errors and update IDs/names consistently.
- Paths and URLs are escaped by path segment, not by raw string concatenation.
- Tests cover names containing spaces, Unicode, `#`, `?`, and `%` when the backend uses URL paths.
- README/docs are concise and only mention supported, user-facing behavior.

## Debugging During Migration

Use live debug to separate layers:

- `debug live state`: confirm mount exists, driver name, encrypted flag, pending/upload/cache summary.
- `debug live resolve <path>`: confirm mount, driver, encryption state, remote ID, parent ID, and cache ID.
- `debug live list <dir>`: bypass directory cache and inspect live remote entries.
- `debug live consistency <path>`: compare VFS state with live remote listing.
- `debug live events warn 100 --path <path>`: inspect recent failures for one path.
- `debug live cache [path]`: inspect read cache hits, puts, and errors.
- `debug live staging [path]`: inspect pending staging file existence, size match, orphan files, and checksum.

## References

- `references/qrypt-driver-contract.md`: qrypt driver contracts, read/write semantics, config rules, cache/staging boundaries, and OpenList migration notes.
