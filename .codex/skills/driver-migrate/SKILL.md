---
name: driver-migrate
description: Migrate a storage driver from OpenList/OpenListTeam into the qrypt Go codebase, or debug a partially migrated one. Use when asked to add a qrypt driver, port an OpenList cloud-drive implementation, compare OpenList behavior with qrypt's driver contract, or fix list/read/write/path/schema/session/upload behavior of a driver under pkg/drivers/.
---

# Driver Migrate

## Workflow

1. Resolve the target driver before coding (see Driver Discovery). Do not start
   implementation until it is explicit.

2. Read qrypt's *real* contract before writing code.
   - `pkg/drive/drive.go`: the `Driver` interface, capability constants, `ParamDef`.
   - `pkg/drive/source.go`, `state.go`, `debug.go`, `session/`: upload sources,
     state store, debug/metrics types, resumable-upload bindings.
   - The closest existing driver under `pkg/drivers/<name>` (e.g. `quark`,
     `p189`, `yun139`).
   - `references/qrypt-driver-contract.md` in this skill.
   - Verify every symbol you are about to call with grep. Interface names from
     memory, from OpenList, or from older qrypt docs are frequently wrong; the
     code is the only authority.

3. Probe the provider live before designing (see Provider Probing).
   OpenList is a *hypothesis* about the provider, not a specification: parts of
   it are wrong or incomplete for a given account. Record the observed
   request/response shapes; they drive the design.

4. Implement in layers.
   - Register: `init()` + `drive.Register` with `ParamDef`s.
   - `Init`/`Drop`, then `List`, then `Read`.
   - Writes: `Mkdir`/`Remove`/`Rename`/`Move`, then `PutSource`.
   - Implement required `Space`, `DebugSnapshot`, and `Metrics` methods; use
     the contract-defined unsupported result where appropriate.
   - Add hash requirements, `RemoteHash`, `Copy`, and resumable sessions as
     supported by the provider.
   - Declare only the capabilities the backend truly implements, and implement
     exactly what you declare (see Contract Checkpoints).

5. Keep VFS boundaries clear.
   - Drivers return backend entries. VFS owns path resolution, read cache,
     staging, the pending journal, upload scheduling, and FUSE semantics.
   - Drivers must not know about qrypt staging or the read cache.
   - Crypt and bandwidth wrapping happen above the raw driver.

6. Register the plumbing, not just the code (see Registration Checklist). The
   schema, generated docs, and capability test all fail CI if skipped.

7. Test, then verify live, then clean up (see Verification).

## Driver Discovery

When the driver is not named:

1. List local drivers with `ls pkg/drivers/` (and `pkg/drivers/all/all.go`).
2. List OpenList drivers:
   `gh api repos/OpenListTeam/OpenList/contents/drivers --jq '.[].name'`.
3. Remove names qrypt already ships; present the rest as candidates.
4. Ask which one to migrate and wait for the answer before editing code.

One OpenList driver is a small directory (`driver.go`, `meta.go`, `types.go`,
`util.go`, `upload.go`):

```bash
gh api repos/OpenListTeam/OpenList/contents/drivers/<driver> --jq '.[] | "\(.name) \(.size)"'
gh api repos/OpenListTeam/OpenList/contents/drivers/<driver>/util.go --jq '.content' | base64 -d
```

Use another repository access method if `gh` is unavailable or unauthenticated,
and say so.

## Provider Probing

Use a throwaway Go program in a session-owned temporary directory; prefer the
standard library, but reuse existing SDKs/helpers when they exercise the real
upload path more faithfully. Within the authorized account/test scope, it:

1. authenticates with the real credentials;
2. lists the root and paginates a directory;
3. uploads a small file, then a *random* file larger than the provider's actual
   multipart threshold/part size (random avoids provider-side dedup); use a
   sufficiently large source to interrupt and resume when testing that branch;
4. downloads a byte range, following whatever redirect/signature layer the
   provider uses;
5. creates a directory, renames, moves, and deletes;
6. records redacted responses, retaining status, field names/types, missing
   fields, and `null` values. Mask tokens, cookies, passwords, temporary storage
   credentials, and signed URL query values before printing or saving them.

Do not dump the user's full config. Keep credential-bearing temporary files
owner-readable only.

