## Context

See `proposal.md` — Why. Current constraints that shape the approach:

- `pkg/config.MountConfig` is `{Name, Type, TestEnabled, Params}` and `pkg/core.buildMount` copies
  `mountCfg.Params` into `drive.Params` before `drive.New` (`pkg/core/core.go:736-757`). Drivers only
  ever see a flat `map[string]string`, so credential merging must happen before that call.
- Every mount gets its own state directory (`state/driver/<mount>`, `pkg/core/core.go:827`) and drivers
  persist their own session files with fixed names (`quark_cookie.json`, `115_cookie.json`,
  `aliyundrive_token.json`, …). There is no cross-mount session object today.
- Capability detection is interface-based (`InstallStateStore`,
  `InstallBandwidthLimiter`). A shared session can follow the same optional-installer pattern without
  changing the `drive.Driver` contract.
- `pkg/core` is the composition root. `Core.Reload` opens the new Core before closing the old one
  (`pkg/core/config_mgmt.go:269-280`), and mobile may hold several Cores, so a per-Core manager would
  produce two "shared" sessions in one process.
- `schema/qrypt.schema.json` is the canonical parameter source. `scripts/schema/gen-drive-schema.py`
  generates `pkg/drive/builtin_schema_gen.go`, `gen-cli-i18n.py` generates the CLI catalog, and
  `gen-driver-docs.py` generates `docs/for-user/support-drivers.md`; `check-all.sh` fails on drift.
- Validation is offline and strict: unknown params, missing required params, and alternative groups
  are rejected before a driver is constructed (`pkg/config/validation.go`).
- `pkg/syncer` already has the `flock` pattern this design reuses
  (`session_lock_unix.go` / `session_lock_windows.go`).

## Goals / Non-Goals

**Goals:**

- One credential definition per `(driver type, account)` used by any number of directory mounts.
- One runtime session and one lifecycle (rotate, refresh, invalidate, persist, observe) per credential.
- Preserve per-mount isolation: independent `root_path`/`rootID`, read cache, upload staging and
  sessions, offline task state.
- Backward-compatible config and state for existing users; explicit, non-destructive migration.
- Fix the failure class where a persisted session masks a replaced credential.

**Non-Goals:**

- No external secret manager, vault, or cross-machine credential sync.
- No automatic account discovery or automatic merging of two credentials with the same account
  (detect and warn only).
- No shared driver instances, connection pools, caches, or upload sessions between mounts.
- No config-time network probing of credentials.
- No change to FUSE mount semantics or to the namespace layout.

## Decisions

### D1. Two layers: static params and runtime session

Credential sharing is split into configuration-level merging (pure `drive.Params`) and an optional
runtime session object. The first solves "one secret in config"; the second solves "one lifecycle".

- Alternative: share only the config params. Rejected — each mount would still rotate independently,
  and single-use refresh tokens would break sibling mounts.
- Alternative: share only the runtime session, keyed by a hash of inline params. Rejected — the config
  would still duplicate secrets, the hash changes when a cookie is re-exported, and there is no stable
  identity for lifecycle metadata.

### D2. Explicit credential names, account as metadata

`[[credentials]] name = "quark-main"` is the identity. Account identity (`__uid`, account email,
`drive_id`) is discovered at authentication time, stored on the session, shown in listings, and used
for duplicate warnings. It is never a config key.

- Alternative: use the provider account ID as identity. Rejected — it is unknown before authenticating,
  which breaks offline validation, and it would force two intentionally separate exports of the same
  account to be treated as one.
- Alternative: automatically merge credentials that resolve to the same account. Rejected — adding a
  mount would silently change another mount's credential lifetime; an explicit `merge` operation is
  safer.

### D3. Scope every parameter instead of maintaining per-driver allowlists

Add `x-param-scope` to each driver parameter in `schema/qrypt.schema.json`, generate
`drive.ParamDef.Scope`, and enforce it in validation. `cookie`/`refresh_token`/`authorization` are
`credential`; `root_path` (and provider-specific mount overrides) are `mount`; `base_url`, `v2_url`,
endpoint and proxy-style parameters are `both`, with mount precedence.

