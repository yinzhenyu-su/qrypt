---
name: qrypt-driver-review
description: Review a contributed or new qrypt storage driver. Use when asked to review a driver PR, audit driver contract compliance, verify capability declarations, check path/root handling, upload retries, share behavior, redaction, or live-test claims.
---

# Qrypt Driver Review

Use `driver-migrate` for implementation context; this skill is the review gate.

## Inputs to read first

- `docs/for-developer/driver-development.md` — the authoritative contract.
- `pkg/drive/drive.go`, `capability.go`, `error_category.go`, `http_error.go`.
- `pkg/drivers/<name>/` — every changed file, not only the diff hunks.
- The PR body: claimed live verification, provider account, test scope.
- `pkg/config/*_test.go` for parameter scope/validation coverage.
- `pkg/drivers/capability_test.go` and `scripts/schema/gen-driver-capabilities/`.

## Review order

### 1. Registration and schema plumbing

- Driver lives under `pkg/drivers/<name>` and registers via `drive.Register`.
- `pkg/drivers/all/all.go` imports it; without this the CLI cannot construct it.
- Required params are declared with `Required`/`Secret`, examples are safe.
- Credential-scope vs mount-scope params are correct (`root_path`, `mount_id`
  are mount-scope; account secrets are credential-scope). Missing validation
  usually shows up as a `pkg/config` test gap.
- `scripts/schema/gen-driver-capabilities/main.go` lists the driver, and
  `schema/qrypt.schema.json` has its definition.
- `docs/for-user/support-drivers.md` is generated, never hand-edited.

### 2. Path and identity semantics

- `root_path` is normalized and cannot escape (`..`, absolute injections,
  trailing/duplicate separators).
- IDs and parent IDs are stable and usable by `List`/`Read`/`Remove`/
  `Rename`/`Move`; path-based drivers normalize consistently.
- `Entry.Name` is a basename, not a full path; directories have size 0.
- `ResolvePath`, `EntryResolver`, and `RemoteNameCodec` agree with listing
  names and round-trip correctly.
- Root-level `Move`/`Rename`/`Remove` are rejected or handled explicitly;
  virtual-root name collisions are not ignored.

### 3. Read behavior

- Offset and size are honored; `size == 0` means read to EOF.
- Partial responses are validated (`Content-Range` starts at the requested
  offset) and capped at the requested size.
- Negative offsets/sizes are rejected before the request.
- Directory reads and missing entries use the right sentinels.

### 4. Writes and uploads

- `PutSource` streams; it must not buffer the whole source into memory.
- Returned entry name/size are validated against the request; a provider that
  canonicalizes case is handled deliberately, not silently accepted.
- Upload commit is atomic from the caller's point of view; temporary objects
  are cleaned or reclaimed.
- Retries are limited to idempotent operations. Do not retry link/share
  creation, deletes, or other non-idempotent writes.
- `429`/`5xx`/transport are retryable; `401`/`403`/`404`/`409`/`422` are not
  unless the driver has a documented provider-specific mapping.
- `Move`/`Rename` use the source path/name, check roots, and preserve
  replacement semantics.
- `Remove` uses provider conditional checks (hash/mtime/size) where supported.
- Permanent failures are wrapped with the right sentinel so VFS does not retry
  them forever.

### 5. Share surface

- Capabilities are opt-in: omit an interface rather than returning
  "unsupported" from a stub.
- `SharePolicy` declares only supported options; no silently dropped fields.
- Partial creation failures roll back the provider link; no orphan links.
- Root ID, single-file links, expiry, download toggle, passcode, and path
  containment are handled and tested.
- Share links/passcodes are not logged or exposed in debug/metrics.

### 6. Errors and redaction

- Driver errors are prefixed (`koofr: list: ...`) and wrap sentinels.
- HTTP bodies/URLs/headers go through `drive.HTTPError` / `drive.Snippet`.
  A driver-local redaction pattern list is a review failure: extend the shared
  redactor instead.
- `DebugSnapshot`, metrics, and `LastError` do not contain tokens, cookies,
  authorization headers, signed URLs, or full response bodies.
- Error categories are stable enough for task status and retry decisions.

### 7. Tests and live claims

- Fake-provider tests cover success, provider errors, and path/root edge cases.
- Writable drivers cover `Mkdir`/`Put`/`Rename`/`Move`/`Remove`; read covers
  offset/size; share covers rollback and option restrictions.
- `drive.Capabilities(driver)` matches the interfaces the driver actually
  implements.
- Live tests:
  - skip cleanly when the account lacks a feature (external mounts, passcode
    editing);
  - never empty the account trash/recycle bin;
  - delete only their own fixtures;
  - never commit credentials, signed URLs, or full provider responses.
- If the contributor claims live verification, require the exact command and
  observed result; do not assume a passing contract test means live parity.

## Verification commands

```bash
go test ./pkg/drivers/<name> ./pkg/config ./pkg/drive \
  ./scripts/schema/gen-driver-capabilities -count=1
go test ./pkg/drivers -run 'TestBuiltinDriverCapabilities|TestShareCapabilities' -count=1
scripts/ci/check-all.sh
```

Then run the relevant live probe only with an authorized test account/mount and
`test_enabled = true`. Do not write credentials into shell history or the PR.

## Severity guide

- **Blocker**: data loss, secret leakage, path escape, capability lying,
  non-idempotent retry, broken root handling, CI red.
- **Should fix**: missing regression test, wrong error category, incomplete
  rollback, unclear live-test claim.
- **Nit**: naming, comment style, log wording.

Leave the PR with concrete file/line references and the command that exposes
each blocker. Do not approve until the blocker list is empty.