For provider-specific pitfalls, see [OpenList Migration Notes](references/qrypt-driver-contract.md#openlist-migration-notes).

Keep the probe out of the repository and delete it when done.

## Registration Checklist

- Package: `pkg/drivers/<name>/`. A Go package name cannot start with a digit,
  so use a `p` prefix and keep the registered name numeric: `p115` → `"115"`,
  `p123` → `"123"`, `p189` → `"189"`.
- `init()` calls `drive.Register(name, factory, ParamDef...)`, with
  `Required`, `Secret`, `Default`, and `Example` set for every user-facing param.
- `pkg/drivers/all/all.go`: add the blank import.
- `qrypt.schema.json`: add the name to the `type` `enum` under
  `definitions.mountConfig`; add an `allOf` `if/then` block that `$ref`s the new
  params; add `definitions.<name>Params` whose descriptions match the `ParamDef`s.
- `scripts/gen-driver-docs.py`: add a `DRIVER_META` entry and put its key in
  `DRIVER_ORDER`.
- `pkg/drivers/capability_test.go`: add an entry with the exact expected
  capability list.
- Regenerate docs: `python3 scripts/gen-driver-docs.py` and
  `python3 scripts/gen-config-docs.py` (CI fails when `docs/for-user/` is stale).
- Avoid new modules; prefer what `go.mod` already has (for S3,
  `github.com/aws/aws-sdk-go-v2/service/s3` is present).

## Contract Checkpoints

Use [the contract reference](references/qrypt-driver-contract.md) for details:

- Entries and wrappers: ids, names, directory sizes, and missing/wrapped Extra.
- Reads: offsets, EOF, ignored ranges, redirects, and signed URL expiry.
- Writes and errors: conflicts, finalized metadata, hashes, and retryable bodies.
- Roots and pagination: root precedence, retried Init, and complete listings.
- Upload sessions: durable identity, confirmed progress, stale handles, and Drop.
- Capabilities: declarations must match behaviour in both directions; do not
  advertise resume, mtime, or server-side copy without implementing them.
- Debug and metrics: verify instant-upload success through the counter, not
  only a phase report.

## Verification

Unit tests first, with fakes:

- Registration/config validation; entry mapping (ids, dirs, sizes, times);
  pagination and its limit error; read offsets/EOF/range-ignored/416/redirects;
  mkdir conflict paths; upload branches (instant, presigned, static-credential,
  multipart retry); session resume and stale-handle restart; `RemoteHash`;
  `Copy`; and a debug snapshot that leaks no credential.
- Fake the provider with `httptest`. For SDK branches fake an S3-compatible
  endpoint path-style (`?uploads`, `?partNumber`, `?uploadId`) and inject
  `500`/`416`/`302`/stale-session responses to reach retry and recovery paths.
- Every bug fix ships with a regression test that fails on the old code. When
  checking that failure, use an isolated copy/worktree or temporary overlay;
  do not revert the user's working tree. Exercise the real call chain (e.g.
  list-normalized metadata through Remove), not only hand-built metadata.

Live verification against a real account:

- The mount needs `test_enabled = true` for `probe test`.
- Run `auth`, `contract`, `crud`, `fs`, `instantupload`, `resume`, and
  `multipart` separately; `contract` is not an alias for all suites. Report
  each result and capability-based skip reason, test sizes, residual objects,
  and cleanup failures. Add read/batch/xfer tests where relevant.
- `./scripts/mount-debug-test.sh <mount> <spec> [flags]` builds qrypt, mounts
  into an isolated temporary tree (its own cache/staging/state), runs one
  `probe test` spec, then tears it down. Prefer it over hand-rolled temporary
  configs when no existing running mount was requested.
- For an existing running mount, first verify the mount/driver, running server
  build, test enablement, staging availability, and pending work using read-only
  diagnostics. The CLI resolves the built-in per-config endpoint from the same
  config; building a new CLI does not upgrade the running server, so a stale
  server keeps serving the old surface. Do not replace or restart the user's
  service without authorization.
- Against a running mount, use `qrypt debug collect --mount NAME` for structured
  diagnostics; pass `--config PATH` when the config is not auto-discovered.
  Individual endpoints and additional test specs are documented in
  `docs/for-developer/debug.md`.
- One-shot `fs` commands debounce: uploads flush after `upload_delay` (default
  30s), deletes after `delete_delay` (default 2s). Lower those in the config or
  wait before believing a result — a pending write is not a driver bug.
- Classify failures by layer before changing code: provider/driver, VFS, or
  environment. Missing staging directories are not a reason to add staging
  knowledge to a raw driver.
- Clean up only remote objects and exclusive temporary directories created by
  this test session. Wait for test work to settle and stop an owned temporary
  service before removing its runtime directories. Never delete an active
  mount's shared state/cache/staging/upload data. Retain redacted failure
  reports and list any residual objects rather than broadening cleanup.

Run `scripts/ci-check.sh` for the full local gate before committing; see
`docs/for-developer/quality-gates.md` for its maintained checks.
For race/runner flakes, repeat the failing test with `-race` before rerunning
the full gate. Prefer explicit synchronization; tests not checking timeout
semantics need realistic scheduling budgets. A local pass is not proof of
GitHub Linux/Windows success; when CI verification is requested, check the
exact submitted commit.

## References

- `references/qrypt-driver-contract.md`: the real qrypt driver contract —
  interfaces, capabilities, entry/read/write semantics, errors, sessions, and
  what is (and is not) trustworthy in OpenList.
- `docs/for-developer/driver-development.md`, `docs/for-developer/debug.md`,
  `docs/for-developer/quality-gates.md`: the repository's own guides.