- Alternative: hand-maintained lists in `pkg/config`. Rejected — a second source of truth that drifts
  from the generated schema and docs; `driver-parameter-schema` explicitly forbids it.
- Alternative: treat every param as `both`. Rejected — it permits `root_path` on a credential, which
  would silently apply one mount's directory to all mounts of that credential.

### D4. Opaque session payloads owned by the driver

The manager stores a driver-defined JSON payload; it only understands `version`, `params_seed`,
`account`, and `updated_at`. Drivers keep owning authentication and provider session schema, matching
the package-boundary rule that `pkg/drivers/<name>` owns provider authentication and session state.

- Alternative: a typed session struct in `pkg/drive` covering all providers. Rejected — it leaks
  provider fields into the neutral layer and forces every driver through the union of all shapes.

### D5. Versioned commits plus coalesced refresh

`Update(expectedVersion, payload)` rejects stale writes; `Refresh(ctx, fn)` runs one provider refresh
per credential and fans the result out. In-process the session object is the authority; persistence
happens on successful commit only.

- Alternative: last-write-wins without versioning. Rejected — a slow mount can overwrite a fresher
  token, which is exactly the divergence this change removes.
- Alternative: no refresh coalescing, rely on drivers. Rejected — concurrent refreshes of a single-use
  refresh token can invalidate every mount.

### D6. Process-wide manager keyed by state directory

The manager lives behind `pkg/core/internal/credentials` with a package-level registry keyed by the
absolute state directory and credential name. Cores acquire handles; sessions outlive a Core.

- Alternative: one manager per Core. Rejected — `Core.Reload` and mobile multi-Core sessions would run
  two managers over one state directory, reintroducing divergence in-process.
- Alternative: one manager per process without a state-dir key. Rejected — two configurations with
  different work dirs must not collide.

### D7. Params fingerprint replaces per-mount seed/account heuristics

The credential's static params fingerprint is persisted with the session. On open, a fingerprint
mismatch discards the session for all referencing mounts. This lifts the guard currently implemented
per mount in `pkg/drivers/quark` (`cookieState.Seed`/`Account`) into the shared layer and applies it to
every driver.

### D8. Legacy inline mounts synthesize a private credential

A mount without `credential` gets an internal credential named `mount:<mount>` whose session stays in
`state/driver/<mount>/`, preserving today's behavior and file locations. `qrypt credential migrate`
copies/adopts that state into a shared credential only when the account matches, and deletes the
legacy file only when asked.

## Configuration model

```toml
[[credentials]]
name = "quark-main"
type = "quark"
params = { cookie = "..." }

[[mounts]]
name = "quark-download"
type = "quark"
credential = "quark-main"
[mounts.params]
root_path = "/Download"

[[mounts]]
name = "quark-test"
type = "quark"
credential = "quark-main"
[mounts.params]
root_path = "/Test"
```

Validation rules:

- Credential names are unique, single path components, and their driver type must be registered.
- A referencing mount's `type` must equal the credential's `type`.
- `credential` and inline credential-scope params are mutually exclusive.
- Credential-scope required/alternative groups are validated on the credential; mount-scope
  requirements on the mount; unknown or out-of-scope params are rejected on both.
- Credentials not referenced by any mount remain valid (they are configuration, not runtime state).

## Runtime contract

```go
// pkg/drive/credential.go
type CredentialSession interface {
    Name() string
    Snapshot() (payload []byte, version uint64)
    Update(expected uint64, payload []byte) (uint64, error)
    Subscribe(fn func(payload []byte, version uint64)) (cancel func())
    Refresh(ctx context.Context, fn func(current []byte) ([]byte, error)) ([]byte, error)
}

type CredentialSessionInstaller interface {
    InstallCredentialSession(session CredentialSession)
}
```

Driver usage:

- Static drivers: nothing to install; they receive merged params.
- Rotating drivers: decode their payload at `Init`; on rotation call `Update`; keep an in-memory
  credential updated through `Subscribe`; on `ErrAuth`, re-read `Snapshot`, retry once, then fall back
  to a full authentication from the static params; token drivers wrap expiry refresh in `Refresh`.

## State layout

```text
state/credentials/<name>/
  session.json   # {type, version, params_seed, account, updated_at, data}
  session.lock   # flock guard for cross-process read-modify-write
state/driver/<mount>/   # unchanged: upload sessions, offline layout, etc.
```

`session.json` is written atomically with mode 0600; `data` is a `json.RawMessage` owned by the
driver. Load order: read under lock, compare `version` before commit, retry once on conflict.

## Startup and lifecycle sequences

1. `config.Load` + `Validate` (scope-aware, credential references resolved).
2. Manager opens/creates each configured credential session; fingerprint mismatch drops the session.
3. Per mount: merge credential params + mount params, `drive.New(type, merged)`, install mount-local
   state store, install the shared credential session when the driver implements the installer, then
   `Init`.
4. Rotation: driver commits a new payload; the manager persists it, bumps the version, and notifies
   subscribers; sibling mounts update in-memory credentials without restart.
5. Refresh: `Refresh` single-flights; only a successful payload is committed.
6. Config change: reload builds a new Core over the same manager; changed credential params invalidate
   that credential's session; removed mounts leave credentials intact.

## Driver migration matrix

| Driver | Credential params | Session payload | Change |
| --- | --- | --- | --- |
| localfs, sftp, s3, webdav | static | none | none |
| quark, 115, 189, yun139 | cookie/authorization | cookie | move cookie load/save to the session; drop the per-mount seed guard |
| aliyundrive | refresh_token | access token + expiry + account | shared session + coalesced refresh |
| 123 | username/password | token + expiry | shared session + coalesced refresh |
| 115-open | refresh_token | access token + expiry | shared session + coalesced refresh |
| baidu_netdisk | (provider params) | access/refresh token + expiry | shared session + coalesced refresh |
| onedrive, onedrive_app | client credentials / refresh token | access token + expiry | shared session + coalesced refresh |

## Impact

- `pkg/config`: `CredentialConfig`, `MountConfig.Credential`, scope-aware validation, implicit
  credential synthesis, include credentials in `Save`/`Load` round-trips.
- `schema/qrypt.schema.json`: `credentials` array, `credentialConfig`, mount `credential`, and
  `x-param-scope` on every driver parameter; regenerate Go metadata, CLI catalog, and user docs.
- `pkg/drive`: scope field, session contract, installer interface.
- `pkg/core`: manager wiring, merged params in `buildMount`, credential summary/debug fields,
  process-wide manager lifetime across `Reload`.
- `pkg/core/internal/credentials` (new): session objects, file store with `flock` and version checks,
  subscriptions, refresh single-flight, migration helpers.
- `pkg/control`, `pkg/mobile`, `internal/cli`: credential endpoints/commands, masked summaries.
- `scripts/ci/checkarch`: register the new package's allowed imports.
- `docs/for-developer/architecture.md` and `package-boundaries.md`: add the package and ownership.

## Risks

- Misclassifying a parameter's scope breaks existing configs. Mitigation: default only dynamically
  registered drivers to `both`; require explicit scopes for built-ins and validate with fixtures.
- Refresh-token providers may rotate the refresh token itself. Mitigation: coalesced refresh commits
  the full payload (including any new refresh token) and never writes on failure.
- Cross-process writers can still race providers between commits. Mitigation: file lock + version
  conflict retry + `ErrAuth` reload path; multi-process usage stays a secondary scenario.
- Migration could adopt state for the wrong account. Mitigation: account comparison is mandatory and
  legacy files are never deleted without an explicit flag.

## Phasing

1. **Configuration sharing** — `[[credentials]]`, `credential` references, parameter scopes, validation,
   CLI/control summary, schema/docs generation, implicit credential compatibility.
2. **Shared sessions** — `CredentialSession` + manager + cookie-rotation drivers (Quark first).
3. **Refresh coalescing and migration** — token drivers, `qrypt credential migrate`, account
   duplicate warnings, debug fields, legacy state adoption.
